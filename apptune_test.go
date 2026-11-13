package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// The app's tune: the server prepares, the app measures, the server deploys from its report alone.
func TestPreparedTuneDeploysFromTheAppReport(t *testing.T) {
	fs := newFake()
	r := &remote.Server{Transport: fs, Conn: remote.Conn{Host: "203.0.113.7"}}
	st := testState()
	st.Users = []state.User{state.NewUser("ann")}
	tu := state.NewUser("tune")
	e1 := state.Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}
	e2 := state.Entry{ID: "hy2", Kind: "hysteria2", Port: 8443}
	e3 := state.Entry{ID: "ssh", Kind: "ssh", Port: 2222}
	e4 := state.Entry{ID: "ovpn", Kind: "openvpn-tcp", Port: 1194}
	vs := []*Variant{
		{Entry: e1, K: state.Knobs{FP: "chrome", Frag: "none"}, Label: "r-tcp:443"},
		{Entry: e1, K: state.Knobs{FP: "firefox", Frag: "none"}, Label: "r-tcp:443 fp=firefox"},
		{Entry: e2, K: state.Knobs{Frag: "none"}, Label: "hy2:8443"},
		{Entry: e3, K: state.Knobs{}, Label: "ssh:2222"},
		{Entry: e4, K: state.Knobs{}, Label: "ovpn:1194"},
	}
	tp, err := PrepareProfile(r, &TuneResult{State: st, User: tu, All: vs}, TuneOpts{Network: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tp.Profile.Configs) != 4 || len(tp.Skipped) != 1 || tp.Skipped[0] != "ovpn:1194" {
		t.Fatalf("want 4 measurable configs and OpenVPN skipped: %d %v", len(tp.Profile.Configs), tp.Skipped)
	}
	ids := map[string]bool{}
	for _, c := range tp.Profile.Configs {
		if ids[c.ID] {
			t.Errorf("duplicate config id %s", c.ID)
		}
		ids[c.ID] = true
		if c.Key != c.Name || c.Key == "" {
			t.Errorf("a config's key is its variant's label: %+v", c)
		}
	}
	if c := tp.Profile.Configs[1]; c.Engine != "xray" || !strings.Contains(string(jsonOf(c.Outbound)), "firefox") {
		t.Errorf("the variant's own client settings: %s", jsonOf(c.Outbound))
	}
	if c := tp.Profile.Configs[3]; c.Engine != "ssh" || c.SSH == nil || c.SSH.User != "tune" {
		t.Errorf("ssh as the test user: %+v", c)
	}
	raw, _ := json.Marshal(tp)
	if strings.Contains(string(raw), st.Users[0].UUID) || strings.Contains(string(raw), st.RealityPriv) {
		t.Error("the profile must not hold real users or server keys")
	}
	if _, err := DeployPending(r, nil, 1, io.Discard); err == nil {
		t.Error("a prepared tune has no measurements of its own: it needs the app's report")
	}
	rep := &NetReport{ID: tp.ID, Network: "mci", ASN: "197207", Results: []CheckResult{
		{ID: "r-tcp", Label: "r-tcp:443", Ok: 5, N: 5, Score: 40},
		{ID: "hy2", Label: "hy2:8443", Ok: 0, N: 5},
		{ID: "ssh", Label: "ssh:2222", Ok: 4, N: 5, Score: 5},
	}}
	var log strings.Builder
	got, err := DeployPending(r, []*NetReport{rep}, 1, &log)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range got.Entries {
		have[e.ID] = true
	}
	if !have["r-tcp"] || !have["ssh"] || have["hy2"] {
		t.Errorf("want the best config plus the SSH family that worked, not the blocked one: %v", have)
	}
	if now, _ := state.Load(r); now == nil || now.Networks["197207"] == nil {
		t.Errorf("the app's network is ranked: %s", log.String())
	}
	if _, ok := fs.files[pendingFile]; ok {
		t.Error("pending tune not removed after deploying")
	}
}

func jsonOf(v any) []byte { b, _ := json.Marshal(v); return b }
