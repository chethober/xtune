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

type TuneOpts struct {
	Depth                         string // smart | deep
	Probes, Parallel, Top, Rounds int
	Secs                          int
	Out                           string
	XrayBin                       string
	ServerTag                     string   // "" = keep / latest
	Only                          []string // test only entries whose ID starts with one of these
	Log                           func(string)
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
}

// ---------------------------------------------------------------- plan

func PlanEntries(probeOut string) ([]Entry, []string) {
	type sni struct {
		t    float64
		host string
	}
	var snis []sni
	used := map[string]map[int]bool{"tcp": {}, "udp": {}}
	for _, line := range strings.Split(probeOut, "\n") {
		f := strings.Fields(line)
		if len(f) == 4 && f[0] == "SNI" && f[2] == "2" { // HTTP/2 over TLS 1.3 = usable REALITY target
			t, _ := strconv.ParseFloat(f[3], 64)
			snis = append(snis, sni{t, f[1]})
		} else if len(f) == 3 && f[0] == "USED" {
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
	sort.Slice(snis, func(i, j int) bool { return snis[i].t < snis[j].t })
	var hosts []string
	for i := 0; i < len(snis) && i < 3; i++ {
		hosts = append(hosts, snis[i].host)
	}
	if len(hosts) == 0 {
		hosts = []string{"www.microsoft.com"}
	}
	taken := map[int]bool{}
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
	return E, hosts
}

func ExpandVariants(entries []Entry, depth string) []*Variant {
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
		case e.Kind == "reality-xhttp":
			for _, f := range fps[:2] {
				for _, g := range fragNames {
					ks = append(ks, Knobs{FP: f, Frag: g, Mode: "auto"})
				}
			}
			for _, m := range modes[1:] {
				ks = append(ks, Knobs{FP: "chrome", Frag: "none", Mode: m})
			}
		case e.Kind == "reality-grpc":
			for _, g := range fragNames {
				ks = append(ks, Knobs{FP: "chrome", Frag: g})
			}
		case e.Kind == "vlessenc-xhttp":
			for _, m := range modes {
				ks = append(ks, Knobs{Frag: "none", Mode: m})
			}
		default:
			ks = []Knobs{{Frag: "none"}}
		}
		for _, k := range ks {
			v := &Variant{Entry: e, K: k, Cold: -1, Warm: -1}
			v.Label = fmt.Sprintf("%s:%d", e.ID, e.Port)
			if e.Reality() {
				v.Label += " sni=" + strings.Split(e.SNI, ".")[len(strings.Split(e.SNI, "."))-2]
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

func startHarness(bin string, st *State, u User, vs []*Variant, dir string) (*harness, error) {
	ports, err := freePorts(len(vs))
	if err != nil {
		return nil, err
	}
	cfg := J{"log": J{"loglevel": "error"}, "inbounds": []J{}, "outbounds": fragOutbounds(), "routing": J{"rules": []J{}}}
	for i, v := range vs {
		v.port = ports[i]
		cfg["inbounds"] = append(cfg["inbounds"].([]J), J{"tag": fmt.Sprintf("in%d", i), "listen": "127.0.0.1",
			"port": v.port, "protocol": "socks", "settings": J{"udp": true}})
		cfg["outbounds"] = append(cfg["outbounds"].([]J), st.Outbound(v.Entry, u, v.K, fmt.Sprintf("out%d", i)))
		rules := cfg["routing"].(J)["rules"].([]J)
		cfg["routing"].(J)["rules"] = append(rules, J{"inboundTag": []string{fmt.Sprintf("in%d", i)}, "outboundTag": fmt.Sprintf("out%d", i)})
	}
	path := filepath.Join(dir, "harness.json")
	if err := writeJSON(path, cfg); err != nil {
		return nil, err
	}
	if out, err := exec.Command(bin, "run", "-test", "-c", path).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("local xray rejected the test config: %v\n%s", err, tail(string(out), 1500))
	}
	lf, err := os.Create(filepath.Join(dir, "xray.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "run", "-c", path)
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		lf.Close()
		return nil, err
	}
	h := &harness{cmd: cmd, logFile: lf}
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
	c := socksClient(v.port, 8*time.Second)
	var ok []int
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", probeURL, nil)
		t0 := time.Now()
		resp, err := c.Do(req)
		ms := int(time.Since(t0).Milliseconds())
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		cancel()
		good := err == nil && resp.StatusCode == 204
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
	c := socksClient(v.port, 10*time.Second)
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
	c := socksClient(v.port, 10*time.Second)
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
	h, err := startHarness(o.XrayBin, st, u, vs, o.Out)
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
	for _, v := range vs {
		if v.Ok > 0 {
			alive = append(alive, v)
		}
	}
	if len(alive) == 0 {
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
		link := st.Link(v.Entry, u, v.K)
		note := ""
		if v.K.Frag != "none" {
			note = "  (best result used client-side TLS fragment " + v.K.Frag + ")"
		}
		fmt.Fprintf(w, "%2d. %7.2f  %-56s down=%s up=%s warm=%sms%s\n", i+1, v.Score, v.Label, benchStr(v, v.Down), benchStr(v, v.Up), ms(v.Warm), note)
		fmt.Fprintln(lf, link)
		writeJSON(filepath.Join(o.Out, "client_"+v.Entry.ID+".json"), ClientConfig(st.Outbound(v.Entry, u, v.K, "proxy")))
	}
	return best, nil
}

// ---------------------------------------------------------------- orchestration

type TuneResult struct {
	State   *State
	Entries []Entry
	All     []*Variant
	Best    []*Variant
}

// Tune installs/uses the isolated server Xray, starts the test matrix and measures it from here.
func Tune(r *Remote, o TuneOpts) (*TuneResult, error) {
	o.defaults()
	if err := os.MkdirAll(o.Out, 0o700); err != nil {
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
	entries, snis := PlanEntries(probeOut)
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
	o.Log("[*] REALITY targets (fastest TLS1.3+h2 from the server): " + strings.Join(snis, ", "))
	st, err := NewState(r.Conn.Host, o.XrayBin)
	if err != nil {
		return nil, err
	}
	tuneUser := NewUser("tune")
	testDir := remoteDir + "/test"
	cfg := st.ServerConfig(entries, []User{tuneUser})
	testCerts(cfg, testDir)
	cfgJSON, _ := json.MarshalIndent(cfg, "", " ")
	for p, c := range map[string]string{testDir + "/hy.crt": st.CertPEM, testDir + "/hy.key": st.KeyPEM, remoteDir + "/test.json": string(cfgJSON)} {
		if err := r.Upload(p, []byte(c), "600"); err != nil {
			return nil, err
		}
	}
	var specs []string
	for _, e := range entries {
		specs = append(specs, fmt.Sprintf("%d/%s", e.Port, e.Proto()))
	}
	defer func() {
		if _, err := r.Helper("stop-test"); err != nil {
			o.Log("[!] stopping server test instance failed: " + err.Error())
		}
	}()
	if out, err := r.Helper(append([]string{"start"}, specs...)...); err != nil || !strings.HasSuffix(out, "active") {
		return nil, fmt.Errorf("server test instance failed to start: %v %s", err, out)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, fmt.Sprintf("%s:%d", e.ID, e.Port))
	}
	o.Log("[*] test inbounds: " + strings.Join(ids, ", "))
	time.Sleep(time.Second)

	vs := ExpandVariants(entries, o.Depth)
	if err := RunTests(o, st, tuneUser, vs); err != nil {
		return nil, err
	}
	var buf strings.Builder
	best, err := Report(&buf, o, st, tuneUser, vs)
	if err != nil {
		return nil, err
	}
	o.Log(buf.String())
	return &TuneResult{State: st, Entries: entries, All: vs, Best: best}, nil
}

// testCerts points Hysteria2 at the test copy of the certificate so tuning never replaces the
// certificate a live deployment's clients have pinned.
func testCerts(cfg J, dir string) {
	for _, ib := range cfg["inbounds"].([]J) {
		if ib["protocol"] == "hysteria" {
			ts := ib["streamSettings"].(J)["tlsSettings"].(J)
			ts["certificates"] = []J{{"certificateFile": dir + "/hy.crt", "keyFile": dir + "/hy.key"}}
		}
	}
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
