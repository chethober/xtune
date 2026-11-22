package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/state"
)

func TestUserTuneDenyRefusesOnServer(t *testing.T) {
	r, fs := deployed(t, state.NewUser("ann"), state.NewUser("bob"))
	if _, err := SetUserTune(r, "bob", TuneChange{Deny: &[]string{"ss2022"}}); err != nil {
		t.Fatal(err)
	}
	if fs.live["ss2022"]["bob"] != "" || fs.live["r-tcp"]["bob"] == "" || fs.live["ss2022"]["ann"] == "" {
		t.Fatalf("bob should be out of ss2022 only: %v", fs.live)
	}
	st, _ := state.Load(r)
	_, bob := st.User("bob")
	for _, l := range protocols.Links(must(userView(st, *bob, "", -1, time.Now())), *bob) {
		if strings.HasPrefix(l, "ss://") {
			t.Errorf("denied config still in bob's links: %s", l)
		}
	}
	// the policy goes: bob is back in
	if _, err := SetUserTune(r, "bob", TuneChange{Clear: true}); err != nil {
		t.Fatal(err)
	}
	if fs.live["ss2022"]["bob"] == "" {
		t.Errorf("clearing the policy should let bob back in: %v", fs.live)
	}
	st, _ = state.Load(r)
	if _, bob = st.User("bob"); bob.Tune != nil {
		t.Errorf("an empty tuning should be dropped: %+v", bob.Tune)
	}
}

func must(st *state.State, _ string) *state.State { return st }

func TestUserViewOrderKnobsAndMax(t *testing.T) {
	st := testState()
	st.Entries = []state.Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com", FP: "chrome", Frag: "none"},
		{ID: "r-xhttp", Kind: "reality-xhttp", Port: 8443, SNI: "b.com", Path: "/p", FP: "chrome", Frag: "none", Mode: "auto"},
		{ID: "ss2022", Kind: "ss2022", Port: 30001}}
	st.Relays = []state.Relay{{Name: "teh", Addr: "198.51.100.9", Target: protocols.RelayTarget(st),
		Forwards: []state.Forward{{ID: "r-tcp", Port: 9443, To: 443, Net: "tcp"}}}}
	u := state.NewUser("bob")
	u.Tune = &state.UserTune{Policy: state.TunePolicy{Pin: []string{"reality-xhttp"},
		Knobs: map[string]state.KnobOverride{"r-tcp": {Frag: "frag-aggr"}}, Lock: []string{"knobs"}},
		Prefs: state.TunePrefs{Pin: []string{"ss2022"}, Hide: []string{"via:teh"}, Knobs: map[string]state.KnobOverride{"r-xhttp": {FP: "firefox"}}}}
	view, _ := userView(st, u, "", -1, time.Now())
	var ids []string
	cfgs := protocols.ClientProfileFor(view, u).Configs
	for _, c := range cfgs {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []string{"r-xhttp", "ss2022", "r-tcp"}) {
		t.Fatalf("order: %v (policy pin, then the user's, relay hidden)", ids)
	}
	if !strings.Contains(cfgs[2].Link, "fragment") && cfgs[2].Outbound["streamSettings"].(J)["sockopt"] == nil {
		t.Errorf("policy knob not applied to r-tcp: %v", cfgs[2].Outbound)
	}
	if strings.Contains(cfgs[0].Link, "fp=firefox") {
		t.Errorf("knobs are locked: the user's fingerprint must not apply: %s", cfgs[0].Link)
	}
	u.Tune.Policy.Max = 2
	view, _ = userView(st, u, "", -1, time.Now())
	if n := len(protocols.ClientProfileFor(view, u).Configs); n != 2 {
		t.Errorf("max 2: %d configs", n)
	}
	// via:teh allowed only: the direct configs go, the relay's stay
	u.Tune = &state.UserTune{Policy: state.TunePolicy{Allow: []string{"via:teh"}}}
	view, _ = userView(st, u, "", -1, time.Now())
	cfgs = protocols.ClientProfileFor(view, u).Configs
	if len(cfgs) != 1 || cfgs[0].ID != "teh-r-tcp" {
		t.Errorf("via:teh only: %+v", cfgs)
	}
	if !st.ServesUser(u, st.Entries[2]) {
		t.Error("a via: policy must not keep the user out of the inbound the relay forwards to")
	}
}

