package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	awgtun "github.com/amnezia-vpn/amneziawg-go/tun"
	wgtun "golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// AmneziaWG is WireGuard with the handshake disguised: junk packets before it, padded handshake
// messages and changed message types, so DPI that recognises WireGuard misses it. Xray cannot
// serve it, so xtune does: `xtune awg run CONF` (the xray-autotune-awg service) runs one
// amneziawg-go device per entry on a TUN interface of its own, and the clients' range is NATed
// out like the VPNs'. Tuning connects through an in-process amneziawg-go on a userspace network
// stack, so unlike OpenVPN and IKEv2 it is measured end to end, without root.
//
// amneziawg-go keeps the message types in package variables, so every device in one process
// must use the same S1/S2/H1-H4: they are the deployment's (State.AWG). Variants only change
// what a client alone decides: junk packets, the I1 signature packet and the MTU.

// AWGParams are the obfuscation settings server and clients share.
type AWGParams struct {
	Jc     int    `json:"jc"`
	Jmin   int    `json:"jmin"`
	Jmax   int    `json:"jmax"`
	S1     int    `json:"s1"`
	S2     int    `json:"s2"`
	H1     uint32 `json:"h1"`
	H2     uint32 `json:"h2"`
	H3     uint32 `json:"h3"`
	H4     uint32 `json:"h4"`
	Priv   string `json:"privateKey"` // server key (base64)
	Public string `json:"publicKey"`
}

func randInt(lo, hi int) int {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(hi-lo+1)))
	return lo + int(n.Int64())
}

// newAWGParams picks the deployment's settings. S1+56 must not equal S2, or the padded
// handshake initiation and response have the same size; the H values must differ from each
// other and from WireGuard's 1-4.
func newAWGParams() (*AWGParams, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	p := &AWGParams{Jc: randInt(3, 6), Jmin: 40, Jmax: 70, S1: randInt(15, 150), S2: randInt(15, 150),
		Priv: base64.StdEncoding.EncodeToString(k.Bytes()), Public: base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())}
	for p.S1+56 == p.S2 {
		p.S2 = randInt(15, 150)
	}
	seen := map[uint32]bool{}
	for _, h := range []*uint32{&p.H1, &p.H2, &p.H3, &p.H4} {
		for *h < 5 || seen[*h] {
			*h = uint32(randInt(5, 1<<31-1))
		}
		seen[*h] = true
	}
	return p, nil
}

// awgSubnets: deployed entries get 10.80.0.0/16, 10.81.0.0/16, ...; test entries 10.90.0.0/16, ...,
// so a test instance never collides with the deployment on the same server.
func awgSubnet(i int, test bool) string {
	base := 80
	if test {
		base = 90
	}
	return fmt.Sprintf("10.%d.0.0/16", base+i)
}

// renumberAWG gives the AWG entries the deployed subnets, in order.
func renumberAWG(es []Entry) {
	i := 0
	for j := range es {
		if es[j].Kind == "amneziawg" {
			es[j].Subnet = awgSubnet(i, false)
			i++
		}
	}
}

// awgIface is the TUN interface name of an entry: xtawg80 for 10.80.0.0/16.
func awgIface(e Entry) string {
	return "xtawg" + strings.Split(e.Subnet, ".")[1]
}

// awgAddr is the user's tunnel address in the entry's subnet: the last two bytes of their
// WireGuard address. The server is .0.1, which WireGuard addresses never use.
func awgAddr(e Entry, u User) netip.Addr {
	p := netip.MustParsePrefix(e.Subnet).Addr().As4()
	w, err := netip.ParseAddr(u.WGIP)
	if err != nil {
		w = wgSubnet.Addr().Next().Next()
	}
	b := w.As4()
	return netip.AddrFrom4([4]byte{p[0], p[1], b[2], b[3]})
}

func awgServerAddr(e Entry) netip.Prefix {
	p := netip.MustParsePrefix(e.Subnet)
	return netip.PrefixFrom(p.Addr().Next(), p.Bits())
}

