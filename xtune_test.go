package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v26.10.10", "26.9.30", true},
		{"v26.9.30", "v26.10.10", false},
		{"v26.9.30", "26.9.30", false},
		{"v1.0.0", "none", true},
		{"", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPlanEntriesAvoidsUsedPorts(t *testing.T) {
	probe := strings.Join([]string{
		"SNI www.a.com 2 0.20", "SNI www.b.com 2 0.10", "SNI www.c.com 1.1 0.05", "SNI www.d.com fail",
		"USED tcp 0.0.0.0:443", "USED tcp [::]:8443", "USED udp 0.0.0.0:443",
	}, "\n")
	entries, snis := PlanEntries(probe, false)
	if snis[0] != "www.b.com" || len(snis) != 2 {
		t.Fatalf("SNIs not ranked/filtered by h2+latency: %v", snis)
	}
	seen := map[int]bool{}
	for _, e := range entries {
		if e.Port == 443 || (e.Port == 8443 && e.Proto() == "tcp") {
			t.Errorf("%s got a used port %d", e.ID, e.Port)
		}
		if seen[e.Port] {
			t.Errorf("port %d assigned twice", e.Port)
		}
		seen[e.Port] = true
	}
	if !entries[0].Primary || entries[0].SNI != "www.b.com" {
		t.Errorf("primary entry should use the fastest SNI: %+v", entries[0])
	}
}

func testState() *State {
	return &State{Host: "203.0.113.7", RealityPriv: "priv", RealityPub: "pub", ShortID: "abcd1234",
		VencDec: "mlkem768x25519plus.native.600s.dec", VencEnc: "mlkem768x25519plus.native.0rtt.enc",
		SSKey: "c2VydmVya2V5c2VydmVya2V5", CertSNI: "www.bing.com", CertHash: "ff"}
}

func TestLinks(t *testing.T) {
	st := testState()
	u := User{Name: "bob", UUID: "11111111-2222-4333-8444-555555555555", SSKey: "dXNlcmtleXVzZXJrZXk="}
	l := st.Link(Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 2053, SNI: "www.b.com"}, u, Knobs{FP: "firefox"})
	pu, err := url.Parse(l)
	if err != nil {
		t.Fatal(err)
	}
	q := pu.Query()
	if pu.Scheme != "vless" || pu.Port() != "2053" || q.Get("security") != "reality" || q.Get("fp") != "firefox" ||
		q.Get("flow") != "xtls-rprx-vision" || q.Get("pbk") != "pub" || q.Get("sni") != "www.b.com" {
		t.Errorf("bad reality link: %s", l)
	}
	l = st.Link(Entry{ID: "venc", Kind: "vlessenc-xhttp", Port: 3000, Path: "/p"}, u, Knobs{Mode: "packet-up"})
	q = mustQuery(t, l)
	if q.Get("encryption") != st.VencEnc || q.Get("mode") != "packet-up" || q.Get("path") != "/p" || q.Get("security") != "none" {
		t.Errorf("bad vless-encryption link: %s", l)
	}
	l = st.Link(Entry{ID: "ss", Kind: "ss2022", Port: 4000}, u, Knobs{})
	if !strings.HasPrefix(l, "ss://2022-blake3-aes-128-gcm%3A") || !strings.Contains(l, "@203.0.113.7:4000") {
		t.Errorf("bad ss link: %s", l)
	}
	l = st.Link(Entry{ID: "hy", Kind: "hysteria2", Port: 5000}, u, Knobs{})
	if !strings.HasPrefix(l, "hysteria2://"+u.UUID+"@203.0.113.7:5000?") || !strings.Contains(l, "pinSHA256=ff") {
		t.Errorf("bad hysteria2 link: %s", l)
	}
	st.Host = "2001:db8::1"
	if l = st.Link(Entry{ID: "x", Kind: "reality-tcp", Port: 1}, u, Knobs{}); !strings.Contains(l, "@[2001:db8::1]:1?") {
		t.Errorf("IPv6 host not bracketed: %s", l)
	}
}

func mustQuery(t *testing.T, l string) url.Values {
	pu, err := url.Parse(l)
	if err != nil {
		t.Fatal(err)
	}
	return pu.Query()
}

