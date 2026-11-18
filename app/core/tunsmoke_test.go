package core

import (
	"net"
	"os"
	"testing"

	"github.com/chethober/xtune/pkg/protocols"
)

func TestTunSmoke(t *testing.T) {
	if os.Getenv("XTUNE_TUN_SMOKE") == "" {
		t.Skip("set XTUNE_TUN_SMOKE=1 (needs root and /dev/net/tun)")
	}
	geoDir(t)
	p := &Profile{Configs: []protocols.ClientEntry{{ID: "a", Engine: "xray", Outbound: vlessOutbound(1)}}}
	sp, _ := freePort()
	hp, _ := freePort()
	if err := Start(p, Options{Mode: "tun", TunName: "xtunetest0", SocksPort: sp, HTTPPort: hp}); err != nil {
		t.Fatal(err)
	}
	if _, err := net.InterfaceByName("xtunetest0"); err != nil {
		t.Error("no TUN interface:", err)
	}
	Stop()
	// Xray's TUN inbound has no Close: closeTun must take its stack and interface down, or the
	// old stack goes on reading the tunnel and feeding the closed instance (the FakeDNS panic)
	if _, err := net.InterfaceByName("xtunetest0"); err == nil {
		t.Error("the TUN interface outlived Stop")
	}
	for i := 0; i < 2; i++ { // reconnects with FakeDNS, the case that crashed
		if err := Start(p, Options{Mode: "tun", TunName: "xtunetest0", FakeDNS: true, SocksPort: sp, HTTPPort: hp}); err != nil {
			t.Fatal(err)
		}
		Stop()
	}
}
