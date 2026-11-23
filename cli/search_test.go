package main

import (
	"bytes"
	"encoding/json"
	mrand "math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/state"
)

func TestFragSpec(t *testing.T) {
	for _, ok := range []string{"frag:tlshello:20-60:5-15", "frag:1-3:100-200:0-0", "none", "frag-mild"} {
		if !protocols.ValidFrag(ok) {
			t.Errorf("%s should be valid", ok)
		}
	}
	for _, bad := range []string{"frag:tlshello:60-20:5-15", "frag:tlshello:20-6000:5-15", "frag:x:1-2:1-2", "frag:tlshello:20-60", "frag-wild"} {
		if protocols.ValidFrag(bad) {
			t.Errorf("%s should be invalid", bad)
		}
	}
	// a config that dials through a spec gets that fragmenting outbound
	st := testState()
	e := state.Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}
	cfg := protocols.ClientConfig(protocols.Outbound(st, e, state.NewUser("u"), state.Knobs{FP: "chrome", Frag: "frag:tlshello:20-60:5-15"}, "proxy"))
	b, _ := json.Marshal(cfg)
	if !strings.Contains(string(b), `"tag":"frag:tlshello:20-60:5-15"`) || !strings.Contains(string(b), `"length":"20-60"`) {
		t.Errorf("spec outbound missing: %s", b)
	}
}

func TestSearchVariantsKeepsGridAndSamples(t *testing.T) {
	entries := []state.Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "www.a.com", Primary: true},
		{ID: "wg", Kind: "wireguard", Port: 51820}}
	grid := ExpandVariants(entries, "smart")
	rng := mrand.New(mrand.NewPCG(1, 2))
	vs := SearchVariants(entries, "smart", nil, Prior{}, rng)
	labels := map[string]bool{}
	for _, v := range vs {
		if labels[v.Label] {
			t.Errorf("duplicate %s", v.Label)
		}
		labels[v.Label] = true
	}
	for _, g := range grid {
		if !labels[g.Label] {
			t.Errorf("search lost the grid's %s", g.Label)
		}
	}
	if len(vs) != len(grid)+searchSamples["smart"] {
		t.Errorf("want %d grid + %d samples, have %d", len(grid), searchSamples["smart"], len(vs))
	}
	// a prior that only likes one fingerprint pulls the samples towards it
	prior := Prior{W: func(kind, dim, v string) float64 {
		if dim == "fp" && v == "safari" {
			return 1
		}
		return 0
	}}
	sp := SpaceFor(entries[0], "smart", nil)
	safari := 0
	for _, k := range sp.Sample(mrand.New(mrand.NewPCG(3, 4)), 40, prior) {
		if k.FP == "safari" {
			safari++
		}
		if !protocols.ValidFrag(k.Frag) {
			t.Errorf("sampled invalid frag %q", k.Frag)
		}
	}
	if safari < 10 {
		t.Errorf("prior ignored: %d of 40 safari", safari)
	}
	// round 2 mutates the best, never WireGuard (its variants are server-side test peers)
	best := []*Variant{variantFor(entries[0], state.Knobs{FP: "chrome", Frag: "frag:tlshello:20-60:5-15"}), variantFor(entries[1], state.Knobs{Frag: "none", MTU: 1420})}
	next := Evolve(best, vs, "smart", Prior{}, mrand.New(mrand.NewPCG(5, 6)), 3)
	if len(next) == 0 {
		t.Fatal("nothing evolved")
	}
	for _, v := range next {
		if v.Entry.Kind == "wireguard" || labels[v.Label] {
			t.Errorf("bad round-2 variant %s", v.Label)
		}
	}
}

