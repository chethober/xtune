package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"text/tabwriter"
	"time"
)

// `xtune check` measures the deployed configs from this network, quickly: are they reachable,
// how fast do they answer, and do they hold their speed or get throttled after a few seconds.
// It uses a hidden check user, so no real user's traffic or devices are touched.

// CheckResult is one deployed config as measured from here.
type CheckResult struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Ok     int     `json:"ok"`
	N      int     `json:"n"`
	Warm   int     `json:"warmMs"`
	Down   float64 `json:"downMbps"`
	Early  float64 `json:"earlyMbps,omitempty"` // download rate in the first and last third of the check
	Late   float64 `json:"lateMbps,omitempty"`
	Up     float64 `json:"upMbps,omitempty"`
	Score  float64 `json:"score,omitempty"`
	Status string  `json:"status"` // ok | slow | throttled | flaky | blocked
}

// ensureCheckUser gives the deployment its hidden check user, live, when it has none.
func ensureCheckUser(r *Server) (*State, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	if st.Check != nil {
		return st, nil
	}
	u := NewUser(checkUserName)
	u.SubToken = ""
	st.Check = &u
	return st, commit(r, st)
}

// throughput is a sustained download: the average, and the rate in its first and last third,
// which shows throttling that starts after a few seconds.
type throughput struct{ Avg, Early, Late float64 }

type rateWriter struct {
	t0    time.Time
	marks []mark // cumulative bytes over time
	n     int64
}

type mark struct {
	t time.Duration
	n int64
}

func (w *rateWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	w.marks = append(w.marks, mark{time.Since(w.t0), w.n})
	return len(p), nil
}

// at is the byte count at time t.
func (w *rateWriter) at(t time.Duration) int64 {
	var n int64
	for _, m := range w.marks {
		if m.t > t {
			break
		}
		n = m.n
	}
	return n
}

func (w *rateWriter) rates(el time.Duration) throughput {
	if el <= 0 {
		return throughput{}
	}
	third := el / 3
	mbps := func(bytes int64, d time.Duration) float64 { return round2(float64(bytes) * 8 / d.Seconds() / 1e6) }
	return throughput{Avg: mbps(w.n, el), Early: mbps(w.at(third), third), Late: mbps(w.n-w.at(2*third), el-2*third)}
}

// sustained downloads for secs seconds through the variant from the first mirror that answers.
func sustained(v *Variant, secs int, log func(string)) throughput {
	c := socksClient(v.port, 10*time.Second)
	mirrorsMu.Lock()
	ms := append([]string(nil), mirrors...)
	mirrorsMu.Unlock()
	for _, u := range ms {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs)*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		resp, err := c.Do(req)
		if err != nil {
			cancel()
			return throughput{}
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			cancel()
			log(fmt.Sprintf("  [!] %s answered HTTP %d; trying the next mirror", u, resp.StatusCode))
			continue
		}
		w := &rateWriter{t0: time.Now()}
		io.Copy(w, resp.Body)
		el := time.Since(w.t0)
		resp.Body.Close()
		cancel()
		return w.rates(el)
	}
	return throughput{}
}

// checkStatus judges one measured config.
func checkStatus(r CheckResult, t throughput) string {
	switch {
	case r.Ok == 0:
		return "blocked"
	case r.Ok*2 < r.N:
		return "flaky"
	case t.Early >= 2 && t.Late < 0.4*t.Early:
		return "throttled"
	case t.Avg < 1:
		return "slow"
	}
	return "ok"
}

