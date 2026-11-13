package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chethober/xtune/pkg/remote"
)

// Tuning a server from the app. The server starts its test matrix (`xtune tune --prepare`) and
// hands it over as a client profile, one config per variant; this device measures every one
// from its own network, which is the network that matters; the server then deploys the best
// (`xtune deploy REPORT`), keeping users and their links.

// TuneOptions is what the operator chose.
type TuneOptions struct {
	Protocols []string `json:"protocols,omitempty"` // ssh, dnstt, mtproto, awg, trojan, worker (with the deployed ones)
	Depth     string   `json:"depth,omitempty"`     // smart (default) | deep
	DNSDomain string   `json:"dnsDomain,omitempty"` // the DNS tunnel's zone, delegated to the server
	Network   string   `json:"network,omitempty"`   // this network's name in the rankings
	Seconds   int      `json:"seconds,omitempty"`   // throughput test per config (default 4)
}

// testProfile is xtune's TestProfile: the prepared tune as the app measures it.
type testProfile struct {
	ID      string   `json:"id"`
	Expires int64    `json:"expires"`
	Profile Profile  `json:"profile"`
	Skipped []string `json:"skipped,omitempty"`
}

// TuneView is a tune as the app shows it.
type TuneView struct {
	ID      string    `json:"id"`
	Server  string    `json:"server"` // the server's host
	Phase   string    `json:"phase"`  // preparing | measuring | measured | deploying | done | failed | cancelled
	Error   string    `json:"error,omitempty"`
	Failed  string    `json:"failed,omitempty"` // the phase it failed in
	Started int64     `json:"started"`
	Expires int64     `json:"expires,omitempty"`
	Total   int       `json:"total"`
	Done    int       `json:"done"`
	Results []Result  `json:"results,omitempty"` // best first once measured
	Scores  []float64 `json:"scores,omitempty"`  // Results' scores, same order
	Skipped []string  `json:"skipped,omitempty"`
	Output  string    `json:"output,omitempty"` // the deploy's
}

type tuneJob struct {
	mu    sync.Mutex
	view  TuneView
	conn  ServerConn
	opts  TuneOptions
	tp    *testProfile
	probe *Job
}

var (
	tunesMu sync.Mutex
	tunes   = map[string]*tuneJob{}
	tuneSeq int
)

func (t *tuneJob) set(f func(v *TuneView)) {
	t.mu.Lock()
	f(&t.view)
	t.mu.Unlock()
}

func (t *tuneJob) snapshot() TuneView {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := t.view
	if t.probe != nil && v.Phase == "measuring" {
		p := t.probe.snapshot()
		v.Results, v.Done = p.Results, 0
		for _, r := range p.Results {
			if r.State != "testing" {
				v.Done++
			}
		}
	}
	return v
}

var protoOK = map[string]bool{"ssh": true, "dnstt": true, "mtproto": true, "awg": true, "trojan": true, "worker": true}

// prepareArgs is the server command that prepares the tune.
func prepareArgs(o TuneOptions) ([]string, error) {
	args := []string{"tune", "--prepare"}
	depth := o.Depth
	if depth == "" {
		depth = "smart"
	}
	if depth != "smart" && depth != "deep" {
		return nil, fmt.Errorf("depth: smart or deep")
	}
	args = append(args, "--depth", depth)
	var ps []string
	for _, p := range o.Protocols {
		if !protoOK[p] {
			return nil, fmt.Errorf("unknown protocol %q", p)
		}
		ps = append(ps, p)
	}
	if len(ps) > 0 {
		args = append(args, "--protocols", strings.Join(ps, ","))
	}
	if o.DNSDomain != "" {
		if strings.ContainsAny(o.DNSDomain, " /\\'\"") || strings.HasPrefix(o.DNSDomain, "-") {
			return nil, errors.New("the DNS tunnel's zone is a domain name")
		}
		args = append(args, "--dns-domain", o.DNSDomain)
	}
	return args, nil
}