func TestServerConfigOnlyEnabledUsers(t *testing.T) {
	st := testState()
	on, off := NewUser("on"), NewUser("off")
	off.Enabled = false
	entries := []Entry{{ID: "a", Kind: "reality-tcp", Port: 1, SNI: "x.com"}, {ID: "s", Kind: "ss2022", Port: 2}}
	cfg := st.ServerConfig(entries, []User{on, off})
	ibs := cfg["inbounds"].([]J)
	if len(ibs) != 2 {
		t.Fatalf("want 2 inbounds, got %d", len(ibs))
	}
	clients := ibs[0]["settings"].(J)["clients"].([]J)
	if len(clients) != 1 || clients[0]["email"] != "on" || clients[0]["flow"] != "xtls-rprx-vision" {
		t.Errorf("disabled user leaked or flow missing: %v", clients)
	}
	if got := st.ServerConfig(entries, []User{off})["inbounds"].([]J); len(got) != 0 {
		t.Errorf("no enabled users should mean no inbounds, got %d", len(got))
	}
}

func TestPickPrefersSimplestWithinTie(t *testing.T) {
	plain := &Variant{K: Knobs{FP: "chrome", Frag: "none"}, Score: 95}
	frag := &Variant{K: Knobs{FP: "chrome", Frag: "frag-aggr"}, Score: 100}
	if pick([]*Variant{frag, plain}) != plain {
		t.Error("a 5% gain should not justify fragmentation")
	}
	frag.Score = 130
	if pick([]*Variant{frag, plain}) != frag {
		t.Error("a clear win should be picked")
	}
	if pick([]*Variant{{Score: 0}}) != nil {
		t.Error("all-failed group should return nil")
	}
}

func TestPlanEntriesCDN(t *testing.T) {
	probe := "SNI www.a.com 2 0.20\nUSED tcp 0.0.0.0:2053"
	entries, _ := PlanEntries(probe, true)
	var cdn []Entry
	for _, e := range entries {
		if e.CDN() {
			cdn = append(cdn, e)
		}
	}
	if len(cdn) != 1 || cdn[0].Port != 2083 || cdn[0].Path == "" {
		t.Fatalf("want one CDN entry on the first free CDN port 2083: %+v", cdn)
	}
	for _, e := range entries {
		if !e.CDN() && e.Port == cdn[0].Port {
			t.Errorf("%s shares the CDN port", e.ID)
		}
	}
	all := map[string]map[int]bool{"tcp": {}, "udp": {}}
	for _, p := range cdnPorts {
		all["tcp"][p] = true
	}
	if _, ok := cdnEntry(all, map[int]bool{}); ok {
		t.Error("no free CDN port should mean no CDN entry")
	}
	if entries, _ := PlanEntries(probe, false); hasCDN(entries) {
		t.Error("CDN entry planned without a CDN domain")
	}
}

func TestDomainsInLinks(t *testing.T) {
	st := testState()
	st.Domain, st.CDNDomain = "direct.example.com", "cdn.example.com"
	u := NewUser("ann")
	l := st.Link(Entry{ID: "r", Kind: "reality-tcp", Port: 443, SNI: "www.b.com"}, u, Knobs{})
	if !strings.Contains(l, "@direct.example.com:443?") {
		t.Errorf("direct entry should use the direct domain: %s", l)
	}
	if l = st.Link(Entry{ID: "hy", Kind: "hysteria2", Port: 5000}, u, Knobs{}); !strings.Contains(l, "@direct.example.com:5000?") {
		t.Errorf("hysteria2 should use the direct domain: %s", l)
	}
	cdn := Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2053, Path: "/p"}
	l = st.Link(cdn, u, Knobs{FP: "firefox", Mode: "packet-up"})
	pu, err := url.Parse(l)
	if err != nil {
		t.Fatal(err)
	}
	q := pu.Query()
	if pu.Host != "cdn.example.com:2053" || q.Get("type") != "xhttp" || q.Get("security") != "tls" || q.Get("sni") != "cdn.example.com" ||
		q.Get("host") != "cdn.example.com" || q.Get("fp") != "firefox" || q.Get("mode") != "packet-up" || q.Get("path") != "/p" || q.Get("flow") != "" {
		t.Errorf("bad CDN link: %s", l)
	}
	ob := st.Outbound(cdn, u, Knobs{}, "proxy")
	if addr := ob["settings"].(J)["vnext"].([]J)[0]["address"]; addr != "cdn.example.com" {
		t.Errorf("CDN outbound address %v", addr)
	}
	ss := ob["streamSettings"].(J)
	if ss["security"] != "tls" || ss["xhttpSettings"].(J)["host"] != "cdn.example.com" {
		t.Errorf("CDN outbound stream settings: %v", ss)
	}
	ib := st.ServerConfig([]Entry{cdn}, []User{u})["inbounds"].([]J)[0]
	iss := ib["streamSettings"].(J)
	if iss["security"] != "tls" || iss["network"] != "xhttp" || iss["tlsSettings"].(J)["certificates"] == nil {
		t.Errorf("CDN inbound needs TLS with the origin certificate: %v", iss)
	}
	st.Domain = ""
	if l = st.Link(Entry{ID: "s", Kind: "ss2022", Port: 4000}, u, Knobs{}); !strings.Contains(l, "@203.0.113.7:4000") {
		t.Errorf("without a direct domain links use the IP: %s", l)
	}
}

