package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

const (
	probeURL = "https://www.gstatic.com/generate_204"
	upURL    = "https://speed.cloudflare.com/__up"
)

var sniCandidates = []string{"www.samsung.com", "www.nvidia.com", "www.amd.com", "www.microsoft.com", "dl.google.com",
	"www.speedtest.net", "www.intel.com", "www.sony.com", "swdist.apple.com", "addons.mozilla.org"}
var preferredPorts = []int{443, 8443, 2053, 2083, 2087, 2096, 8880, 2052, 2082, 2086, 2095}

// cdnPorts are the HTTPS ports Cloudflare proxies; 443 comes last so REALITY can keep it.
var cdnPorts = []int{2053, 2083, 2087, 2096, 8443, 443}

type TuneOpts struct {
	Depth                         string // smart | deep
	Probes, Parallel, Top, Rounds int
	Secs                          int
	Out                           string
	XrayBin                       string
	ServerTag                     string   // "" = keep / latest
	Only                          []string // test only entries whose ID starts with one of these
	Domain, CDNDomain             string   // optional direct and CDN-proxied domains
	KeepTest                      bool     // leave the server test instance running (it expires by itself)
	Network                       string   // name of the network measured from, for the history
	Protocols                     []string // opt-in protocol families: ssh, dnstt, awg, worker, openvpn, ikev2
	DNSDomain                     string   // zone delegated to the server, for the DNS tunnel
	Resolvers                     []string // DNS tunnel resolvers to try first ("udp:IP:53", "doh:URL", "dot:HOST:853")
	Cloudflare                    string   // API token ("" = the deployment's): DNS records for new domains, the Worker relay
	Zone                          string   // Cloudflare zone: domains not given get random names in it
	Log                           func(string)

	deployed *State  // the server's deployment, if any
	worker   *Worker // the Worker the test entries go through
}

// optionalProtocols can be added to the test matrix with --protocols.
var optionalProtocols = []string{"ssh", "dnstt", "awg", "worker", "openvpn", "ikev2"}

// parseProtocols checks a --protocols list; "all" means every optional protocol.
func parseProtocols(list string) ([]string, error) {
	var out []string
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
		case p == "all":
			return append([]string(nil), optionalProtocols...), nil
		case p == "vpn":
			out = append(out, "openvpn", "ikev2")
		case contains2s(optionalProtocols, p):
			out = append(out, p)
		default:
			return nil, fmt.Errorf("unknown protocol %q (choose from %s, vpn, all)", p, strings.Join(optionalProtocols, ", "))
		}
	}
	return out, nil
}

func (o *TuneOpts) defaults() {
	if o.Depth == "" {
		o.Depth = "deep"
	}
	if o.Probes == 0 {
		o.Probes = 5
	}
	if o.Parallel == 0 {
		o.Parallel = 6
	}
	if o.Top == 0 {
		o.Top = 30
	}
	if o.Rounds == 0 {
		o.Rounds = 2
	}
	if o.Secs == 0 {
		o.Secs = 8
	}
	if o.Out == "" {
		o.Out = "xtune-results"
	}
	if o.Log == nil {
		o.Log = func(s string) { fmt.Println(s) }
	}
}

type Variant struct {
	Entry      Entry
	K          Knobs
	Label      string
	port       int
	Ok, N      int
	Cold, Warm int // ms, -1 = none
	Downs, Ups []float64
	Down, Up   float64
	Benched    bool
	Score      float64

	dial      func(ctx context.Context, network, addr string) (net.Conn, error) // non-Xray tunnels
	handshake func(ctx context.Context) (time.Duration, error)                  // VPNs: handshake only
}

// client is an HTTP client through the variant: its local Xray inbound, or its own dialer.
func (v *Variant) client(timeout time.Duration) *http.Client {
	if v.dial == nil {
		return socksClient(v.port, timeout)
	}
	return &http.Client{Transport: uaTransport{&http.Transport{Proxy: nil, DialContext: v.dial,
		DisableKeepAlives: true, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout}}}
}

// ---------------------------------------------------------------- plan

// usedPorts parses the helper's "USED proto addr:port" lines.
func usedPorts(probeOut string) map[string]map[int]bool {
	used := map[string]map[int]bool{"tcp": {}, "udp": {}}
	for _, line := range strings.Split(probeOut, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "USED" {
			proto := "tcp"
			if strings.HasPrefix(f[1], "udp") {
				proto = "udp"
			}
			if i := strings.LastIndex(f[2], ":"); i >= 0 {
				p, _ := strconv.Atoi(f[2][i+1:])
				used[proto][p] = true
			}
		}
	}
	return used
}

// cdnEntry returns a CDN entry on a free CDN-proxied port, or false when none is free.
func cdnEntry(used map[string]map[int]bool, taken map[int]bool) (Entry, bool) {
	for _, p := range cdnPorts {
		if !used["tcp"][p] && !taken[p] {
			taken[p] = true
			return Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: p, Path: "/" + randHex(6)}, true
		}
	}
	return Entry{}, false
}

// PlanEntries picks REALITY targets and ports for the test matrix. With cdn, a CDN entry is
// added first, because only a few ports pass through the CDN.
func PlanEntries(probeOut string, cdn bool) ([]Entry, []string) {
	return planEntries(probeOut, PlanOpts{CDN: cdn})
}

// PlanOpts are what the test matrix holds besides the default configs.
type PlanOpts struct {
	CDN       bool
	Protocols []string // opt-in families, see optionalProtocols
	DNS       bool     // a DNS tunnel domain is set
	IKEBusy   bool     // UDP 500 is taken by something other than xtune's IKEv2
	Worker    bool     // a Cloudflare token is set, for the Worker relay
}