func awgUserKey(u User) (priv, pub string) {
	k, err := ecdh.X25519().NewPrivateKey(secret(u, "amneziawg"))
	if err != nil {
		panic(err) // any 32 bytes are an X25519 key
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

// awgModes are the client-side variants: the deployment's junk packets, more and larger junk
// packets, and an I1 packet that looks like a QUIC Initial sent before the handshake (AmneziaWG
// 1.5; older apps do not know the I1 line).
var awgModes = []string{"", "junk", "quic"}

// awgQUIC is the I1 signature: a QUIC v1 long header (Initial, 8-byte random connection ID) and
// a random body, 1250 bytes in all, under a 1280-byte path MTU.
const awgQUIC = "<b 0xc30000000108><r 8><b 0x000044d0><r 616><r 616>" // random blocks are under 1000 bytes

// awgClientParams are p as a client in mode sends them.
func awgClientParams(p AWGParams, mode string) (jc, jmin, jmax int, i1 string) {
	jc, jmin, jmax = p.Jc, p.Jmin, p.Jmax
	switch mode {
	case "junk":
		jc, jmin, jmax = 10, 64, 1000
	case "quic":
		i1 = awgQUIC
	}
	return
}

func b64hex(s string) string {
	b, _ := base64.StdEncoding.DecodeString(s)
	return hex.EncodeToString(b)
}

func (p AWGParams) uapiShared(b *strings.Builder) {
	fmt.Fprintf(b, "s1=%d\ns2=%d\nh1=%d\nh2=%d\nh3=%d\nh4=%d\n", p.S1, p.S2, p.H1, p.H2, p.H3, p.H4)
}

// awgVariantUser is who a tuning variant connects as: like WireGuard (wgVariantUser), variants
// measured at once on one device need keys and addresses of their own. Variant 0 is u.
func awgVariantUser(u User, k Knobs) User {
	i := awgVariantIndex(k)
	if i == 0 {
		return u
	}
	a := u
	a.Name = fmt.Sprintf("%s-awg%d", u.Name, i)
	a.UUID = fmt.Sprintf("%s/awg%d", u.UUID, i)
	if ip, err := netip.ParseAddr(u.WGIP); err == nil {
		for range i {
			ip = ip.Next()
		}
		a.WGIP = ip.String()
	}
	return a
}

// awgVariants are the knobs ExpandVariants measures for an AWG entry.
func awgVariants() []Knobs {
	ks := []Knobs{{Frag: "none", MTU: 1420}, {Frag: "none", MTU: 1280}}
	for _, m := range awgModes[1:] {
		ks = append(ks, Knobs{Frag: "none", MTU: 1420, Mode: m})
	}
	return ks
}

func awgVariantIndex(k Knobs) int {
	for i, v := range awgVariants() {
		if v.MTU == wgMTU(k) && v.Mode == k.Mode {
			return i
		}
	}
	return 0
}

// awgTestPeers are the test instance's peers: u as every variant.
func awgTestPeers(u User) []User {
	var out []User
	for _, k := range awgVariants() {
		out = append(out, awgVariantUser(u, k))
	}
	return out
}

// ---------------------------------------------------------------- server

// AWGConf is what `xtune awg run` serves.
type AWGConf struct {
	Params  AWGParams `json:"params"`
	Entries []Entry   `json:"entries"`
	Peers   []AWGPeer `json:"peers"`
	Stats   string    `json:"stats"` // where the server writes its counters (tunnelStats)
}

type AWGPeer struct {
	Name string `json:"name"`
	Pub  string `json:"pub"`
	WGIP string `json:"wgIp"`
}

// AWGConf builds the AmneziaWG server's config for these users (enabled ones only).
func (st *State) AWGConf(entries []Entry, users []User, statsPath string) AWGConf {
	c := AWGConf{Entries: filterEntries(entries, func(e Entry) bool { return e.Kind == "amneziawg" }), Stats: statsPath,
		Peers: []AWGPeer{}}
	if st.AWG != nil {
		c.Params = *st.AWG
	}
	for _, u := range users {
		if u.Enabled {
			_, pub := awgUserKey(u)
			c.Peers = append(c.Peers, AWGPeer{Name: u.Name, Pub: pub, WGIP: u.WGIP})
		}
	}
	return c
}

// uapiServer is the device config of entry e. The server sends junk packets too when it starts
// a handshake, so it gets the deployment's Jc/Jmin/Jmax.
func (c AWGConf) uapiServer(e Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\nlisten_port=%d\njc=%d\njmin=%d\njmax=%d\n", b64hex(c.Params.Priv), e.Port,
		c.Params.Jc, c.Params.Jmin, c.Params.Jmax)
	c.Params.uapiShared(&b)
	b.WriteString(c.uapiPeers(e))
	return b.String()
}

func (c AWGConf) uapiPeers(e Entry) string {
	var b strings.Builder
	b.WriteString("replace_peers=true\n")
	for _, p := range c.Peers {
		fmt.Fprintf(&b, "public_key=%s\nallowed_ip=%s/32\n", b64hex(p.Pub), awgAddr(e, User{WGIP: p.WGIP}))
	}
	return b.String()
}

func cmdAWG(args []string) error {
	if len(args) != 2 || args[0] != "run" {
		return errors.New("usage: xtune awg run CONF (started by the xray-autotune-awg service)")
	}
	return runAWG(args[1])
}

var awgStatsEvery = 15 * time.Second

// runAWG serves conf until a device fails or the entries in the file change (the service
// manager starts it again with the new ones). Peer changes are applied in place.
func runAWG(path string) error {
	var c AWGConf
	if err := readJSONFile(path, &c); err != nil {
		return err
	}
	if len(c.Entries) == 0 {
		return errors.New("no AmneziaWG entries")
	}
	devs := map[string]*device.Device{}
	defer func() {
		for _, d := range devs {
			d.Close()
		}
	}()
	for _, e := range c.Entries {
		d, err := awgServerDevice(e, c.uapiServer(e))
		if err != nil {
			return fmt.Errorf("%s: %w", e.ID, err)
		}
		devs[e.ID] = d
	}
	boot := randHex(8)
	var last time.Time
	if fi, err := os.Stat(path); err == nil {
		last = fi.ModTime()
	}
	tick := time.NewTicker(awgStatsEvery)
	defer tick.Stop()
	watch := time.NewTicker(tunnelWatchEvery)
	defer watch.Stop()
	for {
		select {
		case <-tick.C:
			writeAWGStats(c, devs, boot)
		case <-watch.C:
			fi, err := os.Stat(path)
			if err != nil || fi.ModTime().Equal(last) {
				continue
			}
			last = fi.ModTime()
			var nc AWGConf
			if readJSONFile(path, &nc) != nil {
				continue
			}
			if !reflect.DeepEqual(nc.Entries, c.Entries) || nc.Params != c.Params {
				writeAWGStats(c, devs, boot)
				return errors.New("entries changed; restarting")
			}
			if !reflect.DeepEqual(nc.Peers, c.Peers) {
				c = nc
				for _, e := range c.Entries {
					if err := devs[e.ID].IpcSet(c.uapiPeers(e)); err != nil {
						return fmt.Errorf("%s: peers: %w", e.ID, err)
					}
				}
			}
		}
	}
}

// awgPeerStat is one peer of `IpcGet` output.
type awgPeerStat struct {
	Pub       string // base64
	Rx, Tx    int64
	Handshake int64 // unix seconds
	Endpoint  string
}

func parseAWGStats(s string) []awgPeerStat {
	var out []awgPeerStat
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k == "public_key" {
			b, _ := hex.DecodeString(v)
			out = append(out, awgPeerStat{Pub: base64.StdEncoding.EncodeToString(b)})
			continue
		}
		if len(out) == 0 {
			continue
		}
		p := &out[len(out)-1]
		switch k {
		case "rx_bytes":
			p.Rx, _ = strconv.ParseInt(v, 10, 64)
		case "tx_bytes":
			p.Tx, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_sec":
			p.Handshake, _ = strconv.ParseInt(v, 10, 64)
		case "endpoint":
			p.Endpoint = v
		}
	}
	return out
}

