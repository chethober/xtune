package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/features/dns"
)

// The leak test: is traffic really leaving through the tunnel, and are DNS lookups answered by
// resolvers outside the device's own network? The address a site sees is fetched through the
// connection and directly; DNS goes through bash.ws (the service behind dnsleaktest.com): names
// only it can answer are looked up the way apps' lookups go in VPN mode (Xray's DNS), and it
// reports which resolvers asked it.

var (
	leakBase       = "https://bash.ws"
	traceURL       = "https://speed.cloudflare.com/cdn-cgi/trace"
	leakLookupWait = 10 * time.Second
)

// Resolver is a DNS server the test saw.
type Resolver struct {
	IP      string `json:"ip"`
	Country string `json:"country"`
	ASN     string `json:"asn"`
}

// LeakResult is what the test found.
type LeakResult struct {
	ExitIP        string     `json:"exitIp"`        // what sites see through the connection
	ExitCountry   string     `json:"exitCountry"`   // two letters
	DirectIP      string     `json:"directIp"`      // the device's own address
	DirectCountry string     `json:"directCountry"` //
	Tunneled      bool       `json:"tunneled"`      // the two differ
	Resolvers     []Resolver `json:"resolvers"`
	DNSLeak       bool       `json:"dnsLeak"` // a resolver is in the device's own country, not the exit's
	DNSError      string     `json:"dnsError,omitempty"`
	IPv6          string     `json:"ipv6"` // blocked | tunneled
	Conclusion    string     `json:"conclusion,omitempty"`
}

func trace(c *http.Client) (ip, country string, err error) {
	resp, err := c.Get(traceURL)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "ip="); ok {
			ip = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "loc="); ok {
			country = strings.TrimSpace(v)
		}
	}
	if ip == "" {
		return "", "", errors.New("unexpected answer")
	}
	return ip, country, nil
}

// LeakTest runs the test on the running connection.
func LeakTest() (*LeakResult, error) {
	engine.mu.Lock()
	inst, o := engine.inst, engine.opts
	engine.mu.Unlock()
	if inst == nil {
		return nil, errors.New("connect first")
	}
	pu, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", o.SocksPort))
	tunnelled := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 15 * time.Second}
	res := &LeakResult{IPv6: "blocked"}
	if o.IPv6 {
		res.IPv6 = "tunneled"
	}
	var wg sync.WaitGroup
	var exitErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		res.ExitIP, res.ExitCountry, exitErr = trace(tunnelled)
	}()
	go func() {
		defer wg.Done()
		res.DirectIP, res.DirectCountry, _ = trace(directClient)
	}()
	wg.Wait()
	if exitErr != nil {
		return nil, fmt.Errorf("nothing came back through the connection: %w", exitErr)
	}
	res.Tunneled = res.DirectIP == "" || res.ExitIP != res.DirectIP

	// DNS
	if err := dnsLeak(tunnelled, inst.GetFeature(dns.ClientType()), res); err != nil {
		res.DNSError = err.Error()
	}
	switch {
	case !res.Tunneled:
		res.Conclusion = "Sites see your own address: traffic is not going through the connection."
	case res.DNSLeak:
		res.Conclusion = "Traffic goes through the connection, but some DNS lookups are answered in your own network."
	case res.DNSError != "":
		res.Conclusion = "Traffic goes through the connection. The DNS check could not finish."
	default:
		res.Conclusion = "No leaks: traffic and DNS lookups go through the connection."
	}
	return res, nil
}

func dnsLeak(c *http.Client, feature any, res *LeakResult) error {
	resp, err := c.Get(leakBase + "/id")
	if err != nil {
		return fmt.Errorf("the DNS test service could not be reached: %w", err)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	resp.Body.Close()
	id := strings.TrimSpace(string(b))
	if id == "" || strings.ContainsAny(id, "/. <") {
		return errors.New("the DNS test service gave no test ID")
	}
	host := strings.TrimPrefix(strings.TrimPrefix(leakBase, "https://"), "http://")
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if client, ok := feature.(dns.Client); ok {
		ctx, cancel := context.WithTimeout(context.Background(), leakLookupWait)
		var lw sync.WaitGroup
		for i := 1; i <= 6; i++ {
			lw.Add(1)
			go func(i int) {
				defer lw.Done()
				name := fmt.Sprintf("%d.%s.%s", i, id, host)
				done := make(chan struct{})
				go func() { client.LookupIP(name, dns.IPOption{IPv4Enable: true}); close(done) }()
				select {
				case <-done:
				case <-ctx.Done():
				}
			}(i)
		}
		lw.Wait()
		cancel()
	}
	resp, err = c.Get(leakBase + "/dnsleak/test/" + id + "?json")
	if err != nil {
		return fmt.Errorf("the DNS test result could not be fetched: %w", err)
	}
	defer resp.Body.Close()
	var items []struct {
		IP      string `json:"ip"`
		Country string `json:"country"`
		Name    string `json:"country_name"`
		ASN     string `json:"asn"`
		Type    string `json:"type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&items); err != nil {
		return errors.New("the DNS test service answered something unexpected")
	}
	for _, it := range items {
		if it.Type != "dns" {
			continue
		}
		r := Resolver{IP: it.IP, Country: strings.ToUpper(it.Country), ASN: it.ASN}
		res.Resolvers = append(res.Resolvers, r)
		if r.Country != "" && res.DirectCountry != "" && strings.EqualFold(r.Country, res.DirectCountry) &&
			!strings.EqualFold(r.Country, res.ExitCountry) {
			res.DNSLeak = true
		}
	}
	if len(res.Resolvers) == 0 {
		return errors.New("no lookup reached the DNS test service")
	}
	return nil
}