// StartTune prepares a tune on c's server and measures it from here. Measuring done, the tune
// waits in "measured" for DeployTune or CancelTune.
func StartTune(c ServerConn, o TuneOptions) (TuneView, error) {
	args, err := prepareArgs(o)
	if err != nil {
		return TuneView{}, err
	}
	if _, err := server(c); err != nil {
		return TuneView{}, err
	}
	tunesMu.Lock()
	for _, t := range tunes {
		v := t.snapshot()
		if v.Server == c.Host && (v.Phase == "preparing" || v.Phase == "measuring" || v.Phase == "deploying") {
			tunesMu.Unlock()
			return v, errors.New("a tune of this server is already running")
		}
	}
	tuneSeq++
	t := &tuneJob{conn: c, opts: o, view: TuneView{ID: "t" + strconv.Itoa(tuneSeq), Server: c.Host, Phase: "preparing", Started: time.Now().Unix()}}
	tunes[t.view.ID] = t
	tunesMu.Unlock()
	go t.run(args)
	return t.snapshot(), nil
}

func (t *tuneJob) fail(err error) {
	t.set(func(v *TuneView) { v.Failed, v.Phase, v.Error = v.Phase, "failed", err.Error() })
}

func (t *tuneJob) run(args []string) {
	out, err := xtune(t.conn, args...)
	if t.snapshot().Phase == "cancelled" {
		return
	}
	if err != nil {
		t.fail(fmt.Errorf("preparing the test on the server: %w", err))
		return
	}
	var tp testProfile
	line := strings.TrimSpace(out)
	if i := strings.LastIndex(line, "\n{"); i >= 0 {
		line = line[i+1:]
	}
	if err := json.Unmarshal([]byte(line), &tp); err != nil || tp.ID == "" {
		t.fail(errors.New("the server's xtune cannot prepare a tune for the app: update it first (Update → xtune)"))
		return
	}
	if len(tp.Profile.Configs) == 0 {
		t.fail(errors.New("the server prepared nothing this app can measure"))
		xtune(t.conn, "tune", "--discard")
		return
	}
	secs := t.opts.Seconds
	if secs == 0 {
		secs = 4
	}
	job := StartProbe(&tp.Profile, ProbeOptions{Probes: 3, Seconds: secs, Parallel: 4, TimeoutMs: 8000})
	t.mu.Lock()
	t.tp, t.probe = &tp, job
	t.view.Phase, t.view.Expires, t.view.Total, t.view.Skipped = "measuring", tp.Expires, len(tp.Profile.Configs), tp.Skipped
	t.mu.Unlock()
	for {
		time.Sleep(500 * time.Millisecond)
		p := job.snapshot()
		if t.snapshot().Phase == "cancelled" {
			return
		}
		if p.Done {
			res := append([]Result(nil), p.Results...)
			sortByScore(res)
			t.set(func(v *TuneView) {
				v.Phase, v.Results, v.Done = "measured", res, len(res)
				v.Scores = make([]float64, len(res))
				for i, r := range res {
					v.Scores[i] = Score(r)
				}
			})
			return
		}
	}
}

// Score is how xtune ranks a measured config: reliability first, then speed and latency.
func Score(r Result) float64 {
	if r.Ok == 0 || r.N == 0 {
		return 0
	}
	rel := float64(r.Ok) / float64(r.N)
	warm := float64(r.Warm)
	if r.Warm < 0 {
		warm = 3000
	}
	return round2(rel * rel * (r.Down + 3000/(warm+100)))
}

