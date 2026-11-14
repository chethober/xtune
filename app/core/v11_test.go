package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/netinfo"
	"github.com/chethober/xtune/pkg/protocols"
	xcore "github.com/xtls/xray-core/core"
)

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestParseLinks(t *testing.T) {
	vmess := base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"vm ws","add":"vm.example.com","port":"443","id":"` + testUUID +
		`","aid":"0","net":"ws","type":"none","host":"cdn.example.com","path":"/ray","tls":"tls","sni":"cdn.example.com"}`))
	cases := []struct {
		link, kind, label string
		want              []string // fragments of the outbound's JSON
	}{
		{"vless://" + testUUID + "@1.2.3.4:443?type=tcp&security=reality&sni=www.microsoft.com&fp=chrome&pbk=PUBKEY&sid=ab12&flow=xtls-rprx-vision#R%20TCP",
			"vless", "REALITY", []string{`"flow":"xtls-rprx-vision"`, `"publicKey":"PUBKEY"`, `"shortId":"ab12"`, `"serverName":"www.microsoft.com"`}},
		{"vless://" + testUUID + "@cdn.example.com:2053?type=xhttp&security=tls&sni=cdn.example.com&path=%2Fx&mode=packet-up&host=cdn.example.com#CDN",
			"vless", "VLESS · XHTTP", []string{`"network":"xhttp"`, `"mode":"packet-up"`, `"path":"/x"`, `"security":"tls"`}},
		{"vless://" + testUUID + "@h.example.com:443?type=grpc&security=tls&serviceName=svc#G", "vless", "VLESS · GRPC", []string{`"serviceName":"svc"`}},
		{"trojan://secret@t.example.com:443?type=ws&path=%2Fws&sni=t.example.com#T", "trojan", "Trojan · WS",
			[]string{`"protocol":"trojan"`, `"password":"secret"`, `"wsSettings"`, `"security":"tls"`}},
		{"vmess://" + vmess, "vmess", "VMess · WS", []string{`"protocol":"vmess"`, `"path":"/ray"`, `"host":"cdn.example.com"`, `"security":"tls"`}},
		{"ss://" + base64.RawURLEncoding.EncodeToString([]byte("2022-blake3-aes-128-gcm:a:b")) + "@5.6.7.8:8388#SS",
			"shadowsocks", "Shadowsocks", []string{`"method":"2022-blake3-aes-128-gcm"`, `"password":"a:b"`, `"port":8388`}},
		{"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:pw@5.6.7.8:443")) + "#old", "shadowsocks", "Shadowsocks", []string{`"method":"aes-256-gcm"`}},
		{"hysteria2://pass@9.9.9.9:443?sni=h.example.com&mport=20000-30000#H", "hysteria2", "Hysteria2",
			[]string{`"protocol":"hysteria"`, `"auth":"pass"`, `"remotePorts":"20000-30000"`}},
		{"wireguard://cHJpdg%3D%3D@7.7.7.7:51820?publickey=cHVi&address=10.0.0.2%2F32&mtu=1380#W", "wireguard", "WireGuard",
			[]string{`"secretKey":"cHJpdg=="`, `"publicKey":"cHVi"`, `"mtu":1380`}},
	}
	for _, c := range cases {
		e, err := ParseLink(c.link)
		if err != nil {
			t.Errorf("%s: %v", c.link[:12], err)
			continue
		}
		s := jsonOf(e.Outbound)
		if e.Kind != c.kind || e.Label != c.label || e.Engine != "xray" {
			t.Errorf("%s: kind %s label %q", c.link[:12], e.Kind, e.Label)
		}
		for _, w := range c.want {
			if !strings.Contains(s, w) {
				t.Errorf("%s: outbound lacks %s: %s", c.label, w, s)
			}
		}
	}
	if _, err := ParseLink("hysteria2://p@h:443?obfs=salamander"); err == nil {
		t.Error("obfuscated Hysteria2 should be refused")
	}
	if _, err := ParseLink("tuic://x@h:443"); err == nil {
		t.Error("unsupported schemes should be refused")
	}

	// a base64 subscription body, with junk and duplicate names
	body := base64.StdEncoding.EncodeToString([]byte(cases[0].link + "\nnot a link\n" + cases[0].link + "\n" + cases[3].link))
	cs, skipped := ParseLinks(body)
	if len(cs) != 3 || skipped != 1 || cs[0].ID == cs[1].ID {
		t.Errorf("subscription body: %d configs, %d skipped, ids %v", len(cs), skipped, []string{cs[0].ID, cs[1].ID})
	}

	conf := "[Interface]\nPrivateKey = cHJpdg==\nAddress = 10.8.0.2/32\nMTU = 1420\n\n[Peer]\nPublicKey = cHVi\nEndpoint = 8.8.4.4:51820\nAllowedIPs = 0.0.0.0/0\n"
	cs, _ = ParseLinks(conf)
	if len(cs) != 1 || !strings.Contains(jsonOf(cs[0].Outbound), `"endpoint":"8.8.4.4:51820"`) {
		t.Errorf("wireguard conf: %v", cs)
	}
}

