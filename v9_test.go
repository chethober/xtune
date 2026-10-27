package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	mrand "math/rand/v2"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// ---------------------------------------------------------------- page languages

func TestPageLanguages(t *testing.T) {
	for _, l := range langs {
		for k := range pageText["en"] {
			if pageText[l][k] == "" {
				t.Errorf("%s: no text for %q", l, k)
			}
		}
	}
	st := testStateWithCert(t)
	st.Sub = SubConfig{Mode: "cdn", Port: 2053, Path: "p"}
	st.CDNDomain = "cdn.example.com"
	u := NewUser("bob")
	u.Expires = time.Now().Add(-time.Hour).Unix()
	for lang, want := range map[string]string{"ru": "истекла", "zh": "已到期", "en": ">expired<"} {
		var b strings.Builder
		if err := subPage.Execute(&b, pageData(st, u, &UserUsage{}, time.Now(), lang)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), want) || strings.Contains(b.String(), "%s") {
			t.Errorf("%s page: no %q status, or an unfilled %%s", lang, want)
		}
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if l := pageLang(req, ""); l != "zh" {
		t.Errorf("zh-CN browser got %s", l)
	}
	if validLang("ru") != nil || validLang("de") == nil {
		t.Error("ru should be valid, de not")
	}
}

// ---------------------------------------------------------------- metrics

func TestMetrics(t *testing.T) {
	st := testStateWithCert(t)
	st.Sub = SubConfig{Mode: "cdn", Port: 2053, Path: "p", Metrics: "tok"}
	st.CDNDomain = "cdn.example.com"
	st.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443}}
	st.Users = []User{{Name: "a\"b", Enabled: true, Quota: 1 << 30, SubToken: "t1"}, {Name: "c", Enabled: false, SubToken: "t2"}}
	now := time.Now()
	us := &Usage{Users: map[string]*UserUsage{"a\"b": {Up: 5, Down: 7, Online: []string{"1.2.3.4"}}}, Updated: now.Unix(),
		Entries: map[string]*EntryUsage{"r-tcp/443": {Bytes: 99, Active: now.Unix()}}}
	h := subHandler{load: func() (*State, *Usage, error) { return st, us, nil }}
	get := func(path, auth string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if code, _ := get("/p/metrics", ""); code != 404 {
		t.Errorf("metrics without a token: %d", code)
	}
	if code, _ := get("/p/metrics", "nope"); code != 404 {
		t.Errorf("metrics with a wrong token: %d", code)
	}
	code, body := get("/p/metrics", "tok")
	if code != 200 {
		t.Fatalf("metrics: %d", code)
	}
	for _, want := range []string{`xtune_users{status="active"} 1`, `xtune_users{status="disabled"} 1`,
		`xtune_user_traffic_bytes{user="a\"b",direction="down"} 7`, `xtune_user_online_devices{user="a\"b"} 1`,
		`xtune_config_traffic_bytes{config="r-tcp",kind="reality-tcp"} 99`, "xtune_xray_up 1", "# TYPE xtune_user_traffic_bytes counter"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s:\n%s", want, body)
		}
	}
	if strings.Count(body, "# HELP xtune_user_traffic_bytes") != 1 {
		t.Error("a family's HELP line is repeated")
	}
	if code, _ := get("/p/metrics/tok", ""); code != 200 {
		t.Errorf("token in the URL: %d", code)
	}
	st.Sub.Metrics = ""
	if code, _ := get("/p/metrics", "tok"); code != 404 {
		t.Errorf("metrics off: %d", code)
	}
}

// ---------------------------------------------------------------- scheduled backups

