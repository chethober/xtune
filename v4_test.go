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