func TestImportGenericSubscription(t *testing.T) {
	link := "vless://" + testUUID + "@1.2.3.4:443?type=tcp&security=tls&sni=a.com#One"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=100; expire=0")
		w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte("Other Panel")))
		io.WriteString(w, base64.StdEncoding.EncodeToString([]byte(link)))
	}))
	defer srv.Close()
	im, err := Import(srv.URL+"/api/sub/abc?flag=1", "")
	if err != nil {
		t.Fatal(err)
	}
	if im.URL == "" || im.Profile.Name != "Other Panel" || len(im.Profile.Configs) != 1 || im.Profile.Usage.Quota != 100 {
		t.Errorf("generic subscription: %+v", im.Profile)
	}
	// pasted links: no URL to refresh from
	im, err = Import(link+"\n"+link, "")
	if err != nil || im.URL != "" || len(im.Profile.Configs) != 2 {
		t.Errorf("pasted links: %+v %v", im, err)
	}
	if _, err := Import("hello", ""); err == nil {
		t.Error("text without links should be refused")
	}
}

func TestBuildConfigOptions(t *testing.T) {
	vless := J{"protocol": "vless", "settings": J{"vnext": []J{{"address": "1.2.3.4", "port": 443, "users": []J{{"id": testUUID}}}}},
		"streamSettings": J{"network": "tcp", "security": "tls", "tlsSettings": J{"serverName": "a.com", "fingerprint": "chrome"}}}
	vision := clone(vless)
	vision["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)["flow"] = "xtls-rprx-vision"
	wg := J{"protocol": "wireguard", "settings": J{"secretKey": "k", "mtu": 1280}}
	p := &Profile{Country: "ir", Configs: []protocols.ClientEntry{
		{ID: "a", Engine: "xray", Outbound: vless}, {ID: "v", Engine: "xray", Outbound: vision}, {ID: "w", Engine: "xray", Outbound: wg},
		{ID: "x", Engine: "xray", Outbound: clone(vless)},
	}}
	o := Options{Mode: "tun", Domestic: true, Pool: []string{"v", "a", "w", "gone"},
		Tweaks:       Tweaks{Fingerprint: "firefox", Mux: "on", MTU: 1380},
		ConfigTweaks: map[string]Tweaks{"a": {Fragment: "on", FragLength: "50-80"}},
		Rules:        []Rule{{Match: "domain", Value: "bank.example", Action: "direct"}, {Match: "geoip", Value: "ru", Action: "block"}, {Match: "port", Value: "25", Action: "block"}},
		BlockQUIC:    true, BypassPush: true, BypassDomains: []string{"*.local.example"},
		DNS: "tcp://8.8.8.8", DomesticDNS: "udp://217.218.155.155", FakeDNS: true}
	cs, err := chosen(p, o)
	if err != nil || len(cs) != 3 || cs[0].ID != "v" || cs[1].ID != "a" || cs[2].ID != "w" {
		t.Fatalf("pool order: %v %v", cs, err)
	}
	cfg := BuildConfig(p, o, cs, nil, nil)
	s := jsonOf(cfg)
	for _, want := range []string{`"fallbackTag":"proxy-v"`, `"fingerprint":"firefox"`, `"dialerProxy":"frag-tweak-50-80-10-20"`, `"length":"50-80"`,
		`"mtu":1380`, `"domain":["domain:bank.example"],"outboundTag":"direct"`, `"ip":["geoip:ru"],"outboundTag":"block"`, `"port":"25"`,
		`"network":"udp","outboundTag":"block","port":"443"`, `"domain:push.apple.com"`, `"domain:local.example"`, `"::/0"`,
		`"fakedns"`, `"address":"217.218.155.155","domains":["geosite:category-ir"]`, `"tcp://8.8.8.8"`, `"queryStrategy":"UseIPv4"`} {
		if !strings.Contains(s, want) {
			t.Errorf("config lacks %s", want)
		}
	}
	var outs []map[string]any
	json.Unmarshal([]byte(jsonOf(cfg["outbounds"])), &outs)
	for _, ob := range outs {
		switch ob["tag"] {
		case "proxy-a":
			if ob["mux"] == nil {
				t.Error("mux should be on for plain VLESS over TLS")
			}
		case "proxy-v":
			if ob["mux"] != nil {
				t.Error("mux must stay off with Vision")
			}
			if strings.Contains(jsonOf(ob), "dialerProxy") {
				t.Error("fragmentation was set for config a only")
			}
		}
	}
	// user rules come before the defaults: the bank rule precedes the domestic ones
	rs := jsonOf(cfg["routing"])
	if strings.Index(rs, "bank.example") > strings.Index(rs, "geosite:category-ir") {
		t.Error("user rules should come first")
	}
	o.IPv6, o.ProxyLAN = true, true
	s = jsonOf(BuildConfig(p, o, cs, nil, nil))
	if !strings.Contains(s, `"queryStrategy":"UseIP"`) || strings.Contains(s, `"::/0"`) || strings.Contains(s, "geoip:private") {
		t.Error("IPv6 on and LAN through the proxy")
	}
	if _, err := chosen(p, Options{Pool: []string{"gone"}}); err == nil {
		t.Error("an empty pool should fail clearly")
	}
}

func TestRedact(t *testing.T) {
	in := `vless://` + testUUID + `@1.2.3.4:443#x "password": "hunter2" https://h.example.com:2096/p4th/0123456789abcdef0123 user@example.com 5.200.14.7 127.0.0.1 ` +
		"-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"
	out := Redact(in)
	for _, leak := range []string{testUUID, "hunter2", "0123456789abcdef0123", "user@example.com", "200.14.7", "abc\n"} {
		if strings.Contains(out, leak) {
			t.Errorf("redacted text still has %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "5.200.x.x") || !strings.Contains(out, "127.0.0.1") {
		t.Errorf("networks should stay readable: %s", out)
	}
}

// TestSmartReconnect moves the device to another network and expects the profile for that
// network, and a reconnect with it.
func TestSmartReconnect(t *testing.T) {
	geoDir(t)
	site := testSite(t)
	srv := testServer(t, strings.TrimPrefix(site.URL, "http://"))
	var asked atomic.Value
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asn := r.URL.Query().Get("asn")
		asked.Store(asn)
		json.NewEncoder(w).Encode(Profile{V: 1, Network: "net " + asn, Configs: []protocols.ClientEntry{
			{ID: "on-" + asn, Key: "k", Engine: "xray", Outbound: vlessOutbound(srv)}}})
	}))
	defer sub.Close()
	network := atomic.Value{}
	network.Store("AS100")
	old := netinfo.Client
	netinfo.Client = func() (netinfo.Network, error) { return netinfo.Network{ASN: network.Load().(string)}, nil }
	defer func() { netinfo.Client = old }()
	oldI := smartInterval.Load()
	smartInterval.Store(int64(100 * time.Millisecond))
	defer smartInterval.Store(oldI)

	subURL := sub.URL + "/p/tok"
	p, err := FetchProfile(subURL, "100")
	if err != nil {
		t.Fatal(err)
	}
	sp, hp := mustPort(t), mustPort(t)
	if err := Start(p, Options{Mode: "proxy", SocksPort: sp, HTTPPort: hp, SmartURL: subURL, ASN: "AS100"}); err != nil {
		t.Fatal(err)
	}
	defer Disconnect()
	time.Sleep(400 * time.Millisecond)
	if st := CurrentStatus(); st.ProfileRev != 0 || st.Active != "on-100" {
		t.Fatalf("no network change yet: %+v", st)
	}
	network.Store("AS200")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && CurrentStatus().Active != "on-200" {
		time.Sleep(100 * time.Millisecond)
	}
	st := CurrentStatus()
	cp, rev := CurrentProfile()
	if st.Active != "on-200" || rev == 0 || cp.Network != "net 200" || asked.Load() != "200" || st.ASN != "200" {
		t.Fatalf("after moving networks: %+v rev %d", st, rev)
	}
	// still carries traffic
	pu, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", sp))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	resp, err := c.Get("http://probe.test/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	Disconnect()
	if CurrentStatus().Running {
		t.Error("Disconnect should stop it")
	}
}

