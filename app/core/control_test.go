package core

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNextConfig(t *testing.T) {
	geoDir(t)
	if _, err := NextConfig(); err == nil {
		t.Error("nothing to switch while disconnected")
	}
	site := testSite(t)
	a := testServer(t, strings.TrimPrefix(site.URL, "http://"))
	b := testServer(t, strings.TrimPrefix(site.URL, "http://"))
	im, err := Import(fmt.Sprintf("vless://%s@127.0.0.1:%d?type=tcp&security=none#A\nvless://%s@127.0.0.1:%d?type=tcp&security=none#B", testUUID, a, testUUID, b), "")
	if err != nil {
		t.Fatal(err)
	}
	sp, hp := mustPort(t), mustPort(t)
	if err := Start(im.Profile, Options{Mode: "proxy", SocksPort: sp, HTTPPort: hp}); err != nil {
		t.Fatal(err)
	}
	defer Disconnect()
	before := CurrentStatus()
	s, err := NextConfig()
	if err != nil {
		t.Fatal(err)
	}
	if s.Selected == "" || s.Selected == before.Active || s.Active != s.Selected {
		t.Errorf("pinned to the config after %s: %+v", before.Active, s)
	}
	if s.SelRev != before.SelRev+1 || s.ActiveName == "" {
		t.Errorf("the app is told (selRev %d → %d) and the name given (%q)", before.SelRev, s.SelRev, s.ActiveName)
	}
	pu, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", sp))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	resp, err := c.Get("http://probe.test/")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b2) != "through the proxy" {
		t.Errorf("traffic after switching: %q", b2)
	}
	s2, _ := NextConfig()
	if s2.Selected != before.Active && before.Active != "" {
		t.Errorf("it cycles back: %s, first was %s", s2.Selected, before.Active)
	}
}