func TestTestCertsCoverAllTLSInbounds(t *testing.T) {
	st := testState()
	cfg := st.ServerConfig([]Entry{{ID: "hy", Kind: "hysteria2", Port: 1}, {ID: "c", Kind: "cdn-xhttp", Port: 2, Path: "/p"},
		{ID: "r", Kind: "reality-tcp", Port: 3, SNI: "x.com"}, {ID: "s", Kind: "ss2022", Port: 4}}, []User{NewUser("a")})
	testCerts(cfg, "/t")
	for _, ib := range cfg["inbounds"].([]J) {
		ss, _ := ib["streamSettings"].(J)
		if ts, ok := ss["tlsSettings"].(J); ok {
			if f := ts["certificates"].([]J)[0]["certificateFile"]; f != "/t/hy.crt" {
				t.Errorf("%s still uses %v during tuning", ib["tag"], f)
			}
		}
	}
}

func TestServerOnlyRejectsConnFlags(t *testing.T) {
	if serverOnly("users", []string{"add", "bob", "--host", "1.2.3.4"}) == nil {
		t.Error("users with --host should point at the server console")
	}
	if serverOnly("users", []string{"add", "bob"}) != nil {
		t.Error("plain users command rejected")
	}
}

func TestParseAllowsFlagsAfterArgs(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	q := fs.Float64("quota", 0, "")
	f := fs.Bool("f", false, "")
	pos := parse(fs, []string{"add", "bob", "--quota", "5", "-f"})
	if strings.Join(pos, " ") != "add bob" || *q != 5 || !*f {
		t.Errorf("pos=%v quota=%v f=%v", pos, *q, *f)
	}
}

func TestSubscriptionIsBase64Links(t *testing.T) {
	st := testState()
	st.Entries = []Entry{{ID: "a", Kind: "reality-tcp", Port: 1, SNI: "x.com"}}
	u := NewUser("z")
	raw, err := base64.StdEncoding.DecodeString(st.Subscription(u))
	if err != nil || !strings.HasPrefix(string(raw), "vless://"+u.UUID) {
		t.Errorf("subscription body: %q %v", raw, err)
	}
}