func sortByScore(rs []Result) {
	for i := 1; i < len(rs); i++ { // insertion sort: small, and stable
		for j := i; j > 0 && Score(rs[j]) > Score(rs[j-1]); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

// netReport is xtune's NetReport.
type netReport struct {
	ID      string        `json:"id"`
	Network string        `json:"network"`
	ASN     string        `json:"asn,omitempty"`
	ASName  string        `json:"asName,omitempty"`
	Time    int64         `json:"time"`
	Results []checkResult `json:"results"`
}

type checkResult struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Ok    int     `json:"ok"`
	N     int     `json:"n"`
	Warm  int     `json:"warmMs"`
	Down  float64 `json:"downMbps"`
	Score float64 `json:"score,omitempty"`
}

// report is the measurements as the server's deploy reads them.
func report(id string, n Network, name string, rs []Result) netReport {
	rep := netReport{ID: id, Network: orStr(name, orStr(n.Name, "xtune app")), ASN: trimAS(n.ASN), ASName: n.Name, Time: time.Now().Unix()}
	for _, r := range rs {
		if r.State == "skipped" || r.State == "testing" {
			continue
		}
		entry, _, _ := strings.Cut(r.ID, "#")
		rep.Results = append(rep.Results, checkResult{ID: entry, Label: r.Key, Ok: r.Ok, N: r.N, Warm: r.Warm, Down: r.Down, Score: Score(r)})
	}
	return rep
}

const appReport = remote.Dir + "/app-report.json"

// DeployTune deploys the n best of a measured tune (more when needed to keep each protocol).
func DeployTune(id string, n int) (TuneView, error) {
	t := getTune(id)
	if t == nil {
		return TuneView{}, errors.New("no such tune")
	}
	v := t.snapshot()
	if v.Phase != "measured" {
		return v, fmt.Errorf("the tune is %s, not measured", v.Phase)
	}
	if time.Now().Unix() > v.Expires {
		return v, errors.New("the test on the server expired (it runs for two hours): start a new tune")
	}
	if n <= 0 {
		n = 3
	}
	t.set(func(v *TuneView) { v.Phase = "deploying" })
	go func() {
		netw, _ := CurrentNetwork()
		rep := report(t.tp.ID, netw, t.opts.Network, v.Results)
		b, _ := json.Marshal(rep)
		r, err := server(t.conn)
		if err == nil {
			_, _, err = r.Transport.Exec("umask 077; cat > "+appReport, string(b), true)
		}
		if err != nil {
			t.fail(fmt.Errorf("sending the measurements to the server: %w", err))
			return
		}
		out, err := xtune(t.conn, "deploy", "-n", strconv.Itoa(n), appReport)
		r.Transport.Exec("rm -f "+appReport, "", true)
		t.set(func(v *TuneView) {
			v.Output = out
			if err != nil {
				v.Failed, v.Phase, v.Error = v.Phase, "failed", err.Error()
			} else {
				v.Phase = "done"
			}
		})
	}()
	return t.snapshot(), nil
}

// CancelTune stops a tune and the test on the server.
func CancelTune(id string) error {
	t := getTune(id)
	if t == nil {
		return errors.New("no such tune")
	}
	v := t.snapshot()
	if v.Phase == "deploying" {
		return errors.New("the deploy has started; it cannot be cancelled now")
	}
	t.set(func(v *TuneView) { v.Phase = "cancelled" })
	if t.probe != nil {
		StopJob(t.probe.ID)
	}
	if v.Phase != "done" && v.Phase != "failed" {
		go xtune(t.conn, "tune", "--discard")
	}
	return nil
}

func getTune(id string) *tuneJob {
	tunesMu.Lock()
	defer tunesMu.Unlock()
	return tunes[id]
}

// TuneStatus is tune id, or with no id the latest tune of host ("" phase when there is none).
func TuneStatus(id, host string) TuneView {
	if id != "" {
		if t := getTune(id); t != nil {
			return t.snapshot()
		}
		return TuneView{}
	}
	tunesMu.Lock()
	var last *tuneJob
	for _, t := range tunes {
		if t.view.Server == host && (last == nil || t.view.Started >= last.view.Started) {
			last = t
		}
	}
	tunesMu.Unlock()
	if last == nil {
		return TuneView{}
	}
	return last.snapshot()
}

// Update updates xtune or Xray on the server.
func Update(c ServerConn, what string) (string, error) {
	switch what {
	case "xtune":
		return xtune(c, "self-update")
	case "xray":
		return xtune(c, "xray", "update")
	}
	return "", fmt.Errorf("update xtune or xray, not %q", what)
}
