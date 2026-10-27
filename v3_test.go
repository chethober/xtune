package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// localXray is a local Xray to validate generated configs with; tests that need it skip without.
func localXray(t *testing.T) string {
	t.Helper()
	b := FindLocalXray("")
	if b == "" || XrayVersion(b) == "broken" {
		t.Skip("no local xray")
	}
	return b
}

func testStateWithCert(t *testing.T) *State {
	st := testState()
	if err := st.makeCert(); err != nil {
		t.Fatal(err)
	}
	return st
}

// ---------------------------------------------------------------- port hopping

func TestPlanEntriesHop(t *testing.T) {
	probe := "SNI www.a.com 2 0.20\nUSED udp 0.0.0.0:20500\nHOP yes"
	entries, _ := PlanEntries(probe, false)
	var hop *Entry
	for i := range entries {
		if entries[i].Hop != "" {
			hop = &entries[i]
		}
	}
	if hop == nil || hop.Kind != "hysteria2" {
		t.Fatalf("no port-hopping entry when the server can forward: %+v", entries)
	}
	lo, _ := strconv.Atoi(strings.Split(hop.Hop, "-")[0])
	hi, _ := strconv.Atoi(strings.Split(hop.Hop, "-")[1])
	if hi-lo != 999 || (lo <= 20500 && 20500 <= hi) {
		t.Errorf("hop range %s overlaps a used port or has the wrong size", hop.Hop)
	}
	for _, e := range entries {
		if e.Proto() == "udp" && e.Port >= lo && e.Port <= hi {
			t.Errorf("%s listens inside the hop range", e.ID)
		}
	}
	if es, _ := PlanEntries("SNI www.a.com 2 0.20\nHOP no", false); len(es) != len(entries)-1 {
		t.Error("hop entry planned without forwarding support")
	}
	specs := strings.Join(portSpecs([]Entry{*hop}), " ")
	if specs != fmt.Sprintf("%d/udp hop:%s:%d", hop.Port, hop.Hop, hop.Port) {
		t.Errorf("helper specs: %s", specs)
	}
	st := testState()
	l := st.Link(*hop, NewUser("a"), Knobs{})
	if q := mustQuery(t, l); q.Get("mport") != hop.Hop {
		t.Errorf("hysteria2 link lacks the hop range: %s", l)
	}
	ob, _ := json.Marshal(st.Outbound(*hop, NewUser("a"), Knobs{}, "p"))
	if !strings.Contains(string(ob), `"udphop"`) || !strings.Contains(string(ob), `"remotePorts":"`+hop.Hop+`"`) {
		t.Errorf("outbound lacks udphop: %s", ob)
	}
}

// ---------------------------------------------------------------- XHTTP split

func TestSplitCDNVariant(t *testing.T) {
	st := testState()
	st.CDNDomain, st.Domain = "cdn.example.com", "direct.example.com"
	cdn := Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2053, Path: "/p"}
	n := 0
	for _, v := range ExpandVariants([]Entry{cdn, {ID: "r-xhttp", Kind: "reality-xhttp", Port: 1, SNI: "a.b.com", Path: "/x"}}, "smart") {
		if v.K.Split != "" {
			n++
			if !v.Entry.CDN() || !strings.Contains(v.Label, "split=cdn-up") {
				t.Errorf("split variant on %s: %s", v.Entry.ID, v.Label)
			}
		}
	}
	if n != 2 {
		t.Errorf("want 2 split variants (packet-up, stream-up), got %d", n)
	}
	k := Knobs{FP: "chrome", Frag: "none", Mode: "packet-up", Split: "cdn-up"}
	ss := st.Outbound(cdn, NewUser("a"), k, "p")["streamSettings"].(J)
	down := ss["xhttpSettings"].(J)["extra"].(J)["downloadSettings"].(J)
	if down["address"] != "direct.example.com" || down["port"] != 2053 || down["tlsSettings"].(J)["pinnedPeerCertSha256"] != "ff" {
		t.Errorf("download half should go straight to the server, pinned: %v", down)
	}
	if ss["tlsSettings"].(J)["serverName"] != "cdn.example.com" {
		t.Error("upload half should stay on the CDN")
	}
	q := mustQuery(t, st.Link(cdn, NewUser("a"), k))
	var extra J
	if err := json.Unmarshal([]byte(q.Get("extra")), &extra); err != nil || extra["downloadSettings"] == nil {
		t.Errorf("link extra: %q %v", q.Get("extra"), err)
	}
	if q := mustQuery(t, st.Link(cdn, NewUser("a"), Knobs{Mode: "auto"})); q.Get("extra") != "" {
		t.Error("extra on an unsplit link")
	}
	if e := (Entry{Kind: "cdn-xhttp", Split: "cdn-up", Mode: "stream-up"}); e.Knobs().Split != "cdn-up" {
		t.Error("deployed entries must keep their split setting")
	}
}