func TestUserTunePresetMerge(t *testing.T) {
	st := testState()
	st.TunePresets = map[string]state.TunePreset{"mci": {Allow: []string{"cdn"}, Deny: []string{"udp"}, Lock: []string{"order"},
		Knobs: map[string]state.KnobOverride{"cdn": {Frag: "frag-aggr"}}}}
	u := state.NewUser("bob")
	u.Tune = &state.UserTune{Preset: "mci", Policy: state.TunePolicy{Deny: []string{"cdn-ssh"}, Knobs: map[string]state.KnobOverride{"cdn": {FP: "firefox"}}}}
	p := st.Policy(u)
	if !slices.Equal(p.Allow, []string{"cdn"}) || !slices.Equal(p.Deny, []string{"udp", "cdn-ssh"}) || !p.Locked("order") ||
		p.Knobs["cdn"].FP != "firefox" {
		t.Errorf("merged policy: %+v", p)
	}
	if p.Allowed(state.Entry{ID: "r-tcp", Kind: "reality-tcp"}, "") || !p.Allowed(state.Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp"}, "") ||
		p.Allowed(state.Entry{ID: "cdn-ssh", Kind: "cdn-ssh"}, "") {
		t.Error("allow/deny")
	}
}

func TestUserTuneCLIAndChecks(t *testing.T) {
	r, _ := deployed(t, state.NewUser("bob"))
	fs := newUsersFlags()
	tf := addTuneFlags(fs)
	if err := fs.Parse([]string{"--deny", "ss2022", "--knob", "r-tcp:fp=firefox,frag=frag-mild", "--lock", "knobs", "--self-tune", "on"}); err != nil {
		t.Fatal(err)
	}
	c, err := tf.change(flagsSet(fs))
	if err != nil {
		t.Fatal(err)
	}
	st, err := SetUserTune(r, "bob", c)
	if err != nil {
		t.Fatal(err)
	}
	_, bob := st.User("bob")
	v := tuneViewOf(st, *bob)
	if !v.Applied.SelfTune || v.Applied.Knobs["r-tcp"].FP != "firefox" || len(v.Configs) != 2 {
		t.Fatalf("view: %+v", v)
	}
	for _, c := range v.Configs {
		if c.ID == "ss2022" && (c.Allowed || c.Listed) || c.ID == "r-tcp" && (!c.Listed || !strings.Contains(c.Knobs, "fp=firefox")) {
			t.Errorf("config: %+v", c)
		}
	}
	for _, bad := range []TuneChange{{Allow: &[]string{"Bad Sel"}}, {Lock: &[]string{"everything"}},
		{Knobs: map[string]state.KnobOverride{"r-tcp": {FP: "netscape"}}}, {Knobs: map[string]state.KnobOverride{"r-tcp": {MTU: 99}}}} {
		if _, err := SetUserTune(r, "bob", bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	// the user's own preferences: knobs are locked, order is not
	if _, err := setUserPrefs(r, "bob", prefsBody{Knobs: map[string]state.KnobOverride{"r-tcp": {FP: "safari"}}}); err == nil ||
		!strings.Contains(err.Error(), "knobs") {
		t.Errorf("locked knobs: %v", err)
	}
	if _, err := setUserPrefs(r, "bob", prefsBody{Pin: []string{"r-tcp"}}); err != nil {
		t.Error(err)
	}
}

func newUsersFlags() *flag.FlagSet { return flag.NewFlagSet("users", flag.ContinueOnError) }

func TestUserPrefsEndpoint(t *testing.T) {
	r, srv := publicServer(t)
	st, _ := state.Load(r)
	_, ann := st.User("ann")
	base := srv.URL + "/subs/" + ann.SubToken + "/prefs"
	res, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	var v userTuneView
	json.NewDecoder(res.Body).Decode(&v)
	res.Body.Close()
	if res.StatusCode != 200 || v.Name != "ann" || len(v.Configs) == 0 {
		t.Fatalf("GET prefs: %d %+v", res.StatusCode, v)
	}
	body, _ := json.Marshal(prefsBody{Hide: []string{"ss2022"}})
	req, _ := http.NewRequest(http.MethodPut, base, bytes.NewReader(body))
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("PUT prefs: %d", res.StatusCode)
	}
	res, _ = http.Get(srv.URL + "/subs/" + ann.SubToken + "/client")
	var p protocols.ClientProfile
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	for _, c := range p.Configs {
		if c.ID == "ss2022" {
			t.Error("a hidden config is still in the client profile")
		}
	}
	if len(p.Configs) == 0 {
		t.Error("no configs left")
	}
}
