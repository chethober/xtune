package main

import (
	"encoding/base64"
	"errors"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/netinfo"
	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/state"
)

// Tests never look anything up on the internet: lookups that a test does not stand in for fail.
func TestMain(m *testing.M) {
	netinfo.CountryLookup = func(net.IP) (string, error) { return "", errors.New("no lookups in tests") }
	netinfo.OutboundCountry = func() (string, error) { return "", errors.New("no lookups in tests") }
	os.Exit(m.Run())
}

func stubCountry(t *testing.T, cc string) *int {
	t.Helper()
	old := netinfo.CountryLookup
	t.Cleanup(func() { netinfo.CountryLookup = old })
	calls := 0
	netinfo.CountryLookup = func(net.IP) (string, error) {
		calls++
		if cc == "" {
			return "", errors.New("lookup failed")
		}
		return cc, nil
	}
	return &calls
}

// stubOutbound stands in for Cloudflare's trace: cc, or a failure when "".
func stubOutbound(t *testing.T, cc string) *int {
	t.Helper()
	old := netinfo.OutboundCountry
	t.Cleanup(func() { netinfo.OutboundCountry = old })
	calls := 0
	netinfo.OutboundCountry = func() (string, error) {
		calls++
		if cc == "" {
			return "", errors.New("trace failed")
		}
		return cc, nil
	}
	return &calls
}

func linkName(t *testing.T, link string) string {
	t.Helper()
	_, frag, ok := strings.Cut(link, "#")
	if !ok {
		t.Fatalf("no name in %s", link)
	}
	n, err := url.PathUnescape(frag)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Deployments that never set names keep the names they had before 1.0; only the flag is new.
func TestDefaultNamesUnchanged(t *testing.T) {
	st := testState()
	u := state.User{Name: "bob", UUID: "11111111-2222-4333-8444-555555555555", SSKey: "dXNlcmtleXVzZXJrZXk="}
	e := state.Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}
	if n := linkName(t, protocols.Link(st, e, u, state.Knobs{})); n != "bob-r-tcp" {
		t.Errorf("default config name %q", n)
	}
	if n := protocols.SubName(st, u); n != "xtune - bob" {
		t.Errorf("default subscription name %q", n)
	}
	st.Naming.Flag = "DE"
	if n := linkName(t, protocols.Link(st, e, u, state.Knobs{})); n != "🇩🇪 bob-r-tcp" {
		t.Errorf("with a flag %q", n)
	}
	if n := protocols.FailoverConfig(st, u)["remarks"]; n != "🇩🇪 xtune - bob" {
		t.Errorf("failover config remarks %q", n)
	}
	st.Naming.Flag = "off"
	if n := linkName(t, protocols.Link(st, e, u, state.Knobs{})); n != "bob-r-tcp" {
		t.Errorf("flag off %q", n)
	}
}