// The configs xtune generates must be ones Xray accepts.
func TestXrayAcceptsNewConfigs(t *testing.T) {
	bin := localXray(t)
	st := testStateWithCert(t)
	st.CDNDomain = "cdn.example.com"
	u := NewUser("a")
	u.UUID = "11111111-2222-4333-8444-555555555555"
	hop := Entry{ID: "hy2-hop", Kind: "hysteria2", Port: 30000, Hop: "21000-21999"}
	cdn := Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2053, Path: "/p"}
	for name, cfg := range map[string]J{
		"hop":   ClientConfig(st.Outbound(hop, u, Knobs{}, "proxy")),
		"split": ClientConfig(st.Outbound(cdn, u, Knobs{FP: "chrome", Mode: "packet-up", Split: "cdn-up"}, "proxy")),
	} {
		if err := xrayAccepts(bin, cfg); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a full server config with WARP, the API and every inbound kind
	st.Warp = &Warp{PrivateKey: "YFYOAdbw1bKTHlNNi+aEjBM3BO7unuFC5rOkMRAz9XY=", Address4: "172.16.0.2", Address6: "2606:4700::1",
		PeerKey: "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=", Endpoint: "engage.cloudflareclient.com:2408", Reserved: []int{1, 2, 3}}
	st.APIPort = 41234
	priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	st.RealityPriv = base64.RawURLEncoding.EncodeToString(priv.Bytes())
	st.RealityPub = base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	st.SSKey = ssKey()
	dir := t.TempDir()
	os.WriteFile(dir+"/hy.crt", []byte(st.CertPEM), 0o600)
	os.WriteFile(dir+"/hy.key", []byte(st.KeyPEM), 0o600)
	entries := []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 30001, SNI: "www.microsoft.com"}, cdn, hop,
		{ID: "ss2022", Kind: "ss2022", Port: 30002}}
	u.SSKey = ssKey()
	cfg := st.ServerConfig(entries, []User{u})
	testCerts(cfg, dir)
	if err := xrayAccepts(bin, cfg); err != nil {
		t.Errorf("server config: %v", err)
	}
	st.Entries = entries
	if err := xrayAccepts(bin, st.FailoverConfig(u)); err != nil {
		t.Errorf("failover config: %v", err)
	}
}

// ---------------------------------------------------------------- check user, resets

func TestCheckUserIsHiddenAndLive(t *testing.T) {
	r, fs := deployed(t, NewUser("ann"))
	st, err := ensureCheckUser(r)
	if err != nil || st.Check == nil {
		t.Fatal(err)
	}
	if fs.live["r-tcp"][checkUserName] != st.Check.UUID {
		t.Errorf("check user not live: %v", fs.live["r-tcp"])
	}
	again, _ := ensureCheckUser(r)
	if again.Check.UUID != st.Check.UUID {
		t.Error("check user re-created")
	}
	var b strings.Builder
	us, _ := LoadUsage(r)
	printUsers(&b, again, us)
	if strings.Contains(b.String(), checkUserName) {
		t.Error("check user listed with the users")
	}
	if validUserName(checkUserName) == nil {
		t.Error("the check user's name must be reserved")
	}
	// it survives user changes and redeploys
	if _, _, err := AddUser(r, "bob", Limits{}); err != nil {
		t.Fatal(err)
	}
	if fs.live["r-tcp"][checkUserName] == "" {
		t.Error("check user dropped by a user change")
	}
}

