package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// Clean IPs: on many censored networks, part of Cloudflare's edge is blocked or throttled, and
// which part differs by ISP. A CDN or Worker config works there only if the client dials an edge
// IP that gets through, with the domain kept as SNI and Host. The scanner samples Cloudflare's
// published ranges from this machine: a TLS handshake for the domain, then /cdn-cgi/trace, which
// only a real Cloudflare edge answers. Tuning then measures the CDN and Worker configs through
// the best few, as one more client knob (Knobs.Addr): the honest ranking keeps the plain domain
// unless a clean IP measurably beats it.

// defaultCleanIPs is how many clean IPs tuning tries for the CDN and Worker configs.
const defaultCleanIPs = 3

// cfRanges are Cloudflare's IPv4 ranges (https://www.cloudflare.com/ips-v4).
var cfRanges = []string{"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
	"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15",
	"104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22"}

// CleanIP is one edge IP as measured from here.
type CleanIP struct {
	IP   string `json:"ip"`
	Ms   int    `json:"ms"` // median time to an answer from /cdn-cgi/trace
	Ok   int    `json:"ok"`
	N    int    `json:"n"`
	Colo string `json:"colo,omitempty"` // the edge's data center
}

type ScanOpts struct {
	SNI      string // the domain clients send: the CDN domain or the Worker's hostname
	Port     int
	Samples  int // IPs tried in the first pass
	Keep     int // IPs returned
	Parallel int
	Timeout  time.Duration
	Log      func(string)
}

func (o *ScanOpts) defaults() {
	o.SNI = orStr(o.SNI, "speed.cloudflare.com")
	if o.Port == 0 {
		o.Port = 443
	}
	if o.Samples == 0 {
		o.Samples = 300
	}
	if o.Keep == 0 {
		o.Keep = 5
	}
	if o.Parallel == 0 {
		o.Parallel = 32
	}
	if o.Timeout == 0 {
		o.Timeout = 2500 * time.Millisecond
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
}

// sampleCF picks n random addresses in Cloudflare's ranges, at most one per /24 so the sample
// spreads over the edge.
func sampleCF(n int, rnd *mrand.Rand) []string {
	var ps []netip.Prefix
	for _, r := range cfRanges {
		ps = append(ps, netip.MustParsePrefix(r))
	}
	seen := map[[3]byte]bool{}
	var out []string
	for tries := 0; len(out) < n && tries < n*20; tries++ {
		p := ps[rnd.IntN(len(ps))]
		a := p.Addr().As4()
		base := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
		size := uint32(1) << (32 - p.Bits())
		v := base + rnd.Uint32N(size)
		if b := byte(v); b == 0 || b == 255 {
			continue
		}
		ip := netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
		k := [3]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8)}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ip.String())
	}
	return out
}

