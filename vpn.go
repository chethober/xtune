package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chethober/xtune/internal/protocols"
	"github.com/chethober/xtune/internal/remote"
	"github.com/chethober/xtune/internal/state"
)

// OpenVPN and IKEv2 run on the distribution's openvpn and strongSwan (installed with --vpn).
// Users log in with their name and tunnelPassword; OpenVPN asks `xtune vpn-auth` and strongSwan
// gets EAP-MSCHAPv2 secrets for the active users. Both trust a CA of the deployment's own,
// which the profiles carry. Client traffic is NATed out of the server's default interface;
// clients cannot reach private addresses (as with Xray's geoip:private block).

const vpnDir = remote.Dir + "/vpn"

// ---------------------------------------------------------------- PKI

func makeVPNPKI(st *state.State) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "xtune VPN CA " + randHex(3)},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(15, 0, 0), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	st.VPNCA = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	st.VPNCAKey = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	st.VPNCert, st.VPNKey, st.VPNName = "", "", ""
	return nil
}

// ensureVPNCert issues the server certificate for the address clients use, again when it changed.
func ensureVPNCert(st *state.State) error {
	name := st.Addr()
	if st.VPNCert != "" && st.VPNName == name {
		return nil
	}
	if st.VPNCA == "" {
		if err := makeVPNPKI(st); err != nil {
			return err
		}
	}
	ca, err := protocols.ParseCertPEM(st.VPNCA)
	if err != nil {
		return err
	}
	b, _ := pem.Decode([]byte(st.VPNCAKey))
	if b == nil {
		return errors.New("bad VPN CA key")
	}
	caKey, err := x509.ParsePKCS1PrivateKey(b.Bytes)
	if err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		// IP security IKE intermediate: Windows wants it for IKEv2
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		UnknownExtKeyUsage: []asn1.ObjectIdentifier{{1, 3, 6, 1, 5, 5, 8, 2, 2}}}
	if ip := net.ParseIP(name); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{name}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	st.VPNCert = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	st.VPNKey = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	st.VPNName = name
	return nil
}

// ---------------------------------------------------------------- server configs

// vpnFiles are the files the VPN entries need in dir, by path.
func vpnFiles(st *state.State, entries []state.Entry, users []state.User, dir string) map[string]string {
	files := map[string]string{}
	uj, _ := json.Marshal(protocols.VPNUsers(users))
	files[dir+"/users.json"] = string(uj)
	for _, e := range entries {
		switch e.Engine() {
		case "openvpn":
			files[dir+"/"+e.ID+".conf"] = protocols.OpenVPNConf(st, e, dir)
		case "ikev2":
			files[dir+"/ikev2.crt"] = st.VPNCert
			files[dir+"/ikev2.key"] = st.VPNKey
			files[dir+"/swanctl.conf"] = protocols.SwanctlConf(st, users)
		}
	}
	return files
}

// vpnSpecs are the helper's arguments for the VPN entries: "openvpn:ID", "ikev2" and
// "net:SUBNET" for each client range to NAT.
func vpnSpecs(entries []state.Entry) []string {
	var out []string
	for _, e := range entries {
		switch e.Engine() {
		case "openvpn":
			out = append(out, "openvpn:"+e.ID, "net:"+protocols.VPNSubnet(e))
		case "ikev2":
			out = append(out, "ikev2", "net:"+protocols.VPNSubnet(e))
		}
	}
	return out
}

// ---------------------------------------------------------------- xtune vpn-auth

// cmdVPNAuth is OpenVPN's auth-user-pass-verify script: args are the users file and the file
// OpenVPN wrote the username and password to. A user over their device limit is refused.
func cmdVPNAuth(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: xtune vpn-auth USERS CREDFILE")
	}
	var users map[string]protocols.VPNUser
	if err := readJSONFile(args[0], &users); err != nil {
		return err
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	lines := strings.SplitN(string(raw), "\n", 3)
	if len(lines) < 2 {
		return errors.New("bad credentials file")
	}
	name, pass := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	u, ok := users[name]
	if !ok || u.Pass != pass {
		return errors.New("denied")
	}
	if u.Devices > 0 {
		ips := map[string]bool{}
		for _, s := range openvpnSessions(filepath.Dir(args[0])) {
			if s.User == name {
				ips[s.IP] = true
			}
		}
		if ip := os.Getenv("untrusted_ip"); !ips[ip] && len(ips) >= u.Devices {
			return errors.New("device limit")
		}
	}
	return nil
}

// ---------------------------------------------------------------- sessions and traffic

// vpnSession is one connected VPN client with its traffic so far.
type vpnSession struct {
	Key      string // stable for the session's lifetime
	User, IP string
	Up, Down int64
}