func TestMonthlyReset(t *testing.T) {
	ann := NewUser("ann")
	ann.Quota = 1000
	r, fs := deployed(t, ann)
	monthly := "monthly"
	if _, err := SetLimits(r, "ann", Limits{Reset: &monthly}); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(r)
	_, u := st.User("ann")
	if want := time.Now().AddDate(0, 1, 0).Unix(); u.ResetAt < want-5 || u.ResetAt > want+5 {
		t.Fatalf("next reset %d, want about %d", u.ResetAt, want)
	}
	fs.stats = `{"stat":[{"name":"user>>>ann>>>traffic>>>downlink","value":2000}]}`
	if err := Enforce(r); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.live["r-tcp"]["ann"]; ok {
		t.Fatal("ann should be over quota")
	}
	// the period ends: usage starts from zero and ann is back
	st, _ = LoadState(r)
	_, u = st.User("ann")
	past := time.Now().Add(-time.Hour)
	u.ResetAt = past.Unix()
	saveState(r, st)
	fs.stats = ""
	if err := Enforce(r); err != nil {
		t.Fatal(err)
	}
	us, _ := LoadUsage(r)
	if uu := us.Users["ann"]; uu.Up+uu.Down != 0 {
		t.Errorf("usage not reset: %+v", uu)
	}
	if fs.live["r-tcp"]["ann"] == "" {
		t.Error("ann not live again after the reset")
	}
	st, _ = LoadState(r)
	if _, u = st.User("ann"); u.ResetAt != nextReset("monthly", past).Unix() {
		t.Errorf("next reset should be one period on: %v", time.Unix(u.ResetAt, 0))
	}
	bad := "yearly"
	if _, err := SetLimits(r, "ann", Limits{Reset: &bad}); err == nil {
		t.Error("unknown period accepted")
	}
	none := ""
	st, _ = SetLimits(r, "ann", Limits{Reset: &none})
	if _, u = st.User("ann"); u.Reset != "" || u.ResetAt != 0 {
		t.Error("reset not cleared")
	}
}

// ---------------------------------------------------------------- alerts

func captureAlerts(t *testing.T) *[]string {
	var got []string
	old := sendAlert
	sendAlert = func(_ Notify, _, event, text string) error { got = append(got, event+": "+text); return nil }
	t.Cleanup(func() { sendAlert = old })
	return &got
}

func TestAlerts(t *testing.T) {
	got := captureAlerts(t)
	ann, bob := NewUser("ann"), NewUser("bob")
	ann.Quota = 1000
	bob.Expires = time.Now().Add(24 * time.Hour).Unix()
	r, fs := deployed(t, ann, bob)
	if err := SetNotify(r, Notify{Webhook: "https://hooks.example.com/x"}); err != nil {
		t.Fatal(err)
	}
	*got = nil
	fs.stats = `{"stat":[{"name":"user>>>ann>>>traffic>>>downlink","value":850}]}`
	Enforce(r)
	fs.stats = ""
	Enforce(r) // no repeats
	if s := strings.Join(*got, "\n"); !strings.Contains(s, "ann:quota80: ann has used 85%") || !strings.Contains(s, "bob:expiring") || len(*got) != 2 {
		t.Fatalf("want one 80%% alert for ann and one expiry warning for bob:\n%s", s)
	}
	*got = nil
	fs.stats = `{"stat":[{"name":"user>>>ann>>>traffic>>>downlink","value":200}]}`
	Enforce(r)
	if len(*got) != 1 || !strings.HasPrefix((*got)[0], "ann:quota:") {
		t.Fatalf("over-quota alert: %v", *got)
	}
	us, _ := LoadUsage(r)
	if us.Notified["ann:quota80"] != 0 {
		t.Error("80% alert should be re-armed once over quota")
	}
	// Xray down, then back
	*got = nil
	saved := fs.live
	fs.live = map[string]map[string]string{"x": {}} // inbounds gone: API calls on tags fail, statsquery still answers
	st, _ := LoadState(r)
	st.APIPort = 1
	saveState(r, st)
	failing := &failAPI{fakeServer: fs}
	r.sh = failing
	Enforce(r)
	Enforce(r)
	if len(*got) != 0 {
		t.Fatalf("alerted before %d failures: %v", xrayDownAfter, *got)
	}
	Enforce(r)
	Enforce(r)
	r.sh = fs
	fs.live = saved
	Enforce(r)
	if s := strings.Join(*got, "\n"); strings.Count(s, "xray-down") != 1 || !strings.Contains(s, "xray-up") {
		t.Errorf("want one down and one up alert:\n%s", s)
	}
}

