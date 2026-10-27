package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"runtime"
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
	// deploying keeps the domain: the clean IP reaches the network it was found on through the rankings
	viaIP := &Variant{Entry: e, K: k, Score: 30, Ok: 5, N: 5}
	plain := &Variant{Entry: e, K: Knobs{FP: "chrome", Frag: "none", Mode: "auto"}, Score: 4, Ok: 5, N: 5}
	frag := &Variant{Entry: e, K: Knobs{FP: "chrome", Frag: "frag-mild", Mode: "auto"}, Score: 6, Ok: 5, N: 5}
	all := []*Variant{viaIP, plain, frag}
	if got := domainVariant(viaIP, all); got != frag {
		t.Errorf("deploy should take the best variant through the domain: %+v", got.K)
	}
	if got := domainVariant(plain, all); got != plain {
		t.Error("a domain variant is deployed as it is")
	}
	blocked := []*Variant{viaIP, {Entry: e, K: Knobs{FP: "firefox", Frag: "frag-aggr"}}, {Entry: e, K: Knobs{FP: "chrome", Frag: "none"}}}
	if got := domainVariant(viaIP, blocked); got.K.Addr != "" || got.K.Frag != "none" {
		t.Errorf("nothing through the domain from here: its plainest settings, got %+v", got.K)
	}
	ent := e
	ent.FP, ent.Frag, ent.Mode = frag.K.FP, frag.K.Frag, frag.K.Mode
	if _, addrs := variantRank([]Entry{ent}, all); addrs[entryKey(ent)] != "104.16.1.1" {
		t.Errorf("the tuning network's ranking should carry the clean IP: %v", addrs)
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
	if runtime.GOOS == "windows" {
		t.Skip("the custom resolver needs Go's own DNS client")
	}
	// a resolver that answers like origin.asn.cymru.com
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	var mu sync.Mutex
	var qname string
	asked := func() string { mu.Lock(); defer mu.Unlock(); return qname }
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
			mu.Lock()
			qname = q.Name.String()
			mu.Unlock()
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
	if err != nil || asn != "AS12880" || asked() != "2.1.200.5.origin.asn.cymru.com." {
		t.Errorf("%q %v (asked %s)", asn, err, asked())
	}
	cymruASN(res, net.ParseIP("2001:db8::1"))
	if !strings.HasSuffix(asked(), ".8.b.d.0.1.0.0.2.origin6.asn.cymru.com.") {
		t.Errorf("IPv6 query %s", asked())
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

// ---------------------------------------------------------------- Worker limits and extra Workers

// multiAccountCF is the part of the Cloudflare API that extra Workers use, with an account per token.
type multiAccountCF struct {
	mu       sync.Mutex
	accounts map[string]string            // token -> account
	scripts  map[string]map[string]string // account/name -> routes
	deleted  []string
}

func (f *multiAccountCF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	acc := f.accounts[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if acc == "" {
		json.NewEncoder(w).Encode(J{"success": false, "errors": []J{{"message": "bad token"}}})
		return
	}
	p := r.URL.Path
	var result any = J{}
	parts := strings.Split(p, "/")
	switch {
	case p == "/user/tokens/verify":
		result = J{"status": "active"}
	case p == "/accounts":
		result = []J{{"id": acc}}
	case strings.HasSuffix(p, "/workers/subdomain"):
		result = J{"subdomain": "sub-" + acc}
	case len(parts) > 5 && parts[2] != acc:
		json.NewEncoder(w).Encode(J{"success": false, "errors": []J{{"message": "not your account"}}})
		return
	case strings.HasSuffix(p, "/subdomain"):
	case r.Method == "DELETE":
		f.deleted = append(f.deleted, acc+"/"+parts[5])
	case r.Method == "PUT":
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			if part.FormName() == "metadata" {
				var meta struct{ Bindings []struct{ Text string } }
				json.NewDecoder(part).Decode(&meta)
				var routes map[string]string
				json.Unmarshal([]byte(meta.Bindings[0].Text), &routes)
				f.scripts[acc+"/"+parts[5]] = routes
			}
		}
	}
	json.NewEncoder(w).Encode(J{"success": true, "result": result})
}

func TestExtraWorkers(t *testing.T) {
	cf := &multiAccountCF{accounts: map[string]string{"tokA": "acc1", "tokB": "acc2"}, scripts: map[string]map[string]string{}}
	srv := httptest.NewServer(cf)
	defer srv.Close()
	old := cloudflareAPI
	cloudflareAPI = srv.URL
	defer func() { cloudflareAPI = old }()

	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
	st := testState()
	st.Cloudflare.Token = "tokA"
	st.Entries = []Entry{{ID: "worker", Kind: "worker-ws", Port: 30000, Path: "/w", SNI: "main.sub-acc1.workers.dev"},
		{ID: "worker-dom", Kind: "worker-ws", Port: 30001, Path: "/d", SNI: "x.example.com"}}
	st.Worker = &Worker{Account: "acc1", Name: "main", Host: "main.sub-acc1.workers.dev", Domain: "x.example.com",
		Routes: map[string]string{"/w": "203.0.113.7:30000", "/d": "203.0.113.7:30001"}}
	saveState(r, st)
	if _, _, err := addExtraWorker(r, "tokA", func(string) {}); err == nil {
		t.Error("a second Worker in the same account adds no requests")
	}
	st, x, err := addExtraWorker(r, "tokB", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if x.Account != "acc2" || !strings.HasSuffix(x.Host, ".sub-acc2.workers.dev") || cf.scripts["acc2/"+x.Name]["/w"] != "203.0.113.7:30000" {
		t.Fatalf("extra Worker %+v, scripts %v", x, cf.scripts)
	}
	// clients get a copy of the workers.dev config through it, not of the custom-domain one
	u := NewUser("bob")
	links := st.NamedLinks(u)
	var names []string
	for _, l := range links {
		names = append(names, l.Name)
	}
	if strings.Join(names, ",") != "worker,worker-2,worker-dom" || !strings.Contains(links[1].URL, "sni="+x.Host) {
		t.Errorf("links %v", names)
	}
	if obs := st.FailoverConfig(u)["outbounds"].([]J); obs[1]["tag"] != "proxy-worker-2" {
		t.Errorf("failover outbounds: %v", obs[1]["tag"])
	}
	// moving the inbound moves both Workers
	st.Entries[0].Port = 30500
	if err := syncWorker(st); err != nil {
		t.Fatal(err)
	}
	if cf.scripts["acc2/"+x.Name]["/w"] != "203.0.113.7:30500" || cf.scripts["acc1/main"]["/w"] != "203.0.113.7:30500" {
		t.Errorf("routes after a move: %v", cf.scripts)
	}
	saveState(r, st)
	if err := removeExtraWorker(r, x.Host); err != nil || !slices.Contains(cf.deleted, "acc2/"+x.Name) {
		t.Errorf("remove: %v %v", err, cf.deleted)
	}
	if got, _ := LoadState(r); len(got.Worker.Extra) != 0 {
		t.Error("the extra Worker is still listed")
	}
}

func TestWorkerUsageAlerts(t *testing.T) {
	counts := map[string]int64{"main": 85000, "spare": 1000}
	var since string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Variables map[string]string `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		since = q.Variables["since"]
		if r.Header.Get("Authorization") == "Bearer noanalytics" {
			w.Write([]byte(`{"data":null,"errors":[{"message":"not authorized"}]}`))
			return
		}
		fmt.Fprintf(w, `{"data":{"viewer":{"accounts":[{"workersInvocationsAdaptive":[{"sum":{"requests":%d}}]}]}},"errors":null}`, counts[q.Variables["s"]])
	}))
	defer srv.Close()
	old := cfGraphQLAPI
	cfGraphQLAPI = srv.URL
	defer func() { cfGraphQLAPI = old }()
	var sent []string
	oldSend := sendAlert
	sendAlert = func(n Notify, server, event, text string) error { sent = append(sent, event); return nil }
	defer func() { sendAlert = oldSend }()

	st := testState()
	st.Notify = Notify{Webhook: "https://hook"}
	st.Cloudflare.Token = "tok"
	st.Worker = &Worker{Account: "a", Name: "main", Host: "main.workers.dev",
		Extra: []*WorkerExtra{{Token: "tok2", Account: "b", Name: "spare", Host: "spare.workers.dev"}}}
	us := &Usage{}
	now := time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)
	if !workerUsageDue(st, us, now) {
		t.Fatal("the first count is due at once")
	}
	if err := checkWorkerUsage(st, us, now); err != nil {
		t.Fatal(err)
	}
	if since != "2026-10-27T00:00:00Z" {
		t.Errorf("counted since %s, not UTC midnight", since)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "main.workers.dev") || !strings.HasSuffix(sent[0], "/80") {
		t.Errorf("alerts %v", sent)
	}
	if len(us.WorkerUse) != 2 || us.WorkerUse[0].Requests != 85000 {
		t.Errorf("use %+v", us.WorkerUse)
	}
	if workerUsageDue(st, us, now.Add(30*time.Minute)) || !workerUsageDue(st, us, now.Add(time.Hour)) {
		t.Error("counted hourly")
	}
	counts["main"] = 100000
	checkWorkerUsage(st, us, now.Add(time.Hour))
	if len(sent) != 2 || !strings.HasSuffix(sent[1], "/100") {
		t.Errorf("alerts at the limit %v", sent)
	}
	checkWorkerUsage(st, us, now.Add(2*time.Hour))
	if len(sent) != 2 {
		t.Errorf("an alert repeated: %v", sent)
	}
	st.Worker.Limit = -1
	if workerUsageDue(st, us, now.Add(5*time.Hour)) {
		t.Error("limit none turns the count off")
	}
	st.Worker.Limit = 0
	st.Worker.Extra[0].Token = "noanalytics"
	if err := checkWorkerUsage(st, us, now.Add(6*time.Hour)); err == nil || !strings.Contains(err.Error(), "Analytics") {
		t.Errorf("a token without analytics should say so: %v", err)
	}
	if fmtCount(1234567) != "1,234,567" || fmtCount(12) != "12" {
		t.Error("fmtCount")
	}
}

// ---------------------------------------------------------------- autopilot

func blockedSetup(now time.Time) (*State, *Usage) {
	st := testState()
	st.CDNDomain = "cdn.example.com"
	st.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443}, {ID: "hy2", Kind: "hysteria2", Port: 8443},
		{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2053}}
	old := now.Add(-5 * time.Hour).Unix()
	us := &Usage{Users: map[string]*UserUsage{}, Entries: map[string]*EntryUsage{
		"r-tcp/443": {First: old, Active: now.Add(-3 * time.Hour).Unix()}, "hy2/8443": {First: old},
		"cdn-xhttp/2053": {First: old, Active: now.Add(-time.Minute).Unix()}}}
	return st, us
}

func TestIPBlocked(t *testing.T) {
	now := time.Now()
	st, us := blockedSetup(now)
	if b, why := ipBlocked(st, us, now); !b || !strings.Contains(why, "2 hours") {
		t.Errorf("direct configs quiet while the CDN works: %v %q", b, why)
	}
	us.Entries["hy2/8443"].Active = now.Unix()
	if b, _ := ipBlocked(st, us, now); b {
		t.Error("a direct config still in use: not blocked")
	}
	us.Entries["hy2/8443"].Active = 0
	us.Entries["cdn-xhttp/2053"].Active = now.Add(-time.Hour).Unix()
	if b, _ := ipBlocked(st, us, now); b {
		t.Error("nothing in use at all is an idle server, not a blocked one")
	}
	// a recent check from some network
	st.Networks = map[string]*NetRank{"AS1": {Name: "TCI", Updated: now.Add(-time.Hour).Unix(),
		Scores: map[string]float64{"r-tcp/443": 0, "hy2/8443": 0, "cdn-xhttp/2053": 12}}}
	if b, why := ipBlocked(st, us, now); !b || !strings.Contains(why, "TCI") {
		t.Errorf("check signal: %v %q", b, why)
	}
	st.Networks["AS1"].Scores["hy2/8443"] = 3
	if b, _ := ipBlocked(st, us, now); b {
		t.Error("one direct config works from there: not blocked")
	}
	st.Networks["AS1"].Scores["hy2/8443"] = 0
	us.RotatedAt = now.Unix()
	if b, _ := ipBlocked(st, us, now); b {
		t.Error("a check from before the last move says nothing about the new IP")
	}
	st.Entries = st.Entries[:2]
	if b, _ := ipBlocked(st, &Usage{}, now); b {
		t.Error("without a relayed config there is nothing to compare with")
	}
}

func TestAutopilotRotates(t *testing.T) {
	var calls []string
	assigned := map[string]int64{"198.51.100.50": 77}
	next := "198.51.100.60"
	do := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer dotok" {
			w.WriteHeader(401)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/reserved_ips")
		switch {
		case r.Method == "POST" && p == "":
			assigned[next] = 77
			json.NewEncoder(w).Encode(J{"reserved_ip": J{"ip": next}})
		case r.Method == "POST" && strings.HasSuffix(p, "/actions"):
			delete(assigned, strings.Split(p, "/")[1])
		case r.Method == "GET":
			ip := strings.TrimPrefix(p, "/")
			var d any
			if id, ok := assigned[ip]; ok {
				d = J{"id": id}
			}
			json.NewEncoder(w).Encode(J{"reserved_ip": J{"ip": ip, "droplet": d}})
		case r.Method == "DELETE":
		}
	}))
	defer do.Close()
	oldAPI, oldWait := doAPI, doWait
	doAPI, doWait = do.URL, time.Millisecond
	defer func() { doAPI, doWait = oldAPI, oldWait }()
	var sent []string
	oldSend := sendAlert
	sendAlert = func(n Notify, server, event, text string) error { sent = append(sent, event+": "+text); return nil }
	defer func() { sendAlert = oldSend }()

	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
	now := time.Now()
	st, us := blockedSetup(now)
	st.Notify = Notify{Webhook: "https://hook"}
	st.Sub = SubConfig{Mode: "cdn", Port: 2096, Path: "p"}
	st.Autopilot = &Autopilot{Provider: "digitalocean", Token: "dotok", Droplet: 77, Owned: "198.51.100.50"}
	// the traffic signal alone alerts and asks for a check, but does not move the server
	runAutopilot(r, st, us, now)
	if st.ClientIP != "" || len(calls) != 0 || len(sent) != 1 || !strings.Contains(sent[0], "xtune check --host 203.0.113.7") {
		t.Fatalf("moved on traffic alone, or no alert asking for a check: %q %v %v", st.ClientIP, calls, sent)
	}
	sent = nil
	delete(us.Notified, "ip-blocked")
	// a check from some network confirms it
	st.Networks = map[string]*NetRank{"AS1": {Name: "TCI", Updated: now.Unix(),
		Scores: map[string]float64{"r-tcp/443": 0, "hy2/8443": 0, "cdn-xhttp/2053": 9}}}
	runAutopilot(r, st, us, now)
	if st.ClientIP != next || st.Addr() != next || st.Autopilot.Owned != next || us.RotatedAt != now.Unix() {
		t.Fatalf("not moved: %+v %+v", st.Autopilot, st.ClientIP)
	}
	want := []string{"POST /reserved_ips/198.51.100.50/actions", "POST /reserved_ips", "DELETE /reserved_ips/198.51.100.50"}
	for _, w := range want {
		if !slices.Contains(calls, w) {
			t.Errorf("no %s in %v", w, calls)
		}
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "moved to "+next) {
		t.Errorf("alerts %v", sent)
	}
	if b, _ := ipBlocked(st, us, now.Add(time.Minute)); b {
		t.Error("right after a move the configs start over")
	}
	if got, _ := LoadState(r); got == nil || got.ClientIP != next {
		t.Error("the new address was not saved")
	}
	link := st.Link(st.Entries[0], NewUser("bob"), Knobs{})
	if !strings.Contains(link, "@"+next+":443") {
		t.Errorf("links should use the new address: %s", link)
	}
	// blocked again the same day: alerts, does not move
	st2, us2 := blockedSetup(now)
	st2.Notify, st2.Autopilot = st.Notify, st.Autopilot
	st2.Networks = map[string]*NetRank{"AS1": {Updated: now.Unix(), Scores: map[string]float64{"r-tcp/443": 0, "cdn-xhttp/2053": 9}}}
	us2.RotatedAt = now.Add(-time.Hour).Unix()
	sent = nil
	runAutopilot(r, st2, us2, now)
	if st2.ClientIP != "" || len(sent) != 1 || !strings.Contains(sent[0], "xtune migrate --host 203.0.113.7") {
		t.Errorf("within a day of a move: %q %v", st2.ClientIP, sent)
	}
	// a migrated backup does not take the reserved IP along
	b := &Backup{State: st, Usage: &Usage{}}
	nfs := newFake()
	nfs.helper = map[string]string{"status": "ARCH x86_64\nXRAY v26.10.10\nXTUNE dev"}
	nr := &Server{sh: nfs, Conn: Conn{Host: "192.0.2.10"}}
	seeded, err := seed(nr, b, "", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if seeded.ClientIP != "" || seeded.Autopilot.Droplet != 0 || seeded.Autopilot.Owned != "" || seeded.Addr() != "192.0.2.10" {
		t.Errorf("seeded %+v %+v", seeded.ClientIP, seeded.Autopilot)
	}
}

// ---------------------------------------------------------------- new transports

func TestTrojanReality(t *testing.T) {
	st := testState()
	u := User{Name: "bob", UUID: "11111111-2222-4333-8444-555555555555"}
	e := Entry{ID: "trojan", Kind: "trojan-reality", Port: 8443, SNI: "www.a.com"}
	if !e.Reality() || e.Net() != "tcp" || !e.Xray() || e.family() != "trojan" {
		t.Errorf("kind: reality=%v net=%q", e.Reality(), e.Net())
	}
	ib := st.inbound(e, []User{u})
	ss := ib["streamSettings"].(J)
	if ib["protocol"] != "trojan" || ss["network"] != "tcp" || ss["security"] != "reality" ||
		ib["settings"].(J)["clients"].([]J)[0]["password"] != u.UUID {
		t.Errorf("inbound %v", ib)
	}
	ob := st.Outbound(e, u, Knobs{FP: "firefox", Frag: "frag-mild"}, "p")
	srv := ob["settings"].(J)["servers"].([]J)[0]
	oss := ob["streamSettings"].(J)
	if ob["protocol"] != "trojan" || srv["password"] != u.UUID || srv["port"] != 8443 ||
		oss["realitySettings"].(J)["fingerprint"] != "firefox" || oss["sockopt"].(J)["dialerProxy"] != "frag-mild" {
		t.Errorf("outbound %v", ob)
	}
	l := st.Link(e, u, Knobs{FP: "chrome"})
	pu, _ := url.Parse(l)
	q := pu.Query()
	if pu.Scheme != "trojan" || pu.User.Username() != u.UUID || q.Get("security") != "reality" || q.Get("pbk") != "pub" ||
		q.Get("sni") != "www.a.com" || q.Get("type") != "tcp" || q.Has("encryption") || q.Has("flow") {
		t.Errorf("link %s", l)
	}
	es, _ := planEntries("SNI www.a.com 2 0.20", PlanOpts{Protocols: []string{"trojan"}})
	if !slices.ContainsFunc(es, Entry.Trojan) {
		t.Error("--protocols trojan adds no Trojan entry")
	}
	if es, _ := planEntries("SNI www.a.com 2 0.20", PlanOpts{}); slices.ContainsFunc(es, Entry.Trojan) {
		t.Error("Trojan is opt-in")
	}
}

func TestTrojanUsersStayLive(t *testing.T) {
	r, fs := deployed(t, NewUser("ann"))
	st, _ := LoadState(r)
	st.Entries = append(st.Entries, Entry{ID: "trojan", Kind: "trojan-reality", Port: 8443, SNI: "a.com"})
	if err := Apply(r, st); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddUser(r, "bob", Limits{}); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadState(r)
	_, bob := st.User("bob")
	if fs.live["trojan"]["bob"] != bob.UUID {
		t.Fatalf("bob not on the Trojan inbound: %v", fs.live["trojan"])
	}
	before := fs.rmus
	if err := reconcile(r, st, st.activeUsers(&Usage{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	if fs.rmus != before {
		t.Error("Trojan users were removed and re-added although nothing changed")
	}
}

func TestCDNOverHTTP3(t *testing.T) {
	st := testState()
	st.CDNDomain = "cdn.example.com"
	e := Entry{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: 2053, Path: "/p"}
	var h3 []*Variant
	for _, v := range ExpandVariants([]Entry{e}, "smart") {
		if v.K.ALPN == "h3" {
			h3 = append(h3, v)
		}
	}
	if len(h3) != 2 || !strings.HasSuffix(h3[0].Label, " h3") || h3[0].K.Frag != "none" {
		t.Fatalf("h3 variants %+v", h3)
	}
	u := NewUser("bob")
	tls := st.Outbound(e, u, h3[0].K, "p")["streamSettings"].(J)["tlsSettings"].(J)
	if a := tls["alpn"].([]string); len(a) != 1 || a[0] != "h3" || tls["fingerprint"] != nil || tls["serverName"] != "cdn.example.com" {
		t.Errorf("h3 tls %v", tls)
	}
	q := mustQuery(t, st.Link(e, u, h3[0].K))
	if q.Get("alpn") != "h3" || q.Has("fp") || q.Get("sni") != "cdn.example.com" {
		t.Errorf("h3 link %v", q)
	}
	if q := mustQuery(t, st.Link(e, u, Knobs{FP: "chrome"})); q.Get("alpn") != "h2,http/1.1" {
		t.Errorf("TCP link lost its ALPN: %v", q)
	}
	// the server side does not change: the CDN reaches the origin over TCP
	ib := st.inbound(e, []User{u})
	if a := ib["streamSettings"].(J)["tlsSettings"].(J)["alpn"].([]string); a[0] != "h2" {
		t.Errorf("origin ALPN %v", a)
	}
	v := &Variant{Entry: e, K: h3[0].K}
	if simplicity(v) <= simplicity(&Variant{Entry: e, K: Knobs{FP: "chrome", Frag: "none", Mode: "auto"}}) {
		t.Error("HTTP/3 should win only when it measurably helps")
	}
	if (Entry{ALPN: "h3"}).Knobs().ALPN != "h3" {
		t.Error("Entry.Knobs drops ALPN")
	}
}

// ---------------------------------------------------------------- before merge

func TestRankingUpdatesAnOldServer(t *testing.T) {
	oldVersion := version
	version = "0.9.0"
	defer func() { version = oldVersion }()
	installed := func(fs *fakeServer) bool {
		return slices.ContainsFunc(fs.calls, func(c string) bool { return strings.HasPrefix(c, "xtune-") })
	}
	for _, tc := range []struct {
		server string
		update bool
	}{{"0.8.0", true}, {"none", true}, {"0.9.0", false}, {"0.10.1", false}} {
		fs := newFake()
		fs.helper = map[string]string{"status": "ARCH x86_64\nXTUNE " + tc.server + "\nXTUNE_SHA 00"}
		r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
		saveState(r, rankedState())
		err := RecordNetwork(r, Network{ASN: "AS1"}, map[string]float64{"r-tcp/443": 3}, nil)
		if got := installed(fs); got != tc.update {
			t.Errorf("server xtune %s: updated=%v (err %v, calls %v)", tc.server, got, err, fs.calls)
		}
	}
}

func TestManageStatusReportsJobs(t *testing.T) {
	r, _ := deployed(t, NewUser("ann"))
	now := time.Now()
	st, us := blockedSetup(now)
	st.Backups = &BackupPlan{Every: 86400}
	us.BackupAt, us.LastBackup, us.BackupErr = now.Unix(), now.Add(-time.Hour).Unix(), "s3: HTTP 403"
	st.Worker = &Worker{Host: "w.workers.dev"}
	us.WorkerUse = []WorkerUse{{Host: "w.workers.dev", Requests: 42}}
	s := manageStatus(r, st, us, &manager{}, now)
	if s.IPBlocked == "" || s.Backup == nil || s.Backup.Error != "s3: HTTP 403" || len(s.Workers) != 1 || s.WorkerMax != workerFreeLimit {
		t.Errorf("admin status %+v %+v", s, s.Backup)
	}
	rs := manageStatus(r, st, us, &manager{reseller: "rex", res: &Reseller{}}, now)
	if rs.IPBlocked != "" || rs.Backup != nil || rs.Workers != nil {
		t.Error("resellers get users, not the server's jobs")
	}
}
