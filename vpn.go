package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OpenVPN and IKEv2 run on the distribution's openvpn and strongSwan (installed with --vpn).
// Users log in with their name and tunnelPassword; OpenVPN asks `xtune vpn-auth` and strongSwan
// gets EAP-MSCHAPv2 secrets for the active users. Both trust a CA of the deployment's own,
// which the profiles carry. Client traffic is NATed out of the server's default interface;
// clients cannot reach private addresses (as with Xray's geoip:private block).

const vpnDir = remoteDir + "/vpn"

// vpnSubnet is each VPN entry's client address range.
func vpnSubnet(e Entry) string {
	switch e.Kind {
	case "openvpn-udp":
		return "10.68.0.0/20"
	case "openvpn-tcp":
		return "10.68.16.0/20"
	}
	return "10.69.0.0/16" // ikev2
}

// ---------------------------------------------------------------- PKI

func (st *State) makeVPNPKI() error {
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
func (st *State) ensureVPNCert() error {
	name := st.Addr()
	if st.VPNCert != "" && st.VPNName == name {
		return nil
	}
	if st.VPNCA == "" {
		if err := st.makeVPNPKI(); err != nil {
			return err
		}
	}
	ca, err := parseCertPEM(st.VPNCA)
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

// vpnUser is what `xtune vpn-auth` checks an OpenVPN login against.
type vpnUser struct {
	Pass    string `json:"pass"`
	Devices int    `json:"devices,omitempty"`
}

func vpnUsers(users []User) map[string]vpnUser {
	out := map[string]vpnUser{}
	for _, u := range users {
		if u.Enabled {
			out[u.Name] = vpnUser{Pass: tunnelPassword(u), Devices: u.Devices}
		}
	}
	return out
}

// OpenVPNConf is the server config of one OpenVPN entry; files live in dir.
func (st *State) OpenVPNConf(e Entry, dir string) string {
	proto := "udp"
	if e.Kind == "openvpn-tcp" {
		proto = "tcp-server"
	}
	_, n, _ := net.ParseCIDR(vpnSubnet(e))
	var b strings.Builder
	fmt.Fprintf(&b, `# written by xtune
port %d
proto %s
dev tun
topology subnet
server %s %s
dh none
ecdh-curve prime256v1
tls-version-min 1.2
data-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305
verify-client-cert none
username-as-common-name
duplicate-cn
script-security 2
auth-user-pass-verify "%s/xtune vpn-auth %s/users.json" via-file
push "redirect-gateway def1 bypass-dhcp"
push "dhcp-option DNS 1.1.1.1"
push "dhcp-option DNS 8.8.8.8"
push "block-outside-dns"
keepalive 10 60
persist-key
persist-tun
status %s/%s.status 10
status-version 2
management %s/%s.sock unix
verb 1
`, e.Port, proto, n.IP, net.IP(n.Mask), remoteDir, dir, dir, e.ID, dir, e.ID)
	if proto == "udp" {
		b.WriteString("explicit-exit-notify 1\n")
	}
	fmt.Fprintf(&b, "<ca>\n%s</ca>\n<cert>\n%s</cert>\n<key>\n%s</key>\n", st.VPNCA, st.VPNCert, st.VPNKey)
	return b.String()
}

// SwanctlConf is strongSwan's config for the ikev2 entry and the active users' EAP secrets. The
// helper installs the certificate and key into strongSwan's x509 and private directories, the
// only place it loads unencrypted keys from.
func (st *State) SwanctlConf(users []User) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# written by xtune
connections {
  xtune-ikev2 {
    version = 2
    proposals = aes256gcm16-prfsha384-ecp384,aes256-sha256-modp2048,aes256-sha256-ecp256,aes128-sha256-modp2048,aes256-sha1-modp2048,aes256-sha1-modp1024,default
    local_addrs = %%any
    pools = xtune-v4
    send_cert = always
    unique = never
    dpd_delay = 30s
    local {
      auth = pubkey
      certs = xtune-ikev2.crt
      id = %s
    }
    remote {
      auth = eap-mschapv2
      eap_id = %%any
    }
    children {
      net {
        local_ts = 0.0.0.0/0
        esp_proposals = aes256gcm16,aes128gcm16,aes256-sha256,aes256-sha1,default
        dpd_action = clear
      }
    }
  }
}
pools {
  xtune-v4 {
    addrs = %s
    dns = 1.1.1.1, 8.8.8.8
  }
}
secrets {
`, st.VPNName, vpnSubnet(Entry{Kind: "ikev2"}))
	names := []string{}
	pass := map[string]string{}
	for n, u := range vpnUsers(users) {
		names = append(names, n)
		pass[n] = u.Pass
	}
	sort.Strings(names)
	for i, n := range names {
		fmt.Fprintf(&b, "  eap-%d {\n    id = %s\n    secret = \"%s\"\n  }\n", i, n, pass[n])
	}
	b.WriteString("}\n")
	return b.String()
}

// vpnFiles are the files the VPN entries need in dir, by path.
func (st *State) vpnFiles(entries []Entry, users []User, dir string) map[string]string {
	files := map[string]string{}
	uj, _ := json.Marshal(vpnUsers(users))
	files[dir+"/users.json"] = string(uj)
	for _, e := range entries {
		switch e.Engine() {
		case "openvpn":
			files[dir+"/"+e.ID+".conf"] = st.OpenVPNConf(e, dir)
		case "ikev2":
			files[dir+"/ikev2.crt"] = st.VPNCert
			files[dir+"/ikev2.key"] = st.VPNKey
			files[dir+"/swanctl.conf"] = st.SwanctlConf(users)
		}
	}
	return files
}

// vpnSpecs are the helper's arguments for the VPN entries: "openvpn:ID", "ikev2" and
// "net:SUBNET" for each client range to NAT.
func vpnSpecs(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		switch e.Engine() {
		case "openvpn":
			out = append(out, "openvpn:"+e.ID, "net:"+vpnSubnet(e))
		case "ikev2":
			out = append(out, "ikev2", "net:"+vpnSubnet(e))
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
	var users map[string]vpnUser
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

// Profile is a file a user imports into an app.
type Profile struct {
	Name string // shown to the user
	File string
	Type string // MIME type
	Body string
}

// Profiles are the user's files for the entries that have no share link, and a WireGuard config
// for the official app.
func (st *State) Profiles(u User) []Profile {
	var out []Profile
	for _, s := range st.servers() {
		for _, e := range s.Entries {
			out = append(out, s.profiles(e, u)...)
		}
	}
	return out
}

func (st *State) profiles(e Entry, u User) []Profile {
	base := u.Name + "-" + e.ID
	switch e.Kind {
	case "wireguard":
		return []Profile{{Name: "WireGuard app (" + e.ID + ")", File: base + ".conf", Type: "text/plain", Body: st.WGConf(e, u)}}
	case "openvpn-udp", "openvpn-tcp":
		return []Profile{{Name: "OpenVPN (" + e.ID + ")", File: base + ".ovpn", Type: "application/x-openvpn-profile", Body: st.OVPNProfile(e, u)}}
	case "ikev2":
		return []Profile{
			{Name: "IKEv2, strongSwan app (Android)", File: base + ".sswan", Type: "application/vnd.strongswan.profile", Body: st.SswanProfile(u)},
			{Name: "IKEv2, iPhone / iPad / Mac", File: base + ".mobileconfig", Type: "application/x-apple-aspen-config", Body: st.MobileConfig(u)},
		}
	case "dnstt":
		return []Profile{{Name: "DNS tunnel (SlowDNS / dnstt)", File: base + ".txt", Type: "text/plain", Body: st.DNSTTInfo(e, u)}}
	case "ssh", "ssh-tls", "cdn-ssh":
		return []Profile{{Name: "SSH apps, HTTP Injector / NetMod (" + e.ID + ")", File: base + ".txt", Type: "text/plain", Body: st.SSHInfo(e, u)}}
	}
	return nil
}

// hasLogin is true when the deployment has configs users log in to with name and password.
func (st *State) hasLogin() bool {
	for _, s := range st.servers() {
		if hasEngine(s.Entries, "tunnel") || hasEngine(s.Entries, "openvpn") || hasEngine(s.Entries, "ikev2") {
			return true
		}
	}
	return false
}

// MobileConfig is an Apple configuration profile (iOS, macOS) with the CA and the IKEv2 VPN.
func (st *State) MobileConfig(u User) string {
	ca, _ := pem.Decode([]byte(st.VPNCA))
	caCert, _ := parseCertPEM(st.VPNCA)
	id := func(s string) string { return strings.ToUpper(hex.EncodeToString(secret(u, "mobileconfig/"+s)[:16])) }
	cn := ""
	if caCert != nil {
		cn = caCert.Subject.CommonName
	}
	x := html.EscapeString
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>PayloadContent</key>
  <array>
    <dict>
      <key>PayloadType</key><string>com.apple.security.root</string>
      <key>PayloadIdentifier</key><string>xtune.ca.%s</string>
      <key>PayloadUUID</key><string>%s</string>
      <key>PayloadVersion</key><integer>1</integer>
      <key>PayloadDisplayName</key><string>%s</string>
      <key>PayloadContent</key><data>%s</data>
    </dict>
    <dict>
      <key>PayloadType</key><string>com.apple.vpn.managed</string>
      <key>PayloadIdentifier</key><string>xtune.vpn.%s</string>
      <key>PayloadUUID</key><string>%s</string>
      <key>PayloadVersion</key><integer>1</integer>
      <key>PayloadDisplayName</key><string>xtune IKEv2</string>
      <key>UserDefinedName</key><string>xtune %s</string>
      <key>VPNType</key><string>IKEv2</string>
      <key>IKEv2</key>
      <dict>
        <key>RemoteAddress</key><string>%s</string>
        <key>RemoteIdentifier</key><string>%s</string>
        <key>LocalIdentifier</key><string>%s</string>
        <key>AuthenticationMethod</key><string>None</string>
        <key>ExtendedAuthEnabled</key><integer>1</integer>
        <key>AuthName</key><string>%s</string>
        <key>AuthPassword</key><string>%s</string>
        <key>ServerCertificateIssuerCommonName</key><string>%s</string>
        <key>EnablePFS</key><integer>1</integer>
        <key>IKESecurityAssociationParameters</key>
        <dict><key>EncryptionAlgorithm</key><string>AES-256</string><key>IntegrityAlgorithm</key><string>SHA2-256</string><key>DiffieHellmanGroup</key><integer>14</integer></dict>
        <key>ChildSecurityAssociationParameters</key>
        <dict><key>EncryptionAlgorithm</key><string>AES-256</string><key>IntegrityAlgorithm</key><string>SHA2-256</string><key>DiffieHellmanGroup</key><integer>14</integer></dict>
      </dict>
    </dict>
  </array>
  <key>PayloadDisplayName</key><string>xtune %s</string>
  <key>PayloadIdentifier</key><string>xtune.%s</string>
  <key>PayloadType</key><string>Configuration</string>
  <key>PayloadUUID</key><string>%s</string>
  <key>PayloadVersion</key><integer>1</integer>
</dict>
</plist>
`, id("ca"), uuidFmt(id("ca")), x(cn), base64.StdEncoding.EncodeToString(ca.Bytes),
		id("vpn"), uuidFmt(id("vpn")), x(u.Name), x(st.VPNName), x(st.VPNName), x(u.Name), x(u.Name), x(tunnelPassword(u)), x(cn),
		x(u.Name), id("root"), uuidFmt(id("root")))
}

func uuidFmt(h string) string {
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// OVPNProfile is an OpenVPN client profile; the app asks for the user name and password.
func (st *State) OVPNProfile(e Entry, u User) string {
	proto := "udp"
	if e.Kind == "openvpn-tcp" {
		proto = "tcp"
	}
	return fmt.Sprintf(`# xtune: %s, user name %s, password %s
client
dev tun
proto %s
remote %s %d
nobind
persist-key
persist-tun
remote-cert-tls server
verify-x509-name %s name
auth-user-pass
auth-nocache
data-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305
verb 3
<ca>
%s</ca>
`, e.ID, u.Name, tunnelPassword(u), proto, st.VPNName, e.Port, st.VPNName, st.VPNCA)
}

// SswanProfile is a strongSwan app profile: the CA is included, the app asks for the password.
func (st *State) SswanProfile(u User) string {
	ca, _ := pem.Decode([]byte(st.VPNCA))
	b, _ := json.MarshalIndent(J{"uuid": uuidFmt(hex.EncodeToString(secret(u, "sswan")[:16])), "name": "xtune " + u.Name,
		"type": "ikev2-eap", "remote": J{"addr": st.VPNName, "id": st.VPNName, "cert": base64.StdEncoding.EncodeToString(ca.Bytes)},
		"local": J{"eap_id": u.Name}}, "", "  ")
	return string(b)
}

// DNSTTInfo is what SlowDNS/dnstt apps (HTTP Injector, NetMod, ...) need.
func (st *State) DNSTTInfo(e Entry, u User) string {
	return fmt.Sprintf(`DNS tunnel (dnstt / SlowDNS) for %s

Name server (NS) domain: %s
Public key: %s
DNS resolver: %s (or any resolver that works on your network, e.g. 8.8.8.8, 1.1.1.1)
SSH host: 127.0.0.1 or %s, port 22 (the tunnel ends at the SSH server)
SSH user name: %s
SSH password: %s

It is slow (tens of KB/s): use it when nothing else gets through.
`, u.Name, st.DNSDomain, st.DNSTTPub, strings.SplitN(orStr(e.Resolver, dnsResolvers[0]), ":", 2)[1], st.Addr(), u.Name, tunnelPassword(u))
}

// ---------------------------------------------------------------- handshake probes

// vpnHandshake sends one handshake to a VPN entry and waits for the server's answer. It reports
// the round trip.
func (st *State) vpnHandshake(ctx context.Context, e Entry) (time.Duration, error) {
	addr := hostPort(st.Addr(), e.Port)
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