// awgStats turns the devices' peer counters into the tunnel server's stats format: the server
// receives what clients upload. A peer is online while its last handshake is recent (clients
// redo it every 2 minutes while in use).
func awgStats(c AWGConf, peers map[string][]awgPeerStat, boot string, now time.Time) tunnelStats {
	names := map[string]string{}
	for _, p := range c.Peers {
		names[p.Pub] = p.Name
	}
	s := tunnelStats{Boot: boot, Users: map[string][2]int64{}, Entries: map[string]int64{}, Online: map[string][]string{},
		Written: now.Unix()}
	for id, ps := range peers {
		for _, p := range ps {
			name := names[p.Pub]
			if name == "" {
				continue
			}
			t := s.Users[name]
			s.Users[name] = [2]int64{t[0] + p.Rx, t[1] + p.Tx}
			s.Entries[id] += p.Rx + p.Tx
			if host, _, err := net.SplitHostPort(p.Endpoint); err == nil && now.Unix()-p.Handshake < 180 &&
				!contains2s(s.Online[name], host) {
				s.Online[name] = append(s.Online[name], host)
			}
		}
	}
	return s
}

func writeAWGStats(c AWGConf, devs map[string]*device.Device, boot string) {
	peers := map[string][]awgPeerStat{}
	for id, d := range devs {
		if out, err := d.IpcGet(); err == nil {
			peers[id] = parseAWGStats(out)
		}
	}
	if c.Stats == "" {
		return
	}
	b, _ := json.Marshal(awgStats(c, peers, boot, time.Now()))
	tmp := c.Stats + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, c.Stats)
	}
}

// ---------------------------------------------------------------- client