// The example from AWS's "Signature Calculations for the Authorization Header" (PUT Object).
func TestSigV4Example(t *testing.T) {
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	h := map[string]string{"host": "examplebucket.s3.amazonaws.com", "date": "Fri, 24 May 2013 00:00:00 GMT",
		"x-amz-date": "20130524T000000Z", "x-amz-storage-class": "REDUCED_REDUNDANCY",
		"x-amz-content-sha256": sha256Hex([]byte("Welcome to Amazon S3."))}
	got := sigV4("PUT", s3Escape("/test$file.text"), "", h, h["x-amz-content-sha256"], "us-east-1", "s3",
		"AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", now)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class, " +
		"Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"
	if got != want {
		t.Errorf("SigV4:\n got %s\nwant %s", got, want)
	}
}

func TestParseS3(t *testing.T) {
	s, err := parseS3("https://acc.r2.cloudflarestorage.com/bkt/xtune/a", "", "ak", "sk")
	if err != nil || s.Bucket != "bkt" || s.Prefix != "xtune/a" || s.Region != "auto" || s.Endpoint != "https://acc.r2.cloudflarestorage.com" {
		t.Errorf("%+v %v", s, err)
	}
	if s.key("f.json") != "xtune/a/f.json" {
		t.Errorf("key %s", s.key("f.json"))
	}
	for _, bad := range []string{"http://x/b", "https://x/", "nope"} {
		if _, err := parseS3(bad, "", "ak", "sk"); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := parseS3("https://x/b", "", "", "sk"); err == nil {
		t.Error("no access key accepted")
	}
}

func TestScheduledBackup(t *testing.T) {
	var s3Body, tgFile []byte
	var s3Path, s3Auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT":
			s3Path, s3Auth = r.URL.Path, r.Header.Get("Authorization")
			s3Body, _ = io.ReadAll(r.Body)
		case strings.HasSuffix(r.URL.Path, "/sendDocument"):
			_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				if p.FormName() == "document" {
					tgFile, _ = io.ReadAll(p)
				}
			}
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	old := telegramAPI
	telegramAPI = srv.URL
	defer func() { telegramAPI = old }()

	st := testStateWithCert(t)
	st.Users = []User{NewUser("bob")}
	st.Notify = Notify{TelegramToken: "T", TelegramChat: "1"}
	st.Backups = &BackupPlan{Every: 86400, Pass: "correct horse", Telegram: true,
		S3: &S3Target{Endpoint: srv.URL, Region: "auto", Bucket: "bkt", Prefix: "x", AccessKey: "ak", SecretKey: "sk"}}
	us := &Usage{Users: map[string]*UserUsage{}}
	now := time.Now()
	if !backupDue(st, us, now) {
		t.Fatal("a new schedule should back up at once")
	}
	if err := runScheduledBackup(st, us, now); err != nil {
		t.Fatal(err)
	}
	if us.LastBackup != now.Unix() || backupDue(st, us, now.Add(time.Hour)) || !backupDue(st, us, now.Add(25*time.Hour)) {
		t.Errorf("schedule after a success: %+v", us)
	}
	if !strings.HasPrefix(s3Path, "/bkt/x/xtune-backup-") || !strings.Contains(s3Auth, "Credential=ak/") {
		t.Errorf("S3 upload %s %s", s3Path, s3Auth)
	}
	for name, data := range map[string][]byte{"s3": s3Body, "telegram": tgFile} {
		if strings.Contains(string(data), st.RealityPriv) || !strings.Contains(string(data), sealedFormat) {
			t.Errorf("%s copy is not sealed", name)
		}
		plain, err := unseal(data, func() (string, error) { return "correct horse", nil })
		if err != nil || !strings.Contains(string(plain), `"bob"`) {
			t.Errorf("%s copy does not unseal to the backup: %v", name, err)
		}
	}
	// a failure is retried after an hour, not at the next period
	st.Backups.S3.Endpoint = "http://127.0.0.1:1"
	later := now.Add(25 * time.Hour)
	if err := runScheduledBackup(st, us, later); err == nil || us.BackupErr == "" {
		t.Fatal("an unreachable bucket should fail")
	}
	if backupDue(st, us, later.Add(30*time.Minute)) || !backupDue(st, us, later.Add(61*time.Minute)) {
		t.Error("a failed backup should be retried after an hour")
	}
	st.Follow = &Follow{URL: "https://p/x/peer/s"}
	if backupDue(st, us, later.Add(48*time.Hour)) {
		t.Error("followers do not back up")
	}
}

