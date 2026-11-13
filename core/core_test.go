package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/tunnel"

	"github.com/chethober/xtune/pkg/protocols"
	xcore "github.com/xtls/xray-core/core"
)

const testUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"

// geoDir finds the geo files (CI downloads them; see .github/workflows).
func geoDir(t *testing.T) {
	t.Helper()
	d, _ := filepath.Abs("testdata/geo")
	if _, err := os.Stat(filepath.Join(d, "geoip.dat")); err != nil {
		t.Skip("testdata/geo/geoip.dat missing: run scripts/fetch-geo.sh")
	}
	os.Setenv("xray.location.asset", d)
}

// testServer is a VLESS server whose every connection lands on target, whatever the client asked
// for, so a request for http://probe.test/ proves it went through the proxy.
func testServer(t *testing.T, target string) int {
	t.Helper()
	port, _ := freePort()
	cfg := J{"log": J{"loglevel": "none"},
		"inbounds": []J{{"listen": "127.0.0.1", "port": port, "protocol": "vless",
			"settings": J{"clients": []J{{"id": testUUID}}, "decryption": "none"}}},
		"outbounds": []J{{"protocol": "freedom", "settings": J{"redirect": target}}}}
	b, _ := json.Marshal(cfg)
	xc, err := xcore.LoadConfig("json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := xcore.New(xc)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close() })
	return port
}

func vlessOutbound(port int) J {
	return J{"protocol": "vless", "settings": J{"vnext": []J{{"address": "127.0.0.1", "port": port,
		"users": []J{{"id": testUUID, "encryption": "none"}}}}}, "streamSettings": J{"network": "tcp"}}
}

// testSite answers /204 with no content and /big with a megabyte at a time.
func testSite(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/204":
			w.WriteHeader(204)
		case "/big":
			chunk := bytes.Repeat([]byte("x"), 1<<20)
			for i := 0; i < 20; i++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		default:
			io.WriteString(w, "through the proxy")
		}
	}))
	t.Cleanup(s.Close)
	return s
}

type subServer struct {
	*httptest.Server
	mu      sync.Mutex
	reports []string
	asn     string
}

