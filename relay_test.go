package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chethober/xtune/internal/protocols"
	"github.com/chethober/xtune/internal/remote"
	"github.com/chethober/xtune/internal/state"
)

func relayEntries() []state.Entry {
	return []state.Entry{
		{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"},
		{ID: "hy2", Kind: "hysteria2", Port: 8443, Hop: "20000-20999"},
		{ID: "ss2022", Kind: "ss2022", Port: 30001},
		{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2096, Path: "/p"},
		{ID: "dnstt", Kind: "dnstt", Port: 5300},
		{ID: "ovpn", Kind: "openvpn-udp", Port: 1194},
		{ID: "wg", Kind: "wireguard", Port: 51820},
	}
}

func TestRelayable(t *testing.T) {
	want := map[string]bool{"r-tcp": true, "hy2": true, "ss2022": true, "wg": true, "cdn-xhttp": false, "dnstt": false, "ovpn": false}
	for _, e := range relayEntries() {
		if protocols.Relayable(e) != want[e.ID] {
			t.Errorf("%s relayable = %v", e.ID, protocols.Relayable(e))
		}
	}
	for _, k := range []string{"vlessenc-xhttp", "trojan-reality", "reality-grpc", "ssh", "ssh-tls", "mtproto"} {
		if !protocols.Relayable(state.Entry{Kind: k}) {
			t.Errorf("%s should be relayable", k)
		}
	}
	for _, k := range []string{"worker-ws", "cdn-ssh", "amneziawg", "ikev2"} {
		if protocols.Relayable(state.Entry{Kind: k}) {
			t.Errorf("%s should not be relayable", k)
		}
	}
}

func TestPlanForwards(t *testing.T) {
	st := testState()
	st.Entries = relayEntries()
	used := map[string]map[int]bool{"tcp": {443: true, 22: true}, "udp": {}}
	fs := planForwards(st, nil, used, nil)
	got := map[string]state.Forward{}
	for _, f := range fs {
		got[f.ID] = f
	}
	if len(fs) != 4 || got["cdn-xhttp"].ID != "" || got["dnstt"].ID != "" {
		t.Fatalf("forwards: %+v", fs)
	}
	if f := got["r-tcp"]; f.To != 443 || f.Port == 443 || f.Port < 20000 || f.Net != "tcp" {
		t.Errorf("443 is taken on the relay, want a high port: %+v", f)
	}
	if f := got["hy2"]; f.Port != 8443 || f.Net != "udp" {
		t.Errorf("free port kept: %+v", f)
	}
	// a sync keeps the relay's ports even though they show as used (by the relay itself)
	used = map[string]map[int]bool{"tcp": {443: true, got["r-tcp"].Port: true, 30001: true}, "udp": {8443: true}}
	again := planForwards(st, fs, used, nil)
	for i := range again {
		if again[i].Port != fs[i].Port {
			t.Errorf("sync moved %s from %d to %d", again[i].ID, fs[i].Port, again[i].Port)
		}
	}
	if only := planForwards(st, nil, map[string]map[int]bool{"tcp": {}, "udp": {}}, []string{"ss2022"}); len(only) != 1 || only[0].ID != "ss2022" {
		t.Errorf("--only: %+v", only)
	}
}

func TestRelayConfig(t *testing.T) {
	cfg := relayConfig("203.0.113.7", []state.Forward{{ID: "r-tcp", Port: 443, To: 443, Net: "tcp"}, {ID: "hy2", Port: 9000, To: 8443, Net: "udp"}})
	b, _ := json.Marshal(cfg)
	for _, want := range []string{`"protocol":"dokodemo-door"`, `"address":"203.0.113.7"`, `"port":9000`, `"port":8443`, `"network":"udp"`, `"tag":"fwd-hy2"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("relay config lacks %s:\n%s", want, b)
		}
	}
}

func relayedState() *state.State {
	st := keyedStateNoT()
	st.Entries = relayEntries()
	st.Naming.Flag = "DE"
	st.Relays = []state.Relay{{Name: "teh1", Addr: "198.51.100.20", Target: st.Host, Forwards: []state.Forward{
		{ID: "r-tcp", Port: 20443, To: 443, Net: "tcp", Ms: 40}, {ID: "hy2", Port: 8443, To: 8443, Net: "udp"}}}}
	return st
}

// keyedStateNoT is a test state with the keys links need.
func keyedStateNoT() *state.State {
	st := testState()
	st.WGPub, st.WGPriv = "wgpub", "wgpriv"
	return st
}

func TestRelayLinks(t *testing.T) {
	st := relayedState()
	u := state.User{Name: "bob", UUID: "11111111-2222-4333-8444-555555555555", SSKey: "dXNlcmtleXVzZXJrZXk="}
	links := map[string]string{}
	for _, l := range protocols.NamedLinks(st, u) {
		links[l.Name] = l.URL
	}
	r := links["teh1-r-tcp"]
	if !strings.Contains(r, "@198.51.100.20:20443?") || !strings.Contains(r, "sni=a.com") || linkName(t, r) != "🇩🇪 bob-teh1-r-tcp" {
		t.Errorf("REALITY through the relay: %s", r)
	}
	if h := links["teh1-hy2"]; !strings.Contains(h, "@198.51.100.20:8443?") || strings.Contains(h, "mport") {
		t.Errorf("Hysteria2 through the relay (no port hopping there): %s", h)
	}
	if _, ok := links["teh1-ss2022"]; ok {
		t.Error("a config the relay does not forward is listed")
	}
	if !strings.Contains(links["r-tcp"], "@203.0.113.7:443?") {
		t.Errorf("the direct config changed: %s", links["r-tcp"])
	}
	// {via} names the relay in custom templates
	st.Naming.Config = "{flag} {proto} {via}"
	if n := protocols.ConfigName(protocols.RelayStates(st)[0], protocols.RelayStates(st)[0].Entries[0], u); n != "🇩🇪 REALITY via teh1" {
		t.Errorf("via name %q", n)
	}
	if n := protocols.ConfigName(st, st.Entries[0], u); n != "🇩🇪 REALITY" {
		t.Errorf("direct name %q", n)
	}
	// the failover config tries the relay too
	b, _ := json.Marshal(protocols.FailoverConfig(st, u))
	if !strings.Contains(string(b), `"proxy-teh1-r-tcp"`) || !strings.Contains(string(b), "198.51.100.20") {
		t.Error("the failover config does not use the relay")
	}
}

func TestStaleRelaysLeftOut(t *testing.T) {
	st := relayedState()
	st.Entries[0].Port = 8444 // redeployed: the relay still forwards to 443
	rs := protocols.RelayStates(st)
	if len(rs) != 1 || len(rs[0].Entries) != 1 || rs[0].Entries[0].ID != "teh1-hy2" {
		t.Errorf("a changed config should be left out: %+v", rs)
	}
	if p := relayProblem(st, st.Relays[0], ""); !strings.Contains(p, "1 of its configs changed") {
		t.Errorf("problem: %q", p)
	}
	st = relayedState()
	st.Host = "192.0.2.50" // migrated
	if len(protocols.RelayStates(st)) != 0 || !strings.Contains(relayProblem(st, st.Relays[0], ""), "relay sync teh1") {
		t.Error("a relay forwarding to the old address should be left out")
	}
}

func relayFake() (*remote.Server, *fakeServer) {
	fs := newFake()
	fs.helper = map[string]string{"status": "ARCH x86_64\nXRAY 26.1.1\nSTATE no", "relay-apply": "active",
		"relay-check": "TCP 443 35\nTCP 30001 fail", "relay-remove": "removed"}
	fs.used = "USED tcp 0.0.0.0:22\nUSED tcp 0.0.0.0:443"
	return &remote.Server{Transport: fs, Conn: remote.Conn{Host: "198.51.100.20", Port: 22, User: "root"}}, fs
}

func TestSyncAndRemoveRelay(t *testing.T) {
	r, _ := deployed(t, state.NewUser("ann"))
	st, _ := state.Load(r)
	st.Entries = append(st.Entries, state.Entry{ID: "hy2", Kind: "hysteria2", Port: 8443})
	state.Save(r, st)
	relay, rfs := relayFake()

	var logs []string
	st, err := SyncRelay(r, relay, RelayOpts{Name: "teh1", Log: func(s string) { logs = append(logs, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Relays) != 1 {
		t.Fatalf("relays: %+v", st.Relays)
	}
	rl := st.Relays[0]
	ids := []string{}
	for _, f := range rl.Forwards {
		ids = append(ids, f.ID)
	}
	if rl.Addr != "198.51.100.20" || rl.Target != "203.0.113.7" || rl.SSH.Host != "198.51.100.20" || rl.SSH.Port != 0 || rl.SSH.User != "" {
		t.Errorf("relay: %+v", rl)
	}
	if strings.Join(ids, ",") != "r-tcp,hy2" {
		t.Errorf("forwards %v: ss2022 (unreachable from the relay) should be left out", ids)
	}
	if rl.Forwards[0].Ms != 35 || rl.Forwards[0].Port == 443 {
		t.Errorf("r-tcp forward: %+v (443 is taken on the relay)", rl.Forwards[0])
	}
	var cfg J
	if err := json.Unmarshal([]byte(rfs.files[remote.Dir+"/relay.json"]), &cfg); err != nil || len(cfg["inbounds"].([]any)) != 2 {
		t.Errorf("relay.json: %v %s", err, rfs.files[remote.Dir+"/relay.json"])
	}
	if !strings.Contains(strings.Join(rfs.calls, "\n"), "relay-apply") {
		t.Errorf("relay not applied: %v", rfs.calls)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "not reachable from the relay") {
		t.Errorf("logs: %v", logs)
	}
	if loaded, _ := state.Load(r); len(loaded.Relays) != 1 {
		t.Error("relay not saved")
	}

	if _, err := SyncRelay(r, relay, RelayOpts{Name: "Bad Name"}); err == nil {
		t.Error("a bad relay name was accepted")
	}
	self := &remote.Server{Transport: rfs, Conn: remote.Conn{Host: "203.0.113.7"}}
	if _, err := SyncRelay(r, self, RelayOpts{Name: "self"}); err == nil {
		t.Error("the server was accepted as its own relay")
	}
	rfs.helper["relay-check"] = "TCP 443 fail\nTCP 30001 fail"
	if _, err := SyncRelay(r, relay, RelayOpts{Name: "teh2", Only: []string{"r-tcp"}}); err == nil {
		t.Error("a relay that reaches nothing was accepted")
	}

	if st, err = RemoveRelay(r, relay, "teh1"); err != nil || len(st.Relays) != 0 {
		t.Fatalf("remove: %v %+v", err, st.Relays)
	}
	if !strings.Contains(strings.Join(rfs.calls, "\n"), "relay-remove") {
		t.Error("relay not stopped")
	}
	if _, err := RemoveRelay(r, nil, "teh1"); err == nil {
		t.Error("removed an unknown relay")
	}
}

func TestRedeployKeepsRelays(t *testing.T) {
	r, _ := deployed(t, state.NewUser("ann"))
	st, _ := state.Load(r)
	st.Relays = relayedState().Relays
	state.Save(r, st)
	res := &TuneResult{State: testState(), Best: []*Variant{{Entry: state.Entry{ID: "hy2", Kind: "hysteria2", Port: 8443}}}}
	got, err := Deploy(r, res, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Relays) != 1 || got.Relays[0].Name != "teh1" {
		t.Errorf("redeploy dropped the relays: %+v", got.Relays)
	}
}

func TestParseRelayCheck(t *testing.T) {
	got := parseRelayCheck("TCP 443 12\nTCP 8443 fail\nTCP 9 0\nnoise")
	if got[443] != 12 || got[8443] != -1 || got[9] != 1 || len(got) != 3 {
		t.Errorf("got %v", got)
	}
}
