package protocols

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"

	"github.com/chethober/xtune/pkg/state"
)

// AWGAddr is the user's tunnel address in the entry's subnet: the last two bytes of their
// WireGuard address. The server is .0.1, which WireGuard addresses never use.
func AWGAddr(e state.Entry, u state.User) netip.Addr {
	p := netip.MustParsePrefix(e.Subnet).Addr().As4()
	w, err := netip.ParseAddr(u.WGIP)
	if err != nil {
		w = state.WgSubnet.Addr().Next().Next()
	}
	b := w.As4()
	return netip.AddrFrom4([4]byte{p[0], p[1], b[2], b[3]})
}

func AWGUserKey(u state.User) (priv, pub string) {
	k, err := ecdh.X25519().NewPrivateKey(secret(u, "amneziawg"))
	if err != nil {
		panic(err) // any 32 bytes are an X25519 key
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

// awgQUIC is the I1 signature: a QUIC v1 long header (Initial, 8-byte random connection ID) and
// a random body, 1250 bytes in all, under a 1280-byte path MTU.
const awgQUIC = "<b 0xc30000000108><r 8><b 0x000044d0><r 616><r 616>" // random blocks are under 1000 bytes

// AWGClientParams are p as a client in mode sends them.
func AWGClientParams(p state.AWGParams, mode string) (jc, jmin, jmax int, i1 string) {
	jc, jmin, jmax = p.Jc, p.Jmin, p.Jmax
	switch mode {
	case "junk":
		jc, jmin, jmax = 10, 64, 1000
	case "quic":
		i1 = awgQUIC
	}
	return
}

func B64hex(s string) string {
	b, _ := base64.StdEncoding.DecodeString(s)
	return hex.EncodeToString(b)
}

func UAPIShared(p state.AWGParams, b *strings.Builder) {
	fmt.Fprintf(b, "s1=%d\ns2=%d\nh1=%d\nh2=%d\nh3=%d\nh4=%d\n", p.S1, p.S2, p.H1, p.H2, p.H3, p.H4)
}

// AWGConf is what `xtune awg run` serves.
type AWGConf struct {
	Params  state.AWGParams `json:"params"`
	Entries []state.Entry   `json:"entries"`
	Peers   []AWGPeer       `json:"peers"`
	Stats   string          `json:"stats"` // where the server writes its counters (tunnelStats)
}

type AWGPeer struct {
	Name string `json:"name"`
	Pub  string `json:"pub"`
	WGIP string `json:"wgIp"`
}

// AWGServerConf builds the AmneziaWG server's config for these users (enabled ones only).
func AWGServerConf(st *state.State, entries []state.Entry, users []state.User, statsPath string) AWGConf {
	c := AWGConf{Entries: state.FilterEntries(entries, func(e state.Entry) bool { return e.Kind == "amneziawg" }), Stats: statsPath,
		Peers: []AWGPeer{}}
	if st.AWG != nil {
		c.Params = *st.AWG
	}
	for _, u := range users {
		if u.Enabled {
			_, pub := AWGUserKey(u)
			c.Peers = append(c.Peers, AWGPeer{Name: u.Name, Pub: pub, WGIP: u.WGIP})
		}
	}
	return c
}

// UAPIServer is the device config of entry e. The server sends junk packets too when it starts
// a handshake, so it gets the deployment's Jc/Jmin/Jmax.
func (c AWGConf) UAPIServer(e state.Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\nlisten_port=%d\njc=%d\njmin=%d\njmax=%d\n", B64hex(c.Params.Priv), e.Port,
		c.Params.Jc, c.Params.Jmin, c.Params.Jmax)
	UAPIShared(c.Params, &b)
	b.WriteString(c.UAPIPeers(e))
	return b.String()
}

func (c AWGConf) UAPIPeers(e state.Entry) string {
	var b strings.Builder
	b.WriteString("replace_peers=true\n")
	for _, p := range c.Peers {
		fmt.Fprintf(&b, "public_key=%s\nallowed_ip=%s/32\n", B64hex(p.Pub), AWGAddr(e, state.User{WGIP: p.WGIP}))
	}
	return b.String()
}

// AWGConfFile is the config file the AmneziaVPN and AmneziaWG apps import.
func AWGConfFile(st *state.State, e state.Entry, u state.User) string {
	p := *st.AWG
	priv, _ := AWGUserKey(u)
	jc, jmin, jmax, i1 := AWGClientParams(p, e.Mode)
	var b strings.Builder
	fmt.Fprintf(&b, `[Interface]
PrivateKey = %s
Address = %s/32
DNS = 1.1.1.1, 8.8.8.8
MTU = %d
Jc = %d
Jmin = %d
Jmax = %d
S1 = %d
S2 = %d
H1 = %d
H2 = %d
H3 = %d
H4 = %d
`, priv, AWGAddr(e, u), WGMTU(e.Knobs()), jc, jmin, jmax, p.S1, p.S2, p.H1, p.H2, p.H3, p.H4)
	if i1 != "" {
		fmt.Fprintf(&b, "I1 = %s\n", i1)
	}
	fmt.Fprintf(&b, `
[Peer]
PublicKey = %s
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = %s
PersistentKeepalive = 25
`, p.Public, HostPort(st.Addr(), e.Port))
	return b.String()
}
