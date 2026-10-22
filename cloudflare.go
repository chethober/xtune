package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// With a Cloudflare API token, xtune keeps the domains' DNS records pointed at the server itself
// (after a migrate, or when the domains change) and can read and fix the SSL/TLS mode the CDN
// entry needs. The token needs Zone:Read, DNS:Edit and Zone Settings:Edit on the zones.

type Cloudflare struct {
	Token string `json:"token,omitempty"`
}

// cloudflareAPI is a variable so tests can point it at a local server.
var cloudflareAPI = "https://api.cloudflare.com/client/v4"

func cfCall(token, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, cloudflareAPI+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var res struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&res); err != nil {
		return fmt.Errorf("cloudflare: HTTP %d", resp.StatusCode)
	}
	if !res.Success {
		var msgs []string
		for _, e := range res.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("cloudflare: %s", orStr(strings.Join(msgs, "; "), fmt.Sprintf("HTTP %d", resp.StatusCode)))
	}
	if out != nil {
		return json.Unmarshal(res.Result, out)
	}
	return nil
}

func cfVerify(token string) error {
	var r struct{ Status string }
	if err := cfCall(token, "GET", "/user/tokens/verify", nil, &r); err != nil {
		return err
	}
	if r.Status != "active" {
		return fmt.Errorf("cloudflare: the token is %s", r.Status)
	}
	return nil
}

// cfZone finds the zone a domain belongs to.
func cfZone(token, domain string) (string, error) {
	labels := strings.Split(strings.TrimSuffix(domain, "."), ".")
	for i := 0; i+2 <= len(labels); i++ {
		var zones []struct{ ID, Name string }
		name := strings.Join(labels[i:], ".")
		if err := cfCall(token, "GET", "/zones?name="+url.QueryEscape(name), nil, &zones); err != nil {
			return "", err
		}
		if len(zones) > 0 {
			return zones[0].ID, nil
		}
	}
	return "", fmt.Errorf("cloudflare: no zone for %s that this token can see", domain)
}

type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl,omitempty"`
}

// cfPoint makes name's address record point at ip with the given proxying, replacing an A record
// with an AAAA one (or back) when the server's address family changes.
func cfPoint(token, name, ip string, proxied bool) (string, error) {
	zone, err := cfZone(token, name)
	if err != nil {
		return "", err
	}
	want := "A"
	if strings.Contains(ip, ":") {
		want = "AAAA"
	}
	var recs []cfRecord
	if err := cfCall(token, "GET", "/zones/"+zone+"/dns_records?name="+url.QueryEscape(name), nil, &recs); err != nil {
		return "", err
	}
	done := false
	for _, rec := range recs {
		switch rec.Type {
		case want:
			if done { // a second record would send some clients elsewhere
				if err := cfCall(token, "DELETE", "/zones/"+zone+"/dns_records/"+rec.ID, nil, nil); err != nil {
					return "", err
				}
				continue
			}
			done = true
			if rec.Content == ip && rec.Proxied == proxied {
				continue
			}
			if err := cfCall(token, "PATCH", "/zones/"+zone+"/dns_records/"+rec.ID,
				J{"content": ip, "proxied": proxied}, nil); err != nil {
				return "", err
			}
		case "A", "AAAA":
			if err := cfCall(token, "DELETE", "/zones/"+zone+"/dns_records/"+rec.ID, nil, nil); err != nil {
				return "", err
			}
		case "CNAME":
			return "", fmt.Errorf("%s is a CNAME record: change it in Cloudflare yourself", name)
		}
	}
	if !done {
		if err := cfCall(token, "POST", "/zones/"+zone+"/dns_records",
			cfRecord{Type: want, Name: name, Content: ip, Proxied: proxied, TTL: 1}, nil); err != nil {
			return "", err
		}
	}
	mode := "DNS only"
	if proxied {
		mode = "proxied"
	}
	return fmt.Sprintf("%s -> %s (%s)", name, ip, mode), nil
}

// CloudflarePoint points the direct domain (DNS only) and the CDN domain (proxied) at ip, and
// delegates the DNS tunnel's zone to the server.
func CloudflarePoint(st *State, ip string, log func(string)) error {
	if st.Cloudflare.Token == "" {
		return errors.New("no Cloudflare token: xtune cloudflare token")
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%q is not an IP address", ip)
	}
	if st.Domain == "" && st.CDNDomain == "" && st.DNSDomain == "" {
		return errors.New("no domains to point (xtune domains --direct D --cdn C --dns Z)")
	}
	if st.DNSDomain != "" {
		ns := nsHost(st.DNSDomain)
		msg, err := cfPoint(st.Cloudflare.Token, ns, ip, false)
		if err != nil {
			return err
		}
		log(msg)
		msg, err = cfNS(st.Cloudflare.Token, st.DNSDomain, ns)
		if err != nil {
			return err
		}
		log(msg)
	}
	for _, d := range []struct {
		name    string
		proxied bool
	}{{st.Domain, false}, {st.CDNDomain, true}} {
		if d.name == "" {
			continue
		}
		msg, err := cfPoint(st.Cloudflare.Token, d.name, ip, d.proxied)
		if err != nil {
			return err
		}
		log(msg)
	}
	return nil
}

// nsHost is the name server name xtune uses for a DNS tunnel zone: t.example.com is served by
// ns-t.example.com.
func nsHost(zone string) string { return "ns-" + zone }

