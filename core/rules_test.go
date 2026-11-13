package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/stats"
)

// A "via" rule sends a site through a chosen config, even one Auto does not run.
func TestViaRule(t *testing.T) {
	geoDir(t)
	siteA := testSite(t)
	siteB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "through B") }))
	t.Cleanup(siteB.Close)
	a := testServer(t, strings.TrimPrefix(siteA.URL, "http://"))
	b := testServer(t, strings.TrimPrefix(siteB.URL, "http://"))
	im, err := Import(fmt.Sprintf("vless://%s@127.0.0.1:%d?type=tcp&security=none#A\nvless://%s@127.0.0.1:%d?type=tcp&security=none#B", testUUID, a, testUUID, b), "")
	if err != nil {
		t.Fatal(err)
	}
	idA, idB := im.Profile.Configs[0].ID, im.Profile.Configs[1].ID
	sp, hp := mustPort(t), mustPort(t)
	o := Options{Mode: "proxy", SocksPort: sp, HTTPPort: hp, Pool: []string{idA}, // Auto runs A only
		Rules: []Rule{{Match: "full", Value: "b.test", Action: "via", Config: idB}, {Match: "full", Value: "gone.test", Action: "via", Config: "nope"}}}
	if err := Start(im.Profile, o); err != nil {
		t.Fatal(err)
	}
	defer Disconnect()
	pu, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", sp))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	get := func(u string) string {
		resp, err := c.Get(u)
		if err != nil {
			return "error: " + err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	if got := get("http://b.test/"); got != "through B" {
		t.Errorf("b.test goes through B: %q", got)
	}
	if got := get("http://other.test/"); got != "through the proxy" {
		t.Errorf("the rest through Auto (A): %q", got)
	}
	if got := get("http://gone.test/"); got != "through the proxy" {
		t.Errorf("a rule for a config that is gone is skipped: %q", got)
	}
	engine.mu.Lock()
	m := engine.inst.GetFeature(stats.ManagerType()).(stats.Manager)
	engine.mu.Unlock()
	if n := m.GetCounter("outbound>>>via-" + idB + ">>>traffic>>>downlink"); n == nil || n.Value() == 0 {
		t.Error("B carried b.test, and its traffic is counted")
	}
}

// Every Auto mode and the app rule load in Xray.
func TestAutoModesAndAppRulesLoad(t *testing.T) {
	geoDir(t)
	link := "vless://" + testUUID + "@1.2.3.4:443?type=tcp&security=tls&sni=a.com#A"
	p, _ := Import(link+"\n"+strings.Replace(link, "#A", "#B", 1)+"\n"+strings.Replace(link, "#A", "#C", 1), "")
	for _, mode := range []string{"", "latency", "stable"} {
		o := Options{Mode: "proxy", AutoMode: mode, Pool: []string{p.Profile.Configs[0].ID, p.Profile.Configs[1].ID},
			Rules: []Rule{{Match: "domain", Value: "x.test", Action: "via", Config: p.Profile.Configs[2].ID}}}
		if appRules {
			o.Rules = append(o.Rules, Rule{Match: "app", Value: "telegram.exe, Telegram", Action: "direct"})
		}
		cs, err := chosen(p.Profile, o)
		if err != nil {
			t.Fatal(err)
		}
		cfg := BuildConfig(p.Profile, o, cs, nil, nil)
		b, _ := json.Marshal(cfg)
		if _, err := xcore.LoadConfig("json", bytes.NewReader(b)); err != nil {
			t.Fatalf("%q: Xray refuses it: %v", mode, err)
		}
		if !strings.Contains(string(b), `"via-`+p.Profile.Configs[2].ID) {
			t.Errorf("C gets an outbound of its own, outside Auto")
		}
	}
	if _, err := ruleFor(Rule{Match: "app", Value: "x", Action: "via"}, J{}, nil); err == nil {
		t.Error("via needs a config")
	}
}