// ---------------------------------------------------------------- clean IPs

func TestSampleCF(t *testing.T) {
	ips := sampleCF(400, mrand.New(mrand.NewPCG(1, 2)))
	if len(ips) != 400 {
		t.Fatalf("%d samples", len(ips))
	}
	var ps []netip.Prefix
	for _, r := range cfRanges {
		ps = append(ps, netip.MustParsePrefix(r))
	}
	seen := map[string]bool{}
	for _, s := range ips {
		a := netip.MustParseAddr(s)
		if !slices.ContainsFunc(ps, func(p netip.Prefix) bool { return p.Contains(a) }) {
			t.Errorf("%s is not Cloudflare's", s)
		}
		k := s[:strings.LastIndex(s, ".")]
		if seen[k] {
			t.Errorf("two samples in %s.0/24", k)
		}
		seen[k] = true
	}
}

func TestTraceColo(t *testing.T) {
	ok := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nfl=1\nh=cdn.example.com\nip=5.6.7.8\ncolo=FRA\nhttp=http/1.1\n"
	if c, err := traceColo(strings.NewReader(ok)); err != nil || c != "FRA" {
		t.Errorf("%q %v", c, err)
	}
	for _, bad := range []string{"HTTP/1.1 403 Forbidden\r\n\r\nno", "HTTP/1.1 200 OK\r\n\r\n<html>blocked</html>"} {
		if _, err := traceColo(strings.NewReader(bad)); err == nil {
			t.Errorf("%q accepted as an edge", bad)
		}
	}
}

func TestScanCleanIPs(t *testing.T) {
	old := cfProbe
	defer func() { cfProbe = old }()
	var mu sync.Mutex
	tries := map[string]int{}
	cfProbe = func(ctx context.Context, ip string, port int, sni string) (string, error) {
		mu.Lock()
		tries[ip]++
		n := tries[ip]
		mu.Unlock()
		if port != 2053 || sni != "cdn.example.com" {
			return "", errors.New("wrong target")
		}
		last := ip[strings.LastIndex(ip, ".")+1:]
		switch {
		case strings.HasPrefix(last, "1"): // fast and steady
			return "FRA", nil
		case strings.HasPrefix(last, "2") && n == 1: // answers once, then never
			return "AMS", nil
		}
		return "", errors.New("blocked")
	}
	ips := ScanCleanIPs(ScanOpts{SNI: "cdn.example.com", Port: 2053, Samples: 200, Keep: 4})
	if len(ips) == 0 || len(ips) > 4 {
		t.Fatalf("%d clean IPs", len(ips))
	}
	for _, c := range ips {
		if !strings.HasPrefix(c.IP[strings.LastIndex(c.IP, ".")+1:], "1") || c.Ok < 2 || c.Colo != "FRA" {
			t.Errorf("not a steady IP: %+v", c)
		}
	}
}