func planEntries(probeOut string, o PlanOpts) ([]Entry, []string) {
	cdn := o.CDN
	type sni struct {
		t    float64
		host string
	}
	var snis []sni
	for _, line := range strings.Split(probeOut, "\n") {
		f := strings.Fields(line)
		if len(f) == 4 && f[0] == "SNI" && f[2] == "2" { // HTTP/2 over TLS 1.3 = usable REALITY target
			t, _ := strconv.ParseFloat(f[3], 64)
			snis = append(snis, sni{t, f[1]})
		}
	}
	used := usedPorts(probeOut)
	sort.Slice(snis, func(i, j int) bool { return snis[i].t < snis[j].t })
	var hosts []string
	for i := 0; i < len(snis) && i < 3; i++ {
		hosts = append(hosts, snis[i].host)
	}
	if len(hosts) == 0 {
		hosts = []string{"www.microsoft.com"}
	}
	taken := map[int]bool{}
	var cdnE []Entry
	want := func(p string) bool { return slices.Contains(o.Protocols, p) }
	if cdn {
		if e, ok := cdnEntry(used, taken); ok {
			cdnE = append(cdnE, e)
		}
		if want("ssh") { // SSH over WebSocket through the CDN, on another of its few ports
			if e, ok := cdnEntry(used, taken); ok {
				cdnE = append(cdnE, Entry{ID: "cdn-ssh", Kind: "cdn-ssh", Port: e.Port})
			}
		}
	}
	port := func(proto string, preferred bool) int {
		if preferred {
			for _, p := range preferredPorts {
				if !used[proto][p] && !taken[p] {
					taken[p] = true
					return p
				}
			}
		}
		for {
			p := 20000 + mrand.IntN(30000)
			if !used["tcp"][p] && !used["udp"][p] && !taken[p] {
				taken[p] = true
				return p
			}
		}
	}
	short := func(h string) string {
		parts := strings.Split(h, ".")
		if len(parts) >= 2 {
			return parts[len(parts)-2]
		}
		return h
	}
	E := []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: port("tcp", true), SNI: hosts[0], Primary: true}}
	for _, h := range hosts[1:] {
		E = append(E, Entry{ID: "r-tcp-" + short(h), Kind: "reality-tcp", Port: port("tcp", true), SNI: h})
	}
	E = append(E,
		Entry{ID: "r-tcp-hiport", Kind: "reality-tcp", Port: port("tcp", false), SNI: hosts[0]},
		Entry{ID: "r-xhttp", Kind: "reality-xhttp", Port: port("tcp", true), SNI: hosts[0], Path: "/" + randHex(6)},
		Entry{ID: "r-grpc", Kind: "reality-grpc", Port: port("tcp", true), SNI: hosts[0], Svc: randHex(5)},
		Entry{ID: "venc-tcp", Kind: "vlessenc-tcp", Port: port("tcp", false)},
		Entry{ID: "venc-xhttp", Kind: "vlessenc-xhttp", Port: port("tcp", false), Path: "/" + randHex(6)},
		Entry{ID: "hy2", Kind: "hysteria2", Port: port("udp", true)},
		// UDP 443 (QUIC) is filtered on many censored networks; a high port often is not
		Entry{ID: "hy2-hiport", Kind: "hysteria2", Port: port("udp", false)},
		Entry{ID: "ss2022", Kind: "ss2022", Port: port("tcp", false)},
	)
	nat := strings.Contains(probeOut, "\nHOP yes") || strings.HasPrefix(probeOut, "HOP yes")
	if nat {
		// port hopping: the server forwards a UDP range to one port, the client keeps changing ports
		if hop, ok := hopRange(used["udp"], taken); ok {
			E = append(E, Entry{ID: "hy2-hop", Kind: "hysteria2", Port: port("udp", false), Hop: hop})
		}
	}
	E = append(E, Entry{ID: "wg", Kind: "wireguard", Port: port("udp", false)},
		Entry{ID: "wg-std", Kind: "wireguard", Port: port("udp", true)})
	if want("awg") {
		E = append(E, Entry{ID: "awg", Kind: "amneziawg", Port: port("udp", false), Subnet: awgSubnet(0, true)},
			Entry{ID: "awg-std", Kind: "amneziawg", Port: port("udp", true), Subnet: awgSubnet(1, true)})
	}
	if want("worker") && o.Worker { // workers.dev, and a hostname on the user's zone when there is one
		E = append(E, Entry{ID: "worker", Kind: "worker-ws", Port: port("tcp", false), Path: "/" + randHex(6)},
			Entry{ID: "worker-dom", Kind: "worker-ws", Port: port("tcp", false), Path: "/" + randHex(6)})
	}
	if want("ssh") {
		E = append(E, Entry{ID: "ssh", Kind: "ssh", Port: port("tcp", true)},
			Entry{ID: "ssh-hiport", Kind: "ssh", Port: port("tcp", false)},
			Entry{ID: "ssh-tls", Kind: "ssh-tls", Port: port("tcp", true), SNI: hosts[0]})
	}
	if want("dnstt") && o.DNS && nat && !used["udp"][53] {
		// public UDP 53 is forwarded to the tunnel's port (systemd-resolved keeps 127.0.0.53:53)
		E = append(E, Entry{ID: "dnstt", Kind: "dnstt", Port: port("udp", false)})
	}
	if want("openvpn") {
		p := 1194
		if used["udp"][p] || taken[p] {
			p = port("udp", false)
		}
		taken[p] = true
		E = append(E, Entry{ID: "ovpn-udp", Kind: "openvpn-udp", Port: p}, Entry{ID: "ovpn-tcp", Kind: "openvpn-tcp", Port: port("tcp", true)})
	}
	if want("ikev2") && !o.IKEBusy {
		E = append(E, Entry{ID: "ikev2", Kind: "ikev2", Port: 500})
	}
	return append(E, cdnE...), hosts
}

