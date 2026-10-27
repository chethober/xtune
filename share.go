package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// Several networks, one deployment. `xtune tune --share bundle.json` leaves the test matrix
// running on the server (it expires after 2 hours) and writes a bundle anyone can measure it with,
// no SSH needed: `xtune measure bundle.json --network NAME` writes a report. `xtune deploy
// REPORT...` then deploys configs chosen so every network has at least one that works there.

const testLifetime = 2 * time.Hour // RuntimeMaxSec of the test instance, see helper.sh

// Bundle is what a tester needs to measure the test matrix. It holds no server secrets: the
// test user only exists on the temporary test instance.
type Bundle struct {
	ID      string  `json:"id"`
	Created int64   `json:"created"`
	Expires int64   `json:"expires"`
	Depth   string  `json:"depth"`
	State   *State  `json:"state"` // public parts only
	User    User    `json:"user"`
	Entries []Entry `json:"entries"`
}

// Pending is a finished tune waiting on the server (pending.json) to be deployed.
type Pending struct {
	ID       string     `json:"id"`
	Created  int64      `json:"created"`
	Network  string     `json:"network,omitempty"`
	ASN      string     `json:"asn,omitempty"` // the tuning network's, for the per-network rankings
	State    *State     `json:"state"`
	Variants []*Variant `json:"variants"`
}

// NetReport is one network's measurements of a bundle.
type NetReport struct {
	ID      string        `json:"id"`
	Network string        `json:"network"`
	ASN     string        `json:"asn,omitempty"`
	ASName  string        `json:"asName,omitempty"`
	Time    int64         `json:"time"`
	Results []CheckResult `json:"results"`
}

const pendingFile = remoteDir + "/pending.json"

// publicState is what clients need to connect: no private keys.
func publicState(st *State) *State {
	p := st.public()
	p.Entries = nil
	return p
}

// Share stores the tune on the server for a later deploy and returns the bundle for testers.
func Share(r *Server, res *TuneResult, depth, network string) (*Bundle, error) {
	now := time.Now()
	p := Pending{ID: randHex(8), Created: now.Unix(), Network: orStr(network, res.NetName), ASN: res.Network.ASN,
		State: res.State, Variants: res.All}
	b, _ := json.Marshal(p)
	if err := r.Upload(pendingFile, b, "600"); err != nil {
		return nil, err
	}
	return &Bundle{ID: p.ID, Created: now.Unix(), Expires: now.Add(testLifetime).Unix(), Depth: depth,
		State: publicState(res.State), User: res.User, Entries: res.Entries}, nil
}

func loadPending(r *Server) (*Pending, error) {
	out, err := r.Root("cat " + pendingFile + " 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("no tune is waiting to be deployed on %s: run xtune tune first", r.Name())
	}
	var p Pending
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		return nil, fmt.Errorf("parse pending.json: %w", err)
	}
	return &p, nil
}