func TestDeployChoiceKeepsCDN(t *testing.T) {
	a := &Variant{Entry: Entry{ID: "r-tcp", Kind: "reality-tcp"}}
	b := &Variant{Entry: Entry{ID: "hy2", Kind: "hysteria2"}}
	c := &Variant{Entry: Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp"}}
	if got := deployChoice([]*Variant{a, b, c}, 1); len(got) != 2 || got[0] != a || got[1] != c {
		t.Errorf("top 1 plus the CDN entry expected, got %d", len(got))
	}
	if got := deployChoice([]*Variant{a, c}, 5); len(got) != 2 {
		t.Errorf("n beyond the list: got %d", len(got))
	}
	if got := deployChoice([]*Variant{a, c}, 0); len(got) != 0 {
		t.Error("n=0 deploys nothing")
	}
}

// fakeServer is an in-memory server: uploads land in files, helper calls return canned output,
// and "xray api" calls act on an in-memory set of live users per inbound.
type fakeServer struct {
	files    map[string]string
	used     string                       // "USED ..." lines for probe
	live     map[string]map[string]string // inbound tag -> email -> credential
	stats    string                       // statsquery output
	online   string                       // statsonlineiplist output
	sib      []string                     // last sib arguments
	helper   map[string]string            // canned output of other helper commands
	calls    []string                     // helper commands run, with their arguments
	restarts int
	apiCalls int
}

func (f *fakeServer) api(args []string) (string, error) {
	f.apiCalls++
	unq := func(s string) string { return strings.Trim(s, "'") }
	tag := ""
	var rest []string
	for _, a := range args[1:] {
		a = unq(a)
		switch {
		case strings.HasPrefix(a, "--server="):
		case strings.HasPrefix(a, "-tag="):
			tag = strings.TrimPrefix(a, "-tag=")
		default:
			rest = append(rest, a)
		}
	}
	if _, ok := f.live[tag]; tag != "" && !ok {
		return "", fmt.Errorf("handler not found: %s", tag)
	}
	switch args[0] {
	case "inbounduser":
		var us []J
		for email, c := range f.live[tag] {
			if strings.HasPrefix(tag, "wg") {
				us = append(us, J{"email": email, "account": J{"publicKey": c}})
				continue
			}
			us = append(us, J{"email": email, "account": J{"id": c}})
		}
		b, _ := json.Marshal(J{"users": us})
		return string(b), nil
	case "rmu":
		n := 0
		for _, e := range rest {
			if _, ok := f.live[tag][e]; ok {
				delete(f.live[tag], e)
				n++
			}
		}
		return fmt.Sprintf("Removed %d user(s) in total.", n), nil
	case "adu":
		var cfg struct{ Inbounds []J }
		json.Unmarshal([]byte(f.files[rest[0]]), &cfg)
		n := 0
		for _, ib := range cfg.Inbounds {
			t := ib["tag"].(string)
			if f.live[t] == nil {
				f.live[t] = map[string]string{}
			}
			for _, c := range ib["settings"].(map[string]any)["clients"].([]any) {
				c := c.(map[string]any)
				cred, _ := c["id"].(string)
				if cred == "" {
					cred, _ = c["auth"].(string)
				}
				if cred == "" {
					cred, _ = c["password"].(string)
				}
				f.live[t][c["email"].(string)] = cred
				n++
			}
		}
		return fmt.Sprintf("Added %d user(s) in total.", n), nil
	case "statsquery":
		return orStr(f.stats, "{}"), nil
	case "statsonlineiplist":
		return orStr(f.online, "{}"), nil
	case "sib":
		f.sib = rest
	case "rmrules":
		f.sib = nil
	}
	return "{}", nil
}

func (f *fakeServer) exec(cmd, stdin string, _ bool) (string, string, error) {
	if _, args, ok := strings.Cut(cmd, "helper.sh "); ok {
		f.calls = append(f.calls, strings.ReplaceAll(args, "'", ""))
	}
	switch {
	case strings.HasPrefix(cmd, remoteDir+"/xray api "):
		out, err := f.api(strings.Fields(strings.TrimPrefix(cmd, remoteDir+"/xray api ")))
		return out, "", err
	case strings.HasPrefix(cmd, "cat "):
		return f.files[strings.Fields(cmd)[1]], "", nil
	case strings.HasPrefix(cmd, "rm -f '"):
		delete(f.files, strings.Trim(strings.Fields(cmd)[2], "'"))
	case strings.Contains(cmd, "cat > '"):
		dst := strings.SplitN(strings.SplitN(cmd, "cat > '", 2)[1], "'", 2)[0]
		f.files[dst] = stdin
	case strings.Contains(cmd, "helper.sh 'apply'"):
		f.restarts++
		f.live = map[string]map[string]string{} // a restart loads config.json's users
		var cfg struct{ Inbounds []J }
		json.Unmarshal([]byte(f.files[remoteDir+"/config.json"]), &cfg)
		for _, ib := range cfg.Inbounds {
			f.live[ib["tag"].(string)] = map[string]string{}
			if ib["protocol"] == "wireguard" {
				for _, p := range ib["settings"].(map[string]any)["peers"].([]any) {
					p := p.(map[string]any)
					f.live[ib["tag"].(string)][p["email"].(string)] = p["publicKey"].(string)
				}
				continue
			}
			for _, c := range ib["settings"].(map[string]any)["clients"].([]any) {
				c := c.(map[string]any)
				cred, _ := c["id"].(string)
				if cred == "" {
					cred, _ = c["auth"].(string)
				}
				if cred == "" {
					cred, _ = c["password"].(string)
				}
				f.live[ib["tag"].(string)][c["email"].(string)] = cred
			}
		}
		return "active\n", "", nil
	case strings.Contains(cmd, "helper.sh 'sub-enable'"):
		return "active\n", "", nil
	case strings.Contains(cmd, "helper.sh 'probe'"):
		return f.used, "", nil
	case strings.Contains(cmd, "helper.sh '"):
		name := strings.SplitN(strings.SplitN(cmd, "helper.sh '", 2)[1], "'", 2)[0]
		return f.helper[name], "", nil
	}
	return "", "", nil
}
func newFake() *fakeServer {
	return &fakeServer{files: map[string]string{}, live: map[string]map[string]string{}}
}

func (f *fakeServer) stream(context.Context, string, func(string)) error { return nil }
func (f *fakeServer) close()                                             {}

func TestRedeployKeepsServerIdentityAndUsers(t *testing.T) {
	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
	first := testState()
	first.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}}
	first.Users = []User{NewUser("ann")}
	if err := Apply(r, first); err != nil {
		t.Fatal(err)
	}
	fresh := testState()
	fresh.RealityPriv, fresh.RealityPub, fresh.CertHash = "new-priv", "new-pub", "00"
	fresh.CDNDomain = "cdn.example.com"
	res := &TuneResult{State: fresh, Best: []*Variant{
		{Entry: Entry{ID: "hy2", Kind: "hysteria2", Port: 8443}},
		{Entry: Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2053, Path: "/p"}, K: Knobs{FP: "chrome", Frag: "none", Mode: "packet-up"}},
	}}
	st, err := Deploy(r, res, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st.RealityPub != "pub" || st.CertHash != "ff" {
		t.Errorf("server keys were replaced on redeploy: %s %s", st.RealityPub, st.CertHash)
	}
	if len(st.Users) != 1 || st.Users[0].UUID != first.Users[0].UUID {
		t.Errorf("users not kept: %+v", st.Users)
	}
	if len(st.Entries) != 2 || st.Entries[1].Mode != "packet-up" {
		t.Errorf("want top 1 plus the CDN entry with its tuned knobs: %+v", st.Entries)
	}
	if !strings.Contains(fs.files[remoteDir+"/config.json"], `"cdn-xhttp"`) {
		t.Error("config.json does not contain the CDN inbound")
	}

	// clearing the CDN domain removes its entry; setting it again re-adds one on a free CDN port
	fs.used = "USED tcp 0.0.0.0:2053"
	if st, _, err = SetDomains(r, "", "", func(s string) { t.Log(s) }); err != nil || hasCDN(st.Entries) {
		t.Fatalf("CDN entry kept after clearing: %v %+v", err, st.Entries)
	}
	st, added, err := SetDomains(r, "", "cdn2.example.invalid", func(s string) { t.Log(s) })
	if err != nil || !added || !hasCDN(st.Entries) {
		t.Fatalf("CDN entry not added: %v %v", err, added)
	}
	for _, e := range st.Entries {
		if e.CDN() && (e.Port == 2053 || e.Mode != "packet-up") {
			t.Errorf("new CDN entry on a used port or without the CDN-safe mode: %+v", e)
		}
	}
	if loaded, _ := LoadState(r); loaded == nil || loaded.CDNDomain != "cdn2.example.invalid" {
		t.Error("domains not persisted in state.json")
	}
}