// Check measures every deployed config from this machine.
func Check(r *Server, xrayBin string, probes, secs int, out string, log func(string)) ([]CheckResult, *State, error) {
	st, err := ensureCheckUser(r)
	if err != nil {
		return nil, nil, err
	}
	if len(st.Entries) == 0 {
		return nil, st, fmt.Errorf("no configs are deployed")
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, nil, err
	}
	var vs []*Variant
	for _, e := range supported(xrayBin, st, st.Entries, log) {
		vs = append(vs, &Variant{Entry: e, K: e.Knobs(), Label: fmt.Sprintf("%s:%d", e.ID, e.Port), Cold: -1, Warm: -1})
	}
	h, err := startHarness(xrayBin, st, *st.Check, vs, out)
	if err != nil {
		return nil, st, err
	}
	setActiveHarness(h)
	defer func() { setActiveHarness(nil); h.stop() }()
	log(fmt.Sprintf("[*] checking %d deployed configs from this network (%d probes, %ds download each)", len(vs), probes, secs))
	var wg sync.WaitGroup
	for _, v := range vs {
		wg.Add(1)
		go func(v *Variant) { defer wg.Done(); probe(v, probes) }(v)
	}
	wg.Wait()
	var res []CheckResult
	for _, v := range vs {
		c := CheckResult{ID: v.Entry.ID, Label: v.Label, Ok: v.Ok, N: v.N, Warm: v.Warm}
		var t throughput
		if v.Ok > 0 {
			t = sustained(v, secs, log)
		}
		c.Down, c.Early, c.Late = t.Avg, t.Early, t.Late
		c.Status = checkStatus(c, t)
		log(fmt.Sprintf("  %-34s ok=%d/%d warm=%5s down=%6.2f Mbps (first third %.2f, last %.2f)  %s",
			v.Label, v.Ok, v.N, ms(v.Warm), t.Avg, t.Early, t.Late, c.Status))
		res = append(res, c)
	}
	return res, st, nil
}

func printCheck(w io.Writer, res []CheckResult) (bad int) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "CONFIG\tSTATUS\tOK\tWARM\tDOWN\tFIRST/LAST THIRD\n")
	for _, c := range res {
		fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%sms\t%.2f Mbps\t%.2f / %.2f\n", c.Label, c.Status, c.Ok, c.N, ms(c.Warm), c.Down, c.Early, c.Late)
		if c.Status != "ok" {
			bad++
		}
	}
	tw.Flush()
	return bad
}

// ---------------------------------------------------------------- history

// Record is one tune or check, kept in history.jsonl so changes over days and networks show.
type Record struct {
	Time    int64         `json:"time"`
	Host    string        `json:"host"`
	Network string        `json:"network,omitempty"`
	Kind    string        `json:"kind"` // tune | check | measure
	Results []CheckResult `json:"results"`
}

func historyPath() string { return filepath.Join(dataDir(), "history.jsonl") }

func appendHistory(rec Record) error {
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(historyPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(rec)
	_, err = f.Write(append(b, '\n'))
	return err
}

// loadHistory returns the records for host ("" = all), oldest first.
func loadHistory(host string) ([]Record, error) {
	f, err := os.Open(historyPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && (host == "" || r.Host == host) {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

// tuneRecord keeps the best variant per config of a tune.
func tuneRecord(host, network string, best []*Variant) Record {
	rec := Record{Time: time.Now().Unix(), Host: host, Network: network, Kind: "tune"}
	for _, v := range best {
		rec.Results = append(rec.Results, CheckResult{ID: v.Entry.ID, Label: v.Label, Ok: v.Ok, N: v.N, Warm: v.Warm,
			Down: v.Down, Up: v.Up, Score: v.Score, Status: map[bool]string{true: "ok", false: "blocked"}[v.Ok > 0]})
	}
	return rec
}

func printHistory(w io.Writer, recs []Record, last int) {
	if len(recs) == 0 {
		fmt.Fprintln(w, "no history yet: it is recorded by xtune tune and xtune check")
		return
	}
	if len(recs) > last {
		recs = recs[len(recs)-last:]
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "WHEN\tSERVER\tNETWORK\tKIND\tCONFIG\tSTATUS\tOK\tWARM\tDOWN\n")
	for _, r := range recs {
		when := time.Unix(r.Time, 0).Format("2006-01-02 15:04")
		for i, c := range r.Results {
			head := when + "\t" + r.Host + "\t" + orStr(r.Network, "-") + "\t" + r.Kind
			if i > 0 {
				head = "\t\t\t"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d/%d\t%sms\t%.2f\n", head, c.ID, c.Status, c.Ok, c.N, ms(c.Warm), c.Down)
		}
	}
	tw.Flush()
}