// parseOpenVPNStatus reads a status-version 2 file.
func parseOpenVPNStatus(id, s string) []vpnSession {
	var cols []string
	var out []vpnSession
	for _, line := range strings.Split(s, "\n") {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) > 2 && f[0] == "HEADER" && f[1] == "CLIENT_LIST" {
			cols = f[1:]
			continue
		}
		if len(f) < 2 || f[0] != "CLIENT_LIST" || cols == nil {
			continue
		}
		get := func(name string) string {
			for i, c := range cols {
				if c == name && i < len(f) {
					return f[i]
				}
			}
			return ""
		}
		user := get("Username")
		if user == "" || user == "UNDEF" {
			user = get("Common Name")
		}
		ip := get("Real Address")
		if h, _, err := net.SplitHostPort(ip); err == nil {
			ip = h
		} else if i := strings.LastIndex(ip, ":"); i > 0 { // "1.2.3.4:5678" without brackets for v6
			ip = ip[:i]
		}
		up, _ := strconv.ParseInt(get("Bytes Received"), 10, 64)
		down, _ := strconv.ParseInt(get("Bytes Sent"), 10, 64)
		out = append(out, vpnSession{Key: "ovpn/" + id + "/" + get("Client ID") + "/" + get("Connected Since (time_t)"),
			User: user, IP: ip, Up: up, Down: down})
	}
	return out
}

func openvpnSessions(dir string) []vpnSession {
	var out []vpnSession
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if id, ok := strings.CutSuffix(f.Name(), ".status"); ok {
			if b, err := os.ReadFile(filepath.Join(dir, f.Name())); err == nil {
				out = append(out, parseOpenVPNStatus(id, string(b))...)
			}
		}
	}
	return out
}

// swanNode is one section of `swanctl --list-sas --raw` output.
type swanNode struct {
	kv   map[string]string
	kids map[string]*swanNode
}

// parseSwanctlRaw parses the brace-nested key=value format `swanctl --raw` prints.
func parseSwanctlRaw(s string) *swanNode {
	toks := swanTokens(s)
	var parse func(i int) (*swanNode, int)
	parse = func(i int) (*swanNode, int) {
		n := &swanNode{kv: map[string]string{}, kids: map[string]*swanNode{}}
		for i < len(toks) {
			t := toks[i]
			switch {
			case t == "}":
				return n, i + 1
			case i+1 < len(toks) && toks[i+1] == "{":
				var kid *swanNode
				kid, i = parse(i + 2)
				for k := t; ; k += "'" { // names repeat across events
					if _, dup := n.kids[k]; !dup {
						n.kids[k] = kid
						break
					}
				}
			case strings.Contains(t, "="):
				k, v, _ := strings.Cut(t, "=")
				n.kv[k] = v
				i++
			default:
				i++
			}
		}
		return n, i
	}
	root, _ := parse(0)
	return root
}