// uapiClient is a client's device config for entry e, connecting to endpoint (an IP:port).
func (st *State) uapiClient(e Entry, u User, k Knobs, endpoint string) string {
	p := *st.AWG
	priv, _ := awgUserKey(u)
	jc, jmin, jmax, i1 := awgClientParams(p, k.Mode)
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\njc=%d\njmin=%d\njmax=%d\n", b64hex(priv), jc, jmin, jmax)
	p.uapiShared(&b)
	if i1 != "" {
		fmt.Fprintf(&b, "i1=%s\n", i1)
	}
	fmt.Fprintf(&b, "public_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\npersistent_keepalive_interval=25\n",
		b64hex(p.Public), endpoint)
	return b.String()
}

// awgDialer connects as u through an in-process AmneziaWG client on a userspace network stack.
// The tunnel comes up on the first dial; names resolve through it (1.1.1.1), as in the apps.
type awgDialer struct {
	st   *State
	e    Entry
	u    User
	k    Knobs
	once sync.Once
	dev  *device.Device
	tnet *netstack.Net
	err  error
}

func (st *State) awgDial(e Entry, u User, k Knobs) *awgDialer {
	return &awgDialer{st: st, e: e, u: u, k: k}
}

func (a *awgDialer) start() {
	if a.st.AWG == nil {
		a.err = errors.New("no AmneziaWG settings")
		return
	}
	host := a.st.Addr()
	ip, err := netip.ParseAddr(host)
	if err != nil {
		ips, err := net.LookupHost(host)
		if err != nil || len(ips) == 0 {
			a.err = fmt.Errorf("resolve %s: %v", host, err)
			return
		}
		ip, _ = netip.ParseAddr(ips[0])
	}
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{awgAddr(a.e, a.u)}, []netip.Addr{netip.MustParseAddr("1.1.1.1")}, wgMTU(a.k))
	if err != nil {
		a.err = err
		return
	}
	d := device.NewDevice(newNetTUN(tdev), conn.NewStdNetBind(), device.NewLogger(device.LogLevelSilent, ""))
	if err := d.IpcSet(a.st.uapiClient(a.e, a.u, a.k, netip.AddrPortFrom(ip, uint16(a.e.Port)).String())); err != nil {
		d.Close()
		a.err = err
		return
	}
	if err := d.Up(); err != nil {
		d.Close()
		a.err = err
		return
	}
	a.dev, a.tnet = d, tnet
}

// netTUN adapts WireGuard's userspace network stack to amneziawg-go, whose TUN interface is the
// same but for its own Event type. (amneziawg-go's own copy of the stack needs mid-2025 gvisor,
// whose Go module builds carry a test file the go command refuses.)
type netTUN struct {
	wgtun.Device
	events chan awgtun.Event
}

func newNetTUN(d wgtun.Device) *netTUN {
	t := &netTUN{Device: d, events: make(chan awgtun.Event, 10)}
	go func() {
		for ev := range d.Events() {
			t.events <- awgtun.Event(ev)
		}
		close(t.events)
	}()
	return t
}

func (t *netTUN) Events() <-chan awgtun.Event { return t.events }

func (a *awgDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	a.once.Do(a.start)
	if a.err != nil {
		return nil, a.err
	}
	return a.tnet.DialContext(ctx, network, addr)
}

func (a *awgDialer) Close() {
	a.once.Do(func() { a.err = errors.New("closed") })
	if a.dev != nil {
		a.dev.Close()
	}
}

// ---------------------------------------------------------------- profiles

// AWGConfFile is the config file the AmneziaVPN and AmneziaWG apps import.
func (st *State) AWGConfFile(e Entry, u User) string {
	p := *st.AWG
	priv, _ := awgUserKey(u)
	jc, jmin, jmax, i1 := awgClientParams(p, e.Mode)
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
`, priv, awgAddr(e, u), wgMTU(e.Knobs()), jc, jmin, jmax, p.S1, p.S2, p.H1, p.H2, p.H3, p.H4)
	if i1 != "" {
		fmt.Fprintf(&b, "I1 = %s\n", i1)
	}
	fmt.Fprintf(&b, `
[Peer]
PublicKey = %s
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = %s
PersistentKeepalive = 25
`, p.Public, hostPort(st.Addr(), e.Port))
	return b.String()
}

// awgSpecs are the helper's arguments for the AWG entries: "awg" and "net:SUBNET" for each
// client range to NAT.
func awgSpecs(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		if e.Kind == "amneziawg" {
			if !slices.Contains(out, "awg") {
				out = append(out, "awg")
			}
			out = append(out, "net:"+e.Subnet)
		}
	}
	return out
}

const awgStatsFile = remoteDir + "/awg-stats.json"