func readJSONFile(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Measure runs a bundle's test matrix from this machine.
func Measure(b *Bundle, o TuneOpts) (*NetReport, error) {
	o.defaults()
	if time.Now().Unix() > b.Expires {
		return nil, fmt.Errorf("this bundle expired at %s: the test instance on the server is gone; ask for a new one",
			time.Unix(b.Expires, 0).Format("15:04"))
	}
	if err := os.MkdirAll(o.Out, 0o700); err != nil {
		return nil, err
	}
	netw := detectNetwork(&o.Network, o.Log)
	entries := supported(o.XrayBin, b.State, b.Entries, o.Log)
	vs := supportedKnobs(o.XrayBin, b.State, ExpandVariants(entries, b.Depth, o.Resolvers...), o.Log)
	vs = addCleanIPVariants(b.State, entries, vs, o.CleanIPs, o.Log) // this network's own clean IPs
	err := RunTests(o, b.State, b.User, vs)
	if err != nil && !strings.Contains(err.Error(), "nothing connected") {
		return nil, err
	}
	if err != nil {
		o.Log("[!] nothing connected from this network; the report says so")
	} else {
		var buf strings.Builder
		if _, err := Report(&buf, o, b.State, b.User, vs); err == nil {
			o.Log(buf.String())
		}
	}
	rep := &NetReport{ID: b.ID, Network: o.Network, ASN: netw.ASN, ASName: netw.Name, Time: time.Now().Unix()}
	for _, v := range vs {
		rep.Results = append(rep.Results, CheckResult{ID: v.Entry.ID, Label: v.Label, Ok: v.Ok, N: v.N, Warm: v.Warm,
			Down: v.Down, Up: v.Up, Score: v.Score})
	}
	return rep, nil
}

// ---------------------------------------------------------------- merging networks

// netScores are one network's scores by variant label.
type netScores struct {
	Name  string
	Score map[string]float64
}

// mergeChoice picks the configs to deploy for several networks: first, greedily, configs that
// cover networks nothing chosen does well on yet (half that network's best score or more); then
// the best overall up to n; plus a working CDN config. Each config's client settings are those that score best across all networks, each
// network's scores relative to its own best. uncovered lists networks where nothing worked.
func mergeChoice(vs []*Variant, nets []netScores, n int) (chosen []*Variant, uncovered []string) {
	best := make([]float64, len(nets))
	for k, ns := range nets {
		for _, s := range ns.Score {
			best[k] = max(best[k], s)
		}
		if best[k] == 0 {
			uncovered = append(uncovered, ns.Name)
		}
	}
	total := func(v *Variant) float64 {
		t := 0.0
		for k, ns := range nets {
			if best[k] > 0 {
				t += ns.Score[v.Label] / best[k]
			}
		}
		return t
	}
	// the best variant of each config: like pick, the simplest within 10% of the top
	tot := map[*Variant]float64{}
	groups := map[string][]*Variant{}
	var ids []string
	for _, v := range vs {
		tot[v] = total(v)
		if _, ok := groups[v.Entry.ID]; !ok {
			ids = append(ids, v.Entry.ID)
		}
		groups[v.Entry.ID] = append(groups[v.Entry.ID], v)
	}
	var cands []*Variant
	for _, id := range ids {
		top := 0.0
		for _, v := range groups[id] {
			top = max(top, tot[v])
		}
		var b *Variant
		for _, v := range groups[id] {
			if top > 0 && tot[v] >= 0.9*top && (b == nil || simplicity(v) < simplicity(b) ||
				simplicity(v) == simplicity(b) && tot[v] > tot[b]) {
				b = v
			}
		}
		if b != nil {
			cands = append(cands, b)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return tot[cands[i]] > tot[cands[j]] })
	// a network is covered by a config that does well there, not one that barely connects
	works := func(v *Variant, k int) bool { return nets[k].Score[v.Label] >= 0.5*best[k] && best[k] > 0 }
	need := map[int]bool{}
	for k := range nets {
		if best[k] > 0 {
			need[k] = true
		}
	}
	taken := map[*Variant]bool{}
	for len(need) > 0 {
		var pick *Variant
		gain := 0
		for _, v := range cands {
			g := 0
			for k := range need {
				if works(v, k) {
					g++
				}
			}
			if !taken[v] && g > gain {
				pick, gain = v, g
			}
		}
		if pick == nil {
			break
		}
		taken[pick] = true
		chosen = append(chosen, pick)
		for k := range need {
			if works(pick, k) {
				delete(need, k)
			}
		}
	}
	for _, v := range cands {
		if len(chosen) >= n {
			break
		}
		if !taken[v] {
			taken[v] = true
			chosen = append(chosen, v)
		}
	}
	for _, v := range cands {
		if v.Entry.CDN() && !taken[v] { // the fallback for a blocked IP, like deployChoice
			chosen = append(chosen, v)
		}
	}
	sort.SliceStable(chosen, func(i, j int) bool { return tot[chosen[i]] > tot[chosen[j]] })
	return chosen, uncovered
}

// DeployPending deploys the tune waiting on the server, merged with other networks' reports.
func DeployPending(r *Server, reports []*NetReport, n int, w io.Writer) (*State, error) {
	p, err := loadPending(r)
	if err != nil {
		return nil, err
	}
	own := netScores{Name: orStr(p.Network, "your network"), Score: map[string]float64{}}
	for _, v := range p.Variants {
		own.Score[v.Label] = v.Score
	}
	nets := []netScores{own}
	for _, rep := range reports {
		if rep.ID != p.ID {
			return nil, fmt.Errorf("the report from %q is for another tune (%s, waiting: %s)", rep.Network, rep.ID, p.ID)
		}
		ns := netScores{Name: orStr(rep.Network, fmt.Sprintf("network %d", len(nets)+1)), Score: map[string]float64{}}
		for _, c := range rep.Results {
			ns.Score[c.Label] = c.Score
		}
		nets = append(nets, ns)
	}
	chosen, uncovered := mergeChoice(p.Variants, nets, n)
	for i, v := range chosen {
		chosen[i] = domainVariant(v, p.Variants)
	}
	if len(chosen) == 0 {
		return nil, fmt.Errorf("no config worked on any network")
	}
	fmt.Fprintf(w, "\nChosen for %d network(s):\n", len(nets))
	for _, v := range chosen {
		var on []string
		for _, ns := range nets {
			if ns.Score[v.Label] > 0 {
				on = append(on, ns.Name)
			}
		}
		fmt.Fprintf(w, "  %-56s works on: %s\n", v.Label, strings.Join(on, ", "))
	}
	for _, name := range uncovered {
		fmt.Fprintf(w, "  [!] nothing worked on %s\n", name)
	}
	st, err := DeployVariants(r, p.State, chosen)
	if err != nil {
		return st, err
	}
	labels := map[string]string{}
	for _, v := range chosen {
		labels[v.Entry.ID] = v.Label
	}
	scores, addrs := variantRank(st.Entries, p.Variants)
	if err := RecordNetwork(r, Network{ASN: p.ASN, Name: p.Network}, scores, addrs); err != nil {
		fmt.Fprintln(w, "[!] ranking the configs for "+own.Name+":", err)
	}
	for _, rep := range reports {
		scores, addrs := labelRank(st.Entries, labels, rep.Results)
		if err := RecordNetwork(r, Network{ASN: rep.ASN, Name: orStr(rep.ASName, rep.Network)}, scores, addrs); err != nil {
			fmt.Fprintln(w, "[!] ranking the configs for "+rep.Network+":", err)
		}
	}
	r.Root("rm -f " + shq(pendingFile))
	if _, err := r.Helper("stop-test"); err != nil {
		fmt.Fprintln(w, "[!] stopping the test instance failed (it expires by itself):", err)
	}
	return st, nil
}
