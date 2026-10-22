package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// keyedState is testState with the keys of the newer protocols.
func keyedState(t *testing.T) *State {
	t.Helper()
	st := testState()
	if err := st.makeCert(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ensureKeys(); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestParseProtocols(t *testing.T) {
	if p, err := parseProtocols("ssh, vpn"); err != nil || strings.Join(p, ",") != "ssh,openvpn,ikev2" {
		t.Errorf("ssh,vpn: %v %v", p, err)
	}
	if p, _ := parseProtocols("all"); len(p) != len(optionalProtocols) {
		t.Errorf("all: %v", p)
	}
	if _, err := parseProtocols("pptp"); err == nil {
		t.Error("unknown protocol accepted")
	}
}

func TestPlanEntriesProtocols(t *testing.T) {
	probe := "SNI www.a.com 2 0.20\nUSED tcp 0.0.0.0:443\nUSED udp 0.0.0.0:500\nHOP yes"
	es, _ := planEntries(probe, PlanOpts{CDN: true, Protocols: optionalProtocols, DNS: true, IKEBusy: true})
	kinds := map[string]int{}
	ports := map[int]string{}
	for _, e := range es {
		kinds[e.Kind]++
		if other, dup := ports[e.Port]; dup {
			t.Errorf("%s and %s share port %d", e.ID, other, e.Port)
		}
		ports[e.Port] = e.ID
	}
	for k, n := range map[string]int{"wireguard": 2, "ssh": 2, "ssh-tls": 1, "cdn-ssh": 1, "dnstt": 1, "openvpn-udp": 1, "openvpn-tcp": 1, "ikev2": 0} {
		if kinds[k] != n {
			t.Errorf("%s: %d entries, want %d", k, kinds[k], n)
		}
	}
	for _, e := range es {
		if e.Kind == "openvpn-udp" && e.Port != 1194 {
			t.Errorf("OpenVPN UDP should take its standard port: %d", e.Port)
		}
		if e.Kind == "dnstt" && !slices.Contains(portSpecs([]Entry{e}), "hop:53-53:"+itoa(e.Port)) {
			t.Errorf("DNS tunnel needs port 53 forwarded: %v", portSpecs([]Entry{e}))
		}
	}
	if es, _ := planEntries(strings.Replace(probe, "HOP yes", "HOP no", 1), PlanOpts{Protocols: []string{"dnstt"}, DNS: true}); hasEngine(es, "tunnel") {
		t.Error("a DNS tunnel without NAT to forward port 53")
	}
	es, _ = planEntries("SNI www.a.com 2 0.20", PlanOpts{Protocols: []string{"ikev2"}})
	if !slices.ContainsFunc(es, func(e Entry) bool { return e.Kind == "ikev2" && e.Port == 500 }) ||
		!slices.Contains(portSpecs(es), "4500/udp") {
		t.Errorf("IKEv2 on 500 with NAT traversal on 4500: %v", portSpecs(es))
	}
	if es, _ := PlanEntries(probe, false); hasEngine(es, "tunnel") || !slices.ContainsFunc(es, func(e Entry) bool { return e.Kind == "wireguard" }) {
		t.Error("the default matrix has WireGuard and no opt-in protocols")
	}
}

func TestDeployChoiceKeepsOptInFamilies(t *testing.T) {
	v := func(id, kind string) *Variant { return &Variant{Entry: Entry{ID: id, Kind: kind}, Ok: 3} }
	best := []*Variant{v("r-tcp", "reality-tcp"), v("wg", "wireguard"), v("ssh-tls", "ssh-tls"), v("hy2", "hysteria2"),
		v("ssh", "ssh"), v("ovpn-tcp", "openvpn-tcp"), v("ovpn-udp", "openvpn-udp"), v("dnstt", "dnstt")}
	var ids []string
	for _, c := range deployChoice(best, 2) {
		ids = append(ids, c.Entry.ID)
	}
	if strings.Join(ids, ",") != "r-tcp,wg,ssh-tls,ovpn-tcp,dnstt" {
		t.Errorf("top 2 plus the best of each opt-in family: %v", ids)
	}
}

func TestWireGuard(t *testing.T) {
	st := keyedState(t)
	wg := Entry{ID: "wg", Kind: "wireguard", Port: 51820}
	st.Entries = []Entry{wg, {ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}}
	st.Users = []User{NewUser("a"), NewUser("b")}
	st.Check = &User{Name: checkUserName, UUID: newUUID(), Enabled: true}
	st.assignWGIPs()
	ips := map[string]bool{}
	for _, u := range append(st.Users, *st.Check) {
		if ips[u.WGIP] || !wgSubnet.Contains(mustAddr(t, u.WGIP)) {
			t.Fatalf("address %q reused or outside %s", u.WGIP, wgSubnet)
		}
		ips[u.WGIP] = true
	}
	before := st.Users[1].WGIP
	st.Users = append(st.Users[1:], NewUser("c"), NewUser("d"))
	st.assignWGIPs()
	if st.Users[0].WGIP != before || st.Users[1].WGIP == before || st.Users[1].WGIP == st.Users[2].WGIP || st.Users[2].WGIP == st.Check.WGIP {
		t.Errorf("addresses must stay put and new ones be free: %+v", st.Users)
	}
	st.Users = st.Users[:2]

	a := st.Users[0]
	cfg := st.ServerConfig(st.Entries, st.Users)
	var in J
	for _, ib := range cfg["inbounds"].([]J) {
		if ib["protocol"] == "wireguard" {
			in = ib
		}
	}
	peers := in["settings"].(J)["peers"].([]J)
	if len(peers) != 2 || peers[0]["email"] != a.Name || peers[0]["allowedIPs"].([]string)[0] != a.WGIP+"/32" {
		t.Fatalf("server peers: %v", peers)
	}
	ob := st.Outbound(wg, a, Knobs{MTU: 1280}, "proxy")
	set := ob["settings"].(J)
	priv, _ := base64.StdEncoding.DecodeString(set["secretKey"].(string))
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil || base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()) != peers[0]["publicKey"] {
		t.Error("the client key does not match the server's peer key")
	}
	if set["mtu"] != 1280 || set["peers"].([]J)[0]["publicKey"] != st.WGPub {
		t.Errorf("client outbound: %v", set)
	}
	l, err := url.Parse(st.Link(wg, a, Knobs{}))
	if err != nil || l.Scheme != "wireguard" || l.Query().Get("publickey") != st.WGPub || l.Query().Get("address") != a.WGIP+"/32" {
		t.Errorf("link: %v %v", l, err)
	}
	if c := st.WGConf(wg, a); !strings.Contains(c, "Address = "+a.WGIP+"/32") || !strings.Contains(c, "Endpoint = 203.0.113.7:51820") {
		t.Errorf("WireGuard app config:\n%s", c)
	}
	if fc := st.FailoverConfig(a); !strings.Contains(mustJSON(t, fc), `"protocol":"wireguard"`) {
		t.Error("the failover config should include WireGuard")
	}
	if bin := FindLocalXray(""); bin != "" { // Xray itself agrees with both ends
		if err := xrayAccepts(bin, st.ServerConfig([]Entry{wg}, st.Users)); err != nil {
			t.Errorf("server config: %v", err)
		}
		if err := xrayAccepts(bin, ClientConfig(ob)); err != nil {
			t.Errorf("client config: %v", err)
		}
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// WireGuard users cannot be added through Xray's API: a change restarts Xray, no change does not.
func TestWireGuardUserChangesRestart(t *testing.T) {
	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
	st := keyedState(t)
	st.Entries = []Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}, {ID: "wg", Kind: "wireguard", Port: 51820}}
	st.Users = []User{NewUser("ann")}
	if err := Apply(r, st); err != nil {
		t.Fatal(err)
	}
	n := fs.restarts
	if err := Enforce(r); err != nil || fs.restarts != n {
		t.Fatalf("nothing changed, yet %d restarts (%v)", fs.restarts-n, err)
	}
	if _, _, err := AddUser(r, "bob", Limits{}); err != nil {
		t.Fatal(err)
	}
	if fs.restarts != n+1 || len(fs.live["wg"]) != 2 {
		t.Errorf("adding a user should restart once and give WireGuard both peers: %d restarts, %v", fs.restarts-n, fs.live["wg"])
	}
}

func TestTunnelServer(t *testing.T) {
	defer func(s, w time.Duration, a func(net.IP) bool) { tunnelStatsEvery, tunnelWatchEvery, allowDest = s, w, a }(
		tunnelStatsEvery, tunnelWatchEvery, allowDest)
	tunnelStatsEvery, tunnelWatchEvery = 50*time.Millisecond, 50*time.Millisecond
	allowDest = func(net.IP) bool { return true } // the test site is on loopback
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") }))
	defer web.Close()

	st := keyedState(t)
	st.Host, st.DNSDomain = "127.0.0.1", "t.example.com"
	ports, _ := freePorts(3)
	st.Entries = []Entry{{ID: "ssh", Kind: "ssh", Port: ports[0]}, {ID: "ssh-tls", Kind: "ssh-tls", Port: ports[1], SNI: "www.a.com"},
		{ID: "dnstt", Kind: "dnstt", Port: ports[2]}}
	ann, bob := NewUser("ann"), NewUser("bob")
	bob.Devices = 1
	dir := t.TempDir()
	conf, stats := filepath.Join(dir, "tunnels.json"), filepath.Join(dir, "stats.json")
	write := func(users ...User) {
		if err := writeJSON(conf, st.TunnelConf(st.Entries, users, stats)); err != nil {
			t.Fatal(err)
		}
	}
	write(ann, bob)
	ready := make(chan []net.Addr, 1)
	go runTunnels(conf, ready)
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel server did not start")
	}

	get := func(e Entry, u User, k Knobs) (string, error) {
		v := &Variant{Entry: e, K: k, dial: st.tunnelDial(e, u, k)}
		resp, err := v.client(20 * time.Second).Get(web.URL)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}
	for _, c := range []struct {
		e Entry
		k Knobs
	}{
		{st.Entries[0], Knobs{}}, {st.Entries[0], Knobs{Mode: "ws"}}, {st.Entries[1], Knobs{}}, {st.Entries[1], Knobs{Mode: "ws"}},
		{st.Entries[2], Knobs{Resolver: "udp:127.0.0.1:" + itoa(ports[2])}},
	} {
		if body, err := get(c.e, ann, c.k); err != nil || body != "hello" {
			t.Errorf("%s %+v: %q %v", c.e.ID, c.k, body, err)
		}
	}
	impostor := ann
	impostor.UUID = newUUID()
	if _, err := get(st.Entries[0], impostor, Knobs{}); err == nil {
		t.Error("a wrong password got through")
	}

	// bob holds a connection open; taking him off the list disconnects it
	held, err := st.tunnelDial(st.Entries[0], bob, Knobs{})(context.Background(), "tcp", strings.TrimPrefix(web.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	time.Sleep(150 * time.Millisecond)
	var ts tunnelStats
	if err := readJSONFile(stats, &ts); err != nil {
		t.Fatal(err)
	}
	if ts.Users["ann"][1] == 0 || ts.Entries["ssh"] == 0 || ts.Entries["dnstt"] == 0 || !slices.Contains(ts.Online["bob"], "127.0.0.1") {
		t.Errorf("stats: %+v", ts)
	}
	write(ann)
	held.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := held.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Errorf("bob's session should have been closed: %v", err)
	}
	if _, err := get(st.Entries[0], bob, Knobs{}); err == nil {
		t.Error("a removed user logged in")
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

func TestAnswerPayloadSplitRequests(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	go func() {
		io.WriteString(b, "CONNECT x:443 HTTP/1.1\r\nHost: x\r\n\r\nGET / HTTP/1.1\r\nHost: y\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\nSSH-2.0-x\r\n")
	}()
	resp := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		tmp := make([]byte, 512)
		for !strings.Contains(buf.String(), "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n") {
			n, err := b.Read(tmp)
			if err != nil {
				break
			}
			buf.Write(tmp[:n])
		}
		resp <- buf.String()
	}()
	c, err := answerPayload(a)
	if err != nil {
		t.Fatal(err)
	}
	line := make([]byte, 9)
	io.ReadFull(c, line)
	if string(line) != "SSH-2.0-x" {
		t.Errorf("SSH should follow the payload: %q", line)
	}
	if r := <-resp; !strings.HasPrefix(r, "HTTP/1.1 200") || !strings.Contains(r, "101 Switching Protocols") {
		t.Errorf("answers: %q", r)
	}
}

const ovpnStatus = `TITLE,OpenVPN 2.6.12 x86_64-pc-linux-gnu
TIME,2026-10-23 10:00:00,1792836000
HEADER,CLIENT_LIST,Common Name,Real Address,Virtual Address,Virtual IPv6 Address,Bytes Received,Bytes Sent,Connected Since,Connected Since (time_t),Username,Client ID,Peer ID,Data Channel Cipher
CLIENT_LIST,ann,198.51.100.4:51234,10.68.0.2,,1000,5000,2026-10-23 09:00:00,1792832400,ann,3,0,AES-256-GCM
HEADER,ROUTING_TABLE,Virtual Address,Common Name,Real Address,Last Ref,Last Ref (time_t)
GLOBAL_STATS,Max bcast/mcast queue length,0
END
`

const swanRaw = `list-sa event {xtune-ikev2 {uniqueid=7 version=2 state=ESTABLISHED local-host=203.0.113.7 local-port=4500 local-id=vpn.example.com remote-host=198.51.100.9 remote-port=4500 remote-id=198.51.100.9 remote-eap-id=bob initiator-spi=1 responder-spi=2 encr-alg=AES_CBC encr-keysize=256 child-sas {net-12 {name=net uniqueid=12 reqid=1 state=INSTALLED mode=TUNNEL protocol=ESP bytes-in=4000 packets-in=10 bytes-out=9000 packets-out=12 local-ts=[0.0.0.0/0] remote-ts=[10.69.0.1/32]}}}}
list-sa event {other {uniqueid=8 remote-eap-id=zed child-sas {c-1 {uniqueid=1 bytes-in=5 bytes-out=5}}}}
list-sas reply {}
`

func TestVPNSessionParsing(t *testing.T) {
	o := parseOpenVPNStatus("ovpn-udp", ovpnStatus)
	if len(o) != 1 || o[0].User != "ann" || o[0].IP != "198.51.100.4" || o[0].Up != 1000 || o[0].Down != 5000 || o[0].Entry() != "ovpn-udp" {
		t.Errorf("OpenVPN: %+v", o)
	}
	i := ikeSessions(swanRaw)
	if len(i) != 1 || i[0].User != "bob" || i[0].Key != "ike/7/12" || i[0].Up != 4000 || i[0].Down != 9000 || i[0].Entry() != "ikev2" {
		t.Errorf("IKEv2: %+v", i)
	}
	all := parseVPNSessions("### ovpn ovpn-udp\n" + ovpnStatus + "### ike\n" + swanRaw)
	if len(all) != 2 {
		t.Errorf("both kinds: %+v", all)
	}
}

func TestCollectExtraAndKick(t *testing.T) {
	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
	st := keyedState(t)
	st.Entries = []Entry{{ID: "ssh", Kind: "ssh", Port: 2222}, {ID: "ovpn-udp", Kind: "openvpn-udp", Port: 1194}, {ID: "ikev2", Kind: "ikev2", Port: 500}}
	stats := func(boot string, up, down int64) {
		b, _ := json.Marshal(tunnelStats{Boot: boot, Users: map[string][2]int64{"ann": {up, down}}, Entries: map[string]int64{"ssh": up + down},
			Online: map[string][]string{"ann": {"192.0.2.1"}}, Written: time.Now().Unix()})
		fs.files[tunnelStatsFile] = string(b)
	}
	fs.helper = map[string]string{"vpn-sessions": "### ovpn ovpn-udp\n" + ovpnStatus + "### ike\n" + swanRaw}
	us := &Usage{}
	now := time.Now()
	stats("b1", 100, 200)
	byTag := map[string]int64{}
	online, sessions := collectExtra(r, st, us, now, byTag)
	if a := us.Users["ann"]; a.Up != 1100 || a.Down != 5200 || us.Users["bob"].Down != 9000 {
		t.Errorf("first run: ann %+v bob %+v", a, us.Users["bob"])
	}
	if byTag["ssh"] != 300 || byTag["ovpn-udp"] != 6000 || byTag["ikev2"] != 13000 {
		t.Errorf("per config: %v", byTag)
	}
	if !slices.Contains(online["ann"], "192.0.2.1") || !slices.Contains(online["ann"], "198.51.100.4") {
		t.Errorf("online: %v", online)
	}
	stats("b1", 150, 200) // only the growth counts
	collectExtra(r, st, us, now, map[string]int64{})
	if a := us.Users["ann"]; a.Up != 1150 || a.Down != 5200 {
		t.Errorf("second run: %+v", a)
	}
	stats("b2", 10, 10) // the tunnel server restarted
	collectExtra(r, st, us, now, map[string]int64{})
	if a := us.Users["ann"]; a.Up != 1160 || a.Down != 5210 {
		t.Errorf("after a restart: %+v", a)
	}

	if err := kickInactive(r, []User{{Name: "ann"}}, sessions); err != nil {
		t.Fatal(err)
	}
	if last := fs.calls[len(fs.calls)-1]; last != "vpn-kick ike:7" {
		t.Errorf("bob's IKEv2 session should be terminated: %q", last)
	}
}

func TestVPNConfigsAndAuth(t *testing.T) {
	st := keyedState(t)
	st.Domain = "vpn.example.com"
	if err := st.ensureVPNCert(); err != nil {
		t.Fatal(err)
	}
	ca, _ := parseCertPEM(st.VPNCA)
	cert, _ := parseCertPEM(st.VPNCert)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{DNSName: "vpn.example.com", Roots: pool}); err != nil {
		t.Errorf("server certificate: %v", err)
	}
	ann, off := NewUser("ann"), NewUser("off")
	off.Enabled = false
	udp, tcp, ike := Entry{ID: "ovpn-udp", Kind: "openvpn-udp", Port: 1194}, Entry{ID: "ovpn-tcp", Kind: "openvpn-tcp", Port: 8443}, Entry{ID: "ikev2", Kind: "ikev2", Port: 500}
	files := st.vpnFiles([]Entry{udp, tcp, ike}, []User{ann, off}, "/v")
	for _, want := range []string{"port 1194\n", "proto udp\n", "server 10.68.0.0 255.255.240.0", "status /v/ovpn-udp.status", "vpn-auth /v/users.json\" via-file", "<cert>"} {
		if !strings.Contains(files["/v/ovpn-udp.conf"], want) {
			t.Errorf("OpenVPN UDP config lacks %q", want)
		}
	}
	if !strings.Contains(files["/v/ovpn-tcp.conf"], "proto tcp-server") || strings.Contains(files["/v/ovpn-tcp.conf"], "explicit-exit-notify") {
		t.Error("OpenVPN TCP config")
	}
	sw := files["/v/swanctl.conf"]
	if !strings.Contains(sw, "id = ann\n    secret = \""+tunnelPassword(ann)+"\"") || strings.Contains(sw, "id = off") || !strings.Contains(sw, "id = vpn.example.com") {
		t.Errorf("swanctl config:\n%s", sw)
	}
	if specs := vpnSpecs([]Entry{udp, ike}); strings.Join(specs, " ") != "openvpn:ovpn-udp net:10.68.0.0/20 ikev2 net:10.69.0.0/16" {
		t.Errorf("helper specs: %v", specs)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "users.json"), []byte(files["/v/users.json"]), 0o600)
	cred := filepath.Join(dir, "cred")
	auth := func(name, pass string) error {
		os.WriteFile(cred, []byte(name+"\n"+pass+"\n"), 0o600)
		return cmdVPNAuth([]string{filepath.Join(dir, "users.json"), cred})
	}
	if err := auth("ann", tunnelPassword(ann)); err != nil {
		t.Errorf("ann: %v", err)
	}
	if auth("ann", "nope") == nil || auth("off", tunnelPassword(off)) == nil {
		t.Error("a wrong password or a disabled user got in")
	}
	ann.Devices = 1 // ann is already on from 198.51.100.4
	uj, _ := json.Marshal(vpnUsers([]User{ann}))
	os.WriteFile(filepath.Join(dir, "users.json"), uj, 0o600)
	os.WriteFile(filepath.Join(dir, "ovpn-udp.status"), []byte(ovpnStatus), 0o600)
	t.Setenv("untrusted_ip", "192.0.2.50")
	if auth("ann", tunnelPassword(ann)) == nil {
		t.Error("a second device got past the limit")
	}
	t.Setenv("untrusted_ip", "198.51.100.4")
	if err := auth("ann", tunnelPassword(ann)); err != nil {
		t.Errorf("the same device again: %v", err)
	}

	st.Entries = []Entry{udp, ike}
	for _, p := range st.Profiles(ann) {
		if p.Body == "" || p.File == "" {
			t.Errorf("empty profile %+v", p)
		}
	}
	if mc := st.MobileConfig(ann); !strings.Contains(mc, "<string>IKEv2</string>") || !strings.Contains(mc, tunnelPassword(ann)) {
		t.Error("Apple profile")
	}
	var sswan J
	if err := json.Unmarshal([]byte(st.SswanProfile(ann)), &sswan); err != nil || sswan["type"] != "ikev2-eap" {
		t.Errorf("strongSwan profile: %v %v", sswan, err)
	}
}