// failAPI makes every Xray API call fail.
type failAPI struct{ *fakeServer }

func (f *failAPI) exec(cmd, stdin string, sudo bool) (string, string, error) {
	if strings.Contains(cmd, "/xray api ") {
		return "", "", fmt.Errorf("connection refused")
	}
	return f.fakeServer.exec(cmd, stdin, sudo)
}

func TestTelegramAlertFormat(t *testing.T) {
	var form url.Values
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		r.ParseForm()
		form = r.PostForm
	}))
	defer srv.Close()
	old := telegramAPI
	telegramAPI = srv.URL
	defer func() { telegramAPI = old }()
	if err := sendAlert(Notify{TelegramToken: "123:abc", TelegramChat: "42"}, "vpn.example.com", "test", "hello"); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:abc/sendMessage" || form.Get("chat_id") != "42" || form.Get("text") != "xtune (vpn.example.com): hello" {
		t.Errorf("telegram request %s %v", path, form)
	}
	if d := (Notify{Webhook: "https://hooks.example.com/secret/path"}).describe(); strings.Contains(d, "secret") {
		t.Errorf("describe leaks the webhook path: %s", d)
	}
}

// ---------------------------------------------------------------- Persian page

func TestJalali(t *testing.T) {
	for _, c := range []struct{ gy, gm, gd, jy, jm, jd int }{
		{2026, 10, 17, 1405, 7, 25}, {2024, 3, 20, 1403, 1, 1}, {2025, 3, 21, 1404, 1, 1}, {2025, 3, 20, 1403, 12, 30},
		{2000, 1, 1, 1378, 10, 11},
	} {
		if y, m, d := jalali(c.gy, c.gm, c.gd); y != c.jy || m != c.jm || d != c.jd {
			t.Errorf("%d-%d-%d: %d/%d/%d, want %d/%d/%d", c.gy, c.gm, c.gd, y, m, d, c.jy, c.jm, c.jd)
		}
	}
	if got := faDigits("3.5 GiB 2026"); got != "۳٫۵ GiB ۲۰۲۶" {
		t.Errorf("faDigits: %s", got)
	}
}

func TestPersianPage(t *testing.T) {
	st := testState()
	st.CDNDomain = "cdn.example.com"
	st.Sub = SubConfig{Mode: "cdn", Port: 2096, Path: "p"}
	st.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}}
	ann := NewUser("ann")
	ann.Quota, ann.Expires = 10<<30, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC).Unix()
	ann.Reset, ann.ResetAt = "monthly", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix()
	st.Users = []User{ann}
	us := &Usage{Users: map[string]*UserUsage{"ann": {Down: 3 << 30}}}
	h := subHandler{load: func() (*State, *Usage, error) { return st, us, nil }}
	get := func(q, accept string) string {
		req := httptest.NewRequest("GET", "/p/"+ann.SubToken+q, nil)
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Accept-Language", accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Body.String()
	}
	fa := get("", "fa-IR,fa;q=0.9,en;q=0.5")
	for _, want := range []string{`dir="rtl"`, `lang="fa"`, "مصرف‌شده", "۳٫۰ GiB", "۱۴۰۵/۰۸/۰۵", "ماهانه", "سهمیه"} {
		if !strings.Contains(fa, want) {
			t.Errorf("Persian page lacks %q", want)
		}
	}
	if en := get("", "en-US"); !strings.Contains(en, `dir="ltr"`) || !strings.Contains(en, "Used") || !strings.Contains(en, "monthly") {
		t.Error("English page")
	}
	if forced := get("?lang=en", "fa"); !strings.Contains(forced, `lang="en"`) {
		t.Error("?lang= should win over the browser")
	}
	st.Sub.Lang = "fa"
	if set := get("", "en"); !strings.Contains(set, `lang="fa"`) {
		t.Error("the server setting should win over the browser")
	}
	if validLang("de") == nil {
		t.Error("unknown language accepted")
	}
}