func TestCleanIPVariants(t *testing.T) {
	old := cfProbe
	defer func() { cfProbe = old }()
	cfProbe = func(ctx context.Context, ip string, port int, sni string) (string, error) { return "FRA", nil }
	st := testState()
	st.CDNDomain = "cdn.example.com"
	entries := []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "www.a.com", Primary: true},
		{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2053, Path: "/p"},
		{ID: "worker", Kind: "worker-ws", Port: 30000, Path: "/w", SNI: "w.x.workers.dev"}}
	vs := ExpandVariants(entries, "smart")
	n := len(vs)
	vs = addCleanIPVariants(st, entries, vs, 2, func(string) {})
	added := vs[n:]
	if len(added) != 4 {
		t.Fatalf("%d clean-IP variants, want 2 per CDN/Worker entry", len(added))
	}
	for _, v := range added {
		if v.K.Addr == "" || !strings.Contains(v.Label, " ip="+v.K.Addr) || v.Entry.Reality() {
			t.Errorf("bad clean-IP variant %+v", v)
		}
		if v.K.Frag != "none" || simplicity(v) != 4 { // the plainest settings, plus the IP
			t.Errorf("%s: knobs %+v simplicity %d", v.Label, v.K, simplicity(v))
		}
	}
	// links and configs dial the IP and keep the domain as SNI and Host
	u := NewUser("bob")
	e := entries[1]
	k := Knobs{FP: "chrome", Frag: "none", Mode: "auto", Addr: "104.16.1.1"}
	l := st.Link(e, u, k)
	pu, _ := url.Parse(l)
	if pu.Hostname() != "104.16.1.1" || pu.Query().Get("sni") != "cdn.example.com" || pu.Query().Get("host") != "cdn.example.com" {
		t.Errorf("clean-IP link %s", l)
	}
	ob := st.Outbound(e, u, k, "p")
	if a := ob["settings"].(J)["vnext"].([]J)[0]["address"]; a != "104.16.1.1" {
		t.Errorf("outbound dials %v", a)
	}
	if st.dialAddr(entries[0], Knobs{Addr: "104.16.1.1"}) != st.Addr() {
		t.Error("a clean IP must not apply to direct configs")
	}
	// deploying keeps it on the entry
	v := &Variant{Entry: e, K: k}
	if got := deployChoice([]*Variant{v}, 1)[0]; got.K.Addr != "104.16.1.1" {
		t.Error("lost the clean IP")
	}
	if e2 := (Entry{Addr: "1.1.1.1"}); e2.Knobs().Addr != "1.1.1.1" {
		t.Error("Entry.Knobs drops Addr")
	}
}

// ---------------------------------------------------------------- per-network rankings

func rankedState() *State {
	st := testState()
	st.CDNDomain = "cdn.example.com"
	st.Sub = SubConfig{Mode: "cdn", Port: 2053, Path: "p"}
	st.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"},
		{ID: "hy2", Kind: "hysteria2", Port: 8443},
		{ID: "ss2022", Kind: "ss2022", Port: 30001},
		{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2096, Path: "/p", FP: "chrome", Frag: "none", Mode: "auto"}}
	return st
}

func TestForNetwork(t *testing.T) {
	st := rankedState()
	now := time.Now()
	st.Networks = map[string]*NetRank{"AS12880": {Name: "TCI", Updated: now.Unix(),
		Scores: map[string]float64{"r-tcp/443": 0, "hy2/8443": 5, "cdn-xhttp/2096": 40},
		Addrs:  map[string]string{"cdn-xhttp/2096": "104.17.3.4", "r-tcp/443": "9.9.9.9"}}}
	v, name := st.forNetwork("AS12880", now)
	var ids []string
	for _, e := range v.Entries {
		ids = append(ids, e.ID)
	}
	// best first, then the one not measured there, the blocked one last
	if strings.Join(ids, ",") != "cdn-xhttp,hy2,ss2022,r-tcp" || name != "TCI" {
		t.Errorf("order %v, name %q", ids, name)
	}
	if v.Entries[0].Addr != "104.17.3.4" || v.Entries[3].Addr != "" {
		t.Errorf("clean IPs: only CDN and Worker configs take one: %+v", v.Entries)
	}
	if st.Entries[0].ID != "r-tcp" || st.Entries[3].Addr != "" {
		t.Error("forNetwork changed the deployment itself")
	}
	if same, n := st.forNetwork("AS1", now); same != st || n != "" {
		t.Error("an unranked network should get the deployment as is")
	}
	if same, _ := st.forNetwork("AS12880", now.Add(31*24*time.Hour)); same != st {
		t.Error("a stale ranking should not be used")
	}
}

