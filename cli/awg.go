package main

import (
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
	"time"

	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
	"github.com/chethober/xtune/pkg/tunnel"
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

func randInt(lo, hi int) int {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(hi-lo+1)))
	return lo + int(n.Int64())
}

// newAWGParams picks the deployment's settings. S1+56 must not equal S2, or the padded
// handshake initiation and response have the same size; the H values must differ from each
// other and from WireGuard's 1-4.
func newAWGParams() (*state.AWGParams, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	p := &state.AWGParams{Jc: randInt(3, 6), Jmin: 40, Jmax: 70, S1: randInt(15, 150), S2: randInt(15, 150),
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
func renumberAWG(es []state.Entry) {
	i := 0
	for j := range es {
		if es[j].Kind == "amneziawg" {
			es[j].Subnet = awgSubnet(i, false)
			i++
		}
	}
}

// awgIface is the TUN interface name of an entry: xtawg80 for 10.80.0.0/16.
func awgIface(e state.Entry) string {
	return "xtawg" + strings.Split(e.Subnet, ".")[1]
}

func awgServerAddr(e state.Entry) netip.Prefix {
	p := netip.MustParsePrefix(e.Subnet)
	return netip.PrefixFrom(p.Addr().Next(), p.Bits())
}

// awgModes are the client-side variants: the deployment's junk packets, more and larger junk
// packets, and an I1 packet that looks like a QUIC Initial sent before the handshake (AmneziaWG
// 1.5; older apps do not know the I1 line).
var awgModes = []string{"", "junk", "quic"}

// awgVariantUser is who a tuning variant connects as: like WireGuard (wgVariantUser), variants
// measured at once on one device need keys and addresses of their own. Variant 0 is u.
func awgVariantUser(u state.User, k state.Knobs) state.User {
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
func awgVariants() []state.Knobs {
	ks := []state.Knobs{{Frag: "none", MTU: 1420}, {Frag: "none", MTU: 1280}}
	for _, m := range awgModes[1:] {
		ks = append(ks, state.Knobs{Frag: "none", MTU: 1420, Mode: m})
	}
	return ks
}

func awgVariantIndex(k state.Knobs) int {
	for i, v := range awgVariants() {
		if v.MTU == protocols.WGMTU(k) && v.Mode == k.Mode {
			return i
		}
	}
	return 0
}

// awgTestPeers are the test instance's peers: u as every variant.
func awgTestPeers(u state.User) []state.User {
	var out []state.User
	for _, k := range awgVariants() {
		out = append(out, awgVariantUser(u, k))
	}
	return out
}

// ---------------------------------------------------------------- server

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
	var c protocols.AWGConf
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
		d, err := awgServerDevice(e, c.UAPIServer(e))
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
			var nc protocols.AWGConf
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
					if err := devs[e.ID].IpcSet(c.UAPIPeers(e)); err != nil {
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
func awgStats(c protocols.AWGConf, peers map[string][]awgPeerStat, boot string, now time.Time) tunnelStats {
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

func writeAWGStats(c protocols.AWGConf, devs map[string]*device.Device, boot string) {
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

// awgDial connects as u through an in-process AmneziaWG client (pkg/tunnel).
func awgDial(st *state.State, e state.Entry, u state.User, k state.Knobs) *tunnel.AWGDialer {
	if st.AWG == nil {
		return tunnel.AWG{}.Dialer() // fails: no AmneziaWG settings
	}
	return protocols.AWGClient(st, e, u, k).Dialer()
}

// ---------------------------------------------------------------- profiles

// awgSpecs are the helper's arguments for the AWG entries: "awg" and "net:SUBNET" for each
// client range to NAT.
func awgSpecs(entries []state.Entry) []string {
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

const awgStatsFile = remote.Dir + "/awg-stats.json"