// ---------------------------------------------------------------- output

func TestUsersJSONAndQR(t *testing.T) {
	st := testState()
	st.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}}
	ann := NewUser("ann")
	ann.Quota = 5
	st.Users = []User{ann}
	var b bytes.Buffer
	if err := printUsersJSON(&b, st, &Usage{Users: map[string]*UserUsage{"ann": {Up: 1, Down: 2, Online: []string{"1.2.3.4"}}}}); err != nil {
		t.Fatal(err)
	}
	var us []userJSON
	if err := json.Unmarshal(b.Bytes(), &us); err != nil || len(us) != 1 || us[0].Down != 2 || us[0].Online[0] != "1.2.3.4" || us[0].Status != "active" {
		t.Errorf("users json: %s %v", b.String(), err)
	}
	b.Reset()
	if err := printLinksJSON(&b, st, "ann"); err != nil || !strings.Contains(b.String(), `"links"`) {
		t.Errorf("links json: %s", b.String())
	}
	b.Reset()
	if err := printLinksQR(&b, st, "ann"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) < 10 || !strings.Contains(b.String(), "▀") {
		t.Errorf("QR output looks wrong (%d lines)", len(lines))
	}
}

// ---------------------------------------------------------------- backup / restore

func TestBackupRestoreKeepsIdentity(t *testing.T) {
	ann := NewUser("ann")
	r, _ := deployed(t, ann)
	st, _ := LoadState(r)
	st.Domain, st.Sub = "vpn.example.com", SubConfig{Mode: "cdn", Port: 2096, Path: "pp"}
	st.CDNDomain = "cdn.example.com"
	saveState(r, st)
	us, _ := LoadUsage(r)
	us.of("ann").Down = 777
	us.of("ann").Online = []string{"9.9.9.9"}
	saveUsage(r, us)

	b, err := TakeBackup(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "b.json")
	if err := writeBackup(path, b, ""); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // no Unix modes there
		t.Errorf("backup mode %v", fi.Mode().Perm())
	}
	if writeBackup(path, b, "") == nil {
		t.Error("an existing backup must not be overwritten")
	}
	b, err = readBackup(path)
	if err != nil {
		t.Fatal(err)
	}

	// a port the backup needs is taken on the new server
	nfs := newFake()
	nfs.helper = map[string]string{"status": "ARCH x86_64\nXRAY v26.10.10\nXTUNE dev"}
	nr := &Server{sh: nfs, Conn: Conn{Host: "198.51.100.9"}}
	nfs.used = "USED tcp 0.0.0.0:443"
	if _, err := Restore(nr, b, func(s string) { t.Log(s) }); err == nil || !strings.Contains(err.Error(), "r-tcp 443/tcp") {
		t.Errorf("want a port conflict, got %v", err)
	}
	nfs.used = ""
	got, err := Restore(nr, b, func(s string) { t.Log(s) })
	if err != nil && !strings.Contains(err.Error(), "xtune could not be installed") {
		t.Fatal(err)
	}
	got, _ = LoadState(nr)
	if got == nil || got.Host != "198.51.100.9" || got.RealityPriv != st.RealityPriv || got.Users[0].SubToken != ann.SubToken ||
		got.Sub.Path != "pp" || got.Domain != "vpn.example.com" {
		t.Fatalf("restored state: %+v", got)
	}
	nus, _ := LoadUsage(nr)
	if nus.Users["ann"].Down != 777 || len(nus.Users["ann"].Online) != 0 {
		t.Errorf("usage not carried over, or online IPs kept: %+v", nus.Users["ann"])
	}
	if _, err := seed(nr, b, "", func(s string) { t.Log(s) }); err == nil {
		t.Error("restoring over an existing deployment must be refused")
	}
	notes := strings.Join(moveNotes(got, "203.0.113.7", false), "\n")
	for _, want := range []string{"vpn.example.com", "cdn.example.com", "198.51.100.9", "subscription links stay the same", "uninstall --host 203.0.113.7"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
}

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]Conn{
		"1.2.3.4":          {Host: "1.2.3.4"},
		"bob@1.2.3.4:2222": {User: "bob", Host: "1.2.3.4", Port: 2222},
		"[2001:db8::1]:22": {Host: "2001:db8::1", Port: 22},
		"2001:db8::1":      {Host: "2001:db8::1"},
	} {
		got, err := parseTarget(in)
		if err != nil || got.Host != want.Host || got.User != want.User || got.Port != want.Port {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
}

// ---------------------------------------------------------------- several networks

func TestMergeChoiceCoversEveryNetwork(t *testing.T) {
	v := func(id, label string) *Variant {
		return &Variant{Entry: Entry{ID: id, Kind: "reality-tcp"}, K: Knobs{FP: "chrome", Frag: "none"}, Label: label}
	}
	a, a2, b, c := v("a", "a"), v("a", "a fp=firefox"), v("b", "b"), v("c", "c")
	a2.K.FP = "firefox"
	cdn := &Variant{Entry: Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp"}, Label: "cdn"}
	nets := []netScores{
		{Name: "mci", Score: map[string]float64{"a": 100, "a fp=firefox": 102, "b": 10, "cdn": 5}},
		{Name: "irancell", Score: map[string]float64{"c": 50, "cdn": 1}},
		{Name: "dead", Score: map[string]float64{}},
	}
	chosen, uncovered := mergeChoice([]*Variant{a, a2, b, c, cdn}, nets, 1)
	ids := map[string]bool{}
	for _, x := range chosen {
		ids[x.Label] = true
	}
	if !ids["a"] || !ids["c"] || ids["a fp=firefox"] {
		t.Errorf("want a (simplest within 10%%) and c (only one on irancell): %v", ids)
	}
	if !ids["cdn"] {
		t.Error("a working CDN config is always kept")
	}
	if len(uncovered) != 1 || uncovered[0] != "dead" {
		t.Errorf("uncovered %v", uncovered)
	}
}

func TestShareAndDeployPending(t *testing.T) {
	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
	st := testState()
	st.Users = []User{NewUser("ann")}
	tu := NewUser("tune")
	e1 := Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}
	e2 := Entry{ID: "hy2", Kind: "hysteria2", Port: 8443}
	vs := []*Variant{{Entry: e1, K: Knobs{FP: "chrome", Frag: "none"}, Label: "r-tcp:443", Score: 50, Ok: 5},
		{Entry: e2, K: Knobs{Frag: "none"}, Label: "hy2:8443", Score: 0}}
	b, err := Share(r, &TuneResult{State: st, User: tu, Entries: []Entry{e1, e2}, All: vs}, "smart", "mci")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	if strings.Contains(string(raw), st.RealityPriv) || strings.Contains(string(raw), st.Users[0].UUID) || b.State.KeyPEM != "" {
		t.Error("the bundle must not hold server secrets or real users")
	}
	if b.User.UUID != tu.UUID || time.Until(time.Unix(b.Expires, 0)) > testLifetime {
		t.Error("bundle user / expiry")
	}
	other := &NetReport{ID: b.ID, Network: "irancell", Results: []CheckResult{{Label: "hy2:8443", Score: 30}}}
	if _, err := DeployPending(r, []*NetReport{{ID: "nope"}}, 1, io.Discard); err == nil {
		t.Error("a report for another tune must be refused")
	}
	got, err := DeployPending(r, []*NetReport{other}, 1, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Errorf("want r-tcp for mci and hy2 for irancell: %+v", got.Entries)
	}
	if _, ok := fs.files[pendingFile]; ok {
		t.Error("pending tune not removed after deploying")
	}
}

// ---------------------------------------------------------------- check measurements

func TestThrottleDetection(t *testing.T) {
	w := &rateWriter{}
	// 1 MB/s for 6 s, then 50 kB/s for 3 s
	var n int64
	for s := 1; s <= 9; s++ {
		if s <= 6 {
			n += 1 << 20
		} else {
			n += 50 << 10
		}
		w.marks = append(w.marks, mark{time.Duration(s) * time.Second, n})
	}
	w.n = n
	tp := w.rates(9 * time.Second)
	if tp.Early < 7 || tp.Late > 1 {
		t.Fatalf("rates %+v", tp)
	}
	if s := checkStatus(CheckResult{Ok: 5, N: 5}, tp); s != "throttled" {
		t.Errorf("status %s", s)
	}
	if s := checkStatus(CheckResult{Ok: 0, N: 5}, throughput{}); s != "blocked" {
		t.Errorf("status %s", s)
	}
	if s := checkStatus(CheckResult{Ok: 5, N: 5}, throughput{Avg: 20, Early: 20, Late: 19}); s != "ok" {
		t.Errorf("status %s", s)
	}
}

func TestHistory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // macOS
	t.Setenv("AppData", t.TempDir())
	for i := 0; i < 3; i++ {
		if err := appendHistory(Record{Time: int64(1000 + i), Host: fmt.Sprint("h", i%2), Kind: "check",
			Results: []CheckResult{{ID: "r-tcp", Ok: 5, N: 5, Status: "ok"}}}); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := loadHistory("h0")
	if err != nil || len(recs) != 2 {
		t.Fatalf("%d records %v", len(recs), err)
	}
	var b strings.Builder
	printHistory(&b, recs, 1)
	if strings.Count(b.String(), "r-tcp") != 1 {
		t.Errorf("history output:\n%s", b.String())
	}
}

// ---------------------------------------------------------------- WARP

func TestWarpRegistrationAndConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/reg" || body["key"] == "" {
			http.Error(w, "bad", 400)
			return
		}
		io.WriteString(w, `{"config":{"client_id":"AQID","peers":[{"public_key":"bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
			"endpoint":{"host":"engage.cloudflareclient.com:2408"}}],"interface":{"addresses":{"v4":"172.16.0.2","v6":"2606:4700::2"}}}}`)
	}))
	defer srv.Close()
	old := warpAPI
	warpAPI = srv.URL
	defer func() { warpAPI = old }()
	r, fs := deployed(t, NewUser("ann"))
	st, err := SetWarp(r, true, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Warp.Address4 != "172.16.0.2" || fmt.Sprint(st.Warp.Reserved) != "[1 2 3]" || st.Warp.PrivateKey == "" {
		t.Fatalf("warp %+v", st.Warp)
	}
	cfg := fs.files[remoteDir+"/config.json"]
	if !strings.Contains(cfg, `"wireguard"`) || !strings.Contains(cfg, `geosite:category-ai-!cn`) {
		t.Errorf("config lacks the WARP outbound or rule")
	}
	key := st.Warp.PrivateKey
	if st, _ = SetWarp(r, true, []string{"geosite:netflix"}, true); st.Warp.PrivateKey != key || !st.Warp.All {
		t.Error("changing WARP routing should keep the account")
	}
	if !strings.Contains(fs.files[remoteDir+"/config.json"], `"outboundTag": "warp"`) {
		t.Error("all-traffic rule missing")
	}
	if st, _ = SetWarp(r, false, nil, false); st.Warp != nil || strings.Contains(fs.files[remoteDir+"/config.json"], "wireguard") {
		t.Error("WARP not removed")
	}
}