// cfNS delegates zone to the name server ns with an NS record.
func cfNS(token, zone, ns string) (string, error) {
	z, err := cfZone(token, zone)
	if err != nil {
		return "", err
	}
	var recs []cfRecord
	if err := cfCall(token, "GET", "/zones/"+z+"/dns_records?name="+url.QueryEscape(zone), nil, &recs); err != nil {
		return "", err
	}
	done := false
	for _, rec := range recs {
		switch {
		case rec.Type == "NS" && strings.EqualFold(rec.Content, ns) && !done:
			done = true
		default: // a delegated name can have no other records
			if err := cfCall(token, "DELETE", "/zones/"+z+"/dns_records/"+rec.ID, nil, nil); err != nil {
				return "", err
			}
		}
	}
	if !done {
		if err := cfCall(token, "POST", "/zones/"+z+"/dns_records", cfRecord{Type: "NS", Name: zone, Content: ns, TTL: 1}, nil); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s NS %s (DNS tunnel)", zone, ns), nil
}

// cfSSL reads the SSL/TLS mode of domain's zone, and sets it to Full when fix is set and it is not.
func cfSSL(token, domain string, fix bool) (string, error) {
	zone, err := cfZone(token, domain)
	if err != nil {
		return "", err
	}
	var s struct{ Value string }
	if err := cfCall(token, "GET", "/zones/"+zone+"/settings/ssl", nil, &s); err != nil {
		return "", err
	}
	if fix && s.Value != "full" {
		if err := cfCall(token, "PATCH", "/zones/"+zone+"/settings/ssl", J{"value": "full"}, nil); err != nil {
			return s.Value, err
		}
		return "full", nil
	}
	return s.Value, nil
}

// serverIP is the address records should point at: the server's address, resolved if a name.
func serverIP(host string) (string, error) {
	if net.ParseIP(host) != nil {
		return host, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("cannot tell this server's IP from %q: pass --ip", host)
	}
	return ips[0].String(), nil
}

func SetCloudflare(r *Server, cf Cloudflare) error {
	if cf.Token != "" {
		if err := cfVerify(cf.Token); err != nil {
			return err
		}
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	st.Cloudflare = cf
	return saveState(r, st)
}

func cmdCloudflare(args []string) error {
	if err := serverOnly("cloudflare", args); err != nil {
		return err
	}
	pos, ip := []string{}, ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--ip" && i+1 < len(args) {
			ip, i = args[i+1], i+1
		} else if v, ok := strings.CutPrefix(args[i], "--ip="); ok {
			ip = v
		} else {
			pos = append(pos, args[i])
		}
	}
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	action := "status"
	if len(pos) > 0 {
		action = pos[0]
	}
	log := func(s string) { fmt.Println(s) }
	if action != "token" && action != "off" && action != "status" && st.Cloudflare.Token == "" {
		return errors.New("no Cloudflare token yet: xtune cloudflare token")
	}
	switch action {
	case "status":
		if st.Cloudflare.Token == "" {
			fmt.Println("Cloudflare: off (xtune cloudflare token)")
			return nil
		}
		fmt.Println("Cloudflare: token set")
		for _, d := range []string{st.Domain, st.CDNDomain} {
			if d == "" {
				continue
			}
			var recs []cfRecord
			zone, err := cfZone(st.Cloudflare.Token, d)
			if err == nil {
				err = cfCall(st.Cloudflare.Token, "GET", "/zones/"+zone+"/dns_records?name="+url.QueryEscape(d), nil, &recs)
			}
			if err != nil {
				fmt.Printf("  %s: %v\n", d, err)
				continue
			}
			for _, rec := range recs {
				fmt.Printf("  %s %s %s proxied=%v\n", rec.Name, rec.Type, rec.Content, rec.Proxied)
			}
		}
		if st.CDNDomain != "" {
			mode, err := cfSSL(st.Cloudflare.Token, st.CDNDomain, false)
			if err != nil {
				return err
			}
			fmt.Printf("  SSL/TLS mode of %s: %s (the CDN entry needs full)\n", st.CDNDomain, mode)
		}
		return nil
	case "token":
		tok := os.Getenv("XTUNE_CLOUDFLARE_TOKEN")
		if tok == "" {
			if tok, err = cliPrompt("Cloudflare API token (Zone:Read, DNS:Edit, Zone Settings:Edit)", true); err != nil {
				return err
			}
		}
		if err := SetCloudflare(r, Cloudflare{Token: strings.TrimSpace(tok)}); err != nil {
			return err
		}
		fmt.Println("Cloudflare: token saved. `xtune migrate` and `xtune domains` now update the DNS records")
		return nil
	case "off":
		return SetCloudflare(r, Cloudflare{})
	case "dns":
		if ip == "" {
			if ip, err = serverIP(st.Host); err != nil {
				return err
			}
		}
		return CloudflarePoint(st, ip, log)
	case "ssl":
		if st.CDNDomain == "" {
			return errors.New("no CDN domain set")
		}
		mode, err := cfSSL(st.Cloudflare.Token, st.CDNDomain, true)
		if err != nil {
			return err
		}
		fmt.Printf("SSL/TLS mode of %s: %s\n", st.CDNDomain, mode)
		return nil
	}
	return fmt.Errorf("unknown cloudflare action %q", action)
}

// pointAfterMove updates the DNS records, from the new server, after a migrate. It reports
// whether it did, so the admin is not told to do it by hand.
func pointAfterMove(r *Server, st *State, log func(string)) bool {
	if st.Cloudflare.Token == "" || st.Domain == "" && st.CDNDomain == "" && st.DNSDomain == "" {
		return false
	}
	ip, err := serverIP(st.Host)
	if err != nil {
		log("[!] " + err.Error())
		return false
	}
	out, err := r.Root(remoteDir + "/xtune cloudflare dns --ip " + shq(ip))
	if err != nil {
		log("[!] the Cloudflare DNS update failed, update the records by hand: " + err.Error())
		return false
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		log("[+] DNS " + l)
	}
	return true
}