// hopRange picks a 1000-port UDP range with no port in use or taken.
func hopRange(used, taken map[int]bool) (string, bool) {
	for try := 0; try < 50; try++ {
		lo := 20000 + 1000*mrand.IntN(30)
		free := true
		for p := lo; p < lo+1000 && free; p++ {
			free = !used[p] && !taken[p]
		}
		if free {
			for p := lo; p < lo+1000; p++ {
				taken[p] = true
			}
			return fmt.Sprintf("%d-%d", lo, lo+999), true
		}
	}
	return "", false
}

// portSpecs are the helper's firewall arguments for entries: "443/tcp", plus "hop:LO-HI:PORT"
// for a port-hopping range forwarded to PORT.
func portSpecs(es []Entry) []string {
	var specs []string
	for _, e := range es {
		specs = append(specs, fmt.Sprintf("%d/%s", e.Port, e.Proto()))
		if e.Hop != "" {
			specs = append(specs, fmt.Sprintf("hop:%s:%d", e.Hop, e.Port))
		}
		switch e.Kind {
		case "dnstt": // resolvers ask on port 53
			specs = append(specs, fmt.Sprintf("hop:53-53:%d", e.Port))
		case "ikev2": // NAT traversal
			specs = append(specs, "4500/udp")
		}
	}
	return specs
}

// ExpandVariants lists every client setting to measure for each entry. resolvers are tried first
// for the DNS tunnel, before this machine's and the public ones.
func ExpandVariants(entries []Entry, depth string, resolvers ...string) []*Variant {
	fps := []string{"chrome", "firefox", "randomized"}
	modes := []string{"auto", "packet-up", "stream-one"}
	if depth == "deep" {
		fps = []string{"chrome", "firefox", "safari", "edge", "randomized"}
		modes = []string{"auto", "packet-up", "stream-up", "stream-one"}
	}
	var out []*Variant
	for _, e := range entries {
		var ks []Knobs
		switch {
		case e.Kind == "reality-tcp" && e.Primary:
			for _, f := range fps {
				for _, g := range fragNames {
					ks = append(ks, Knobs{FP: f, Frag: g})
				}
			}
		case e.Kind == "reality-tcp":
			ks = []Knobs{{FP: "chrome", Frag: "none"}, {FP: "chrome", Frag: "frag-mild"}}
		case e.Kind == "reality-xhttp" || e.Kind == "cdn-xhttp":
			for _, f := range fps[:2] {
				for _, g := range fragNames {
					ks = append(ks, Knobs{FP: f, Frag: g, Mode: "auto"})
				}
			}
			for _, m := range modes[1:] {
				ks = append(ks, Knobs{FP: "chrome", Frag: "none", Mode: m})
			}
			if e.CDN() { // upload through the CDN, download straight from the server
				for _, m := range []string{"packet-up", "stream-up"} {
					ks = append(ks, Knobs{FP: "chrome", Frag: "none", Mode: m, Split: "cdn-up"})
				}
			}
		case e.Kind == "reality-grpc":
			for _, g := range fragNames {
				ks = append(ks, Knobs{FP: "chrome", Frag: g})
			}
		case e.Kind == "vlessenc-xhttp":
			for _, m := range modes {
				ks = append(ks, Knobs{Frag: "none", Mode: m})
			}
		case e.Kind == "wireguard": // a smaller MTU gets past paths that drop large UDP packets
			ks = []Knobs{{Frag: "none", MTU: 1420}, {Frag: "none", MTU: 1280}}
		case e.Kind == "amneziawg":
			ks = awgVariants()
		case e.Kind == "worker-ws": // fragmenting the ClientHello hides workers.dev from SNI filters
			for _, g := range fragNames {
				ks = append(ks, Knobs{FP: "chrome", Frag: g})
			}
			ks = append(ks, Knobs{FP: "firefox", Frag: "none"})
		case e.Kind == "ssh" || e.Kind == "ssh-tls": // plain, or opening with a WebSocket upgrade request
			ks = []Knobs{{Frag: "none"}, {Frag: "none", Mode: "ws"}}
		case e.Kind == "cdn-ssh":
			ks = []Knobs{{Frag: "none", Mode: "ws"}}
		case e.Kind == "dnstt":
			rs := append(resolvers, localResolvers()...)
			if depth == "deep" {
				rs = append(rs, dnsResolvers...)
			} else {
				rs = append(rs, dnsResolvers[0], dnsResolvers[1], dnsResolvers[3])
			}
			seen := map[string]bool{}
			for _, r := range rs {
				if !seen[r] {
					seen[r] = true
					ks = append(ks, Knobs{Frag: "none", Resolver: r})
				}
			}
		default:
			ks = []Knobs{{Frag: "none"}}
		}
		for _, k := range ks {
			v := &Variant{Entry: e, K: k, Cold: -1, Warm: -1}
			v.Label = fmt.Sprintf("%s:%d", e.ID, e.Port)
			if e.Reality() {
				parts := strings.Split(e.SNI, ".")
				v.Label += " sni=" + parts[len(parts)-2]
			}
			if k.FP != "" {
				v.Label += " fp=" + k.FP
			}
			if k.Frag != "none" {
				v.Label += " " + k.Frag
			}
			if k.Mode != "" {
				v.Label += " mode=" + k.Mode
			}
			if k.Split != "" {
				v.Label += " split=" + k.Split
			}
			if e.Hop != "" {
				v.Label += " hop=" + e.Hop
			}
			if k.MTU != 0 {
				v.Label += fmt.Sprintf(" mtu=%d", k.MTU)
			}
			if k.Resolver != "" {
				v.Label += " via=" + k.Resolver
			}
			out = append(out, v)
		}
	}
	return out
}

// ---------------------------------------------------------------- local xray harness

type harness struct {
	cmd     *exec.Cmd
	logFile *os.File
	once    sync.Once
	closers []func() // in-process tunnels (AmneziaWG)
}

