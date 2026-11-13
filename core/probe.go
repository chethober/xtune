package core

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
	"strconv"
	"sync"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	xcore "github.com/xtls/xray-core/core"
)

// The on-device check: every config measured from this device's network, the way `xtune check`
// does from a computer: does it answer, how fast, and does it hold its speed or get throttled
// after a few seconds.

// SpeedURL is what the throughput test downloads.
var SpeedURL = "https://speed.cloudflare.com/__down?bytes=50000000"

// Result is one config as measured here.
type Result struct {
	ID     string  `json:"id"`
	Key    string  `json:"key"`
	Name   string  `json:"name"`
	State  string  `json:"state"` // testing | ok | slow | throttled | flaky | blocked | skipped
	Ok     int     `json:"ok"`
	N      int     `json:"n"`
	Warm   int     `json:"warmMs"`
	Down   float64 `json:"downMbps"`
	Early  float64 `json:"earlyMbps"`
	Late   float64 `json:"lateMbps"`
	Detail string  `json:"detail,omitempty"`
}

// ProbeOptions tune how long a check takes.
type ProbeOptions struct {
	IDs       []string `json:"ids,omitempty"` // only these configs; all when empty
	Probes    int      `json:"probes,omitempty"`
	Seconds   int      `json:"seconds,omitempty"` // of the throughput test; 0 = 6, -1 = none
	Parallel  int      `json:"parallel,omitempty"`
	ProbeURL  string   `json:"probeUrl,omitempty"`
	SpeedURL  string   `json:"speedUrl,omitempty"`
	TimeoutMs int      `json:"timeoutMs,omitempty"`
}

// Job is a check in progress.
type Job struct {
	mu      sync.Mutex
	ID      string
	Done    bool
	Results []Result
	cancel  context.CancelFunc
}

// JobView is a check as the app sees it.
type JobView struct {
	ID      string   `json:"id"`
	Done    bool     `json:"done"`
	Results []Result `json:"results"`
}

func (j *Job) snapshot() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JobView{ID: j.ID, Done: j.Done, Results: append([]Result(nil), j.Results...)}
}

var (
	jobsMu sync.Mutex
	jobs   = map[string]*Job{}
	jobSeq int
)

// StartProbe checks p's configs in the background.
func StartProbe(p *Profile, o ProbeOptions) *Job {
	if o.Probes <= 0 {
		o.Probes = 3
	}
	if o.Seconds == 0 {
		o.Seconds = 6
	}
	if o.Parallel <= 0 {
		o.Parallel = 3
	}
	if o.TimeoutMs <= 0 {
		o.TimeoutMs = 8000
	}
	o.ProbeURL = orStr(o.ProbeURL, orStr(p.Probe, protocols.ProbeURL))
	o.SpeedURL = orStr(o.SpeedURL, SpeedURL)
	want := map[string]bool{}
	for _, id := range o.IDs {
		want[id] = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	jobsMu.Lock()
	jobSeq++
	j := &Job{ID: strconv.Itoa(jobSeq), cancel: cancel}
	jobs[j.ID] = j
	jobsMu.Unlock()
	var todo []int
	for _, c := range p.Configs {
		if len(want) > 0 && !want[c.ID] {
			continue
		}
		r := Result{ID: c.ID, Key: c.Key, Name: c.Name, State: "testing"}
		if !usable(c) {
			r.State, r.Detail = "skipped", "runs in another app"
		} else {
			todo = append(todo, len(j.Results))
		}
		j.Results = append(j.Results, r)
	}
	byID := map[string]protocols.ClientEntry{}
	for _, c := range p.Configs {
		byID[c.ID] = c
	}
	go func() {
		sem := make(chan struct{}, o.Parallel)
		var wg sync.WaitGroup
		for _, i := range todo {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				j.mu.Lock()
				c := byID[j.Results[i].ID]
				j.mu.Unlock()
				r := probeOne(ctx, p, c, o)
				j.mu.Lock()
				j.Results[i] = r
				j.mu.Unlock()
			}(i)
		}
		wg.Wait()
		j.mu.Lock()
		j.Done = true
		j.mu.Unlock()
		cancel()
	}()
	return j
}

func GetJob(id string) (JobView, bool) {
	jobsMu.Lock()
	j := jobs[id]
	jobsMu.Unlock()
	if j == nil {
		return JobView{}, false
	}
	return j.snapshot(), true
}

func StopJob(id string) {
	jobsMu.Lock()
	j := jobs[id]
	jobsMu.Unlock()
	if j != nil {
		j.cancel()
	}
}