// ---------------------------------------------------------------- doctor

func TestDoctor(t *testing.T) {
	r, fs := deployed(t, NewUser("ann"))
	st, _ := LoadState(r)
	st.Domain, st.CDNDomain = "vpn.example.com", "cdn.example.com"
	st.Entries = append(st.Entries, Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2053, Path: "/p"},
		Entry{ID: "hy2-hop", Kind: "hysteria2", Port: 30000, Hop: "21000-21999"})
	saveState(r, st)
	fs.helper = map[string]string{
		"status": "ARCH x86_64\nXRAY v26.10.10\nUNIT active\nSTATE yes\nTIMER active\nFW ufw\nXTUNE " + version,
		"doctor": fmt.Sprintf("TIME %d\nNTP yes\nHOPRULES 0\nUSED tcp 0.0.0.0:443\nUSED tcp 0.0.0.0:30001\nUSED tcp 0.0.0.0:2053", time.Now().Unix()-300),
	}
	oldLookup, oldGet := lookup, cdnGet
	defer func() { lookup, cdnGet = oldLookup, oldGet }()
	lookup = func(h string) ([]string, error) {
		return map[string][]string{"203.0.113.7": {"203.0.113.7"}, "vpn.example.com": {"192.0.2.1"},
			"cdn.example.com": {"104.16.1.1"}}[h], nil
	}
	cdnGet = func(string) (*http.Response, error) {
		return &http.Response{StatusCode: 526, Header: http.Header{"Cf-Ray": {"x"}}}, nil
	}
	var b strings.Builder
	Doctor(r).print(&b)
	out := b.String()
	for _, want := range []string{"vpn.example.com points at 192.0.2.1", "Full (strict)", "hy2-hop: nothing listens on 30000/udp",
		"forwarding rules are missing", "differ by 5m", "CDN domain cdn.example.com goes through the CDN"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "r-tcp: nothing listens") {
		t.Error("a listening port reported as closed")
	}
}

