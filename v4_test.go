package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------- quiet configs, daily usage

func TestInboundStatsAndDailyUsage(t *testing.T) {
	r, fs := deployed(t, NewUser("ann"))
	fs.stats = `{"stat":[{"name":"user>>>ann>>>traffic>>>downlink","value":3000},
		{"name":"inbound>>>r-tcp>>>traffic>>>downlink","value":3000},{"name":"inbound>>>api>>>traffic>>>uplink","value":9}]}`
	if err := Enforce(r); err != nil {
		t.Fatal(err)
	}
	us, _ := LoadUsage(r)
	if us.Users["ann"].Daily[dayKey(time.Now())] != 3000 {
		t.Errorf("daily: %v", us.Users["ann"].Daily)
	}
	if eu := us.Entries["r-tcp/443"]; eu == nil || eu.Bytes != 3000 || eu.Active == 0 {
		t.Errorf("entry usage: %+v", us.Entries)
	}
	if eu := us.Entries["ss2022/30001"]; eu == nil || eu.Active != 0 {
		t.Errorf("ss2022 carried nothing: %+v", eu)
	}
	if !strings.Contains(fs.files[remoteDir+"/config.json"], "statsInboundDownlink") {
		t.Error("the server config should count traffic per inbound")
	}
	st, _ := LoadState(r)
	p := pageData(st, st.Users[0], us.Users["ann"], time.Now(), "en")
	if len(p.History) != 30 || p.History[29].Pct != 100 {
		t.Errorf("page history: %+v", p.History)
	}
}

func TestQuietConfigAlert(t *testing.T) {
	got := captureAlerts(t)
	st := testState()
	st.Entries = []Entry{{ID: "r-tcp", Port: 443}, {ID: "hy2", Port: 8443}}
	st.Notify = Notify{Webhook: "https://hooks.example.com/x"}
	now := time.Now()
	us := &Usage{Entries: map[string]*EntryUsage{
		"r-tcp/443": {First: now.Add(-5 * time.Hour).Unix(), Active: now.Add(-3 * time.Hour).Unix()},
		"hy2/8443":  {First: now.Add(-5 * time.Hour).Unix(), Active: now.Add(-time.Minute).Unix()},
	}}
	notifyEntries(st, us, now)
	notifyEntries(st, us, now)
	if len(*got) != 1 || !strings.Contains((*got)[0], "r-tcp (port 443) has carried no traffic for 3h") {
		t.Fatalf("want one quiet alert: %v", *got)
	}
	// nobody uses anything (night): no alert for a fresh deployment either
	*got = nil
	us2 := &Usage{Entries: map[string]*EntryUsage{"r-tcp/443": {First: now.Add(-5 * time.Hour).Unix()},
		"hy2/8443": {First: now.Add(-5 * time.Hour).Unix()}}}
	notifyEntries(st, us2, now)
	if len(*got) != 0 {
		t.Errorf("no traffic anywhere is not a block: %v", *got)
	}
	// back
	us.Entries["r-tcp/443"].Active = now.Add(time.Second).Unix()
	notifyEntries(st, us, now.Add(time.Second))
	if len(*got) != 1 || !strings.Contains((*got)[0], "config-back") {
		t.Errorf("want a back alert: %v", *got)
	}
	if s := sparkline([]int64{0, 1, 8}); s != "▁▂█" {
		t.Errorf("sparkline %q", s)
	}
}

// ---------------------------------------------------------------- encrypted backups

func TestEncryptedBackup(t *testing.T) {
	r, _ := deployed(t, NewUser("ann"))
	b, err := TakeBackup(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "b.json")
	if err := writeBackup(path, b, "correct horse"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), b.State.RealityPriv) || !strings.Contains(string(raw), sealedFormat) {
		t.Fatal("the backup is not encrypted")
	}
	t.Setenv("XTUNE_BACKUP_PASSWORD", "wrong")
	if _, err := readBackup(path); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("wrong passphrase: %v", err)
	}
	t.Setenv("XTUNE_BACKUP_PASSWORD", "correct horse")
	got, err := readBackup(path)
	if err != nil || got.State.RealityPriv != b.State.RealityPriv || len(got.State.Users) != 1 {
		t.Fatalf("read back: %v", err)
	}
}

// ---------------------------------------------------------------- plans