func TestNamesInEveryLinkKind(t *testing.T) {
	st := keyedState(t)
	st.CDNDomain = "cdn.example.com"
	st.Naming = state.Naming{Label: "Frankfurt", Flag: "DE", Config: "{flag} {server} · {proto} · {port}"}
	u := state.NewUser("bob")
	u.WGIP = "10.66.0.2"
	for _, e := range []state.Entry{
		{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"},
		{ID: "trojan", Kind: "trojan-reality", Port: 8443, SNI: "a.com"},
		{ID: "hy2", Kind: "hysteria2", Port: 8443},
		{ID: "ss", Kind: "ss2022", Port: 30001},
		{ID: "wg", Kind: "wireguard", Port: 51820},
		{ID: "cdn", Kind: "cdn-xhttp", Port: 2096, Path: "/p"},
		{ID: "ssh", Kind: "ssh", Port: 2222},
	} {
		l := protocols.Link(st, e, u, e.Knobs())
		want := "🇩🇪 Frankfurt · " + protocols.KindLabel(e.Kind) + " · " + strconv.Itoa(protocols.ClientPort(e))
		if n := linkName(t, l); n != want {
			t.Errorf("%s: name %q, want %q", e.Kind, n, want)
		}
	}
}

func TestSetNaming(t *testing.T) {
	r, _ := deployed(t, state.NewUser("ann"))
	str := func(s string) *string { return &s }
	now := time.Now()
	stubCountry(t, "NL")

	st, err := SetNaming(r, NamingChange{Label: str(" Amsterdam "), Country: str("auto"), Sub: str("{flag} {server} VPN")}, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.Naming.Label != "Amsterdam" || st.Naming.Flag != "NL" || !st.Naming.Detected {
		t.Errorf("naming: %+v", st.Naming)
	}
	if loaded, _ := state.Load(r); loaded.Naming != st.Naming {
		t.Errorf("not saved: %+v", loaded.Naming)
	}
	if n := protocols.SubName(st, st.Users[0]); n != "🇳🇱 Amsterdam VPN" {
		t.Errorf("subscription name %q", n)
	}

	if st, _ = SetNaming(r, NamingChange{Country: str("de")}, now); st.Naming.Flag != "DE" || st.Naming.Detected {
		t.Errorf("a chosen country: %+v", st.Naming)
	}
	if st, _ = SetNaming(r, NamingChange{Country: str("none")}, now); st.Naming.Flag != "off" {
		t.Errorf("no flag: %+v", st.Naming)
	}
	if st, _ = SetNaming(r, NamingChange{Sub: str("")}, now); st.Naming.Sub != "" || st.Naming.Label != "Amsterdam" {
		t.Errorf("reset the template only: %+v", st.Naming)
	}
	for _, c := range []NamingChange{{Country: str("germany")}, {Config: str("{user} {nope}")}, {Sub: str("{id}")}} {
		if _, err := SetNaming(r, c, now); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestDetectFlag(t *testing.T) {
	st := testState()
	now := time.Now()
	calls := stubCountry(t, "")
	if !detectFlag(st, now) || st.Naming.Flag != "" || st.Naming.Checked != now.Unix() {
		t.Fatalf("a failed lookup should be recorded: %+v", st.Naming)
	}
	if detectFlag(st, now.Add(time.Hour)) || *calls != 1 {
		t.Error("retried within the day")
	}
	stubOutbound(t, "FI")
	if !detectFlag(st, now.Add(25*time.Hour)) || st.Naming.Flag != "FI" || !st.Naming.Detected || st.Naming.Checked != 0 {
		t.Errorf("retry: %+v", st.Naming)
	}
	if detectFlag(st, now.Add(50*time.Hour)) {
		t.Error("looked up again although known")
	}
	forgetDetectedFlag(st)
	if st.Naming.Flag != "" {
		t.Error("a looked-up country should be forgotten after a move")
	}
	st.Naming.Flag = "SE" // chosen by hand
	forgetDetectedFlag(st)
	if st.Naming.Flag != "SE" {
		t.Error("a chosen country was forgotten")
	}
}

// The flag follows where the server's outbound address is, not the registry's country of its IP
// block (a Dutch server in a block registered in Greece is NL). A registry guess stands in while
// the outbound lookup fails, is retried daily, and flags detected by the registry before are
// looked up again.
func TestDetectFlagOutbound(t *testing.T) {
	st := testState()
	now := time.Now()
	stubCountry(t, "GR")
	out := stubOutbound(t, "NL")
	if !detectFlag(st, now) || st.Naming.Flag != "NL" || !st.Naming.Detected || !st.Naming.Outbound {
		t.Fatalf("outbound lookup: %+v", st.Naming)
	}
	if detectFlag(st, now.Add(48*time.Hour)) || *out != 1 {
		t.Error("an outbound flag was looked up again")
	}

	st = testState()
	out = stubOutbound(t, "")
	if !detectFlag(st, now) || st.Naming.Flag != "GR" || st.Naming.Outbound || st.Naming.Checked != now.Unix() {
		t.Fatalf("registry fallback: %+v", st.Naming)
	}
	if detectFlag(st, now.Add(time.Hour)) || *out != 1 {
		t.Error("the registry guess was retried within the day")
	}
	stubOutbound(t, "NL")
	if !detectFlag(st, now.Add(25*time.Hour)) || st.Naming.Flag != "NL" || !st.Naming.Outbound || st.Naming.Checked != 0 {
		t.Errorf("registry guess not replaced: %+v", st.Naming)
	}

	// state from before the outbound lookup: detected, but by the registry
	st = testState()
	st.Naming.Flag, st.Naming.Detected = "GR", true
	if !detectFlag(st, now) || st.Naming.Flag != "NL" || !st.Naming.Outbound {
		t.Errorf("an old registry flag was kept: %+v", st.Naming)
	}
	st.Naming.Flag, st.Naming.Detected, st.Naming.Outbound = "SE", false, false // chosen by hand
	if detectFlag(st, now) || st.Naming.Flag != "SE" {
		t.Errorf("a chosen country was replaced: %+v", st.Naming)
	}
}

func TestTraceCountry(t *testing.T) {
	ip, cc := netinfo.TraceCountry("fl=1\nh=1.1.1.1\nip=203.0.113.7\nts=1\ncolo=AMS\nloc=NL\ntls=TLSv1.3\n")
	if ip != "203.0.113.7" || cc != "NL" {
		t.Errorf("got %q %q", ip, cc)
	}
	for _, body := range []string{"ip=192.0.2.1\nloc=XX", "ip=192.0.2.1\nloc=T1", "ip=192.0.2.1", "<html>"} {
		if _, cc := netinfo.TraceCountry(body); cc != "" {
			t.Errorf("%q: country %q", body, cc)
		}
	}
}

func TestParseCymru(t *testing.T) {
	asn, cc, err := netinfo.ParseCymru([]string{"24940 | 95.216.0.0/16 | fi | ripencc | 2010-06-10"}, net.ParseIP("95.216.1.1"))
	if err != nil || asn != "AS24940" || cc != "FI" {
		t.Errorf("got %q %q %v", asn, cc, err)
	}
	if _, _, err := netinfo.ParseCymru([]string{"NA | x"}, net.ParseIP("192.0.2.1")); err == nil {
		t.Error("no ASN accepted")
	}
}

// A redeploy keeps the names, and the subscription server sends the subscription's name.
func TestNamingSurvivesRedeployAndReachesApps(t *testing.T) {
	r, _ := deployed(t, state.NewUser("ann"))
	st, _ := state.Load(r)
	st.Naming = state.Naming{Label: "Paris", Flag: "FR", Sub: "{flag} {server} · {user}"}
	st.CDNDomain, st.Sub = "cdn.example.com", state.SubConfig{Mode: "cdn", Port: 2053, Path: "subs"}
	state.Save(r, st)

	res := &TuneResult{State: testState(), Best: []*Variant{{Entry: state.Entry{ID: "hy2", Kind: "hysteria2", Port: 8443}}}}
	got, err := Deploy(r, res, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Naming != st.Naming {
		t.Fatalf("redeploy dropped the names: %+v", got.Naming)
	}

	h := subHandler{load: func() (*state.State, *state.Usage, error) {
		st, _ := state.Load(r)
		return st, &state.Usage{Users: map[string]*state.UserUsage{}}, nil
	}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/subs/"+got.Users[0].SubToken, nil))
	title, _ := strings.CutPrefix(w.Header().Get("Profile-Title"), "base64:")
	if b, _ := base64.StdEncoding.DecodeString(title); string(b) != "🇫🇷 Paris · ann" {
		t.Errorf("Profile-Title %q (%d)", b, w.Code)
	}
	raw, _ := base64.StdEncoding.DecodeString(w.Body.String())
	if !strings.Contains(string(raw), "#"+url.PathEscape("🇫🇷 ann-hy2")) {
		t.Errorf("links without the flag:\n%s", raw)
	}
}

// Followers keep their own names (and flags) in the primary's subscriptions.
func TestPeerStateKeepsNaming(t *testing.T) {
	st := testState()
	st.Naming = state.Naming{Label: "Tokyo", Flag: "JP"}
	if p := peerState(st); p.Naming != st.Naming {
		t.Errorf("peer state naming: %+v", p.Naming)
	}
}

func TestSubImports(t *testing.T) {
	sub := "https://cdn.example.com:2053/subs/tok"
	got := map[string]string{}
	for _, a := range subImports(sub, "🇩🇪 Berlin & co") {
		got[a.Name] = a.URL
	}
	want := map[string]string{
		"xtune":     "xtune://import?url=https%3A%2F%2Fcdn.example.com%3A2053%2Fsubs%2Ftok&name=%F0%9F%87%A9%F0%9F%87%AA+Berlin+%26+co",
		"v2rayNG":   "v2rayng://install-sub?url=https%3A%2F%2Fcdn.example.com%3A2053%2Fsubs%2Ftok&name=%F0%9F%87%A9%F0%9F%87%AA+Berlin+%26+co",
		"Hiddify":   "hiddify://import/" + sub + "#%F0%9F%87%A9%F0%9F%87%AA%20Berlin%20&%20co",
		"Streisand": "streisand://import/" + sub + "#%F0%9F%87%A9%F0%9F%87%AA%20Berlin%20&%20co",
		"sing-box":  "sing-box://import-remote-profile?url=https%3A%2F%2Fcdn.example.com%3A2053%2Fsubs%2Ftok#%F0%9F%87%A9%F0%9F%87%AA%20Berlin%20&%20co",
	}
	for app, w := range want {
		if got[app] != w {
			t.Errorf("%s:\n got %s\nwant %s", app, got[app], w)
		}
	}
	if subImports("", "x") != nil {
		t.Error("import links without a subscription server")
	}
}

// The page offers the import links as real links (html/template refuses unknown schemes by default).
func TestPageImportLinks(t *testing.T) {
	r, _ := publicServer(t)
	st, _ := state.Load(r)
	st.Naming = state.Naming{Label: "Oslo", Flag: "NO", Sub: "{flag} {server} <{user}>"}
	state.Save(r, st)
	h := subHandler{load: func() (*state.State, *state.Usage, error) {
		st, _ := state.Load(r)
		return st, &state.Usage{Users: map[string]*state.UserUsage{}}, nil
	}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/subs/"+st.Users[0].SubToken, nil)
	req.Header.Set("Accept", "text/html")
	h.ServeHTTP(w, req)
	body := w.Body.String()
	for _, want := range []string{`href="hiddify://import/https://`, `href="v2rayng://install-sub?url=`, "🇳🇴 Oslo &lt;ann&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(body, "ZgotmplZ") || strings.Contains(body, "<ann>") {
		t.Error("an import link was defanged or the name not escaped")
	}
}

// The server's enforce job looks the flag up (the client's network may filter the lookup).
func TestEnforceDetectsFlag(t *testing.T) {
	r, _ := deployed(t, state.NewUser("ann"))
	calls := stubCountry(t, "SE")
	if err := Enforce(r); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load(r)
	if st.Naming.Flag != "SE" || !st.Naming.Detected || *calls != 1 {
		t.Fatalf("after enforce: %+v (%d lookups)", st.Naming, *calls)
	}
	Enforce(r)
	if *calls != 1 {
		t.Error("looked up again although known")
	}
}