func TestHandshakeProbes(t *testing.T) {
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer pc.Close()
	go func() { // answers OpenVPN resets and IKE_SA_INIT requests
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			b := buf[:n]
			switch {
			case n == 14 && b[0] == 7<<3:
				pc.WriteTo(append([]byte{8 << 3}, b[1:]...), addr)
			case n >= 28 && b[18] == 34 && b[19] == 0x08 && int(binary.BigEndian.Uint32(b[24:])) == n:
				resp := append([]byte{}, b[:28]...)
				resp[19] = 0x20
				pc.WriteTo(resp, addr)
			}
		}
	}()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var hdr [2]byte
			io.ReadFull(c, hdr[:])
			p := make([]byte, binary.BigEndian.Uint16(hdr[:]))
			io.ReadFull(c, p)
			c.Write([]byte{0, byte(len(p)), 8 << 3})
			c.Write(p[1:])
			c.Close()
		}
	}()
	st := keyedState(t)
	st.Host = "127.0.0.1"
	udpPort := pc.LocalAddr().(*net.UDPAddr).Port
	tcpPort := ln.Addr().(*net.TCPAddr).Port
	for _, e := range []Entry{{Kind: "openvpn-udp", Port: udpPort}, {Kind: "ikev2", Port: udpPort}, {Kind: "openvpn-tcp", Port: tcpPort}} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if _, err := st.vpnHandshake(ctx, e); err != nil {
			t.Errorf("%s: %v", e.Kind, err)
		}
		cancel()
	}
	dead, _ := freePorts(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := st.vpnHandshake(ctx, Entry{Kind: "ikev2", Port: dead[0]}); err == nil {
		t.Error("an unanswered handshake succeeded")
	}

	// the IKE request's payload chain adds up: SA, KE (group 14), Nonce
	pkt, _ := ikeSAInit()
	next, off := pkt[16], 28
	var seen []byte
	for next != 0 {
		seen = append(seen, next)
		l := int(binary.BigEndian.Uint16(pkt[off+2:]))
		if next == 34 && (binary.BigEndian.Uint16(pkt[off+4:]) != 14 || l != 4+4+256) {
			t.Errorf("KE payload: group %d, length %d", binary.BigEndian.Uint16(pkt[off+4:]), l)
		}
		next, off = pkt[off], off+l
	}
	if off != len(pkt) || string(seen) != string([]byte{33, 34, 40}) {
		t.Errorf("payloads %v end at %d of %d", seen, off, len(pkt))
	}
}

