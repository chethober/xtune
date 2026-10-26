package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func TestAWGParams(t *testing.T) {
	for range 50 {
		p, err := newAWGParams()
		if err != nil {
			t.Fatal(err)
		}
		hs := []uint32{p.H1, p.H2, p.H3, p.H4}
		slices.Sort(hs)
		if p.S1+56 == p.S2 || hs[0] < 5 || len(slices.Compact(hs)) != 4 {
			t.Fatalf("bad params: %+v", p)
		}
	}
	st := keyedState(t)
	if st.AWG == nil || st.AWG.Priv == "" {
		t.Fatal("ensureKeys made no AmneziaWG settings")
	}
	if pub := st.public(); pub.AWG == nil || pub.AWG.Priv != "" || pub.AWG.H1 != st.AWG.H1 || st.AWG.Priv == "" {
		t.Errorf("the public state keeps the shared settings and drops the key: %+v", pub.AWG)
	}
}

func TestAWGAddressesAndPlan(t *testing.T) {
	probe := "SNI www.a.com 2 0.20\nUSED udp 0.0.0.0:443\nHOP yes"
	es, _ := planEntries(probe, PlanOpts{Protocols: []string{"awg"}})
	var awg []Entry
	for _, e := range es {
		if e.Kind == "amneziawg" {
			awg = append(awg, e)
		}
	}
	if len(awg) != 2 || awg[0].Subnet != "10.90.0.0/16" || awg[1].Subnet != "10.91.0.0/16" || awg[0].Proto() != "udp" {
		t.Fatalf("test entries: %+v", awg)
	}
	if es, _ := planEntries(probe, PlanOpts{}); hasEngine(es, "awg") {
		t.Error("AmneziaWG is opt-in")
	}
	deployed := []Entry{{Kind: "reality-tcp"}, awg[1]}
	renumberAWG(deployed)
	if deployed[1].Subnet != "10.80.0.0/16" || awgIface(deployed[1]) != "xtawg80" {
		t.Errorf("deployed entries leave the test ranges: %+v", deployed[1])
	}
	u := User{WGIP: "10.66.3.7"}
	if a := awgAddr(deployed[1], u); a.String() != "10.80.3.7" {
		t.Errorf("user address %s", a)
	}
	if s := awgServerAddr(deployed[1]); s.String() != "10.80.0.1/16" {
		t.Errorf("server address %s", s)
	}
	if specs := awgSpecs(deployed); strings.Join(specs, " ") != "awg net:10.80.0.0/16" {
		t.Errorf("helper specs %v", specs)
	}
	// every variant is its own peer: distinct keys and addresses
	tu := User{Name: "tune", UUID: newUUID(), WGIP: "10.66.0.2"}
	peers := awgTestPeers(tu)
	keys, ips := map[string]bool{}, map[string]bool{}
	for _, p := range peers {
		_, pub := awgUserKey(p)
		keys[pub], ips[p.WGIP] = true, true
	}
	if len(peers) != len(awgVariants()) || len(keys) != len(peers) || len(ips) != len(peers) {
		t.Errorf("test peers: %+v", peers)
	}
	for _, k := range awgVariants() {
		if !slices.ContainsFunc(peers, func(p User) bool { return p.UUID == awgVariantUser(tu, k).UUID }) {
			t.Errorf("no test peer for %+v", k)
		}
	}
}

func TestAWGProfile(t *testing.T) {
	st := keyedState(t)
	u := NewUser("ann")
	u.WGIP = "10.66.0.9"
	e := Entry{ID: "awg", Kind: "amneziawg", Port: 51999, Subnet: "10.80.0.0/16", MTU: 1280, Mode: "quic"}
	st.Entries = []Entry{e}
	conf := st.AWGConfFile(e, u)
	for _, want := range []string{"Address = 10.80.0.9/32", "MTU = 1280", "Endpoint = 203.0.113.7:51999", "I1 = <b 0xc3",
		"PublicKey = " + st.AWG.Public, "H4 = " + itoa(int(st.AWG.H4)), "S1 = " + itoa(st.AWG.S1)} {
		if !strings.Contains(conf, want) {
			t.Errorf("profile lacks %q:\n%s", want, conf)
		}
	}
	e.Mode = ""
	if strings.Contains(st.AWGConfFile(e, u), "I1") {
		t.Error("the I1 line is only for the quic variant (older apps reject it)")
	}
	ps := st.Profiles(u)
	if len(ps) != 1 || !strings.HasSuffix(ps[0].File, ".conf") || st.Link(e, u, e.Knobs()) != "" {
		t.Errorf("AmneziaWG comes as a profile file: %+v", ps)
	}
}