func TestLocalTransport(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	var l localTransport
	if out, _, err := l.exec("cat; echo -n done", "in-", false); err != nil || out != "in-done" {
		t.Errorf("exec: %q %v", out, err)
	}
	if _, _, err := l.exec("echo boom >&2; exit 3", "", false); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("exec error should carry stderr: %v", err)
	}
	var lines []string
	l.stream(context.Background(), "printf 'a\\nb\\n'", func(s string) { lines = append(lines, s) })
	if strings.Join(lines, ",") != "a,b" {
		t.Errorf("stream: %v", lines)
	}
}

// deployed sets up a fake server with a live deployment (API enabled) and returns it.
func deployed(t *testing.T, users ...User) (*Server, *fakeServer) {
	t.Helper()
	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
	st := testState()
	st.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}, {ID: "ss2022", Kind: "ss2022", Port: 30001}}
	st.Users = users
	if err := Apply(r, st); err != nil {
		t.Fatal(err)
	}
	if st.APIPort == 0 {
		t.Fatal("Apply should give the deployment an API port")
	}
	return r, fs
}

func TestUserChangesAreLiveWithoutRestart(t *testing.T) {
	r, fs := deployed(t, NewUser("ann"))
	before := fs.restarts
	if _, _, err := AddUser(r, "bob", Limits{}); err != nil {
		t.Fatal(err)
	}
	if fs.live["r-tcp"]["bob"] == "" || fs.live["ss2022"]["bob"] == "" || fs.live["r-tcp"]["ann"] == "" {
		t.Fatalf("bob not added live next to ann: %v", fs.live)
	}
	st, _ := LoadState(r)
	_, bob := st.User("bob")
	if fs.live["ss2022"]["bob"] != bob.SSKey {
		t.Error("shadowsocks inbound should carry the user's key")
	}
	old := fs.live["r-tcp"]["bob"]
	if _, _, err := RotateUser(r, "bob"); err != nil {
		t.Fatal(err)
	}
	if fs.live["r-tcp"]["bob"] == old {
		t.Error("rotated credentials not applied live")
	}
	if err := SetUserEnabled(r, "bob", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.live["r-tcp"]["bob"]; ok {
		t.Error("disabled user still live")
	}
	if err := DeleteUser(r, "ann"); err != nil {
		t.Fatal(err)
	}
	if len(fs.live["r-tcp"]) != 0 {
		t.Errorf("deleted/disabled users still live: %v", fs.live["r-tcp"])
	}
	if fs.restarts != before {
		t.Errorf("user changes restarted Xray %d times", fs.restarts-before)
	}
	if strings.Contains(fs.files[remoteDir+"/config.json"], `"ann"`) {
		t.Error("config.json not rewritten, a restart would bring ann back")
	}
}

func TestCommitFallsBackToRestart(t *testing.T) {
	r, fs := deployed(t) // no users: the inbounds do not exist, so the API cannot add to them
	before := fs.restarts
	if _, _, err := AddUser(r, "ann", Limits{}); err != nil {
		t.Fatal(err)
	}
	if fs.restarts != before+1 || fs.live["r-tcp"]["ann"] == "" {
		t.Errorf("want one restart that brings ann up; restarts=%d live=%v", fs.restarts-before, fs.live)
	}
}

func TestEnforceQuotaExpiryAndDevices(t *testing.T) {
	ann, bob, cy := NewUser("ann"), NewUser("bob"), NewUser("cy")
	ann.Quota = 1000
	cy.Devices = 1
	r, fs := deployed(t, ann, bob, cy)
	fs.stats = `{"stat":[{"name":"user>>>ann>>>traffic>>>uplink","value":400},{"name":"user>>>ann>>>traffic>>>downlink","value":700},
		{"name":"user>>>bob>>>traffic>>>downlink","value":5}]}`
	fs.online = `{"users":[{"email":"cy","ips":[{"ip":"198.51.100.1","lastSeen":10},{"ip":"198.51.100.2","lastSeen":20}]}]}`
	if err := Enforce(r); err != nil {
		t.Fatal(err)
	}
	us, _ := LoadUsage(r)
	if u := us.Users["ann"]; u == nil || u.Up != 400 || u.Down != 700 {
		t.Fatalf("traffic not accumulated: %+v", us.Users["ann"])
	}
	if _, ok := fs.live["r-tcp"]["ann"]; ok {
		t.Error("ann is over quota but still live")
	}
	if fs.live["r-tcp"]["bob"] == "" {
		t.Error("bob should stay live")
	}
	if len(fs.sib) == 0 || fs.sib[len(fs.sib)-1] != "198.51.100.2" || strings.Contains(strings.Join(fs.sib, " "), "198.51.100.1") {
		t.Errorf("cy's second device should be blocked, the first kept: %v", fs.sib)
	}
	// the next run adds to the totals; the block holds; a reset puts ann back
	fs.stats = `{"stat":[{"name":"user>>>bob>>>traffic>>>downlink","value":5}]}`
	fs.online = `{"users":[{"email":"cy","ips":[{"ip":"198.51.100.1","lastSeen":30}]}]}`
	if err := Enforce(r); err != nil {
		t.Fatal(err)
	}
	us, _ = LoadUsage(r)
	if us.Users["bob"].Down != 10 || len(us.Blocks) != 1 {
		t.Errorf("bob=%d blocks=%v", us.Users["bob"].Down, us.Blocks)
	}
	if err := ResetUsage(r, "ann"); err != nil {
		t.Fatal(err)
	}
	if fs.live["r-tcp"]["ann"] == "" {
		t.Error("ann not live again after a usage reset")
	}
	// expiry
	days := -1
	if _, err := SetLimits(r, "bob", Limits{Days: &days}); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(r)
	_, b := st.User("bob")
	b.Expires = time.Now().Add(-time.Minute).Unix()
	saveState(r, st)
	if err := Enforce(r); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.live["r-tcp"]["bob"]; ok {
		t.Error("expired bob still live")
	}
}

func TestUserStatus(t *testing.T) {
	now := time.Unix(1000, 0)
	u := User{Enabled: true}
	cases := []struct {
		u    User
		uu   *UserUsage
		want string
	}{
		{u, nil, "active"},
		{User{Enabled: false}, nil, "disabled"},
		{User{Enabled: true, Expires: 999}, nil, "expired"},
		{User{Enabled: true, Expires: 1001}, nil, "active"},
		{User{Enabled: true, Quota: 10}, &UserUsage{Up: 4, Down: 6}, "over quota"},
		{User{Enabled: true, Quota: 10}, &UserUsage{Up: 4, Down: 5}, "active"},
	}
	for i, c := range cases {
		if got := userStatus(c.u, c.uu, now); got != c.want {
			t.Errorf("%d: %s, want %s", i, got, c.want)
		}
	}
}

func TestSubServer(t *testing.T) {
	st := testState()
	st.CDNDomain = "cdn.example.com"
	st.Sub = SubConfig{Mode: "cdn", Port: 2096, Path: "p4th"}
	st.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}}
	ann := NewUser("ann")
	ann.Quota, ann.Expires = 50<<30, 2000000000
	st.Users = []User{ann}
	us := &Usage{Users: map[string]*UserUsage{"ann": {Up: 1 << 30, Down: 2 << 30}}}
	h := subHandler{load: func() (*State, *Usage, error) { return st, us, nil }}
	get := func(path, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	base := "/p4th/" + ann.SubToken
	if want := "https://cdn.example.com:2096" + base; st.SubURL(ann) != want {
		t.Errorf("SubURL %s, want %s", st.SubURL(ann), want)
	}
	for _, p := range []string{"/", "/p4th", "/p4th/nope", "/wrong/" + ann.SubToken, base + "/x", base + "/json/x"} {
		if w := get(p, ""); w.Code != 404 {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
	w := get(base, "")
	raw, err := base64.StdEncoding.DecodeString(w.Body.String())
	if w.Code != 200 || err != nil || !strings.HasPrefix(string(raw), "vless://"+ann.UUID) {
		t.Errorf("subscription body: %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Subscription-Userinfo"); got != fmt.Sprintf("upload=%d; download=%d; total=%d; expire=2000000000", 1<<30, 2<<30, int64(50)<<30) {
		t.Errorf("userinfo header: %s", got)
	}
	w = get(base+"/json", "")
	var cfg J
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil || cfg["routing"] == nil {
		t.Errorf("json config: %v", err)
	}
	w = get(base, "text/html,application/xhtml+xml")
	body := w.Body.String()
	for _, want := range []string{"data:image/png;base64,", "3.0 GiB", "50.0 GiB", ann.SubToken + "/json"} {
		if !strings.Contains(body, want) {
			t.Errorf("html page lacks %q", want)
		}
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Error("html page headers")
	}
	st.Sub.Mode = ""
	if w := get(base, ""); w.Code != 404 {
		t.Error("a disabled subscription server must not answer")
	}
}

func TestFailoverConfig(t *testing.T) {
	st := testState()
	st.Country, st.CDNDomain = "ir", "cdn.example.com"
	st.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}, {ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2053, Path: "/p", Mode: "packet-up"}}
	cfg := st.FailoverConfig(NewUser("ann"))
	b, _ := json.Marshal(cfg)
	s := string(b)
	for _, want := range []string{`"proxy-r-tcp"`, `"proxy-cdn-xhttp"`, `"balancerTag":"auto"`, `"leastPing"`, `geosite:category-ir`, `geoip:ir`, `"fallbackTag":"proxy-r-tcp"`} {
		if !strings.Contains(s, want) {
			t.Errorf("failover config lacks %s", want)
		}
	}
	st.Country = ""
	if b, _ = json.Marshal(st.FailoverConfig(NewUser("ann"))); strings.Contains(string(b), "geoip:ir") {
		t.Error("country rules without a country")
	}
}