func freePorts(n int) ([]int, error) {
	var ls []net.Listener
	var ports []int
	defer func() {
		for _, l := range ls {
			l.Close()
		}
	}()
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		ls = append(ls, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

// startHarness gets every variant ready to measure: Xray variants through one local Xray, SSH and
// DNS tunnels through xtune's own client, AmneziaWG in-process, VPNs as handshake probes. On a test
// instance (testPeers), WireGuard and AmneziaWG variants connect as the test peers the server has
// for them (wgTestPeers, awgTestPeers).
func startHarness(bin string, st *State, u User, all []*Variant, dir string, testPeers bool) (*harness, error) {
	var vs []*Variant
	h := &harness{}
	for _, v := range all {
		switch e := v.Entry; {
		case e.HandshakeOnly():
			v.handshake = func(ctx context.Context) (time.Duration, error) { return st.vpnHandshake(ctx, e) }
		case e.SSH():
			v.dial = st.tunnelDial(e, u, v.K)
		case e.Kind == "amneziawg":
			au := u
			if testPeers {
				au = awgVariantUser(u, v.K)
			}
			d := st.awgDial(e, au, v.K)
			v.dial = d.DialContext
			h.closers = append(h.closers, d.Close)
		default:
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return h, nil
	}
	ports, err := freePorts(len(vs))
	if err != nil {
		h.stop()
		return nil, err
	}
	cfg := J{"log": J{"loglevel": "error"}, "inbounds": []J{}, "outbounds": fragOutbounds(), "routing": J{"rules": []J{}}}
	for i, v := range vs {
		v.port = ports[i]
		cfg["inbounds"] = append(cfg["inbounds"].([]J), J{"tag": fmt.Sprintf("in%d", i), "listen": "127.0.0.1",
			"port": v.port, "protocol": "socks", "settings": J{"udp": true}})
		ou := u
		if v.Entry.Kind == "wireguard" && testPeers {
			ou = wgVariantUser(u, v.K)
		}
		cfg["outbounds"] = append(cfg["outbounds"].([]J), st.Outbound(v.Entry, ou, v.K, fmt.Sprintf("out%d", i)))
		rules := cfg["routing"].(J)["rules"].([]J)
		cfg["routing"].(J)["rules"] = append(rules, J{"inboundTag": []string{fmt.Sprintf("in%d", i)}, "outboundTag": fmt.Sprintf("out%d", i)})
	}
	path := filepath.Join(dir, "harness.json")
	if err := writeJSON(path, cfg); err != nil {
		h.stop()
		return nil, err
	}
	if out, err := exec.Command(bin, "run", "-test", "-c", path).CombinedOutput(); err != nil {
		h.stop()
		return nil, fmt.Errorf("local xray rejected the test config: %v\n%s", err, tail(string(out), 1500))
	}
	lf, err := os.Create(filepath.Join(dir, "xray.log"))
	if err != nil {
		h.stop()
		return nil, err
	}
	cmd := exec.Command(bin, "run", "-c", path)
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		lf.Close()
		h.stop()
		return nil, err
	}
	h.cmd, h.logFile = cmd, lf
	last := vs[len(vs)-1].port
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", last), 300*time.Millisecond); err == nil {
			c.Close()
			return h, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.stop()
	return nil, fmt.Errorf("local xray did not start (see %s)", lf.Name())
}

func (h *harness) stop() {
	h.once.Do(func() {
		for _, c := range h.closers {
			c()
		}
		if h.cmd == nil {
			return
		}
		if h.cmd.Process != nil {
			h.cmd.Process.Kill()
			h.cmd.Wait()
		}
		h.logFile.Close()
	})
}

// ---------------------------------------------------------------- measurements

// uaTransport sets a User-Agent: some speed-test mirrors (Hetzner) drop Go's default one.
type uaTransport struct{ http.RoundTripper }

func (u uaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("User-Agent", "xtune/"+version)
	return u.RoundTripper.RoundTrip(r)
}

func socksClient(port int, timeout time.Duration) *http.Client {
	d, _ := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, &net.Dialer{Timeout: timeout})
	cd := d.(proxy.ContextDialer)
	return &http.Client{Transport: uaTransport{&http.Transport{Proxy: nil, DialContext: cd.DialContext,
		DisableKeepAlives: true, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout}}}
}

func probe(v *Variant, n int) {
	timeout := 8 * time.Second
	if v.Entry.Kind == "dnstt" { // a fresh tunnel, SSH login and request, all over DNS
		timeout = 30 * time.Second
	}
	c := v.client(timeout)
	var ok []int
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		t0 := time.Now()
		var good bool
		if v.handshake != nil {
			_, err := v.handshake(ctx)
			good = err == nil
		} else {
			req, _ := http.NewRequestWithContext(ctx, "GET", probeURL, nil)
			resp, err := c.Do(req)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			good = err == nil && resp.StatusCode == 204
		}
		ms := int(time.Since(t0).Milliseconds())
		cancel()
		if i == 0 && good {
			v.Cold = ms
		}
		if good {
			ok = append(ok, ms)
		}
	}
	v.Ok, v.N = len(ok), n
	if len(ok) > 0 {
		warm := ok
		if len(ok) > 1 {
			warm = ok[1:]
		}
		v.Warm = median(warm)
	}
}

// Download mirrors, tried in order. One that refuses (Cloudflare answers 429 after a few dozen
// runs from one IP) is dropped for the rest of the run, so all variants share one source.
var (
	mirrorsMu sync.Mutex
	mirrors   = []string{"https://nbg1-speed.hetzner.com/1GB.bin", "https://proof.ovh.net/files/1Gb.dat",
		"https://fsn1-speed.hetzner.com/1GB.bin", "https://speed.cloudflare.com/__down?bytes=99000000"}
	upPayload []byte
	upOnce    sync.Once
)

type countReader struct {
	r  io.Reader
	n  int64
	mu sync.Mutex
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.mu.Lock()
	c.n += int64(n)
	c.mu.Unlock()
	return n, err
}