func TestImportedLinkCarriesTraffic(t *testing.T) {
	geoDir(t)
	site := testSite(t)
	srv := testServer(t, strings.TrimPrefix(site.URL, "http://"))
	im, err := Import(fmt.Sprintf("vless://%s@127.0.0.1:%d?type=tcp&security=none#Local", testUUID, srv), "")
	if err != nil {
		t.Fatal(err)
	}
	sp, hp := mustPort(t), mustPort(t)
	if err := Start(im.Profile, Options{Mode: "proxy", SocksPort: sp, HTTPPort: hp}); err != nil {
		t.Fatal(err)
	}
	defer Disconnect()
	pu, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", sp))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	resp, err := c.Get("http://probe.test/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "through the proxy" {
		t.Errorf("got %q", b)
	}
}

func mustPort(t *testing.T) int {
	p, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestOptionsConfigLoads feeds a config with every option on to Xray's own loader.
func TestOptionsConfigLoads(t *testing.T) {
	geoDir(t)
	link := "vless://" + testUUID + "@1.2.3.4:443?type=tcp&security=tls&sni=a.com#A"
	p, err := Import(link+"\n"+strings.Replace(link, "#A", "#B", 1), "")
	if err != nil {
		t.Fatal(err)
	}
	p.Profile.Country = "ir"
	o := Options{Mode: "tun", Domestic: true, BlockAds: true, FakeDNS: true, IPv6: true, BlockQUIC: true, BypassPush: true,
		BypassDomains: []string{"x.example"}, DomesticDNS: "udp://1.1.1.1", DNS: "https://dns.google/dns-query",
		Tweaks: Tweaks{Fragment: "on", Fingerprint: "safari", Mux: "on"},
		Rules:  []Rule{{Match: "keyword", Value: "bank", Action: "direct"}, {Match: "ip", Value: "10.0.0.0/8", Action: "proxy"}}}
	cs, err := chosen(p.Profile, o)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(BuildConfig(p.Profile, o, cs, nil, map[string][]string{"a.com": {"1.2.3.4"}}))
	if _, err := xcore.LoadConfig("json", bytes.NewReader(b)); err != nil {
		t.Fatalf("Xray refuses the config: %v\n%s", err, b)
	}
}
