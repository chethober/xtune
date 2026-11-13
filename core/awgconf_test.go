package core

import (
	"strings"
	"testing"
)

func TestAmneziaWGConf(t *testing.T) {
	conf := `[Interface]
PrivateKey = yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=
Address = 10.8.1.5/32, fd00::5/128
DNS = 1.1.1.1
Jc = 4
Jmin = 40
Jmax = 70
S1 = 52
S2 = 118
H1 = 1755233287
H2 = 1255215151
H3 = 2058937620
H4 = 1494934151

[Peer]
PublicKey = xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=
PresharedKey = MWg/DVtF7dnLKWSDoVcGM2B8n8pp0ALS0y9U6w0bXqg=
Endpoint = 203.0.113.8:51820
AllowedIPs = 0.0.0.0/0
`
	cs, skipped := ParseLinks(conf)
	if len(cs) != 1 || skipped != 0 {
		t.Fatalf("one config: %d (%d skipped)", len(cs), skipped)
	}
	c := cs[0]
	if c.Engine != "awg" || c.AWG == nil || !usable(c) || c.Kind != "amneziawg" {
		t.Fatalf("runs in-process as AmneziaWG: %+v", c)
	}
	u := c.AWG.UAPI
	for _, want := range []string{"private_key=c809f3e5317e9575c9b5ed78b638b7ce530dabe85ddab6142202418", "jc=4\n", "jmin=40\n", "s2=118\n",
		"h4=1494934151\n", "public_key=c5320103", "preshared_key=", "allowed_ip=0.0.0.0/0"} {
		if !strings.Contains(u, want) {
			t.Errorf("UAPI lacks %q:\n%s", want, u)
		}
	}
	if c.AWG.Endpoint != "203.0.113.8:51820" || c.AWG.Address != "10.8.1.5" || c.AWG.DNS != "1.1.1.1" || c.AWG.MTU != 1280 {
		t.Errorf("tunnel: %+v", c.AWG)
	}
	if _, skipped := ParseLinks(strings.Replace(conf, "Address = 10.8.1.5/32, fd00::5/128", "Address = fd00::5/128", 1)); skipped != 1 {
		t.Error("an IPv4 address is needed")
	}
}