func download(v *Variant, secs int, log func(string)) float64 {
	c := v.client(10 * time.Second)
	for {
		mirrorsMu.Lock()
		if len(mirrors) == 0 {
			mirrorsMu.Unlock()
			return 0
		}
		url := mirrors[0]
		mirrorsMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs)*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		t0 := time.Now()
		resp, err := c.Do(req)
		if err != nil {
			cancel()
			return 0
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			cancel()
			if resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 429 {
				mirrorsMu.Lock()
				if len(mirrors) > 1 && mirrors[0] == url {
					log(fmt.Sprintf("  [!] %s answered HTTP %d; switching mirror", url, resp.StatusCode))
					mirrors = mirrors[1:]
				}
				n := len(mirrors)
				mirrorsMu.Unlock()
				if n > 0 {
					continue
				}
			}
			return 0
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		el := time.Since(t0).Seconds()
		cancel()
		return round2(float64(n) * 8 / el / 1e6)
	}
}

func upload(v *Variant, secs int) float64 {
	upOnce.Do(func() { upPayload = make([]byte, 16<<20); rand.Read(upPayload) })
	c := v.client(10 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs)*time.Second)
	defer cancel()
	cr := &countReader{r: bytes.NewReader(upPayload)}
	req, _ := http.NewRequestWithContext(ctx, "POST", upURL, cr)
	req.ContentLength = int64(len(upPayload))
	t0 := time.Now()
	resp, err := c.Do(req)
	el := time.Since(t0).Seconds()
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return 0
		}
	}
	cr.mu.Lock()
	n := cr.n
	cr.mu.Unlock()
	if n < 64<<10 { // nothing meaningful got through
		return 0
	}
	return round2(float64(n) * 8 / el / 1e6)
}

func score(v *Variant) float64 {
	if v.Ok == 0 {
		return 0
	}
	rel := float64(v.Ok) / float64(v.N)
	warm := float64(v.Warm)
	if v.Warm < 0 {
		warm = 3000
	}
	return round2(rel * rel * (v.Down + 0.5*v.Up + 3000/(warm+100)))
}

func simplicity(v *Variant) int {
	s := 0
	if v.K.Frag != "none" && v.K.Frag != "" {
		s += 8
	}
	if v.K.FP != "" && v.K.FP != "chrome" {
		s += 4
	}
	if v.K.Mode != "" && v.K.Mode != "auto" {
		s += 2
	}
	if v.K.Split != "" {
		s += 8
	}
	if v.K.MTU != 0 && v.K.MTU != 1420 {
		s += 2
	}
	return s
}

// pick returns the best variant, preferring the simplest settings within 10% of the top score:
// extra knobs (fragmentation, exotic fingerprints/modes) only earn their place when they measurably win.
func pick(vs []*Variant) *Variant {
	var top float64
	for _, v := range vs {
		top = math.Max(top, v.Score)
	}
	if top == 0 {
		return nil
	}
	var best *Variant
	for _, v := range vs {
		if v.Score >= 0.9*top && (best == nil || simplicity(v) < simplicity(best) ||
			simplicity(v) == simplicity(best) && v.Score > best.Score) {
			best = v
		}
	}
	return best
}

// RunTests screens every variant, then benchmarks the best of each (entry, fragment, mode) group.
func RunTests(o TuneOpts, st *State, u User, vs []*Variant) error {
	h, err := startHarness(o.XrayBin, st, u, vs, o.Out, true)
	if err != nil {
		return err
	}
	setActiveHarness(h)
	defer func() { setActiveHarness(nil); h.stop() }()

	o.Log(fmt.Sprintf("\n[screen] %d variants, %d probes each, %d in parallel", len(vs), o.Probes, o.Parallel))
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, o.Parallel)
	done := 0
	for _, v := range vs {
		wg.Add(1)
		sem <- struct{}{}
		go func(v *Variant) {
			defer wg.Done()
			defer func() { <-sem }()
			probe(v, o.Probes)
			mu.Lock()
			done++
			o.Log(fmt.Sprintf("  %3d/%d %-56s ok=%d/%d cold=%5s warm=%5s", done, len(vs), v.Label, v.Ok, v.N, ms(v.Cold), ms(v.Warm)))
			mu.Unlock()
		}(v)
	}
	wg.Wait()

	var alive []*Variant
	answered := false
	for _, v := range vs {
		if v.Ok > 0 && v.handshake == nil { // VPNs only answer a handshake: nothing to benchmark
			alive = append(alive, v)
		}
		answered = answered || v.Ok > 0
	}
	if !answered {
		return fmt.Errorf("nothing connected - the server may be blocked from this network (see %s/xray.log)", o.Out)
	}
	sort.SliceStable(alive, func(i, j int) bool {
		if alive[i].Ok != alive[j].Ok {
			return alive[i].Ok > alive[j].Ok
		}
		return alive[i].Warm < alive[j].Warm
	})
	// few-ms latency gaps are noise: give every (entry, fragment, mode) group a throughput number
	var promo []*Variant
	seen := map[string]bool{}
	for _, v := range alive {
		g := v.Entry.ID + "|" + v.K.Frag + "|" + v.K.Mode
		if !seen[g] {
			seen[g] = true
			promo = append(promo, v)
		}
	}
	for _, v := range alive {
		if len(promo) >= o.Top {
			break
		}
		if !contains(promo, v) {
			promo = append(promo, v)
		}
	}
	o.Log(fmt.Sprintf("\n[benchmark] %d variants x %d rounds, %ds per transfer", len(promo), o.Rounds, o.Secs))
	for r := 0; r < o.Rounds; r++ {
		for _, v := range promo {
			d, u := download(v, o.Secs, o.Log), upload(v, o.Secs)
			v.Downs, v.Ups = append(v.Downs, d), append(v.Ups, u)
			o.Log(fmt.Sprintf("  r%d %-56s down=%7.2f  up=%6.2f Mbps", r+1, v.Label, d, u))
		}
	}
	for _, v := range promo {
		v.Down, v.Up, v.Benched = medianF(v.Downs), medianF(v.Ups), true
	}
	for _, v := range vs {
		v.Score = score(v)
	}
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].Score > vs[j].Score })
	return nil
}