func TestProfilesOnSubscriptionPage(t *testing.T) {
	st := keyedState(t)
	st.Domain = "vpn.example.com"
	st.ensureVPNCert()
	st.DNSDomain = "t.example.com"
	st.Sub = SubConfig{Mode: "direct", Port: 8443, Path: "p4th"}
	st.Entries = []Entry{{ID: "ovpn-udp", Kind: "openvpn-udp", Port: 1194}, {ID: "ikev2", Kind: "ikev2", Port: 500},
		{ID: "dnstt", Kind: "dnstt", Port: 5300, Resolver: "udp:8.8.8.8:53"}, {ID: "ssh", Kind: "ssh", Port: 2222}}
	ann := NewUser("ann")
	st.Users = []User{ann}
	us := &Usage{Users: map[string]*UserUsage{}}
	h := subHandler{load: func() (*State, *Usage, error) { return st, us, nil }}
	get := func(path, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	base := "/p4th/" + ann.SubToken
	page := get(base, "text/html").Body.String()
	for _, want := range []string{base + "/f/ann-ovpn-udp.ovpn", base + "/f/ann-ikev2.mobileconfig", base + "/f/ann-dnstt.txt", tunnelPassword(ann)} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if w := get(base+"/f/ann-dnstt.txt", ""); w.Code != 200 || !strings.Contains(w.Body.String(), st.DNSTTPub) ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "ann-dnstt.txt") {
		t.Errorf("profile download: %d %q", w.Code, w.Body.String())
	}
	if w := get(base+"/f/bob-dnstt.txt", ""); w.Code != 404 {
		t.Errorf("another user's file: %d", w.Code)
	}
	raw, _ := base64.StdEncoding.DecodeString(get(base, "").Body.String())
	if lines := strings.Split(string(raw), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "ssh://ann:") {
		t.Errorf("the subscription lists only share links: %q", raw)
	}
	// TLS and WebSocket SSH have no link scheme: a settings file says how to connect
	st.Entries = []Entry{{ID: "ssh-tls", Kind: "ssh-tls", Port: 2083, SNI: "www.a.com", Mode: "ws"}}
	if l := st.Link(st.Entries[0], ann, st.Entries[0].Knobs()); l != "" {
		t.Errorf("an ssh:// link for SSH over TLS: %s", l)
	}
	info := get(base+"/f/ann-ssh-tls.txt", "").Body.String()
	for _, want := range []string{"SSL/TLS", "SNI: www.a.com", "Upgrade: websocket", "Port: 2083", tunnelPassword(ann)} {
		if !strings.Contains(info, want) {
			t.Errorf("SSH settings lack %q:\n%s", want, info)
		}
	}
}