func swanTokens(s string) []string {
	var toks []string
	var cur strings.Builder
	depth := 0 // inside [...] lists
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '[':
			depth++
			cur.WriteRune(r)
		case r == ']':
			depth--
			cur.WriteRune(r)
		case depth == 0 && (r == '{' || r == '}'):
			flush()
			toks = append(toks, string(r))
		case depth == 0 && (r == ' ' || r == '\n' || r == '\t'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return toks
}

// ikeSessions lists the IKEv2 clients of the xtune-ikev2 connection in `swanctl --list-sas --raw`
// output: traffic per child SA, which the session key names.
func ikeSessions(raw string) []vpnSession {
	var out []vpnSession
	var walk func(n *swanNode)
	walk = func(n *swanNode) {
		for name, sa := range n.kids {
			if strings.TrimRight(name, "'") != "xtune-ikev2" || sa.kv["uniqueid"] == "" {
				walk(sa)
				continue
			}
			user := orStr(sa.kv["remote-eap-id"], sa.kv["remote-id"])
			cs := sa.kids["child-sas"]
			if cs == nil {
				continue
			}
			for _, c := range cs.kids {
				in, _ := strconv.ParseInt(c.kv["bytes-in"], 10, 64)
				outB, _ := strconv.ParseInt(c.kv["bytes-out"], 10, 64)
				out = append(out, vpnSession{Key: "ike/" + sa.kv["uniqueid"] + "/" + c.kv["uniqueid"], User: user,
					IP: sa.kv["remote-host"], Up: in, Down: outB})
			}
		}
	}
	walk(parseSwanctlRaw(raw))
	return out
}

// ---------------------------------------------------------------- profiles

// hasLogin is true when the deployment has configs users log in to with name and password.
func hasLogin(st *state.State) bool {
	for _, s := range protocols.PeerServers(st) {
		if state.HasEngine(s.Entries, "tunnel") || state.HasEngine(s.Entries, "openvpn") || state.HasEngine(s.Entries, "ikev2") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- handshake probes

// vpnHandshake sends one handshake to a VPN entry and waits for the server's answer. It reports
// the round trip.
func vpnHandshake(st *state.State, ctx context.Context, e state.Entry) (time.Duration, error) {
	addr := protocols.HostPort(st.Addr(), e.Port)
	var d net.Dialer
	t0 := time.Now()
	switch e.Kind {
	case "openvpn-tcp":
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return 0, err
		}
		defer c.Close()
		setDeadline(ctx, c)
		pkt := openvpnReset()
		frame := binary.BigEndian.AppendUint16(nil, uint16(len(pkt)))
		if _, err := c.Write(append(frame, pkt...)); err != nil {
			return 0, err
		}
		var hdr [2]byte
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return 0, err
		}
		resp := make([]byte, binary.BigEndian.Uint16(hdr[:]))
		if _, err := io.ReadFull(c, resp); err != nil {
			return 0, err
		}
		if !openvpnResetReply(resp) {
			return 0, errors.New("not an OpenVPN answer")
		}
	case "openvpn-udp", "ikev2":
		c, err := d.DialContext(ctx, "udp", addr)
		if err != nil {
			return 0, err
		}
		defer c.Close()
		setDeadline(ctx, c)
		pkt, ok := openvpnReset(), openvpnResetReply
		if e.Kind == "ikev2" {
			var spi []byte
			pkt, spi = ikeSAInit()
			ok = func(b []byte) bool { return ikeReply(b, spi) }
		}
		buf := make([]byte, 4096)
		for try := 0; try < 3; try++ { // UDP: resend a lost packet
			c.Write(pkt)
			c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
			if dl, has := ctx.Deadline(); has && time.Until(dl) < 1500*time.Millisecond {
				c.SetReadDeadline(dl)
			}
			n, err := c.Read(buf)
			if err == nil && ok(buf[:n]) {
				return time.Since(t0), nil
			}
			if ctx.Err() != nil {
				break
			}
		}
		return 0, errors.New("no answer")
	default:
		return 0, fmt.Errorf("%s has no handshake probe", e.Kind)
	}
	return time.Since(t0), nil
}

func setDeadline(ctx context.Context, c net.Conn) {
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
}

// openvpnReset is a P_CONTROL_HARD_RESET_CLIENT_V2 packet: a fresh session, no ACKs, packet 0.
func openvpnReset() []byte {
	b := []byte{7 << 3}
	sid := make([]byte, 8)
	rand.Read(sid)
	b = append(b, sid...)
	b = append(b, 0)             // no ACKs
	return append(b, 0, 0, 0, 0) // packet ID 0
}

// openvpnResetReply is true for a P_CONTROL_HARD_RESET_SERVER_V2.
func openvpnResetReply(b []byte) bool { return len(b) >= 9 && b[0]>>3 == 8 }

// ikeSAInit is an IKE_SA_INIT request offering AES-256/SHA-256/MODP-2048 (which xtune's
// strongSwan config accepts). Any IKE answer, even a refusal, shows the server is reachable.
func ikeSAInit() (pkt, spi []byte) {
	spi = make([]byte, 8)
	rand.Read(spi)
	transform := func(last bool, typ byte, id uint16, attr []byte) []byte {
		more := byte(3)
		if last {
			more = 0
		}
		t := []byte{more, 0, 0, 0, typ, 0, byte(id >> 8), byte(id)}
		t = append(t, attr...)
		binary.BigEndian.PutUint16(t[2:], uint16(len(t)))
		return t
	}
	var ts []byte
	ts = append(ts, transform(false, 1, 12, []byte{0x80, 14, 1, 0})...) // ENCR AES-CBC, key length 256
	ts = append(ts, transform(false, 2, 5, nil)...)                     // PRF HMAC-SHA2-256
	ts = append(ts, transform(false, 3, 12, nil)...)                    // INTEG HMAC-SHA2-256-128
	ts = append(ts, transform(true, 4, 14, nil)...)                     // DH MODP-2048
	prop := append([]byte{0, 0, 0, 0, 1, 1, 0, 4}, ts...)               // last proposal, #1, IKE, no SPI, 4 transforms
	binary.BigEndian.PutUint16(prop[2:], uint16(len(prop)))
	payload := func(next byte, body []byte) []byte {
		p := append([]byte{next, 0, 0, 0}, body...)
		binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
		return p
	}
	ke := make([]byte, 4+256)
	ke[1] = 14 // DH group 14
	rand.Read(ke[4:])
	ke[4] &= 0x7f // keep the public value below the MODP-2048 prime
	nonce := make([]byte, 32)
	rand.Read(nonce)
	body := payload(34, prop)                 // SA, next: KE
	body = append(body, payload(40, ke)...)   // KE, next: Nonce
	body = append(body, payload(0, nonce)...) // Nonce
	hdr := append(append([]byte{}, spi...), make([]byte, 8)...)
	hdr = append(hdr, 33, 0x20, 34, 0x08, 0, 0, 0, 0, 0, 0, 0, 0) // next SA, v2.0, IKE_SA_INIT, initiator, msg 0, length
	pkt = append(hdr, body...)
	binary.BigEndian.PutUint32(pkt[24:], uint32(len(pkt)))
	return pkt, spi
}

// ikeReply is true for an IKE_SA_INIT response to the request with this SPI.
func ikeReply(b, spi []byte) bool {
	return len(b) >= 28 && bytes.Equal(b[:8], spi) && b[18] == 34 && b[19]&0x20 != 0
}