// Best returns the chosen variant per entry, best first.
func Best(vs []*Variant) []*Variant {
	groups := map[string][]*Variant{}
	var order []string
	for _, v := range vs {
		if _, ok := groups[v.Entry.ID]; !ok {
			order = append(order, v.Entry.ID)
		}
		groups[v.Entry.ID] = append(groups[v.Entry.ID], v)
	}
	var best []*Variant
	for _, id := range order {
		if b := pick(groups[id]); b != nil {
			best = append(best, b)
		}
	}
	sort.SliceStable(best, func(i, j int) bool { return best[i].Score > best[j].Score })
	return best
}

func Report(w io.Writer, o TuneOpts, st *State, u User, vs []*Variant) ([]*Variant, error) {
	f, err := os.OpenFile(filepath.Join(o.Out, "results.csv"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	cw := csv.NewWriter(f)
	cw.Write([]string{"score", "variant", "kind", "fp", "fragment", "mode", "ok", "probes", "cold_ms", "warm_ms", "down_mbps", "up_mbps"})
	for _, v := range vs {
		cw.Write([]string{ff(v.Score), v.Label, v.Entry.Kind, v.K.FP, v.K.Frag, v.K.Mode, strconv.Itoa(v.Ok), strconv.Itoa(v.N),
			ms(v.Cold), ms(v.Warm), benchStr(v, v.Down), benchStr(v, v.Up)})
	}
	cw.Flush()
	f.Close()

	fmt.Fprintf(w, "\n%2s %7s  %-56s %4s %5s %7s %6s\n", "#", "score", "variant", "ok", "warm", "down", "up")
	for i, v := range vs {
		if i == 25 {
			break
		}
		fmt.Fprintf(w, "%2d %7.2f  %-56s %d/%d %5s %7s %6s\n", i+1, v.Score, v.Label, v.Ok, v.N, ms(v.Warm), benchStr(v, v.Down), benchStr(v, v.Up))
	}
	var dead []string
	for _, v := range vs {
		if v.Ok == 0 {
			dead = append(dead, v.Label)
		}
	}
	if len(dead) > 0 {
		fmt.Fprintf(w, "\nBlocked / failed (%d): %s\n", len(dead), strings.Join(dead, "; "))
	}
	best := Best(vs)
	fmt.Fprintln(w, "\nBest per config (simplest within 10% of its top score):")
	lf, err := os.OpenFile(filepath.Join(o.Out, "links.txt"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer lf.Close()
	for i, v := range best {
		link := st.Link(v.Entry, u, v.K) // "" for VPNs and the DNS tunnel, which take profile files
		note := ""
		if v.K.Frag != "none" {
			note = "  (best result used client-side TLS fragment " + v.K.Frag + ")"
		}
		fmt.Fprintf(w, "%2d. %7.2f  %-56s down=%s up=%s warm=%sms%s\n", i+1, v.Score, v.Label, benchStr(v, v.Down), benchStr(v, v.Up), ms(v.Warm), note)
		if link != "" {
			fmt.Fprintln(lf, link)
		}
		if v.Entry.Xray() {
			writeJSON(filepath.Join(o.Out, "client_"+v.Entry.ID+".json"), ClientConfig(st.Outbound(v.Entry, u, v.K, "proxy")))
		}
	}
	return best, nil
}

// ---------------------------------------------------------------- orchestration

type TuneResult struct {
	State   *State
	User    User // the test user, on the test instance only
	Entries []Entry
	All     []*Variant
	Best    []*Variant
}

// Tune installs/uses the isolated server Xray, starts the test matrix and measures it from here.
func Tune(r *Server, o TuneOpts) (*TuneResult, error) {
	o.defaults()
	if err := os.MkdirAll(o.Out, 0o700); err != nil {
		return nil, err
	}
	o.deployed, _ = LoadState(r)
	made, err := o.tuneCloudflare(r.Conn.Host)
	if err != nil {
		return nil, err
	}
	waitResolve(made, 90*time.Second, o.Log)
	if err := checkDomains(r.Conn.Host, o.Domain, o.CDNDomain, o.Log); err != nil {
		return nil, err
	}
	o.Log("[*] local xray: " + o.XrayBin + " (" + XrayVersion(o.XrayBin) + ")")
	ver, err := EnsureServerXray(r, o.ServerTag, o.Log)
	if err != nil {
		return nil, err
	}
	o.Log("[*] server xray: " + ver)
	probeOut, err := r.Helper(append([]string{"probe"}, sniCandidates...)...)
	if err != nil {
		return nil, err
	}
	if o.DNSDomain != "" && !slices.Contains(o.Protocols, "dnstt") {
		o.Protocols = append(o.Protocols, "dnstt")
	}
	plan := PlanOpts{CDN: o.CDNDomain != "", Protocols: o.Protocols, DNS: o.DNSDomain != "",
		IKEBusy: usedPorts(probeOut)["udp"][500] && !strings.Contains(probeOut, "IKE ours"), Worker: o.Cloudflare != ""}
	if slices.Contains(o.Protocols, "worker") && o.Cloudflare == "" {
		o.Log("[!] the Worker relay needs a Cloudflare token (XTUNE_CLOUDFLARE_TOKEN, or xtune cloudflare token on the server); skipping it")
	}
	if slices.Contains(o.Protocols, "dnstt") && o.DNSDomain == "" {
		o.Log("[!] the DNS tunnel needs --dns-domain (a zone delegated to this server with an NS record); skipping it")
	}
	if plan.IKEBusy && slices.Contains(o.Protocols, "ikev2") {
		o.Log("[!] UDP port 500 is taken by another IPsec server; skipping IKEv2")
	}
	entries, snis := planEntries(probeOut, plan)
	if o.CDNDomain != "" && !hasCDN(entries) {
		o.Log("[!] no CDN port (" + fmt.Sprint(cdnPorts) + ") is free on the server; skipping the CDN entry")
	}
	if len(o.Only) > 0 {
		var keep []Entry
		for _, e := range entries {
			for _, p := range o.Only {
				if p = strings.TrimSpace(p); p != "" && strings.HasPrefix(e.ID, p) {
					keep = append(keep, e)
					break
				}
			}
		}
		if len(keep) == 0 {
			return nil, fmt.Errorf("--only %v matched no test entries", o.Only)
		}
		if !keep[0].Primary { // the full fingerprint/fragment sweep goes to the first kept entry
			keep[0].Primary = true
		}
		entries = keep
	}
	if entries, err = o.prepareExtras(r, entries); err != nil {
		return nil, err
	}
	if o.worker != nil && !o.KeepTest { // the test routes go; a deploy then points the Worker at its own
		defer func() {
			if err := cfWorkerUpload(o.Cloudflare, o.worker, orMap(o.deployedRoutes())); err != nil {
				o.Log("[!] restoring the Cloudflare Worker's routes: " + err.Error())
			}
		}()
	}
	o.Log("[*] REALITY targets (fastest TLS1.3+h2 from the server): " + strings.Join(snis, ", "))
	st, err := NewState(r.Conn.Host, o.XrayBin)
	if err != nil {
		return nil, err
	}
	entries = supported(o.XrayBin, st, entries, o.Log)
	st.Domain, st.CDNDomain, st.DNSDomain = o.Domain, o.CDNDomain, o.DNSDomain
	st.Cloudflare.Token = o.Cloudflare
	if o.worker != nil {
		w := *o.worker
		w.Routes = o.deployedRoutes() // what the Worker has once the test is over
		st.Worker = &w
	}
	if hasEngine(entries, "openvpn") || hasEngine(entries, "ikev2") {
		if err := st.ensureVPNCert(); err != nil {
			return nil, err
		}
	}
	if slices.ContainsFunc(entries, func(e Entry) bool { return e.Kind == "dnstt" }) { // its zone must be delegated here
		d := &doctor{}
		doctorTunnelDNS(d, &State{Host: r.Conn.Host, DNSDomain: o.DNSDomain, Entries: []Entry{{Kind: "dnstt"}}})
		for _, f := range d.out {
			if f.Level == "fail" {
				o.Log("[!] " + f.Text)
			}
		}
	}
	tuneUser := NewUser("tune")
	tuneUser.WGIP = wgSubnet.Addr().Next().Next().String()
	testDir := remoteDir + "/test"
	cfg := st.ServerConfig(entries, wgTestPeers(tuneUser, entries))
	testCerts(cfg, testDir)
	cfgJSON, _ := json.MarshalIndent(cfg, "", " ")
	files := map[string]string{testDir + "/hy.crt": st.CertPEM, testDir + "/hy.key": st.KeyPEM, remoteDir + "/test.json": string(cfgJSON)}
	var extras []string
	if hasEngine(entries, "tunnel") {
		tc, _ := json.MarshalIndent(st.TunnelConf(entries, []User{tuneUser}, ""), "", " ")
		files[testDir+"/tunnels.json"] = string(tc)
		extras = append(extras, "tunnels")
	}
	if hasEngine(entries, "awg") { // its clients' range is NATed for the test only (tnet:)
		ac, _ := json.MarshalIndent(st.AWGConf(entries, awgTestPeers(tuneUser), ""), "", " ")
		files[testDir+"/awg.json"] = string(ac)
		for _, x := range awgSpecs(entries) {
			extras = append(extras, strings.Replace(x, "net:", "tnet:", 1))
		}
	}
	for p, c := range st.vpnFiles(entries, []User{tuneUser}, testDir+"/vpn") {
		files[p] = c
	}
	for _, x := range vpnSpecs(entries) {
		if !strings.HasPrefix(x, "net:") {
			extras = append(extras, x)
		}
	}
	for p, c := range files {
		if err := r.Upload(p, []byte(c), "600"); err != nil {
			return nil, err
		}
	}
	specs := portSpecs(entries)
	defer func() {
		if o.KeepTest {
			return
		}
		if _, err := r.Helper("stop-test"); err != nil {
			o.Log("[!] stopping server test instance failed: " + err.Error())
		}
	}()
	if out, err := r.Helper(append([]string{"start"}, specs...)...); err != nil || !strings.HasSuffix(out, "active") {
		return nil, fmt.Errorf("server test instance failed to start: %v %s", err, out)
	}
	if len(extras) > 0 {
		out, err := r.Helper(append([]string{"test-extras"}, extras...)...)
		if err != nil {
			return nil, fmt.Errorf("server test tunnels failed to start: %v %s", err, out)
		}
		entries = dropFailedExtras(entries, out, o.Log)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, fmt.Sprintf("%s:%d", e.ID, e.Port))
	}
	o.Log("[*] test inbounds: " + strings.Join(ids, ", "))
	time.Sleep(time.Second)

	vs := ExpandVariants(entries, o.Depth, o.Resolvers...)
	if err := RunTests(o, st, tuneUser, vs); err != nil {
		return nil, err
	}
	var buf strings.Builder
	best, err := Report(&buf, o, st, tuneUser, vs)
	if err != nil {
		return nil, err
	}
	o.Log(buf.String())
	return &TuneResult{State: st, User: tuneUser, Entries: entries, All: vs, Best: best}, nil
}

// prepareExtras gets the server ready for the opt-in protocols: xtune itself runs the SSH and DNS
// tunnels and AmneziaWG, OpenVPN and strongSwan come from the distribution, and the Worker relay
// is created or updated on Cloudflare. Protocols the server cannot run are dropped with a warning;
// the rest of the tune goes on.
func (o *TuneOpts) prepareExtras(r *Server, entries []Entry) ([]Entry, error) {
	if hasEngine(entries, "tunnel") || hasEngine(entries, "awg") {
		if err := EnsureServerXtune(r, o.Log); err != nil {
			o.Log("[!] the SSH and DNS tunnels and AmneziaWG need xtune on the server, which failed: " + err.Error() + "; skipping them")
			entries = filterEntries(entries, func(e Entry) bool { return !e.SSH() && e.Engine() != "awg" })
		}
	}
	entries = o.prepareWorker(r.Conn.Host, entries)
	if hasEngine(entries, "openvpn") || hasEngine(entries, "ikev2") {
		o.Log("[*] installing OpenVPN and strongSwan on the server if needed")
		if out, err := r.Helper("vpn-install"); err != nil || !strings.HasSuffix(out, "ok") {
			o.Log(fmt.Sprintf("[!] OpenVPN/strongSwan could not be installed (%v %s); skipping them", err, tail(out, 200)))
			entries = filterEntries(entries, func(e Entry) bool { return !e.HandshakeOnly() })
		}
	}
	return entries, nil
}

// dropFailedExtras removes the entries whose test service did not start, from the helper's
// "SPEC STATE" lines.
func dropFailedExtras(entries []Entry, out string, log func(string)) []Entry {
	failed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] != "active" {
			failed[f[0]] = true
			log("[!] test service " + f[0] + " did not start on the server; skipping it")
		}
	}
	return filterEntries(entries, func(e Entry) bool {
		switch e.Engine() {
		case "tunnel":
			return !failed["tunnels"]
		case "awg":
			return !failed["awg"]
		case "openvpn":
			return !failed["openvpn:"+e.ID]
		case "ikev2":
			return !failed["ikev2"]
		}
		return true
	})
}

// supported drops entries whose client settings the local Xray cannot run (port hopping needs a
// recent Xray), so one unsupported knob does not fail the whole test.
func supported(bin string, st *State, entries []Entry, log func(string)) []Entry {
	var keep []Entry
	for _, e := range entries {
		if e.Hop != "" {
			cfg := ClientConfig(st.Outbound(e, NewUser("t"), Knobs{}, "proxy"))
			if err := xrayAccepts(bin, cfg); err != nil {
				log("[!] the local Xray cannot do Hysteria2 port hopping (update it with `xtune xray update`); skipping " + e.ID)
				continue
			}
		}
		keep = append(keep, e)
	}
	return keep
}

// xrayAccepts checks a config with `xray run -test`.
func xrayAccepts(bin string, cfg J) error {
	f, err := os.CreateTemp("", "xtune-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	json.NewEncoder(f).Encode(cfg)
	f.Close()
	if out, err := exec.Command(bin, "run", "-test", "-c", f.Name()).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, tail(string(out), 300))
	}
	return nil
}