func TestSubServesRankedConfigs(t *testing.T) {
	old := asnLookup
	defer func() { asnLookup = old }()
	asked := ""
	asnLookup = func(ip net.IP) (string, error) {
		asked = ip.String()
		if ip.String() == "5.200.1.2" {
			return "AS12880", nil
		}
		return "AS3320", nil
	}
	st := rankedState()
	u := NewUser("bob")
	st.Users = []User{u}
	st.Networks = map[string]*NetRank{"AS12880": {Name: "TCI", Updated: time.Now().Unix(),
		Scores: map[string]float64{"r-tcp/443": 0, "cdn-xhttp/2096": 40}, Addrs: map[string]string{"cdn-xhttp/2096": "104.17.3.4"}}}
	h := subHandler{load: func() (*State, *Usage, error) { return st, &Usage{Users: map[string]*UserUsage{}}, nil }}
	get := func(ip, path, accept string) string {
		req := httptest.NewRequest("GET", "/p/"+u.SubToken+path, nil)
		req.Header.Set("CF-Connecting-IP", ip)
		req.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Body.String()
	}
	links := func(body string) []string {
		raw, _ := base64.StdEncoding.DecodeString(body)
		return strings.Split(string(raw), "\n")
	}
	tci := links(get("5.200.1.2", "", ""))
	if asked != "5.200.1.2" {
		t.Errorf("looked up %s, not the address Cloudflare saw", asked)
	}
	if !strings.Contains(tci[0], "cdn-xhttp") || !strings.Contains(tci[0], "@104.17.3.4:2096") || !strings.Contains(tci[len(tci)-1], "r-tcp") {
		t.Errorf("TCI order: %v", tci)
	}
	other := links(get("80.1.2.3", "", ""))
	if !strings.Contains(other[0], "r-tcp") || strings.Contains(strings.Join(other, ""), "104.17.3.4") {
		t.Errorf("another network should get the deployment's order: %v", other)
	}
	if page := get("5.200.1.2", "", "text/html"); !strings.Contains(page, "TCI") {
		t.Error("the page should say it is sorted for the network")
	}
	var cfg J
	json.Unmarshal([]byte(get("5.200.1.2", "/json", "")), &cfg)
	if fb := cfg["routing"].(map[string]any)["balancers"].([]any)[0].(map[string]any)["fallbackTag"]; fb != "proxy-cdn-xhttp" {
		t.Errorf("failover config falls back to %v", fb)
	}
}

func TestCymruQuery(t *testing.T) {
	// a resolver that answers like origin.asn.cymru.com
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	var qname string
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil || len(m.Questions) == 0 {
				continue
			}
			q := m.Questions[0]
			qname = q.Name.String()
			m.Header.Response, m.Header.Authoritative = true, true
			if q.Type == dnsmessage.TypeTXT {
				m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: q.Class, TTL: 60},
					Body: &dnsmessage.TXTResource{TXT: []string{"12880 | 5.200.0.0/16 | IR | ripencc | 2008-01-01"}}}}
			}
			out, _ := m.Pack()
			pc.WriteTo(out, addr)
		}
	}()
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", pc.LocalAddr().String())
	}}
	asn, err := cymruASN(res, net.ParseIP("5.200.1.2"))
	if err != nil || asn != "AS12880" || qname != "2.1.200.5.origin.asn.cymru.com." {
		t.Errorf("%q %v (asked %s)", asn, err, qname)
	}
	cymruASN(res, net.ParseIP("2001:db8::1"))
	if !strings.HasSuffix(qname, ".8.b.d.0.1.0.0.2.origin6.asn.cymru.com.") {
		t.Errorf("IPv6 query %s", qname)
	}
}