func testSub(t *testing.T, p Profile) *subServer {
	s := &subServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/p/tok/client" && r.Method == "GET":
			s.mu.Lock()
			s.asn = r.URL.Query().Get("asn")
			s.mu.Unlock()
			json.NewEncoder(w).Encode(p)
		case r.URL.Path == "/p/tok/report" && r.Method == "POST":
			b, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.reports = append(s.reports, string(b))
			s.mu.Unlock()
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestNormalizeSubURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://h.example:2096/p/tok":                 "https://h.example:2096/p/tok",
		" https://h.example/p/tok/json ":               "https://h.example/p/tok",
		"https://h.example/p/tok/client?asn=1":         "https://h.example/p/tok",
		"xtune://import?url=https%3A%2F%2Fh%2Fp%2Ftok": "https://h/p/tok",
	} {
		if got, err := NormalizeSubURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"vless://x@y:1", "https://h.example/", "https://h.example/a/b/c", "ftp://h/p/t"} {
		if _, err := NormalizeSubURL(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestBuildConfigAuto(t *testing.T) {
	p := &Profile{Country: "ir", Probe: "https://p/204", Frag: []J{{"tag": "frag-1", "protocol": "freedom"}}, Configs: []protocols.ClientEntry{
		{ID: "a", Engine: "xray", Outbound: J{"protocol": "vless", "tag": "x"}},
		{ID: "s", Engine: "ssh", SSH: &tunnelSSH},
		{ID: "d", Engine: "ssh", SSH: &tunnelDNSTT},
		{ID: "o", Engine: "handoff"},
	}}
	o := Options{Mode: "tun", Domestic: true, BlockAds: true}
	cs, err := chosen(p, o)
	if err != nil || len(cs) != 2 {
		t.Fatalf("auto should use a and s (not the DNS tunnel, not handoff): %v %v", cs, err)
	}
	cfg := BuildConfig(p, o, cs, map[string]int{"s": 4242}, map[string][]string{"srv.example": {"1.2.3.4"}})
	b, _ := json.Marshal(cfg)
	s := string(b)
	for _, want := range []string{`"protocol":"tun"`, `"balancerTag":"auto"`, `"proxy-a"`, `"port":4242`, `"geosite:category-ir"`,
		`"geosite:category-ads-all"`, `"outboundTag":"dns-out"`, `"burstObservatory"`, `"srv.example":["1.2.3.4"]`, `"domainStrategy":"UseIPv4"`, `"frag-1"`} {
		if !strings.Contains(s, want) {
			t.Errorf("config lacks %s", want)
		}
	}
	if p.Configs[0].Outbound["tag"] != "x" {
		t.Error("building a config must not change the profile")
	}
	o.Selected = "d"
	if cs, _ = chosen(p, o); len(cs) != 1 || cs[0].ID != "d" {
		t.Error("a selected config is used even when Auto would skip it")
	}
	cfg = BuildConfig(p, Options{Selected: "d"}, cs, map[string]int{"d": 1}, nil)
	b, _ = json.Marshal(cfg)
	if strings.Contains(string(b), "balancer") || strings.Contains(string(b), `"protocol":"tun"`) {
		t.Error("one config in proxy mode needs no balancer and no TUN")
	}
	if _, err := chosen(p, Options{Selected: "o"}); err == nil {
		t.Error("handoff configs cannot be started here")
	}
}

func TestEndToEnd(t *testing.T) {
	geoDir(t)
	site := testSite(t)
	srv := testServer(t, strings.TrimPrefix(site.URL, "http://"))
	dead, _ := freePort()
	sub := testSub(t, Profile{V: 1, Name: "test", User: "ann", Probe: "http://probe.test/204", Configs: []protocols.ClientEntry{
		{ID: "dead", Key: "dead/1", Name: "dead", Engine: "xray", Outbound: vlessOutbound(dead)},
		{ID: "live", Key: "live/2", Name: "live", Engine: "xray", Outbound: vlessOutbound(srv)},
	}})
	p, err := FetchProfile(sub.URL+"/p/tok/json", "AS12880")
	if err != nil || len(p.Configs) != 2 {
		t.Fatalf("fetch: %v %v", p, err)
	}
	if sub.asn != "12880" {
		t.Errorf("profile asked for asn %q", sub.asn)
	}

	// connect, Auto: the live config must carry the traffic once health checks find it
	socks, _ := freePort()
	hp, _ := freePort()
	if err := Start(p, Options{Mode: "proxy", SocksPort: socks, HTTPPort: hp}); err != nil {
		t.Fatal(err)
	}
	defer Stop()
	pu, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", socks))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 10 * time.Second}
	var body string
	for i := 0; i < 40; i++ {
		resp, err := c.Get("http://probe.test/")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if body = string(b); body == "through the proxy" {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if body != "through the proxy" {
		t.Fatalf("no traffic through the proxy: %q", body)
	}
	st := CurrentStatus()
	if !st.Running || st.Down == 0 || st.Mode != "proxy" {
		t.Errorf("status: %+v", st)
	}

	// the API, as the app uses it
	var reply struct {
		Ok     bool   `json:"ok"`
		Error  string `json:"error"`
		Result Status `json:"result"`
	}
	json.Unmarshal(Call("vpn.status", nil), &reply)
	if !reply.Ok || !reply.Result.Running {
		t.Errorf("vpn.status: %+v", reply)
	}
	json.Unmarshal(Call("nope", nil), &reply)
	if reply.Ok || !strings.Contains(reply.Error, "unknown method") {
		t.Errorf("unknown method: %+v", reply)
	}
	Stop()
	if CurrentStatus().Running {
		t.Error("still running after Stop")
	}

	// the on-device check, and the report
	j := StartProbe(p, ProbeOptions{Probes: 2, Seconds: 2, ProbeURL: "http://probe.test/204", SpeedURL: "http://probe.test/big", TimeoutMs: 3000})
	var v JobView
	for i := 0; i < 100; i++ {
		v, _ = GetJob(j.ID)
		if v.Done {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !v.Done || len(v.Results) != 2 {
		t.Fatalf("check: %+v", v)
	}
	by := map[string]Result{}
	for _, r := range v.Results {
		by[r.ID] = r
	}
	if r := by["dead"]; r.State != "blocked" || r.Ok != 0 {
		t.Errorf("dead config: %+v", r)
	}
	if r := by["live"]; r.State == "blocked" || r.Ok != 2 || r.Down <= 0 || r.Warm < 0 {
		t.Errorf("live config: %+v", r)
	}
	if err := SendReport(sub.URL+"/p/tok", Network{ASN: "AS12880", Name: "TCI"}, v.Results); err != nil {
		t.Fatal(err)
	}
	if len(sub.reports) != 1 || !strings.Contains(sub.reports[0], `"asn":"12880"`) || !strings.Contains(sub.reports[0], `"key":"live/2"`) {
		t.Errorf("report: %v", sub.reports)
	}
}

func TestSocksEngine(t *testing.T) {
	site := testSite(t)
	srv, err := startSocks(dialerFunc(func(network, addr string) (net.Conn, error) {
		return net.Dial(network, strings.TrimPrefix(site.URL, "http://")) // like a tunnel: everything to the site
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	pu, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", srv.Port()))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	resp, err := c.Get("http://anywhere.test/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "through the proxy" {
		t.Errorf("got %q", b)
	}
}

type dialerFunc func(network, addr string) (net.Conn, error)

func (f dialerFunc) DialContext(_ context.Context, network, addr string) (net.Conn, error) {
	return f(network, addr)
}

var (
	tunnelSSH   = tunnel.SSH{Host: "srv.example", Port: 22, User: "ann", Password: "x", HostKey: "k"}
	tunnelDNSTT = tunnel.SSH{User: "ann", Password: "x", HostKey: "k", DNSTT: &tunnel.DNSTT{Resolver: "udp:8.8.8.8:53", Domain: "t.example", PubKey: "00"}}
)
