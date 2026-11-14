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

// The Home screen's speeds and the usage history come from these counters. They are read from
// the configs' outbounds, which VPN mode goes through too: its TUN inbound never feeds the
// inbound counters, so speeds read zero there in 1.1.
func TestStatusCountsTraffic(t *testing.T) {
	geoDir(t)
	site := testSite(t)
	srv := testServer(t, strings.TrimPrefix(site.URL, "http://"))
	im, _ := Import(fmt.Sprintf("vless://%s@127.0.0.1:%d?type=tcp&security=none#Local", testUUID, srv), "")
	sp, hp := mustPort(t), mustPort(t)
	if err := Start(im.Profile, Options{Mode: "proxy", SocksPort: sp, HTTPPort: hp}); err != nil {
		t.Fatal(err)
	}
	defer Disconnect()
	pu, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", sp))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	for i := 0; i < 3; i++ {
		resp, err := c.Get("http://probe.test/")
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	time.Sleep(300 * time.Millisecond)
	s := CurrentStatus()
	if s.Up <= 0 || s.Down <= 0 {
		t.Errorf("traffic through the tunnel is counted: up %d down %d", s.Up, s.Down)
	}
	pol := BuildConfig(im.Profile, Options{Mode: "tun"}, im.Profile.Configs, nil, nil)["policy"].(J)["system"].(J)
	if pol["statsOutboundUplink"] != true || pol["statsOutboundDownlink"] != true {
		t.Errorf("VPN mode counts on the outbounds: %v", pol)
	}
}