// probeOne measures config c.
func probeOne(ctx context.Context, p *Profile, c protocols.ClientEntry, o ProbeOptions) Result {
	r := Result{ID: c.ID, Key: c.Key, Name: c.Name, N: o.Probes, Warm: -1}
	timeout := time.Duration(o.TimeoutMs) * time.Millisecond
	tr, closeFn, err := transportFor(p, c, timeout)
	if err != nil {
		r.State, r.Detail = "blocked", err.Error()
		return r
	}
	defer closeFn()
	client := &http.Client{Transport: tr, Timeout: timeout}
	var warm []time.Duration
	var lastErr error
	for i := 0; i < o.Probes && ctx.Err() == nil; i++ {
		t0 := time.Now()
		req, _ := http.NewRequestWithContext(ctx, "GET", o.ProbeURL, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		r.Ok++
		if i > 0 || o.Probes == 1 { // the first request pays for the handshake
			warm = append(warm, time.Since(t0))
		}
	}
	if len(warm) > 0 {
		var sum time.Duration
		for _, w := range warm {
			sum += w
		}
		r.Warm = int((sum / time.Duration(len(warm))).Milliseconds())
	}
	if r.Ok == 0 {
		r.State = "blocked"
		if lastErr != nil {
			r.Detail = shortErr(lastErr)
		}
		return r
	}
	if o.Seconds > 0 && ctx.Err() == nil {
		r.Down, r.Early, r.Late = throughput(ctx, tr, o.SpeedURL, time.Duration(o.Seconds)*time.Second)
	}
	r.State = judge(r)
	return r
}

func judge(r Result) string {
	switch {
	case r.Ok == 0:
		return "blocked"
	case r.Ok*2 < r.N:
		return "flaky"
	case r.Early >= 2 && r.Late < 0.4*r.Early:
		return "throttled"
	case r.Down > 0 && r.Down < 1:
		return "slow"
	}
	return "ok"
}

func shortErr(err error) string {
	s := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) {
		s = ue.Err.Error()
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// throughput downloads for d and returns Mbps overall, in the first third and in the last third.
func throughput(ctx context.Context, tr http.RoundTripper, u string, d time.Duration) (avg, early, late float64) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	t0 := time.Now()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return 0, 0, 0
	}
	defer resp.Body.Close()
	third := d / 3
	var total, first, last int64
	buf := make([]byte, 64<<10)
	for {
		n, err := resp.Body.Read(buf)
		el := time.Since(t0)
		total += int64(n)
		if el < third {
			first += int64(n)
		} else if el >= 2*third {
			last += int64(n)
		}
		if err != nil {
			break
		}
	}
	el := time.Since(t0).Seconds()
	mbps := func(b int64, s float64) float64 {
		if s <= 0 {
			return 0
		}
		return float64(b) * 8 / 1e6 / s
	}
	lastSecs := el - 2*third.Seconds()
	if lastSecs < 0 {
		lastSecs = 0
	}
	return round2(mbps(total, el)), round2(mbps(first, third.Seconds())), round2(mbps(last, lastSecs))
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// transportFor is an HTTP transport through config c alone.
func transportFor(p *Profile, c protocols.ClientEntry, timeout time.Duration) (http.RoundTripper, func(), error) {
	base := &http.Transport{Proxy: nil, DisableKeepAlives: false, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost: 1}
	switch {
	case c.SSH != nil:
		base.DialContext = c.SSH.DialFresh(nil)
		return base, base.CloseIdleConnections, nil
	case c.AWG != nil:
		d := c.AWG.Dialer()
		base.DialContext = d.DialContext
		return base, func() { base.CloseIdleConnections(); d.Close() }, nil
	case c.Engine == "xray":
		port, err := freePort()
		if err != nil {
			return nil, nil, err
		}
		ob := clone(c.Outbound)
		ob["tag"] = "proxy"
		obs := []J{ob}
		for _, f := range p.Frag {
			obs = append(obs, clone(f))
		}
		cfg := J{"log": J{"loglevel": "none"}, "outbounds": obs,
			"inbounds": []J{{"tag": "in", "listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": J{"udp": false}}}}
		b, _ := json.Marshal(cfg)
		inst, err := startXray(b)
		if err != nil {
			return nil, nil, err
		}
		pu, _ := url.Parse("socks5h://127.0.0.1:" + strconv.Itoa(port))
		base.Proxy = http.ProxyURL(pu)
		return base, func() { base.CloseIdleConnections(); closeXray(inst) }, nil
	}
	return nil, nil, errors.New("runs in another app")
}

// xrayMu serializes creating and closing Xray instances: xray-core sets process-wide state (its
// system dialer) as an instance is built, so the check's parallel probes and the connection
// must not build theirs at the same moment. Running instances do not need it.
var xrayMu sync.Mutex

func startXray(config []byte) (*xcore.Instance, error) {
	xrayMu.Lock()
	defer xrayMu.Unlock()
	xc, err := xcore.LoadConfig("json", bytes.NewReader(config))
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	inst, err := xcore.New(xc)
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	if err := inst.Start(); err != nil {
		inst.Close()
		return nil, fmt.Errorf("start: %w", err)
	}
	return inst, nil
}

func closeXray(inst *xcore.Instance) {
	xrayMu.Lock()
	defer xrayMu.Unlock()
	inst.Close()
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// SendReport gives the server what was measured, for its ranking of this network.
func SendReport(subURL string, n Network, results []Result) error {
	base, err := NormalizeSubURL(subURL)
	if err != nil {
		return err
	}
	type res struct {
		Key  string  `json:"key"`
		Ok   int     `json:"ok"`
		N    int     `json:"n"`
		Warm int     `json:"warmMs"`
		Down float64 `json:"downMbps"`
		Up   float64 `json:"upMbps"`
	}
	in := struct {
		ASN     string `json:"asn"`
		Name    string `json:"name"`
		Results []res  `json:"results"`
	}{ASN: trimAS(n.ASN), Name: n.Name}
	for _, r := range results {
		if r.State == "skipped" || r.State == "testing" || r.Key == "" {
			continue
		}
		in.Results = append(in.Results, res{Key: r.Key, Ok: r.Ok, N: r.N, Warm: r.Warm, Down: r.Down})
	}
	if in.ASN == "" || len(in.Results) == 0 {
		return errors.New("nothing to report")
	}
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest("POST", base+"/report", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "xtune-client/"+Version)
	resp, err := directClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case 200, 202, 204:
		return nil
	case 404:
		return errors.New("the server does not take reports yet (update xtune on it)")
	}
	return fmt.Errorf("report: HTTP %d", resp.StatusCode)
}

func trimAS(a string) string {
	if len(a) > 2 && (a[:2] == "AS" || a[:2] == "as") {
		return a[2:]
	}
	return a
}