func TestRanksFromTunesAndReports(t *testing.T) {
	cdn := Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2096, FP: "chrome", Frag: "none", Mode: "auto"}
	rt := Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 443, FP: "chrome", Frag: "none"}
	vs := []*Variant{{Entry: cdn, K: cdn.Knobs(), Score: 3}, {Entry: cdn, K: Knobs{FP: "chrome", Frag: "none", Mode: "auto", Addr: "104.1.1.1"}, Score: 30},
		{Entry: rt, K: rt.Knobs(), Score: 20}, {Entry: rt, K: Knobs{FP: "firefox", Frag: "none"}, Score: 25}}
	scores, addrs := variantRank([]Entry{cdn, rt}, vs)
	if scores["cdn-xhttp/2096"] != 3 || scores["r-tcp/443"] != 20 || addrs["cdn-xhttp/2096"] != "104.1.1.1" || len(addrs) != 1 {
		t.Errorf("tune ranks %v %v", scores, addrs)
	}
	rs := []CheckResult{{ID: "cdn-xhttp", Label: "cdn L", Score: 10}, {ID: "cdn-xhttp", Label: "cdn L ip=104.2.2.2", Score: 15},
		{ID: "r-tcp", Label: "rt L", Score: 0}}
	scores, addrs = labelRank([]Entry{cdn, rt}, map[string]string{"cdn-xhttp": "cdn L", "r-tcp": "rt L"}, rs)
	if scores["cdn-xhttp/2096"] != 10 || scores["r-tcp/443"] != 0 || addrs["cdn-xhttp/2096"] != "104.2.2.2" {
		t.Errorf("report ranks %v %v", scores, addrs)
	}
	if s := checkScore(CheckResult{Ok: 0, N: 5}); s != 0 {
		t.Errorf("a blocked config scores %v", s)
	}
}

