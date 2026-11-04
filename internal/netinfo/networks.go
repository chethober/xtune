// Package netinfo finds out about networks: the ASN this machine is on, the ASN and country of
// an IP (Team Cymru's DNS service, cached), for per-network rankings and country flags.
package netinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Network is the network this machine is on.
type Network struct {
	ASN  string // "AS12880"
	Name string
}

// Client looks up this machine's network, straight (not through a proxy). A variable so
// tests can choose a network.
var Client = func() (Network, error) {
	tr := &http.Transport{Proxy: nil}
	defer tr.CloseIdleConnections()
	c := &http.Client{Timeout: 8 * time.Second, Transport: agentTransport{tr}}
	return LookupClient(c, ASNLookup)
}

// UserAgent is what lookups send as User-Agent; main sets it to "xtune/VERSION".
var UserAgent = "xtune"

type agentTransport struct{ http.RoundTripper }

func (a agentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("User-Agent", UserAgent)
	return a.RoundTripper.RoundTrip(r)
}

func LookupClient(c *http.Client, lookup func(net.IP) (string, error)) (Network, error) {
	var errs []string
	// Cloudflare's speed test answers with the client's ASN; ipinfo.io is the fallback
	if b, err := getBody(c, "https://speed.cloudflare.com/meta"); err == nil {
		var m struct {
			ASN  json.Number `json:"asn"`
			Org  string      `json:"asOrganization"`
			Host string      `json:"clientIp"`
		}
		if json.Unmarshal(b, &m) == nil && m.ASN.String() != "" && m.ASN.String() != "0" {
			return Network{ASN: "AS" + m.ASN.String(), Name: m.Org}, nil
		}
		errs = append(errs, "speed.cloudflare.com: no ASN")
	} else {
		errs = append(errs, err.Error())
	}
	if b, err := getBody(c, "https://ipinfo.io/json"); err == nil {
		var m struct{ Org string }
		if json.Unmarshal(b, &m) == nil {
			if asn, name, ok := strings.Cut(m.Org, " "); ok && strings.HasPrefix(asn, "AS") {
				return Network{ASN: asn, Name: name}, nil
			}
		}
		errs = append(errs, "ipinfo.io: no ASN")
	} else {
		errs = append(errs, err.Error())
	}
	// Trace endpoints expose the public IP without relying on a rate-limited
	// geolocation API. Resolve that IP through Team Cymru's DNS service.
	for _, u := range []string{"https://www.cloudflare.com/cdn-cgi/trace", "https://1.1.1.1/cdn-cgi/trace"} {
		b, err := getBody(c, u)
		if err == nil {
			err = errors.New("no public IP in trace")
			for _, line := range strings.Split(string(b), "\n") {
				key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
				ip := net.ParseIP(value)
				if key != "ip" || ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
					continue
				}
				var asn string
				asn, err = lookup(ip)
				if err == nil && asn != "" {
					return Network{ASN: asn}, nil
				}
				if err == nil {
					err = errors.New("empty ASN")
				}
				break
			}
		}
		errs = append(errs, u+": "+err.Error())
	}
	return Network{}, errors.New(strings.Join(errs, "; "))
}

