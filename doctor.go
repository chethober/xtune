package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// `xtune doctor` checks the things that break a deployment without an obvious error: services,
// ports, DNS records, the CDN's SSL mode, clocks and certificates.

type finding struct {
	Level string // ok | warn | fail
	Text  string
}

type doctor struct{ out []finding }

func (d *doctor) add(level, f string, a ...any) {
	d.out = append(d.out, finding{level, fmt.Sprintf(f, a...)})
}
func (d *doctor) ok(f string, a ...any)   { d.add("ok", f, a...) }
func (d *doctor) warn(f string, a ...any) { d.add("warn", f, a...) }
func (d *doctor) fail(f string, a ...any) { d.add("fail", f, a...) }

func (d *doctor) print(w io.Writer) (fails int) {
	for _, f := range d.out {
		mark := map[string]string{"ok": "  ok  ", "warn": " warn ", "fail": " FAIL "}[f.Level]
		fmt.Fprintf(w, "%s %s\n", mark, f.Text)
		if f.Level == "fail" {
			fails++
		}
	}
	return fails
}

// cdnErrors are Cloudflare's answers when it cannot use the origin.
var cdnErrors = map[int]string{
	520: "the server answered something the CDN did not expect: set SSL/TLS mode to Full (not Flexible)",
	521: "the CDN cannot reach the server: firewall, or Xray not running",
	522: "the CDN's connection to the server timed out: firewall or a wrong origin IP",
	523: "the CDN cannot route to the server: check the record's IP",
	525: "TLS between the CDN and the server failed: set SSL/TLS mode to Full",
	526: "SSL/TLS mode is Full (strict), which rejects the server's self-signed certificate: set it to Full",
}

// Doctor runs every check against r, from a client over SSH or on the server itself.
func Doctor(r *Server) *doctor {
	d := &doctor{}
	ss, err := r.Status()
	if err != nil {
		d.fail("server status: %v", err)
		return d
	}
	st, err := LoadState(r)
	if err != nil {
		d.fail("state.json: %v", err)
	}
	if ss.Xray == "none" {
		d.fail("xtune's Xray is not installed on the server (it is installed by the first tune)")
	} else {
		d.ok("Xray %s installed", ss.Xray)
	}
	if st == nil {
		d.warn("nothing is deployed yet")
		return d
	}
	if ss.Unit == "active" {
		d.ok("xray-autotune is running with %d configs", len(st.Entries))
	} else {
		d.fail("xray-autotune is %s (see: xtune logs)", orStr(ss.Unit, "not running"))
	}
	switch {
	case ss.Xtune == "none":
		d.fail("xtune is not installed on the server: users, limits and subscriptions do not work (open the console to install it)")
	case !r.Local && version != "dev" && ss.Xtune != version:
		d.warn("the server runs xtune %s, this computer %s: open the console (or deploy) to update it", ss.Xtune, version)
	default:
		d.ok("xtune %s on the server", ss.Xtune)
	}
	if ss.Timer == "active" {
		d.ok("usage and limits job is running")
	} else {
		d.fail("the usage and limits job is %s: quotas, expiry and device limits are not enforced", orStr(ss.Timer, "off"))
	}
	if st.Sub.Mode != "" {
		if ss.Sub == "active" {
			d.ok("subscription server is running (%s mode)", st.Sub.Mode)
		} else {
			d.fail("the subscription server is %s (see: xtune logs --unit xray-autotune-sub)", orStr(ss.Sub, "not running"))
		}
	}
	if st.Bot.On() {
		if ss.Bot == "active" {
			d.ok("Telegram bot is running")
		} else {
			d.fail("the Telegram bot is %s (see: xtune logs --unit xray-autotune-bot)", orStr(ss.Bot, "not running"))
		}
	}
	info, err := r.Helper("doctor")
	if err != nil {
		d.fail("server checks: %v", err)
		return d
	}
	fields := map[string]string{}
	for _, l := range strings.Split(info, "\n") {
		if k, v, ok := strings.Cut(l, " "); ok && k != "USED" {
			fields[k] = v
		}
	}
	doctorClock(d, r, fields)
	used := usedPorts(info)
	hops := 0
	for _, e := range st.Entries {
		if !used[e.Proto()][e.Port] {
			d.fail("%s: nothing listens on %d/%s", e.ID, e.Port, e.Proto())
		}
		if e.Hop != "" {
			hops++
		}
	}
	if n, _ := strconv.Atoi(fields["HOPRULES"]); hops > 0 && n == 0 {
		d.fail("port hopping is deployed but its forwarding rules are missing: restart Xray to restore them (sudo systemctl restart xray-autotune)")
	}
	switch ss.FW {
	case "ufw", "firewalld":
		d.ok("firewall %s is active; xtune opens its own ports in it", ss.FW)
	default:
		d.ok("no host firewall active (check your provider's firewall allows the deployed ports)")
	}
	doctorDNS(d, st)
	doctorCDN(d, st)
	doctorCerts(d, st)
	return d
}

