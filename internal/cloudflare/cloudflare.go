// Package cloudflare is the small part of Cloudflare's API xtune uses: DNS records, the zone's SSL
// mode and Worker uploads, with an API token.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// API is a variable so tests can point it at a local server.
var API = "https://api.cloudflare.com/client/v4"

// Call sends body as JSON and decodes the result into out.
func Call(token, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	return Do(token, method, path, "application/json", rd, out)
}

// Do sends a request with a body of any type (the Worker upload is multipart).
func Do(token, method, path, contentType string, rd io.Reader, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, API+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
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

func Verify(token string) error {
	var r struct{ Status string }
	if err := Call(token, "GET", "/user/tokens/verify", nil, &r); err != nil {
		return err
	}
	if r.Status != "active" {
		return fmt.Errorf("cloudflare: the token is %s", r.Status)
	}
	return nil
}

// Zone finds the zone a domain belongs to.
func Zone(token, domain string) (string, error) {
	labels := strings.Split(strings.TrimSuffix(domain, "."), ".")
	for i := 0; i+2 <= len(labels); i++ {
		var zones []struct{ ID, Name string }
		name := strings.Join(labels[i:], ".")
		if err := Call(token, "GET", "/zones?name="+url.QueryEscape(name), nil, &zones); err != nil {
			return "", err
		}
		if len(zones) > 0 {
			return zones[0].ID, nil
		}
	}
	return "", fmt.Errorf("cloudflare: no zone for %s that this token can see", domain)
}

type Record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl,omitempty"`
}

// Point makes name's address record point at ip with the given proxying, replacing an A record
// with an AAAA one (or back) when the server's address family changes.
func Point(token, name, ip string, proxied bool) (string, error) {
	zone, err := Zone(token, name)
	if err != nil {
		return "", err
	}
	want := "A"
	if strings.Contains(ip, ":") {
		want = "AAAA"
	}
	var recs []Record
	if err := Call(token, "GET", "/zones/"+zone+"/dns_records?name="+url.QueryEscape(name), nil, &recs); err != nil {
		return "", err
	}
	done := false
	for _, rec := range recs {
		switch rec.Type {
		case want:
			if done { // a second record would send some clients elsewhere
				if err := Call(token, "DELETE", "/zones/"+zone+"/dns_records/"+rec.ID, nil, nil); err != nil {
					return "", err
				}
				continue
			}
			done = true
			if rec.Content == ip && rec.Proxied == proxied {
				continue
			}
			if err := Call(token, "PATCH", "/zones/"+zone+"/dns_records/"+rec.ID,
				J{"content": ip, "proxied": proxied}, nil); err != nil {
				return "", err
			}
		case "A", "AAAA":
			if err := Call(token, "DELETE", "/zones/"+zone+"/dns_records/"+rec.ID, nil, nil); err != nil {
				return "", err
			}
		case "CNAME":
			return "", fmt.Errorf("%s is a CNAME record: change it in Cloudflare yourself", name)
		}
	}
	if !done {
		if err := Call(token, "POST", "/zones/"+zone+"/dns_records",
			Record{Type: want, Name: name, Content: ip, Proxied: proxied, TTL: 1}, nil); err != nil {
			return "", err
		}
	}
	mode := "DNS only"
	if proxied {
		mode = "proxied"
	}
	return fmt.Sprintf("%s -> %s (%s)", name, ip, mode), nil
}

// NS delegates zone to the name server ns with an NS record.
func NS(token, zone, ns string) (string, error) {
	z, err := Zone(token, zone)
	if err != nil {
		return "", err
	}
	var recs []Record
	if err := Call(token, "GET", "/zones/"+z+"/dns_records?name="+url.QueryEscape(zone), nil, &recs); err != nil {
		return "", err
	}
	done := false
	for _, rec := range recs {
		switch {
		case rec.Type == "NS" && strings.EqualFold(rec.Content, ns) && !done:
			done = true
		default: // a delegated name can have no other records
			if err := Call(token, "DELETE", "/zones/"+z+"/dns_records/"+rec.ID, nil, nil); err != nil {
				return "", err
			}
		}
	}
	if !done {
		if err := Call(token, "POST", "/zones/"+z+"/dns_records", Record{Type: "NS", Name: zone, Content: ns, TTL: 1}, nil); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s NS %s (DNS tunnel)", zone, ns), nil
}

// SSL reads the SSL/TLS mode of domain's zone, and sets it to Full when fix is set and it is not.
func SSL(token, domain string, fix bool) (string, error) {
	zone, err := Zone(token, domain)
	if err != nil {
		return "", err
	}
	var s struct{ Value string }
	if err := Call(token, "GET", "/zones/"+zone+"/settings/ssl", nil, &s); err != nil {
		return "", err
	}
	if fix && s.Value != "full" {
		if err := Call(token, "PATCH", "/zones/"+zone+"/settings/ssl", J{"value": "full"}, nil); err != nil {
			return s.Value, err
		}
		return "full", nil
	}
	return s.Value, nil
}

type J = map[string]any

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
