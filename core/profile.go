package core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chethober/xtune/pkg/netinfo"
	"github.com/chethober/xtune/pkg/protocols"
)

// Profile is a subscription as the app keeps it.
type Profile = protocols.ClientProfile

// directClient makes requests straight, never through a proxy the system may have set (which
// may be this app's own).
var directClient = &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{Proxy: nil}}

// NormalizeSubURL accepts what users paste: the subscription link, its /json or /client form, or
// an xtune://import?url= link.
func NormalizeSubURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "xtune://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", err
		}
		s = u.Query().Get("url")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("not a subscription link (it should start with https://)")
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	for _, suf := range []string{"/json", "/client"} {
		u.Path = strings.TrimSuffix(u.Path, suf)
	}
	if strings.Count(strings.Trim(u.Path, "/"), "/") != 1 {
		return "", errors.New("not an xtune subscription link (expected https://HOST/PATH/TOKEN)")
	}
	return u.String(), nil
}

// FetchProfile gets the subscription's client profile, ordered for network asn ("AS12880" or
// "12880"; empty: the server looks it up). Servers from before the client endpoint get their
// Xray configs from the failover config instead.
func FetchProfile(subURL, asn string) (*Profile, error) {
	base, err := NormalizeSubURL(subURL)
	if err != nil {
		return nil, err
	}
	q := ""
	if a := strings.TrimPrefix(strings.ToUpper(asn), "AS"); a != "" {
		q = "?asn=" + url.QueryEscape(a)
	}
	resp, err := directGet(base + "/client" + q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		var p Profile
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&p); err != nil {
			return nil, fmt.Errorf("bad profile: %w", err)
		}
		if p.V > protocols.ClientProfileVersion {
			return nil, errors.New("this subscription needs a newer version of the app")
		}
		return &p, nil
	case 404:
		return legacyProfile(base)
	}
	return nil, fmt.Errorf("subscription server answered HTTP %d", resp.StatusCode)
}

func directGet(u string) (*http.Response, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xtune-client/"+Version)
	resp, err := directClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the subscription server: %w", err)
	}
	return resp, nil
}

// legacyProfile builds a profile from an older server's failover config and subscription headers.
func legacyProfile(base string) (*Profile, error) {
	resp, err := directGet(base)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errors.New("subscription not found: it may have been reset or removed")
	}
	p := &Profile{V: 0, Probe: protocols.ProbeURL}
	p.Usage = parseUserinfo(resp.Header.Get("Subscription-Userinfo"))
	p.Usage.Page = base
	if t := resp.Header.Get("Profile-Title"); strings.HasPrefix(t, "base64:") {
		b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(t, "base64:"))
		p.Name = string(b)
	}
	jr, err := directGet(base + "/json")
	if err != nil {
		return nil, err
	}
	defer jr.Body.Close()
	var cfg struct {
		Remarks   string          `json:"remarks"`
		Outbounds []protocols.J   `json:"outbounds"`
		Routing   json.RawMessage `json:"routing"`
	}
	if err := json.NewDecoder(io.LimitReader(jr.Body, 8<<20)).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("bad failover config: %w", err)
	}
	p.Name = orStr(p.Name, cfg.Remarks)
	for _, ob := range cfg.Outbounds {
		tag, _ := ob["tag"].(string)
		switch {
		case strings.HasPrefix(tag, "proxy-"):
			id := strings.TrimPrefix(tag, "proxy-")
			proto, _ := ob["protocol"].(string)
			p.Configs = append(p.Configs, protocols.ClientEntry{ID: id, Key: id, Name: id, Kind: proto, Label: strings.ToUpper(proto),
				Engine: "xray", Outbound: ob})
		case tag != "direct" && tag != "block":
			p.Frag = append(p.Frag, ob)
		}
	}
	if len(p.Configs) == 0 {
		return nil, errors.New("the subscription has no configs")
	}
	return p, nil
}

func parseUserinfo(h string) protocols.ClientUsage {
	var u protocols.ClientUsage
	for _, part := range strings.Split(h, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		switch k {
		case "upload":
			u.Up = n
		case "download":
			u.Down = n
		case "total":
			u.Quota = n
		case "expire":
			u.Expires = n
		}
	}
	return u
}

// Network is the network this device is on, looked up directly (not through the VPN).
type Network struct {
	ASN  string `json:"asn"`
	Name string `json:"name"`
}

func CurrentNetwork() (Network, error) {
	n, err := netinfo.Client()
	return Network{ASN: n.ASN, Name: n.Name}, err
}

func orStr(s, d string) string {
	if s != "" {
		return s
	}
	return d
}