func TestPlansAndRenew(t *testing.T) {
	r, _ := deployed(t, NewUser("ann"))
	if err := SetPlan(r, "basic", Plan{QuotaGB: 50, Days: 30, Devices: 2, Reset: "monthly"}); err != nil {
		t.Fatal(err)
	}
	plan := "basic"
	five := 5.0
	st, u, err := AddUser(r, "bob", Limits{Plan: &plan, QuotaGB: &five})
	if err != nil {
		t.Fatal(err)
	}
	if u.Quota != 5<<30 || u.Devices != 2 || u.Plan != "basic" || u.Reset != "monthly" || time.Until(time.Unix(u.Expires, 0)) < 29*24*time.Hour {
		t.Fatalf("bob: %+v", u)
	}
	if _, _, err := AddUser(r, "eve", Limits{Plan: ptr("gold")}); err == nil {
		t.Error("an unknown plan was accepted")
	}
	// over quota and disabled: renewing extends from the current expiry, resets usage, enables
	SetUserEnabled(r, "bob", false)
	us, _ := LoadUsage(r)
	us.of("bob").Down = 6 << 30
	saveUsage(r, us)
	before := st.Users[1].Expires
	_, nu, err := Renew(r, "bob", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !nu.Enabled || nu.Expires != time.Unix(before, 0).AddDate(0, 0, 30).Unix() {
		t.Errorf("renewed bob: %+v (expired %d)", nu, before)
	}
	if us, _ = LoadUsage(r); us.Users["bob"] != nil && us.Users["bob"].Down != 0 {
		t.Error("renewal keeps the usage")
	}
	// a user without plan or expiry stays without expiry; with --days it gets one
	if _, nu, _ = Renew(r, "ann", "", 0); nu.Expires != 0 {
		t.Errorf("ann got an expiry: %+v", nu)
	}
	if _, nu, _ = Renew(r, "ann", "basic", 0); nu.Plan != "basic" || nu.Quota != 50<<30 || nu.Expires == 0 {
		t.Errorf("ann with a plan: %+v", nu)
	}
}

func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------- bot: roles, language, self-service

func botFor(t *testing.T, users ...User) (*botServer, *Server, *fakeTelegram) {
	ft := startTelegram(t)
	r, fs := deployed(t, users...)
	fs.helper = map[string]string{"bot-enable": "active"}
	if _, err := SetBot(r, Bot{Token: "123:abc", Admins: []int64{42}}); err != nil {
		t.Fatal(err)
	}
	ft.take()
	return &botServer{r: r, tg: newTG("123:abc"), now: time.Now, name: "xt_bot"}, r, ft
}

func TestBotResellers(t *testing.T) {
	b, r, ft := botFor(t, NewUser("ann"))
	if _, err := changeBot(r, func(b *Bot) error {
		b.Resellers = []Reseller{{ID: 9, Name: "shop", MaxUsers: 2, QuotaGB: 30}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	say := func(text string) []string {
		b.handle(textMsg(9, text, now))
		return texts(ft.take(), "sendMessage")
	}
	if s := say("/users"); len(s) != 1 || strings.Contains(s[0], "ann") {
		t.Fatalf("a reseller sees others' users: %v", s)
	}
	if s := say("/user ann"); !strings.Contains(strings.Join(s, ""), "no user") {
		t.Fatalf("a reseller reached ann: %v", s)
	}
	if s := say("/add c1"); !strings.Contains(strings.Join(s, ""), "need a quota") {
		t.Fatalf("unlimited user for a reseller with a quota pool: %v", s)
	}
	say("/add c1 20 30")
	if s := say("/add c2 20 30"); !strings.Contains(strings.Join(s, ""), "more quota than you have left") {
		t.Fatalf("quota pool: %v", s)
	}
	say("/add c2 10 30")
	if s := say("/add c3 1 30"); !strings.Contains(strings.Join(s, ""), "at most 2 users") {
		t.Fatalf("user count: %v", s)
	}
	if s := say("/limit c2 quota=11"); !strings.Contains(strings.Join(s, ""), "more quota") {
		t.Fatalf("limit beyond the pool: %v", s)
	}
	st, _ := LoadState(r)
	if _, c1 := st.User("c1"); c1 == nil || c1.Owner != 9 {
		t.Fatalf("c1 should belong to the reseller: %+v", c1)
	}
	if s := say("/backup"); !strings.Contains(strings.Join(s, ""), "unknown command") {
		t.Errorf("a reseller got a backup: %v", s)
	}
	// admins see everyone, with the owner
	b.handle(textMsg(42, "/user c1", now))
	if s := texts(ft.take(), "sendMessage"); len(s) != 1 || !strings.Contains(s[0], "Reseller: shop") {
		t.Errorf("admin card: %v", s)
	}
}

func TestBotSelfServiceAndPersian(t *testing.T) {
	ann := NewUser("ann")
	ann.Quota = 1000
	b, r, ft := botFor(t, ann)
	now := time.Now()
	b.handle(textMsg(77, "hello", now))
	if s := texts(ft.take(), "sendMessage"); len(s) != 1 || !strings.Contains(s[0], "Not authorized") {
		t.Fatalf("self-service off: %v", s)
	}
	changeBot(r, func(b *Bot) error { b.SelfService, b.Lang = true, "fa"; return nil })
	b.handle(textMsg(77, "hello", now))
	if s := texts(ft.take(), "sendMessage"); len(s) != 1 || !strings.Contains(s[0], "لینک اشتراکتان") {
		t.Fatalf("Persian prompt: %v", s)
	}
	b.handle(textMsg(77, "/start 0123456789abcdef0123456789abcdef", now))
	if s := texts(ft.take(), "sendMessage"); !strings.Contains(strings.Join(s, ""), "لینک اشتراکتان") {
		t.Fatalf("a wrong token linked: %v", s)
	}
	b.handle(textMsg(77, "/start "+ann.SubToken, now))
	s := texts(ft.take(), "sendMessage")
	if len(s) != 1 || !strings.Contains(s[0], "به ann وصل شد") || !strings.Contains(s[0], "۱۰۰۰ B") {
		t.Fatalf("linking: %v", s)
	}
	st, _ := LoadState(r)
	if st.Users[0].Telegram != 77 {
		t.Fatal("not linked")
	}
	// the user's own commands only
	b.handle(textMsg(77, "/del ann", now))
	if st, _ = LoadState(r); len(st.Users) != 1 {
		t.Fatal("a user deleted themselves")
	}
	ft.take()
	// warnings reach the linked user
	var msgs []string
	old := sendUserMsg
	sendUserMsg = func(_ string, chat int64, text string) error { msgs = append(msgs, text); return nil }
	defer func() { sendUserMsg = old }()
	us, _ := LoadUsage(r)
	us.of("ann").Down = 900
	notifyUsers(st, us, now)
	notifyUsers(st, us, now)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "۹۰٪") {
		t.Errorf("user warning: %v", msgs)
	}
}

func TestBotBackupAndRenew(t *testing.T) {
	b, r, ft := botFor(t, NewUser("ann"))
	now := time.Now()
	b.handle(textMsg(42, "/backup", now))
	if s := texts(ft.take(), "sendMessage"); !strings.Contains(strings.Join(s, ""), "passphrase") {
		t.Fatalf("backup without a passphrase: %v", s)
	}
	changeBot(r, func(b *Bot) error { b.Backup = BotBackup{Hours: 24, Passphrase: "secret pass"}; return nil })
	b.maybeBackup()
	calls := ft.take()
	var doc *tgCall
	for i := range calls {
		if calls[i].method == "sendDocument" {
			doc = &calls[i]
		}
	}
	if doc == nil || doc.params["chat_id"] != "42" {
		t.Fatalf("scheduled backup: %v", calls)
	}
	b.maybeBackup()
	if len(ft.take()) != 0 {
		t.Error("the backup was sent twice")
	}
	if us, _ := LoadUsage(r); us.BackupAt == 0 {
		t.Error("the backup time was not saved")
	}
	SetPlan(r, "basic", Plan{QuotaGB: 10, Days: 30})
	b.handle(press(42, "renew ann"))
	if s := texts(ft.take(), "editMessageText"); len(s) != 1 || !strings.Contains(s[0], "Renewed") {
		t.Fatalf("renew button: %v", s)
	}
	b.handle(textMsg(42, "/add bob basic devices=3", now))
	st, _ := LoadState(r)
	if _, bob := st.User("bob"); bob == nil || bob.Plan != "basic" || bob.Quota != 10<<30 || bob.Devices != 3 {
		t.Fatalf("/add with a plan: %+v", bob)
	}
}

// ---------------------------------------------------------------- Cloudflare

type fakeCF struct {
	mu      sync.Mutex
	records map[string]cfRecord
	ssl     string
	calls   []string
}

func (f *fakeCF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	var result any = true
	p := r.URL.Path
	switch {
	case p == "/user/tokens/verify":
		result = J{"status": "active"}
	case p == "/zones":
		result = []J{}
		if r.URL.Query().Get("name") == "example.com" {
			result = []J{{"id": "z1", "name": "example.com"}}
		}
	case p == "/zones/z1/settings/ssl":
		if r.Method == "PATCH" {
			var b J
			json.NewDecoder(r.Body).Decode(&b)
			f.ssl = b["value"].(string)
		}
		result = J{"value": f.ssl}
	case p == "/zones/z1/dns_records" && r.Method == "GET":
		var out []cfRecord
		for _, rec := range f.records {
			if rec.Name == r.URL.Query().Get("name") {
				out = append(out, rec)
			}
		}
		result = out
	case p == "/zones/z1/dns_records" && r.Method == "POST":
		var rec cfRecord
		json.NewDecoder(r.Body).Decode(&rec)
		rec.ID = "r" + rec.Name
		f.records[rec.ID] = rec
	case strings.HasPrefix(p, "/zones/z1/dns_records/"):
		id := strings.TrimPrefix(p, "/zones/z1/dns_records/")
		if r.Method == "DELETE" {
			delete(f.records, id)
		} else {
			var b J
			json.NewDecoder(r.Body).Decode(&b)
			rec := f.records[id]
			rec.Content, rec.Proxied = b["content"].(string), b["proxied"].(bool)
			f.records[id] = rec
		}
	}
	json.NewEncoder(w).Encode(J{"success": true, "result": result})
}

func TestCloudflareDNSAndSSL(t *testing.T) {
	f := &fakeCF{ssl: "flexible", records: map[string]cfRecord{
		"a1": {ID: "a1", Type: "A", Name: "vpn.example.com", Content: "198.51.100.1"},
		"a2": {ID: "a2", Type: "AAAA", Name: "vpn.example.com", Content: "2001:db8::1"},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	old := cloudflareAPI
	cloudflareAPI = srv.URL
	defer func() { cloudflareAPI = old }()
	st := &State{Domain: "vpn.example.com", CDNDomain: "cdn.example.com", Cloudflare: Cloudflare{Token: "t"}}
	var log []string
	if err := CloudflarePoint(st, "203.0.113.9", func(s string) { log = append(log, s) }); err != nil {
		t.Fatal(err)
	}
	want := map[string]cfRecord{
		"a1":               {ID: "a1", Type: "A", Name: "vpn.example.com", Content: "203.0.113.9"},
		"rcdn.example.com": {ID: "rcdn.example.com", Type: "A", Name: "cdn.example.com", Content: "203.0.113.9", Proxied: true, TTL: 1},
	}
	if len(f.records) != 2 || f.records["a1"] != want["a1"] || f.records["rcdn.example.com"] != want["rcdn.example.com"] {
		t.Errorf("records: %+v", f.records)
	}
	if mode, err := cfSSL("t", "cdn.example.com", true); err != nil || mode != "full" || f.ssl != "full" {
		t.Errorf("ssl: %s %v", mode, err)
	}
}

// ---------------------------------------------------------------- several servers

func TestPrimaryAndFollower(t *testing.T) {
	ann := NewUser("ann")
	ann.Quota = 10 << 30
	pr, _ := deployed(t, ann, NewUser("bob"))
	st, _ := LoadState(pr)
	st.CDNDomain, st.Sub = "cdn.example.com", SubConfig{Mode: "cdn", Port: 2053, Path: "subs"}
	saveState(pr, st)
	if _, err := EnablePeers(pr); err != nil {
		t.Fatal(err)
	}
	SetUserEnabled(pr, "bob", false)
	load := func() (*State, *Usage, error) {
		st, err := LoadState(pr)
		if err != nil {
			return nil, nil, err
		}
		us, err := LoadUsage(pr)
		return st, us, err
	}
	srv := httptest.NewTLSServer(subHandler{load: load, r: pr})
	defer srv.Close()
	old := peerClient
	peerClient = srv.Client()
	defer func() { peerClient = old }()
	st, _ = LoadState(pr)
	peerURL := srv.URL + "/subs/peer/" + st.Peers.Secret

	fr, ffs := deployed(t, NewUser("local"))
	fst, _ := LoadState(fr)
	fst.Host = "198.51.100.20"
	saveState(fr, fst)
	if _, err := SetFollow(fr, srv.URL+"/subs/peer/wrong", "de1"); err == nil {
		t.Fatal("a wrong secret was accepted")
	}
	fst, err := SetFollow(fr, peerURL, "de1")
	if err != nil {
		t.Fatal(err)
	}
	if len(fst.Users) != 1 || fst.Users[0].Name != "ann" || fst.Users[0].UUID != ann.UUID {
		t.Fatalf("follower users: %+v", fst.Users)
	}
	if _, _, err := AddUser(fr, "x", Limits{}); err == nil || !strings.Contains(err.Error(), "managed there") {
		t.Errorf("a follower added a user: %v", err)
	}
	// the follower's traffic counts on the primary
	ffs.stats = `{"stat":[{"name":"user>>>ann>>>traffic>>>downlink","value":5000}]}`
	if err := Enforce(fr); err != nil {
		t.Fatal(err)
	}
	ffs.stats = ""
	Enforce(fr) // nothing new to report
	us, _ := LoadUsage(pr)
	if us.Users["ann"].Down != 5000 {
		t.Errorf("primary's count of ann: %+v", us.Users["ann"])
	}
	// the primary's subscription lists the follower's configs, at the follower's address
	st, _ = LoadState(pr)
	var names []string
	for _, l := range st.NamedLinks(st.Users[0]) {
		names = append(names, l.Name)
		if strings.HasPrefix(l.Name, "de1-") && !strings.Contains(l.URL, "198.51.100.20") {
			t.Errorf("follower link %s points elsewhere", l.URL)
		}
	}
	if got := strings.Join(names, ","); got != "r-tcp,ss2022,de1-r-tcp,de1-ss2022" {
		t.Errorf("links: %s", got)
	}
	if fc, _ := json.Marshal(st.FailoverConfig(st.Users[0])); !strings.Contains(string(fc), "proxy-de1-r-tcp") {
		t.Error("the failover config lacks the follower")
	}
	if st.Peers.Servers["de1"].State.RealityPriv != "" {
		t.Error("the primary stored the follower's private key")
	}
}

// ---------------------------------------------------------------- REALITY targets

func TestSNIRefresh(t *testing.T) {
	got := captureAlerts(t)
	r, fs := deployed(t, NewUser("ann"))
	fs.used = "SNI a.com fail\nSNI a.com fail\nSNI a.com fail\nSNI www.amd.com 2 0.050\nSNI www.nvidia.com 2 0.020\nSNI dl.google.com 1.1 0.010"
	st, _ := LoadState(r)
	st.Notify = Notify{Webhook: "https://hooks.example.com/x"}
	saveState(r, st)
	if err := checkSNIs(r); err != nil {
		t.Fatal(err)
	}
	if st, _ = LoadState(r); st.Entries[0].SNI != "a.com" {
		t.Fatal("changed a link without subscriptions")
	}
	if len(*got) != 1 || !strings.Contains((*got)[0], "use www.nvidia.com") {
		t.Fatalf("alert: %v", *got)
	}
	*got = nil
	st.CDNDomain, st.Sub = "cdn.example.com", SubConfig{Mode: "cdn", Port: 2053, Path: "subs"}
	saveState(r, st)
	if err := checkSNIs(r); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadState(r)
	if st.Entries[0].SNI != "www.nvidia.com" || !strings.Contains(fs.files[remoteDir+"/config.json"], "www.nvidia.com:443") {
		t.Errorf("not refreshed: %+v", st.Entries[0])
	}
	if len(*got) != 1 || !strings.Contains((*got)[0], "changed REALITY targets") {
		t.Errorf("alert: %v", *got)
	}
	// weekly
	us := &Usage{}
	now := time.Now()
	if sniDue(st, us, now) || us.SNIAt == 0 || sniDue(st, us, now.Add(time.Hour)) || !sniDue(st, us, now.Add(8*24*time.Hour)) {
		t.Error("sniDue schedule")
	}
}