// awgTestServer runs the server side of entry e on a userspace network stack, with an HTTP
// server answering 204 on the server's tunnel address.
func awgTestServer(t *testing.T, c AWGConf, e Entry) *device.Device {
	t.Helper()
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{awgServerAddr(e).Addr()}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	d := device.NewDevice(newNetTUN(tdev), conn.NewStdNetBind(), device.NewLogger(device.LogLevelSilent, ""))
	if err := d.IpcSet(c.uapiServer(e)); err != nil {
		t.Fatal(err)
	}
	if err := d.Up(); err != nil {
		t.Fatal(err)
	}
	ln, err := tnet.ListenTCP(&net.TCPAddr{Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(204)
	}))
	t.Cleanup(func() { ln.Close(); d.Close() })
	return d
}

// TestAWGEndToEnd connects every tuning variant through the in-process client to an in-process
// server built from the configs xtune writes, then reads the server's counters.
func TestAWGEndToEnd(t *testing.T) {
	st := keyedState(t)
	st.Host = "127.0.0.1"
	ports, _ := freeUDPPorts(t, 1)
	e := Entry{ID: "awg", Kind: "amneziawg", Port: ports[0], Subnet: awgSubnet(0, true)}
	tu := User{Name: "tune", UUID: newUUID(), Enabled: true, WGIP: "10.66.0.2"}
	c := st.AWGConf([]Entry{e}, awgTestPeers(tu), "")
	srv := awgTestServer(t, c, e)

	for _, k := range awgVariants() {
		d := st.awgDial(e, awgVariantUser(tu, k), k)
		hc := &http.Client{Transport: &http.Transport{DialContext: d.DialContext}, Timeout: 10 * time.Second}
		resp, err := hc.Post("http://"+awgServerAddr(e).Addr().String()+"/", "text/plain", strings.NewReader(strings.Repeat("x", 100000)))
		if err != nil || resp.StatusCode != 204 {
			t.Fatalf("variant %+v: %v %v", k, resp, err)
		}
		resp.Body.Close()
		d.Close()
	}
	out, err := srv.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	stats := awgStats(c, map[string][]awgPeerStat{e.ID: parseAWGStats(out)}, "boot", time.Now())
	if up := stats.Users["tune"][0]; up < 100000 || len(stats.Users) != len(awgVariants()) || stats.Entries["awg"] < up {
		t.Errorf("server counters: %+v", stats)
	}
	if ips := stats.Online["tune"]; len(ips) != 1 || ips[0] != "127.0.0.1" {
		t.Errorf("online: %v", stats.Online)
	}

	// a peer the server does not have gets nowhere
	stranger := User{Name: "x", UUID: newUUID(), WGIP: "10.66.0.50"}
	d := st.awgDial(e, stranger, Knobs{})
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if c, err := d.DialContext(ctx, "tcp", awgServerAddr(e).Addr().String()+":80"); err == nil {
		c.Close()
		t.Error("an unknown peer connected")
	}
}

// TestAWGPeerReload applies a new user list the way the running server does.
func TestAWGPeerReload(t *testing.T) {
	st := keyedState(t)
	st.Host = "127.0.0.1"
	ports, _ := freeUDPPorts(t, 1)
	e := Entry{ID: "awg", Kind: "amneziawg", Port: ports[0], Subnet: awgSubnet(0, true)}
	ann, bob := User{Name: "ann", UUID: newUUID(), Enabled: true, WGIP: "10.66.0.2"}, User{Name: "bob", UUID: newUUID(), Enabled: true, WGIP: "10.66.0.3"}
	srv := awgTestServer(t, st.AWGConf([]Entry{e}, []User{ann}, ""), e)
	get := func(u User) error {
		d := st.awgDial(e, u, Knobs{})
		defer d.Close()
		hc := &http.Client{Transport: &http.Transport{DialContext: d.DialContext}, Timeout: 4 * time.Second}
		resp, err := hc.Get("http://" + awgServerAddr(e).Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(bob); err == nil {
		t.Fatal("bob connected before being added")
	}
	bob2 := bob
	ann.Enabled = false
	if err := srv.IpcSet(st.AWGConf([]Entry{e}, []User{ann, bob2}, "").uapiPeers(e)); err != nil {
		t.Fatal(err)
	}
	if err := get(bob); err != nil {
		t.Errorf("bob after the reload: %v", err)
	}
	if err := get(ann); err == nil {
		t.Error("a disabled user still connects")
	}
}

func freeUDPPorts(t *testing.T, n int) ([]int, error) {
	t.Helper()
	var out []int
	for range n {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c.LocalAddr().(*net.UDPAddr).Port)
		c.Close()
	}
	return out, nil
}