func getBody(c *http.Client, u string) ([]byte, error) {
	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

// Detect looks up this machine's network for the rankings, and names it when the user did
// not: it logs and returns an empty network when it cannot tell.
func Detect(name *string, log func(string)) Network {
	n, err := Client()
	if err != nil {
		log("[!] could not tell this network's ASN (" + tail(err.Error(), 200) + "): continuing configuration without per-network subscription ranking")
		return Network{}
	}
	if *name == "" {
		*name = n.Name
	}
	log(fmt.Sprintf("[*] this network: %s (%s)", n.ASN, orStr(n.Name, "?")))
	return n
}

// ASNLookup finds the ASN an IP belongs to. A variable so tests can stand in for DNS.
var ASNLookup = func(ip net.IP) (string, error) { return CymruASN(net.DefaultResolver, ip) }

// CymruASN asks Team Cymru's IP-to-ASN DNS service for the ASN an IP belongs to.
func CymruASN(res *net.Resolver, ip net.IP) (string, error) {
	asn, _, err := CymruOrigin(res, ip)
	return asn, err
}

// CymruOrigin asks Team Cymru's IP-to-ASN DNS service: TXT "ASN | prefix | CC | registry | date".
// It returns the ASN ("AS1234") and the country the prefix is registered in ("DE").
func CymruOrigin(res *net.Resolver, ip net.IP) (asn, cc string, err error) {
	var q string
	if v4 := ip.To4(); v4 != nil {
		q = fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", v4[3], v4[2], v4[1], v4[0])
	} else {
		hex := fmt.Sprintf("%x", []byte(ip.To16()))
		var b strings.Builder
		for i := len(hex) - 1; i >= 0; i-- {
			b.WriteByte(hex[i])
			b.WriteByte('.')
		}
		q = b.String() + "origin6.asn.cymru.com"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	txts, err := res.LookupTXT(ctx, q)
	if err != nil {
		return "", "", err
	}
	return ParseCymru(txts, ip)
}

func ParseCymru(txts []string, ip net.IP) (asn, cc string, err error) {
	for _, t := range txts {
		parts := strings.Split(t, "|")
		f := strings.Fields(parts[0])
		if len(f) > 0 { // several ASNs may announce it; the first will do
			if _, err := strconv.Atoi(f[0]); err == nil {
				if len(parts) > 2 {
					cc = strings.ToUpper(strings.TrimSpace(parts[2]))
				}
				return "AS" + f[0], cc, nil
			}
		}
	}
	return "", "", errors.New("no ASN for " + ip.String())
}

// asnCache keeps lookups by /24 (IPv4) or /48 (IPv6) for a day: apps ask every few hours.
var asnCache = struct {
	sync.Mutex
	m map[string]asnCached
}{m: map[string]asnCached{}}

type asnCached struct {
	asn string
	at  time.Time
}

func ASNOf(ip net.IP, now time.Time) string {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() {
		return ""
	}
	key := ip.Mask(net.CIDRMask(48, 128)).String()
	if v4 := ip.To4(); v4 != nil {
		key = v4.Mask(net.CIDRMask(24, 32)).String()
	}
	asnCache.Lock()
	c, ok := asnCache.m[key]
	asnCache.Unlock()
	if ok && now.Sub(c.at) < 24*time.Hour {
		return c.asn
	}
	asn, err := ASNLookup(ip)
	if err != nil {
		asn = "" // cached too, briefly: a failing lookup should not slow every request
		now = now.Add(-23 * time.Hour)
	}
	asnCache.Lock()
	if len(asnCache.m) > 50000 {
		asnCache.m = map[string]asnCached{}
	}
	asnCache.m[key] = asnCached{asn, now}
	asnCache.Unlock()
	return asn
}

// ResetASNCache forgets every cached lookup, for tests that stand in for DNS.
func ResetASNCache() {
	asnCache.Lock()
	asnCache.m = map[string]asnCached{}
	asnCache.Unlock()
}

// TraceCountry reads the ip= and loc= lines of a Cloudflare /cdn-cgi/trace answer: the address a
// machine's traffic leaves from, and the country Cloudflare places it in. cc is "" when absent.
func TraceCountry(body string) (ip, cc string) {
	for _, line := range strings.Split(body, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "ip":
			ip = v
		case "loc":
			if v = strings.ToUpper(v); isCountryCode(v) && v != "XX" && v != "T1" {
				cc = v
			}
		}
	}
	return ip, cc
}

// OutboundCountry is the country this machine's traffic leaves from, as Cloudflare locates its
// outbound address. Unlike CountryLookup (the registry's country of the IP block, often where the
// hosting company is based, not where the server is) it follows the server's real location.
// A variable so tests can stand in for the network.
var OutboundCountry = func() (string, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	var errs []string
	for _, u := range []string{"https://1.1.1.1/cdn-cgi/trace", "https://www.cloudflare.com/cdn-cgi/trace"} {
		b, err := getBody(c, u)
		if err == nil {
			if _, cc := TraceCountry(string(b)); cc != "" {
				return cc, nil
			}
			err = errors.New("no country in trace")
		}
		errs = append(errs, u+": "+err.Error())
	}
	return "", errors.New(strings.Join(errs, "; "))
}

// CountryLookup finds the country an IP is registered in. A variable so tests can stand in for DNS.
var CountryLookup = func(ip net.IP) (string, error) {
	_, cc, err := CymruOrigin(net.DefaultResolver, ip)
	if err == nil && !isCountryCode(cc) {
		err = errors.New("no country for " + ip.String())
	}
	return cc, err
}

func isCountryCode(cc string) bool {
	return len(cc) == 2 && cc[0] >= 'A' && cc[0] <= 'Z' && cc[1] >= 'A' && cc[1] <= 'Z'
}
func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