// cfProbe is one try of an edge IP: TLS for sni, then /cdn-cgi/trace. It returns the colo. A
// variable so tests can stand in for the edge.
var cfProbe = func(ctx context.Context, ip string, port int, sni string) (string, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", hostPort(ip, port))
	if err != nil {
		return "", err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	tc := tls.Client(c, &tls.Config{ServerName: sni, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	if err := tc.HandshakeContext(ctx); err != nil {
		return "", err
	}
	fmt.Fprintf(tc, "GET /cdn-cgi/trace HTTP/1.1\r\nHost: %s\r\nUser-Agent: xtune/%s\r\nConnection: close\r\n\r\n", sni, version)
	return traceColo(io.LimitReader(tc, 8<<10))
}

// traceColo reads the colo= line of a /cdn-cgi/trace answer.
func traceColo(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first && !strings.Contains(line, " 200 ") {
			return "", fmt.Errorf("answered %q", line)
		}
		first = false
		if v, ok := strings.CutPrefix(line, "colo="); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("not a Cloudflare edge answer")
}

// ScanCleanIPs finds the Cloudflare edge IPs that answer best from this machine: one pass over a
// random sample, then three more tries of the fastest few.
func ScanCleanIPs(o ScanOpts) []CleanIP {
	o.defaults()
	rnd := mrand.New(mrand.NewPCG(uint64(time.Now().UnixNano()), 7))
	try := func(ips []string, rounds int) []CleanIP {
		res := make([]CleanIP, len(ips))
		var wg sync.WaitGroup
		sem := make(chan struct{}, o.Parallel)
		for i, ip := range ips {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, ip string) {
				defer wg.Done()
				defer func() { <-sem }()
				c := CleanIP{IP: ip, N: rounds}
				var times []int
				for range rounds {
					ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
					t0 := time.Now()
					colo, err := cfProbe(ctx, ip, o.Port, o.SNI)
					cancel()
					if err == nil {
						c.Ok++
						c.Colo = colo
						times = append(times, int(time.Since(t0).Milliseconds()))
					}
				}
				c.Ms = -1
				if len(times) > 0 {
					c.Ms = median(times)
				}
				res[i] = c
			}(i, ip)
		}
		wg.Wait()
		return res
	}
	rank := func(cs []CleanIP) []CleanIP {
		var ok []CleanIP
		for _, c := range cs {
			if c.Ok > 0 {
				ok = append(ok, c)
			}
		}
		sort.SliceStable(ok, func(i, j int) bool {
			if ok[i].Ok != ok[j].Ok {
				return ok[i].Ok > ok[j].Ok
			}
			return ok[i].Ms < ok[j].Ms
		})
		return ok
	}
	o.Log(fmt.Sprintf("[*] clean IPs: trying %d Cloudflare edge IPs for %s on port %d", o.Samples, o.SNI, o.Port))
	first := rank(try(sampleCF(o.Samples, rnd), 1))
	o.Log(fmt.Sprintf("[*] clean IPs: %d of %d answered", len(first), o.Samples))
	if len(first) == 0 {
		return nil
	}
	var top []string
	for i := 0; i < len(first) && i < 3*o.Keep; i++ {
		top = append(top, first[i].IP)
	}
	best := rank(try(top, 3))
	var out []CleanIP
	for _, c := range best {
		if len(out) < o.Keep && c.Ok >= 2 { // one lucky answer is not clean
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------- tuning through clean IPs

// cleanIPTargets are the CDN and Worker entries, by the hostname and port the scan must use.
func cleanIPTargets(st *State, entries []Entry) map[string][]Entry {
	out := map[string][]Entry{}
	for _, e := range entries {
		if !e.Xray() || !e.CDN() && !e.Worker() {
			continue
		}
		host, port := st.CDNDomain, e.Port
		if e.Worker() {
			host, port = e.SNI, 443
		}
		if host == "" {
			continue
		}
		k := hostPort(host, port)
		out[k] = append(out[k], e)
	}
	return out
}

// addCleanIPVariants scans for clean IPs for every CDN and Worker hostname and adds, for each
// such entry, its plainest variant dialed through each one.
func addCleanIPVariants(st *State, entries []Entry, vs []*Variant, keep int, log func(string)) []*Variant {
	if keep <= 0 {
		return vs
	}
	for k, es := range cleanIPTargets(st, entries) {
		host, p, _ := net.SplitHostPort(k)
		var port int
		fmt.Sscan(p, &port)
		ips := ScanCleanIPs(ScanOpts{SNI: host, Port: port, Keep: keep, Log: log})
		if len(ips) == 0 {
			log("[!] clean IPs: no Cloudflare edge IP answered for " + host + " from here")
			continue
		}
		var list []string
		for _, c := range ips {
			list = append(list, fmt.Sprintf("%s (%dms, %s)", c.IP, c.Ms, orStr(c.Colo, "?")))
		}
		log("[*] clean IPs for " + host + ": " + strings.Join(list, ", "))
		for _, e := range es {
			var base *Variant
			for _, v := range vs {
				if v.Entry.ID == e.ID && (base == nil || simplicity(v) < simplicity(base)) {
					base = v
				}
			}
			if base == nil {
				continue
			}
			for _, c := range ips {
				v := *base
				v.K.Addr = c.IP
				v.Label = base.Label + " ip=" + c.IP
				v.Downs, v.Ups = nil, nil
				vs = append(vs, &v)
			}
		}
	}
	return vs
}

// ---------------------------------------------------------------- command

func cmdCFScan(args []string) error {
	fs := flag.NewFlagSet("cfscan", flag.ExitOnError)
	c := addConnFlags(fs)
	o := ScanOpts{}
	fs.StringVar(&o.SNI, "sni", "", "the hostname clients send (default: the deployment's CDN domain, else speed.cloudflare.com)")
	fs.IntVar(&o.Port, "port", 0, "edge port (default: the CDN config's, else 443)")
	fs.IntVar(&o.Samples, "samples", 300, "edge IPs to try")
	fs.IntVar(&o.Keep, "n", 10, "IPs to list")
	parse(fs, args)
	if (*c.host != "" || *c.profile != "") && o.SNI == "" {
		r, err := connect(c)
		if err != nil {
			return err
		}
		st, err := LoadState(r)
		r.Close()
		if err != nil {
			return err
		}
		if st != nil {
			for _, e := range st.Entries {
				if e.CDN() && e.Xray() && o.Port == 0 {
					o.Port = e.Port
				}
			}
			o.SNI = st.CDNDomain
		}
	}
	o.Log = func(s string) { fmt.Println(s) }
	ips := ScanCleanIPs(o)
	if len(ips) == 0 {
		return errors.New("no Cloudflare edge IP answered from this network")
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "\nIP\tMS\tOK\tCOLO\n")
	for _, c := range ips {
		fmt.Fprintf(tw, "%s\t%d\t%d/%d\t%s\n", c.IP, c.Ms, c.Ok, c.N, c.Colo)
	}
	tw.Flush()
	fmt.Println("\nTune with these (the CDN and Worker configs are measured through the best ones): xtune tune --clean-ips 3")
	return nil
}