func TestParseResolvers(t *testing.T) {
	rs, err := parseResolvers("10.0.0.1, udp:9.9.9.9:5353,doh:https://dns.example/q,dot:dns.example:853")
	if err != nil || strings.Join(rs, " ") != "udp:10.0.0.1:53 udp:9.9.9.9:5353 doh:https://dns.example/q dot:dns.example:853" {
		t.Errorf("%v %v", rs, err)
	}
	for _, bad := range []string{"udp:1.2.3.4", "doh:http://x", "tcp:1.2.3.4:53", "dns.example"} {
		if _, err := parseResolvers(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	vs := ExpandVariants([]Entry{{ID: "dnstt", Kind: "dnstt", Port: 5300}}, "smart", "udp:10.0.0.1:53", "udp:8.8.8.8:53")
	if vs[0].K.Resolver != "udp:10.0.0.1:53" {
		t.Errorf("given resolvers come first: %s", vs[0].K.Resolver)
	}
	n := 0
	for _, v := range vs {
		if v.K.Resolver == "udp:8.8.8.8:53" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("a resolver given twice is tried %d times", n)
	}
}

// A check measures the deployment as its real user; only a test instance knows the MTU test peers.
func TestWireGuardHarnessPeers(t *testing.T) {
	bin := FindLocalXray("")
	if bin == "" {
		t.Skip("no local xray")
	}
	st := keyedState(t)
	u := NewUser("tune")
	u.WGIP = "10.66.0.2"
	e := Entry{ID: "wg", Kind: "wireguard", Port: 51820}
	key := func(testPeers bool) string {
		dir := t.TempDir()
		v := &Variant{Entry: e, K: Knobs{MTU: 1280}}
		h, err := startHarness(bin, st, u, []*Variant{v}, dir, testPeers)
		if err != nil {
			t.Fatal(err)
		}
		h.stop()
		var cfg struct{ Outbounds []J }
		readJSONFile(filepath.Join(dir, "harness.json"), &cfg)
		for _, ob := range cfg.Outbounds {
			if ob["protocol"] == "wireguard" {
				return ob["settings"].(map[string]any)["secretKey"].(string)
			}
		}
		return ""
	}
	own, _ := wgUserKey(u)
	alias, _ := wgUserKey(wgVariantUser(u, Knobs{MTU: 1280}))
	if got := key(false); got != own {
		t.Error("a check must connect with the user's own key")
	}
	if got := key(true); got != alias || alias == own {
		t.Error("a tune's MTU variant must connect as its own test peer")
	}
	peers := wgTestPeers(u, []Entry{e})
	if len(peers) != 2 || peers[1].WGIP == u.WGIP {
		t.Errorf("test peers: %+v", peers)
	}
}
