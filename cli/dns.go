package main

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// Without cgo, Go reads nameservers from /etc/resolv.conf only. Android (Termux) has none, so
// every lookup went to [::1]:53 and failed. There, xtune asks these servers instead, in order:
// XTUNE_DNS (comma-separated), Termux's own resolv.conf, then public resolvers.
var publicDNS = []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "208.67.222.222"}

func init() {
	if runtime.GOOS != "linux" && runtime.GOOS != "android" {
		return
	}
	if _, err := os.Stat("/etc/resolv.conf"); err == nil && os.Getenv("XTUNE_DNS") == "" {
		return
	}
	termux := ""
	if p := os.Getenv("PREFIX"); p != "" {
		termux = filepath.Join(p, "etc/resolv.conf")
	}
	servers := fallbackNameservers(os.Getenv("XTUNE_DNS"), termux)
	var next atomic.Uint32
	net.DefaultResolver = &net.Resolver{PreferGo: true,
		// Go retries a failed query by dialing again: each dial takes the next server, so a
		// blocked resolver costs one timeout instead of failing the lookup.
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			s := servers[int(next.Add(1)-1)%len(servers)]
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, s)
		}}
}

// fallbackNameservers returns host:port nameservers: env's, then those in resolvConf ("" = none),
// then the public resolvers, without duplicates.
func fallbackNameservers(env, resolvConf string) []string {
	var hosts []string
	for _, h := range strings.Split(env, ",") {
		hosts = append(hosts, strings.TrimSpace(h))
	}
	hosts = append(hosts, resolvConfServers(resolvConf)...)
	hosts = append(hosts, publicDNS...)
	var out []string
	seen := map[string]bool{}
	for _, h := range hosts {
		if h == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(h); err != nil {
			h = net.JoinHostPort(strings.Trim(h, "[]"), "53")
		}
		if ip, _, _ := net.SplitHostPort(h); net.ParseIP(ip) == nil || net.ParseIP(ip).IsLoopback() || seen[h] {
			continue // a hostname can't be resolved yet, and loopback is the thing that failed
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

func resolvConfServers(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}