func doctorClock(d *doctor, r *Server, f map[string]string) {
	if f["NTP"] == "no" {
		d.warn("the server's clock is not synchronized (timedatectl set-ntp true)")
	}
	if r.Local {
		return
	}
	t, err := strconv.ParseInt(f["TIME"], 10, 64)
	if err != nil {
		return
	}
	skew := time.Since(time.Unix(t, 0)).Round(time.Second)
	if skew < 0 {
		skew = -skew
	}
	if skew > time.Minute {
		d.warn("this computer's clock and the server's differ by %s: TLS and REALITY can fail; sync both clocks", skew)
	} else {
		d.ok("clocks agree (%s apart)", skew)
	}
}

// lookup is a variable so tests can fake DNS.
var lookup = net.LookupHost

func doctorDNS(d *doctor, st *State) {
	server := map[string]bool{st.Host: true}
	if ips, err := lookup(st.Host); err == nil {
		for _, ip := range ips {
			server[ip] = true
		}
	}
	points := func(name string) (bool, []string, error) {
		ips, err := lookup(name)
		for _, ip := range ips {
			if server[ip] {
				return true, ips, err
			}
		}
		return false, ips, err
	}
	if st.Domain != "" {
		switch ok, ips, err := points(st.Domain); {
		case err != nil:
			d.fail("direct domain %s does not resolve: %v", st.Domain, err)
		case !ok:
			d.fail("direct domain %s points at %s, not the server %s (DNS only / grey cloud?)", st.Domain, strings.Join(ips, " "), st.Host)
		default:
			d.ok("direct domain %s points at the server", st.Domain)
		}
	}
	if st.CDNDomain != "" {
		switch ok, _, err := points(st.CDNDomain); {
		case err != nil:
			d.fail("CDN domain %s does not resolve: %v", st.CDNDomain, err)
		case ok:
			d.fail("CDN domain %s points straight at the server: turn the CDN proxy on (orange cloud)", st.CDNDomain)
		default:
			d.ok("CDN domain %s goes through the CDN", st.CDNDomain)
		}
	}
}

// cdnGet is a variable so tests can fake the CDN.
var cdnGet = func(url string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
	return resp, err
}

// doctorCDN asks the CDN for the CDN entry's path: the answer shows whether it reaches the server.
// With a Cloudflare token, the SSL/TLS mode is also read from the API.
func doctorCDN(d *doctor, st *State) {
	if st.Cloudflare.Token != "" && st.CDNDomain != "" && hasCDN(st.Entries) {
		switch mode, err := cfSSL(st.Cloudflare.Token, st.CDNDomain, false); {
		case err != nil:
			d.warn("could not read the SSL/TLS mode from Cloudflare: %v", err)
		case mode == "full":
			d.ok("Cloudflare SSL/TLS mode of %s is Full", st.CDNDomain)
		default:
			d.fail("Cloudflare SSL/TLS mode of %s is %s; the CDN entry needs Full: run xtune cloudflare ssl on the server", st.CDNDomain, mode)
		}
	}
	for _, e := range st.Entries {
		if !e.CDN() || st.CDNDomain == "" {
			continue
		}
		resp, err := cdnGet("https://" + hostPort(st.CDNDomain, e.Port) + e.Path)
		switch {
		case err != nil:
			d.warn("could not reach %s through the CDN from here: %v", st.CDNDomain, err)
		case resp.Header.Get("Cf-Ray") == "" && resp.Header.Get("Server") != "cloudflare":
			d.ok("%s answers on port %d (not Cloudflare, so the SSL mode was not checked)", st.CDNDomain, e.Port)
		case cdnErrors[resp.StatusCode] != "":
			d.fail("%s: HTTP %d: %s", e.ID, resp.StatusCode, cdnErrors[resp.StatusCode])
		default:
			d.ok("%s reaches the server through the CDN (HTTP %d)", e.ID, resp.StatusCode)
		}
	}
}

// tlsCert is a variable so tests can fake the subscription server's certificate.
var tlsCert = func(addr, name string) (*x509.Certificate, error) {
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{ServerName: name})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0], nil
}

func doctorCerts(d *doctor, st *State) {
	if blk, _ := pem.Decode([]byte(st.CertPEM)); blk != nil {
		if c, err := x509.ParseCertificate(blk.Bytes); err == nil && time.Until(c.NotAfter) < 30*24*time.Hour {
			d.fail("the server's self-signed certificate expires %s: re-tune and deploy to renew it", c.NotAfter.Format("2006-01-02"))
		}
	}
	if st.Sub.Mode != "direct" {
		return
	}
	c, err := tlsCert(hostPort(st.Domain, st.Sub.Port), st.Domain)
	switch {
	case err != nil:
		d.fail("subscription server https://%s: %v", hostPort(st.Domain, st.Sub.Port), err)
	case time.Until(c.NotAfter) < 14*24*time.Hour:
		d.warn("the subscription server's Let's Encrypt certificate expires %s and was not renewed yet", c.NotAfter.Format("2006-01-02"))
	default:
		d.ok("subscription server certificate valid until %s", c.NotAfter.Format("2006-01-02"))
	}
}
