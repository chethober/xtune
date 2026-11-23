package main

import (
	"bytes"
	"encoding/json"
	mrand "math/rand/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

func TestMemoryHedgeForgets(t *testing.T) {
	nm := &state.NetMemory{}
	now := int64(1_800_000_000)
	for range 4 {
		nm.Observe("reality-tcp", "frag", "frag-aggr", 1, now)
		nm.Observe("reality-tcp", "frag", "none", 0, now)
	}
	good, _ := nm.Weight("reality-tcp", "frag", "frag-aggr", now)
	bad, _ := nm.Weight("reality-tcp", "frag", "none", now)
	if good < 0.9 || bad > 0.1 {
		t.Fatalf("weights after evidence: %.2f %.2f", good, bad)
	}
	// three weeks on, without news, the evidence has mostly faded: the censor may have changed
	later := now + 21*24*3600
	good, _ = nm.Weight("reality-tcp", "frag", "frag-aggr", later)
	bad, _ = nm.Weight("reality-tcp", "frag", "none", later)
	if good > 0.6 || bad < 0.4 {
		t.Errorf("old evidence should fade: %.2f %.2f", good, bad)
	}
	if w, known := nm.Weight("cdn-xhttp", "fp", "chrome", now); known || w != 0.5 {
		t.Error("unknown values are even")
	}
	// a value that starts failing loses its weight within a few observations
	nm.Observe("reality-tcp", "frag", "frag-aggr", 0, later)
	nm.Observe("reality-tcp", "frag", "frag-aggr", 0, later)
	if w, _ := nm.Weight("reality-tcp", "frag", "frag-aggr", later); w > 0.4 {
		t.Errorf("newly blocked value kept its weight: %.2f", w)
	}
}

func TestLearnGuidesTheNextSearch(t *testing.T) {
	e := state.Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "www.a.com"}
	spec := "frag:tlshello:30-50:3-6"
	var vs []*Variant
	for _, fp := range []string{"chrome", "firefox", "safari"} {
		for _, f := range []string{"none", "frag-mild", spec} {
			v := variantFor(e, state.Knobs{FP: fp, Frag: f})
			v.N = 5
			if fp == "safari" && f == spec {
				v.Ok, v.Warm, v.Down = 5, 90, 30
			}
			v.Score = score(v)
			vs = append(vs, v)
		}
	}
	m := &state.Memory{}
	now := time.Now().Unix()
	learnVariants(m, "44244", "Irancell", vs, now)
	if m.Nets["44244"].Name != "Irancell" || m.Nets[""] == nil || len(m.Nets["44244"].Frags) != 1 {
		t.Fatalf("memory: %+v", m.Nets)
	}
	p := priorFrom(m, "44244", now)
	if p.W("reality-tcp", "fp", "safari") <= p.W("reality-tcp", "fp", "chrome") || p.W("reality-tcp", "frag", "range") <= p.W("reality-tcp", "frag", "none") {
		t.Errorf("prior: safari %.2f chrome %.2f range %.2f", p.W("reality-tcp", "fp", "safari"), p.W("reality-tcp", "fp", "chrome"),
			p.W("reality-tcp", "frag", "range"))
	}
	// another network falls back to what all networks learned
	if p2 := priorFrom(m, "12880", now); p2.W("reality-tcp", "fp", "safari") <= p2.W("reality-tcp", "fp", "chrome") {
		t.Error("an unknown network should use all networks' memory")
	}
	// the next tune's samples lean to safari and to ranges near the one that worked
	sp := SpaceFor(e, "smart", nil)
	safari, chrome, near := 0, 0, 0
	ks := sp.Sample(mrand.New(mrand.NewPCG(9, 9)), 60, p)
	for _, k := range ks {
		switch k.FP {
		case "safari":
			safari++
		case "chrome":
			chrome++
		}
		if m, ok := protocols.FragSpec(k.Frag); ok && m["packets"] == "tlshello" && len(m["length"]) <= 5 {
			lo := 0
			for _, c := range strings.Split(m["length"], "-")[0] {
				lo = lo*10 + int(c-'0')
			}
			if lo >= 15 && lo <= 70 {
				near++
			}
		}
	}
	// values never tried keep a good chance (exploration); of the tried ones, the one that worked wins
	if safari <= chrome || near < 5 {
		t.Errorf("samples ignore memory: %d safari, %d chrome, %d near the good range", safari, chrome, near)
	}
}