// ---------------------------------------------------------------- self-update

func TestSelfUpdateArchive(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{{"README.md", "readme"}, {"xtune", "binary"}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	sums := []byte(hex.EncodeToString(sum[:]) + "  xtune_linux_amd64.tar.gz\n")
	if err := verifySum(sums, "xtune_linux_amd64.tar.gz", buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if verifySum(sums, "xtune_linux_amd64.tar.gz", append(buf.Bytes(), 0)) == nil {
		t.Error("tampered archive accepted")
	}
	if verifySum(sums, "xtune_darwin_arm64.tar.gz", buf.Bytes()) == nil {
		t.Error("archive missing from the sums accepted")
	}
	bin, err := extractXtune("xtune_linux_amd64.tar.gz", buf.Bytes())
	if err != nil || string(bin) != "binary" {
		t.Errorf("extract: %q %v", bin, err)
	}
	exe := filepath.Join(t.TempDir(), "xtune")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := replaceExe(exe, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Error("binary not replaced")
	}
}

// --host of a saved server takes its user and port; flags still win.
func TestHostUsesSavedProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := saveProfile(Conn{Host: "203.0.113.5", User: "chth", Port: 2222}); err != nil {
		t.Fatal(err)
	}
	conn := func(args ...string) Conn {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		c := addConnFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		cn, err := c.conn()
		if err != nil {
			t.Fatal(err)
		}
		return cn
	}
	if cn := conn("--host", "203.0.113.5"); cn.User != "chth" || cn.Port != 2222 {
		t.Errorf("saved profile not used: %+v", cn)
	}
	if cn := conn("--host", "203.0.113.5", "--user", "root"); cn.User != "root" || cn.Port != 2222 {
		t.Errorf("--user should win: %+v", cn)
	}
	if cn := conn("--host", "198.51.100.1"); cn.User != "" {
		t.Errorf("an unknown host got a profile's user: %+v", cn)
	}
}