func TestRecordNetwork(t *testing.T) {
	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
	st := rankedState()
	if err := saveState(r, st); err != nil {
		t.Fatal(err)
	}
	n := Network{ASN: "AS44244", Name: "Irancell"}
	if err := RecordNetwork(r, n, map[string]float64{"r-tcp/443": 9, "gone/1": 4}, map[string]string{"cdn-xhttp/2096": "104.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	// a check keeps the clean IPs and adds its scores
	if err := recordCheck(r, n, st, []CheckResult{{ID: "hy2", Ok: 5, N: 5, Warm: 100, Down: 10}}); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadState(r)
	nr := got.Networks["AS44244"]
	if nr == nil || nr.Scores["r-tcp/443"] != 9 || nr.Scores["hy2/8443"] == 0 || nr.Addrs["cdn-xhttp/2096"] != "104.1.1.1" {
		t.Fatalf("ranking %+v", nr)
	}
	if _, ok := nr.Scores["gone/1"]; ok {
		t.Error("a config that is not deployed was ranked")
	}
	if RecordNetwork(r, Network{}, map[string]float64{"r-tcp/443": 1}, nil) != nil {
		t.Error("an unknown network is skipped, not an error")
	}
	var b strings.Builder
	printNetworks(&b, got, time.Now())
	if !strings.Contains(b.String(), "Irancell") || !strings.Contains(b.String(), "r-tcp") {
		t.Errorf("networks list:\n%s", b.String())
	}
}

// ---------------------------------------------------------------- device limits across servers

func TestDeviceLimitsAcrossServers(t *testing.T) {
	ann := NewUser("ann")
	ann.Devices = 1
	r, fs := deployed(t, ann)
	st, _ := LoadState(r)
	now := time.Now()
	st.Peers = &PeerHub{Secret: "s", Servers: map[string]*PeerServer{
		"de": {Seen: now.Unix(), Online: map[string][]string{"ann": {"198.51.100.9"}}},
		"nl": {Seen: now.Add(-time.Hour).Unix(), Online: map[string][]string{"ann": {"198.51.100.77"}}}, // stale: not counted
	}}
	// ann's phone has been on the follower for a while; her laptop just came on the primary
	us := &Usage{Users: map[string]*UserUsage{"ann": {Online: []string{"198.51.100.1"}}},
		Online: map[string]int64{"ann|198.51.100.9": now.Add(-10 * time.Minute).Unix()}}
	if err := applyDeviceLimits(r, st, us, map[string][]string{}, now); err != nil {
		t.Fatal(err)
	}
	if got := us.blocksFor("ann", now); len(got) != 1 || got[0] != "198.51.100.1" {
		t.Errorf("the newcomer on the primary should be blocked, not the established follower device: %v (sib %v)", got, fs.sib)
	}
	// the other way round: the primary's device is established, a new one shows up on the follower
	us = &Usage{Users: map[string]*UserUsage{"ann": {Online: []string{"198.51.100.1"}}}}
	if err := applyDeviceLimits(r, st, us, map[string][]string{"ann": {"198.51.100.1"}}, now); err != nil {
		t.Fatal(err)
	}
	if got := us.blocksFor("ann", now); len(got) != 1 || got[0] != "198.51.100.9" {
		t.Errorf("the follower's newcomer should be blocked: %v", got)
	}
	// the primary tells the follower: limits and blocks travel in its reply
	saveState(r, st)
	saveUsage(r, us)
	rep, err := acceptPeer(r, peerReport{Name: "de", State: testState(), Online: map[string][]string{"ann": {"198.51.100.9"}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Users) != 1 || rep.Users[0].Devices != 1 || !slices.Contains(rep.Users[0].Block, "198.51.100.9") {
		t.Errorf("reply %+v", rep.Users)
	}
	got, _ := LoadState(r)
	if ips := got.Peers.Servers["de"].Online["ann"]; len(ips) != 1 {
		t.Errorf("the follower's online IPs were not kept: %v", got.Peers.Servers["de"])
	}
	// one IP on two servers is one device
	us = &Usage{Users: map[string]*UserUsage{"ann": {Online: []string{"198.51.100.9"}}}}
	st.Peers.Servers["de"].Online["ann"] = []string{"198.51.100.9"}
	applyDeviceLimits(r, st, us, map[string][]string{"ann": {"198.51.100.9"}}, now)
	if len(us.Blocks) != 0 {
		t.Errorf("the same IP on two servers was counted twice: %v", us.Blocks)
	}
}

func TestFollowerTakesLimitsAndBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in peerReport
		json.NewDecoder(r.Body).Decode(&in)
		if len(in.Online["bob"]) != 1 {
			t.Errorf("the follower did not report who is online: %+v", in.Online)
		}
		json.NewEncoder(w).Encode(peerReply{Users: []peerUser{{Name: "bob", UUID: "u", SSKey: "k", Devices: 2, Block: []string{"203.0.113.50", "not-an-ip"}}}})
	}))
	defer srv.Close()
	st := testState()
	st.Follow = &Follow{URL: srv.URL, Name: "de"}
	st.Users = []User{{Name: "bob", UUID: "u", SSKey: "k", Enabled: true}}
	us := &Usage{Users: map[string]*UserUsage{"bob": {Online: []string{"203.0.113.9"}}}}
	changed, err := syncPrimary(st, us)
	if err != nil || !changed {
		t.Fatalf("a new device limit is a change: %v %v", changed, err)
	}
	if st.Users[0].Devices != 2 || len(us.Blocks) != 1 || us.Blocks[0].IP != "203.0.113.50" || us.Blocks[0].User != "bob" {
		t.Errorf("users %+v blocks %+v", st.Users, us.Blocks)
	}
	if changed, _ := syncPrimary(st, us); changed || len(us.Blocks) != 1 {
		t.Errorf("same reply again: changed=%v blocks=%v", changed, us.Blocks)
	}
}