func TestPersonalTune(t *testing.T) {
	r, srv := publicServer(t)
	st, _ := state.Load(r)
	_, ann := st.User("ann")
	base := srv.URL + "/subs/" + ann.SubToken + "/tune"
	if res, _ := http.Get(base); res.StatusCode != http.StatusForbidden {
		t.Fatalf("self-tune off: %d", res.StatusCode)
	}
	on := true
	if _, err := SetUserTune(r, "ann", TuneChange{SelfTune: &on}); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(base + "?asn=12345")
	if err != nil {
		t.Fatal(err)
	}
	var tp TestProfile
	json.NewDecoder(res.Body).Decode(&tp)
	res.Body.Close()
	if res.StatusCode != 200 || len(tp.Profile.Configs) < 3 {
		t.Fatalf("GET tune: %d %+v", res.StatusCode, tp)
	}
	// report: a fragmented variant of r-tcp wins, the rest fail
	var win string
	rep := ClientReport{ASN: "12345", Name: "Test ISP"}
	for _, c := range tp.Profile.Configs {
		r := ClientReportResult{Key: c.Key, N: 3, Warm: -1}
		if win == "" && strings.HasPrefix(c.Key, "r-tcp|") && strings.Contains(c.Key, "frag=frag") {
			win, r.Ok, r.Warm, r.Down = c.Key, 3, 120, 20
		}
		rep.Results = append(rep.Results, r)
	}
	if win == "" {
		t.Fatal("no fragmented r-tcp variant to win")
	}
	body, _ := json.Marshal(rep)
	res, err = http.Post(base, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var pr PersonalResult
	json.NewDecoder(res.Body).Decode(&pr)
	res.Body.Close()
	if res.StatusCode != 200 || pr.Measured == 0 || !strings.Contains(pr.Chosen["r-tcp"], "frag=frag") {
		t.Fatalf("POST tune: %d %+v", res.StatusCode, pr)
	}
	st, _ = state.Load(r)
	_, ann = st.User("ann")
	if ann.Tune.Prefs.Knobs["r-tcp"].Frag == "" || ann.Tune.Prefs.Tuned == 0 {
		t.Errorf("prefs: %+v", ann.Tune.Prefs)
	}
	if st.Networks["12345"] == nil || st.Networks["12345"].Scores["r-tcp/443"] == 0 {
		t.Errorf("network ranking not fed: %+v", st.Networks["12345"])
	}
	// the user's client profile now uses the winning fragment setting
	res, _ = http.Get(srv.URL + "/subs/" + ann.SubToken + "/client")
	var p protocols.ClientProfile
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"dialerProxy":"`+ann.Tune.Prefs.Knobs["r-tcp"].Frag+`"`) {
		t.Errorf("client profile without the tuned fragment: %s", b)
	}
	// once per 10 minutes
	res, _ = http.Post(base, "application/json", bytes.NewReader(body))
	if res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second tune at once: %d", res.StatusCode)
	}
}

// RunTests in rounds, measured by a fake: only fragmented REALITY settings get through.
func TestRunTestsRounds(t *testing.T) {
	oldH, oldP, oldB := harnessFor, probeFor, benchFor
	defer func() { harnessFor, probeFor, benchFor = oldH, oldP, oldB }()
	harnesses := 0
	harnessFor = func(string, *state.State, state.User, []*Variant, string, bool) (*harness, error) {
		harnesses++
		return &harness{}, nil
	}
	probes := map[string]int{}
	var mu sync.Mutex
	probeFor = func(v *Variant, n int) {
		mu.Lock()
		probes[v.Label] += n
		mu.Unlock()
		v.N += n
		if strings.Contains(v.K.Frag, "frag") {
			v.Ok += n
			v.Warm = 100 + len(v.K.Frag)
		}
	}
	benchFor = func(v *Variant, _ int, _ func(string)) (float64, float64) { return 10, 5 }
	e := state.Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "www.a.com", Primary: true}
	o := TuneOpts{Probes: 5, Parallel: 2, Top: 4, Rounds: 1, Secs: 1, Evolve: 2, Seed: 7, Depth: "smart", Out: t.TempDir(), Log: func(string) {}}
	vs := SearchVariants([]state.Entry{e}, "smart", nil, Prior{}, o.rng())
	n1 := len(vs)
	out, err := RunTests(o, testState(), state.NewUser("t"), vs)
	if err != nil {
		t.Fatal(err)
	}
	if harnesses != 2 || len(out) <= n1 {
		t.Fatalf("want a second round: %d harnesses, %d -> %d variants", harnesses, n1, len(out))
	}
	confirmed := 0
	for _, v := range out {
		switch {
		case v.Round == 0 && v.Ok == 0 && probes[v.Label] != 2:
			t.Errorf("%s failed the screen but got %d probes (want 2)", v.Label, probes[v.Label])
		case v.Round == 1 && probes[v.Label] != 5:
			t.Errorf("round 2's %s got %d probes (want 5)", v.Label, probes[v.Label])
		case v.Round == 0 && v.Ok > 0 && probes[v.Label] == 5:
			confirmed++
		}
	}
	if confirmed < o.Top {
		t.Errorf("only %d variants confirmed with all probes, want at least %d (successive halving keeps the best)", confirmed, o.Top)
	}
	if out[0].Score == 0 || !strings.Contains(out[0].K.Frag, "frag") || !out[0].Benched {
		t.Errorf("best: %+v", out[0])
	}
}