// memoryOn has loadMemory read the fake server's memory.json.
func memoryOn(t *testing.T, r *remote.Server) {
	old := loadMemory
	loadMemory = func() *state.Memory { m, _ := state.LoadMemory(r); return m }
	t.Cleanup(func() { loadMemory = old })
}

func TestReportsOrderTheUsersOwnSubscription(t *testing.T) {
	r, srv := publicServer(t)
	memoryOn(t, r)
	st, _ := state.Load(r)
	_, ann := st.User("ann")
	// ann's own app says ss2022 works for her on her network and REALITY does not
	rep := ClientReport{ASN: "44244", Name: "Irancell", Results: []ClientReportResult{
		{Key: "r-tcp/443", Ok: 0, N: 3, Warm: -1}, {Key: "ss2022/30001", Ok: 3, N: 3, Warm: 80, Down: 20}}}
	body, _ := json.Marshal(rep)
	res, err := http.Post(srv.URL+"/subs/"+ann.SubToken+"/report", "application/json", bytes.NewReader(body))
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("report: %v %v", err, res.StatusCode)
	}
	m, _ := state.LoadMemory(r)
	if un := m.Users["ann"].Nets["44244"]; un == nil || un.Scores["ss2022/30001"] == 0 || un.Scores["r-tcp/443"] != 0 {
		t.Fatalf("ann's memory: %+v", m.Users["ann"])
	}
	if w, _ := m.Nets["44244"].Weight("reality-tcp", "frag", orStr(st.Entries[0].Frag, "none"), time.Now().Unix()); w >= 0.5 {
		t.Errorf("the network should learn REALITY's settings failed: %.2f", w)
	}
	res, _ = http.Get(srv.URL + "/subs/" + ann.SubToken + "/client?asn=44244")
	var p protocols.ClientProfile
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	if len(p.Configs) < 2 || p.Configs[0].ID != "ss2022" {
		t.Errorf("ann's configs on her network should start with what works for her: %+v", p.Configs)
	}
	// turned off by the user: back to the deployment's order (here, with no network ranking)
	st, _ = state.Load(r)
	st.Networks = nil
	state.Save(r, st)
	off := false
	if _, err := setUserPrefs(r, "ann", prefsBody{Auto: &off}); err != nil {
		t.Fatal(err)
	}
	res, _ = http.Get(srv.URL + "/subs/" + ann.SubToken + "/client?asn=44244")
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	if p.Configs[0].ID != "r-tcp" {
		t.Errorf("auto off: %s first", p.Configs[0].ID)
	}
}

func TestMemoryPrune(t *testing.T) {
	m := &state.Memory{}
	now := time.Now().Unix()
	m.Net("1").Observe("k", "fp", "chrome", 1, now-90*24*3600)
	m.Net("2").Observe("k", "fp", "chrome", 1, now)
	learnUser(m, "gone", "2", map[string]float64{"r-tcp/443": 3}, now)
	learnUser(m, "ann", "2", map[string]float64{"r-tcp/443": 3, "old/1": 2}, now)
	m.Prune(now, map[string]bool{"ann": true}, map[string]bool{"r-tcp/443": true})
	if m.Nets["1"] != nil || m.Nets["2"] == nil || m.Users["gone"] != nil || len(m.Users["ann"].Nets["2"].Scores) != 1 {
		t.Errorf("prune: %+v %+v", m.Nets, m.Users)
	}
}
