package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/netinfo"
	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

func TestOpenRoutesAndRouteVariants(t *testing.T) {
	relay, rfs := relayFake()
	rfs.helper["relay-test-start"] = "active"
	rfs.helper["relay-check"] = "TCP 443 40\nTCP 2053 fail"
	deployed := testState()
	deployed.Relays = []state.Relay{{Name: "teh1", Addr: "198.51.100.20", SSH: state.RelaySSH{Host: "198.51.100.20"},
		Forwards: []state.Forward{{ID: "r-tcp", Port: 443, To: 443, Net: "tcp"}}}}
	entries := []state.Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "www.a.com"},
		{ID: "cdn", Kind: "cdn-xhttp", Port: 2096, Path: "/p"}, {ID: "hy2", Kind: "hysteria2", Port: 8443, Hop: "20000-20999"}}
	dials := 0
	o := TuneOpts{Log: func(string) {}, deployed: deployed, Relays: []string{"all"},
		RelayDial: func(state.Relay) (*remote.Server, error) { dials++; return relay, nil }}
	routes := openRoutes(o, "203.0.113.7", entries)
	if dials != 1 || len(routes) != 1 {
		t.Fatalf("routes: %d dials, %+v", dials, routes)
	}
	rt := routes[0]
	if _, ok := rt.fwd["cdn"]; ok || rt.fwd["r-tcp"] == 0 || rt.fwd["hy2"] == 0 || rt.fwd["r-tcp"] == 443 {
		t.Errorf("forwards: %v (CDN is not relayable; 443 is the relay's own forward)", rt.fwd)
	}
	var cfg J
	json.Unmarshal([]byte(rfs.files[remote.Dir+"/relay-test.json"]), &cfg)
	if len(cfg["inbounds"].([]any)) != 2 || rt.legB[443] != 40 {
		t.Errorf("relay test config / leg B: %v %v", cfg["inbounds"], rt.legB)
	}
	st := testState()
	vs := []*Variant{variantFor(entries[0], state.Knobs{FP: "chrome", Frag: "none"}), variantFor(entries[1], state.Knobs{FP: "chrome", Frag: "none"}),
		variantFor(entries[2], state.Knobs{Frag: "none"})}
	rv := routeVariants(st, vs, routes)
	if len(rv) != 2 {
		t.Fatalf("route variants: %d", len(rv))
	}
	for _, v := range rv {
		vst, e := v.dialTarget(st)
		ob := protocols.Outbound(vst, e, state.NewUser("t"), v.K, "x")
		b, _ := json.Marshal(ob)
		if v.Via != "teh1" || !strings.HasPrefix(v.Label, "via teh1 ") || e.Port != rt.fwd[v.Entry.ID] || e.Hop != "" ||
			!strings.Contains(string(b), "198.51.100.20") || v.Entry.Port == e.Port {
			t.Errorf("route variant %s dials %s: %s", v.Label, e.ID, b)
		}
	}
	closeRoutes(routes, func(string) {})
	if !strings.Contains(strings.Join(rfs.calls, ";"), "relay-test-stop") {
		t.Error("the relay's test forwarder should be stopped")
	}
}

func TestRouteMatrixRankAndServing(t *testing.T) {
	e := state.Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "www.a.com", FP: "chrome", Frag: "none"}
	direct := variantFor(e, e.Knobs())
	direct.Score, direct.Ok, direct.N = 0, 0, 5
	via := variantFor(e, e.Knobs())
	via.Via, via.Label, via.Score, via.Ok, via.N = "teh1", "via teh1 "+via.Label, 12, 5, 5
	vs := []*Variant{direct, via}
	m := routeMatrix(vs, []*relayRoute{{Relay: state.Relay{Name: "teh1"}, legB: map[int]int{443: 30}}})
	if r, s := m.Best("r-tcp"); r != "teh1" || s != 12 || m.Cells["r-tcp"]["teh1"].LegB != 30 {
		t.Errorf("matrix: %+v", m)
	}
	var b strings.Builder
	printRouteMatrix(&b, m)
	if !strings.Contains(b.String(), "blocked") || !strings.Contains(b.String(), "teh1") {
		t.Errorf("matrix table:\n%s", b.String())
	}
	// an entry that only gets through a relay is still deployed
	if best := Best(vs); len(best) != 1 || best[0].Via != "teh1" {
		t.Errorf("Best: %+v", best)
	}
	scores, _ := variantRank([]state.Entry{e}, vs)
	routeRank([]state.Entry{e}, vs, scores)
	if scores["r-tcp/443"] != 0 || scores["via:teh1/r-tcp/443"] != 12 {
		t.Errorf("scores: %v", scores)
	}
	// served: on that network, the relay's copy comes first, before the blocked direct one
	r, _ := deployed(t, state.NewUser("ann"))
	st, _ := state.Load(r)
	st.Relays = []state.Relay{{Name: "teh1", Addr: "198.51.100.20", Target: protocols.RelayTarget(st),
		Forwards: []state.Forward{{ID: "r-tcp", Port: 9443, To: 443, Net: "tcp"}}}}
	state.Save(r, st)
	if err := RecordNetwork(r, netinfo.Network{ASN: "44244", Name: "Irancell"}, scores, nil); err != nil {
		t.Fatal(err)
	}
	st, _ = state.Load(r)
	if st.Networks["44244"].Scores["via:teh1/r-tcp/443"] != 12 {
		t.Fatalf("route score not kept: %v", st.Networks["44244"].Scores)
	}
	view, _ := userView(st, st.Users[0], "44244", -1, time.Now())
	p := protocols.ClientProfileFor(view, st.Users[0])
	if p.Configs[0].ID != "teh1-r-tcp" || p.Configs[len(p.Configs)-1].ID != "r-tcp" {
		var ids []string
		for _, c := range p.Configs {
			ids = append(ids, c.ID)
		}
		t.Errorf("order on that network: %v", ids)
	}
	// an app's report on the relay's copy feeds the route's score
	if err := applyClientReport(r.Lock, func() (*state.State, error) { return requireState(r) },
		func(st *state.State) error { return state.Save(r, st) },
		ClientReport{ASN: "44244", Results: []ClientReportResult{{Key: "teh1-r-tcp/9443", Ok: 0, N: 3, Warm: -1}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	st, _ = state.Load(r)
	if s := st.Networks["44244"].Scores["via:teh1/r-tcp/443"]; s != 6 {
		t.Errorf("report on the route: %v", s)
	}
}