// testCerts points TLS inbounds at the test copy of the certificate so tuning never replaces the
// certificate a live deployment's clients have pinned.
func testCerts(cfg J, dir string) {
	for _, ib := range cfg["inbounds"].([]J) {
		ss, _ := ib["streamSettings"].(J) // shadowsocks has none
		if ts, ok := ss["tlsSettings"].(J); ok {
			ts["certificates"] = certFiles(dir)
		}
	}
}

func hasCDN(es []Entry) bool {
	for _, e := range es {
		if e.CDN() {
			return true
		}
	}
	return false
}

// checkDomains resolves the domains from this machine. A direct domain that does not resolve is an
// error; one that points elsewhere, or a CDN domain that points straight at the server, is a warning.
func checkDomains(host, direct, cdn string, log func(string)) error {
	for _, d := range []string{direct, cdn} {
		if d != "" && (strings.ContainsAny(d, "/: ") || !strings.Contains(d, ".")) {
			return fmt.Errorf("%q is not a domain name", d)
		}
	}
	if direct == "" && cdn == "" {
		return nil
	}
	server := map[string]bool{}
	if ips, err := net.LookupHost(host); err == nil {
		for _, ip := range ips {
			server[ip] = true
		}
	}
	points := func(d string) (bool, error) {
		ips, err := net.LookupHost(d)
		if err != nil {
			return false, err
		}
		for _, ip := range ips {
			if server[ip] {
				return true, nil
			}
		}
		return false, nil
	}
	if direct != "" {
		ok, err := points(direct)
		if err != nil {
			return fmt.Errorf("direct domain %s does not resolve: %w", direct, err)
		}
		if !ok {
			log("[!] direct domain " + direct + " does not resolve to " + host + " from here (DNS-only / grey cloud?)")
		}
	}
	if cdn != "" {
		ok, err := points(cdn)
		if err != nil {
			log("[!] CDN domain " + cdn + " does not resolve from here: " + err.Error())
		} else if ok {
			log("[!] CDN domain " + cdn + " resolves straight to the server: turn the CDN proxy on (orange cloud)")
		}
	}
	return nil
}

// ---------------------------------------------------------------- small helpers

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func median(xs []int) int {
	s := append([]int(nil), xs...)
	sort.Ints(s)
	return s[len(s)/2]
}

func medianF(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 0 {
		return round2((s[len(s)/2-1] + s[len(s)/2]) / 2)
	}
	return s[len(s)/2]
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
func ff(f float64) string      { return strconv.FormatFloat(f, 'f', 2, 64) }
func ms(v int) string {
	if v < 0 {
		return "-"
	}
	return strconv.Itoa(v)
}
func benchStr(v *Variant, x float64) string {
	if !v.Benched {
		return "-"
	}
	return ff(x)
}
func contains(vs []*Variant, x *Variant) bool {
	for _, v := range vs {
		if v == x {
			return true
		}
	}
	return false
}
