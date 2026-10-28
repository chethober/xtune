package main

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
	"www.bamsoftware.com/git/dnstt.git/noise"
)

// Besides Xray's protocols, xtune serves:
//
//   - wireguard: Xray's WireGuard inbound, tuned and accounted like every other Xray config.
//   - trojan-reality: Trojan over REALITY (opt-in), for apps and networks that do better with
//     Trojan than VLESS; it shares the REALITY keys and targets.
//   - amneziawg: WireGuard with a disguised handshake, served by xtune itself (awg.go).
//   - worker-ws: VLESS over WebSocket through a Cloudflare Worker, relayed to a plain TCP
//     inbound of Xray's (worker.go). It needs a Cloudflare API token.
//   - ssh, ssh-tls, cdn-ssh: an SSH server inside xtune (`xtune tunnels`) that only forwards
//     TCP, for apps such as HTTP Injector, NetMod and NapsternetV. ssh-tls wraps it in TLS
//     ("SSH-SSL"); cdn-ssh is the same TLS port reached through the CDN domain with a WebSocket
//     upgrade. No system accounts are made: users and limits come from the state file.
//   - dnstt: a DNS tunnel (dnstt) to that SSH server, the "SlowDNS" layout. It needs a domain
//     delegated to the server with an NS record, and it is slow: a last resort for networks
//     where only DNS gets through.
//   - openvpn-udp, openvpn-tcp, ikev2: the distribution's OpenVPN and strongSwan. Clients need
//     root to bring a VPN up, so tuning measures only whether and how fast the handshake
//     answers from here.

// kindInfo describes each kind: what serves it and how it is measured.
var kindInfo = map[string]struct {
	Engine    string // xray | tunnel | awg | openvpn | ikev2
	Handshake bool   // tuning can only measure the handshake
}{
	"reality-tcp": {Engine: "xray"}, "reality-xhttp": {Engine: "xray"}, "reality-grpc": {Engine: "xray"},
	"vlessenc-tcp": {Engine: "xray"}, "vlessenc-xhttp": {Engine: "xray"}, "cdn-xhttp": {Engine: "xray"},
	"hysteria2": {Engine: "xray"}, "ss2022": {Engine: "xray"}, "wireguard": {Engine: "xray"}, "worker-ws": {Engine: "xray"},
	"trojan-reality": {Engine: "xray"},
	"amneziawg":      {Engine: "awg"},
	"ssh":            {Engine: "tunnel"}, "ssh-tls": {Engine: "tunnel"}, "cdn-ssh": {Engine: "tunnel"}, "dnstt": {Engine: "tunnel"},
	"mtproto":     {Engine: "tunnel", Handshake: true},
	"openvpn-udp": {Engine: "openvpn", Handshake: true}, "openvpn-tcp": {Engine: "openvpn", Handshake: true},
	"ikev2": {Engine: "ikev2", Handshake: true},
}

// Engine is what serves the entry on the server.
func (e Entry) Engine() string {
	if k, ok := kindInfo[e.Kind]; ok {
		return k.Engine
	}
	return "xray"
}

func (e Entry) Xray() bool { return e.Engine() == "xray" }

// HandshakeOnly entries are VPNs: tuning sees whether the handshake answers, not throughput.
func (e Entry) HandshakeOnly() bool { return kindInfo[e.Kind].Handshake }

// SSH entries are served by xtune's SSH server; dnstt carries that server too.
func (e Entry) SSH() bool { return e.Engine() == "tunnel" }

func filterEntries(es []Entry, keep func(Entry) bool) []Entry {
	var out []Entry
	for _, e := range es {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

func hasEngine(es []Entry, engine string) bool {
	for _, e := range es {
		if e.Engine() == engine {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- keys

// ensureKeys generates the keys of the newer protocols, for states made before they existed.
// It reports whether anything was added.
func (st *State) ensureKeys() (bool, error) {
	added := false
	if st.WGPriv == "" {
		k, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return false, err
		}
		st.WGPriv = base64.StdEncoding.EncodeToString(k.Bytes())
		st.WGPub = base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
		added = true
	}
	if st.SSHHostKey == "" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return false, err
		}
		blk, err := ssh.MarshalPrivateKey(priv, "")
		if err != nil {
			return false, err
		}
		signer, err := ssh.NewSignerFromKey(priv)
		if err != nil {
			return false, err
		}
		st.SSHHostKey = string(pem.EncodeToMemory(blk))
		st.SSHHostPub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
		added = true
	}
	if st.DNSTTKey == "" {
		priv, err := noise.GeneratePrivkey()
		if err != nil {
			return false, err
		}
		st.DNSTTKey = hex.EncodeToString(priv)
		st.DNSTTPub = hex.EncodeToString(noise.PubkeyFromPrivkey(priv))
		added = true
	}
	if st.AWG == nil {
		p, err := newAWGParams()
		if err != nil {
			return false, err
		}
		st.AWG = p
		added = true
	}
	return added, nil // the VPN CA comes with the first VPN entry (ensureVPNCert)
}

// keepIdentity takes over another state's keys, so a redeploy keeps every client working.
func (st *State) keepIdentity(old *State) {
	st.RealityPriv, st.RealityPub, st.ShortID = old.RealityPriv, old.RealityPub, old.ShortID
	st.VencDec, st.VencEnc, st.SSKey = old.VencDec, old.VencEnc, old.SSKey
	st.CertSNI, st.CertHash, st.CertPEM, st.KeyPEM = old.CertSNI, old.CertHash, old.CertPEM, old.KeyPEM
	if old.WGPriv != "" {
		st.WGPriv, st.WGPub = old.WGPriv, old.WGPub
	}
	if old.SSHHostKey != "" {
		st.SSHHostKey, st.SSHHostPub = old.SSHHostKey, old.SSHHostPub
	}
	if old.DNSTTKey != "" {
		st.DNSTTKey, st.DNSTTPub = old.DNSTTKey, old.DNSTTPub
	}
	if old.AWG != nil {
		st.AWG = old.AWG
	}
	if old.VPNCA != "" {
		st.VPNCA, st.VPNCAKey, st.VPNCert, st.VPNKey, st.VPNName = old.VPNCA, old.VPNCAKey, old.VPNCert, old.VPNKey, old.VPNName
	}
}

// secret derives a per-user secret for one protocol from the user's UUID, so rotating a user
// changes every credential at once and followers, which only get the UUID, agree on them.
func secret(u User, proto string) []byte {
	h := sha256.Sum256([]byte("xtune/" + proto + "/" + u.UUID))
	return h[:]
}

// tunnelPassword is the user's password for SSH, OpenVPN and IKEv2.
func tunnelPassword(u User) string {
	return base64.RawURLEncoding.EncodeToString(secret(u, "password"))[:20]
}

// ---------------------------------------------------------------- WireGuard

// wgSubnet holds the users' tunnel addresses; the server never needs one of its own.
var wgSubnet = netip.MustParsePrefix("10.66.0.0/16")

func wgUserKey(u User) (priv, pub string) {
	k, err := ecdh.X25519().NewPrivateKey(secret(u, "wireguard"))
	if err != nil {
		panic(err) // any 32 bytes are an X25519 key
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

// assignWGIPs gives every user (and the check user) a tunnel address that stays theirs.
func (st *State) assignWGIPs() {
	used := map[string]bool{}
	users := []*User{}
	for i := range st.Users {
		users = append(users, &st.Users[i])
	}
	if st.Check != nil {
		users = append(users, st.Check)
	}
	for _, u := range users {
		if u.WGIP != "" {
			used[u.WGIP] = true
		}
	}
	next := wgSubnet.Addr().Next().Next() // .0.2
	for _, u := range users {
		if u.WGIP != "" {
			continue
		}
		for used[next.String()] && wgSubnet.Contains(next) {
			next = next.Next()
		}
		u.WGIP = next.String()
		used[u.WGIP] = true
	}
}

// wgVariantUser is who a tuning variant connects as. WireGuard peers roam: two variants with one
// key on the same inbound would keep taking the peer's endpoint from each other. So each MTU
// variant is its own test peer, with a key and address derived from the test user.
func wgVariantUser(u User, k Knobs) User {
	if k.MTU == 0 || k.MTU == 1420 {
		return u
	}
	a := u
	a.Name = fmt.Sprintf("%s-mtu%d", u.Name, k.MTU)
	a.UUID = fmt.Sprintf("%s/mtu%d", u.UUID, k.MTU)
	a.wgOnly = true
	if ip, err := netip.ParseAddr(u.WGIP); err == nil {
		a.WGIP = ip.Next().String()
	}
	return a
}

// wgTestPeers are the test users the server needs for the WireGuard variants: u and its MTU aliases.
func wgTestPeers(u User, entries []Entry) []User {
	if !slices.ContainsFunc(entries, func(e Entry) bool { return e.Kind == "wireguard" }) {
		return []User{u}
	}
	return []User{u, wgVariantUser(u, Knobs{MTU: 1280})}
}

func wgMTU(k Knobs) int {
	if k.MTU > 0 {
		return k.MTU
	}
	return 1420
}

func (st *State) wgInbound(ib J, users []User) {
	var peers []J
	for _, u := range users {
		_, pub := wgUserKey(u)
		peers = append(peers, J{"publicKey": pub, "allowedIPs": []string{u.WGIP + "/32"}, "email": u.Name})
	}
	ib["protocol"] = "wireguard"
	ib["settings"] = J{"secretKey": st.WGPriv, "peers": peers, "mtu": 1420}
}

func (st *State) wgOutbound(e Entry, u User, k Knobs, tag string) J {
	priv, _ := wgUserKey(u)
	return J{"tag": tag, "protocol": "wireguard", "settings": J{"secretKey": priv, "address": []string{u.WGIP + "/32"},
		"peers":       []J{{"publicKey": st.WGPub, "endpoint": hostPort(st.Addr(), e.Port), "keepAlive": 25}},
		"mtu":         wgMTU(k),
		"noKernelTun": true}}
}

func (st *State) wgLink(e Entry, u User, k Knobs, name string) string {
	priv, _ := wgUserKey(u)
	q := url.Values{"publickey": {st.WGPub}, "address": {u.WGIP + "/32"}, "mtu": {fmt.Sprint(wgMTU(k))}}
	return fmt.Sprintf("wireguard://%s@%s?%s#%s", url.QueryEscape(priv), hostPort(st.Addr(), e.Port), q.Encode(), name)
}

// WGConf is the config file for the official WireGuard apps.
func (st *State) WGConf(e Entry, u User) string {
	priv, _ := wgUserKey(u)
	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/32
DNS = 1.1.1.1, 8.8.8.8
MTU = %d

[Peer]
PublicKey = %s
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = %s
PersistentKeepalive = 25
`, priv, u.WGIP, wgMTU(e.Knobs()), st.WGPub, hostPort(st.Addr(), e.Port))
}

// ---------------------------------------------------------------- SSH links

// sshLink is the ssh:// link NekoBox, Hiddify and sing-box based apps import, for plain SSH. The
// host key is pinned with the public_key parameter.
func (st *State) sshLink(e Entry, u User, name string) string {
	q := url.Values{"public_key": {st.SSHHostPub}}
	return fmt.Sprintf("ssh://%s:%s@%s?%s#%s", url.PathEscape(u.Name), url.PathEscape(tunnelPassword(u)),
		hostPort(st.Addr(), e.Port), q.Encode(), name)
}

// SSHInfo is what HTTP Injector, NetMod and similar apps need for an SSH entry: where to connect,
// whether to wrap it in TLS (with which SNI), and the payload to send first.
func (st *State) SSHInfo(e Entry, u User) string {
	host, mode := st.Addr(), "SSH (direct)"
	sni, payload := "", ""
	switch e.Kind {
	case "ssh-tls":
		mode, sni = "SSH over SSL/TLS", orStr(e.SNI, st.CertSNI)
	case "cdn-ssh":
		host, mode, sni = st.CDNDomain, "SSH over SSL/TLS, through the CDN (WebSocket)", st.CDNDomain
	}
	if e.Mode == "ws" || e.CDN() {
		wsHost := orStr(sni, "www.bing.com")
		payload = "GET / HTTP/1.1[crlf]Host: " + wsHost + "[crlf]Upgrade: websocket[crlf]Connection: Upgrade[crlf][crlf]"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s for %s\n\nMode: %s\nServer: %s\nPort: %d\n", e.ID, u.Name, mode, host, e.Port)
	if sni != "" {
		fmt.Fprintf(&b, "SNI: %s (the server's certificate is self-signed: do not verify it)\n", sni)
	}
	if payload != "" {
		fmt.Fprintf(&b, "Payload: %s\n", payload)
	}
	fmt.Fprintf(&b, "SSH user name: %s\nSSH password: %s\nSSH host key: %s\n", u.Name, tunnelPassword(u), st.SSHHostPub)
	return b.String()
}

// parseCertPEM decodes the first certificate in a PEM block.
func parseCertPEM(s string) (*x509.Certificate, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		return nil, fmt.Errorf("no PEM certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}

// parseResolvers checks a --resolvers list. A bare IP means plain DNS on port 53.
func parseResolvers(list string) ([]string, error) {
	var out []string
	for _, r := range strings.Split(list, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if net.ParseIP(r) != nil {
			r = "udp:" + net.JoinHostPort(r, "53")
		}
		kind, addr, _ := strings.Cut(r, ":")
		switch {
		case kind == "udp" || kind == "dot":
			if _, _, err := net.SplitHostPort(addr); err != nil {
				return nil, fmt.Errorf("resolver %q: want %s:HOST:PORT", r, kind)
			}
		case kind == "doh" && strings.HasPrefix(addr, "https://"):
		default:
			return nil, fmt.Errorf("resolver %q: want an IP, udp:IP:PORT, doh:https://URL or dot:HOST:PORT", r)
		}
		out = append(out, r)
	}
	return out, nil
}

// family groups kinds into the opt-in protocols of --protocols ("" for the default matrix).
func (e Entry) family() string {
	switch e.Kind {
	case "ssh", "ssh-tls", "cdn-ssh":
		return "ssh"
	case "dnstt":
		return "dnstt"
	case "mtproto":
		return "mtproto"
	case "openvpn-udp", "openvpn-tcp":
		return "openvpn"
	case "ikev2":
		return "ikev2"
	case "amneziawg":
		return "awg"
	case "worker-ws":
		return "worker"
	case "trojan-reality":
		return "trojan"
	}
	return ""
}

// inherit fills unset options from the current deployment: its addresses, and the opt-in
// protocols it runs, so a re-tune keeps offering them.
func (o *TuneOpts) inherit(old *State, set map[string]bool) {
	if old == nil {
		return
	}
	if !set["domain"] {
		o.Domain = old.Domain
	}
	if !set["cdn"] {
		o.CDNDomain = old.CDNDomain
	}
	if !set["dns-domain"] {
		o.DNSDomain = old.DNSDomain
	}
	if !set["protocols"] {
		for _, e := range old.Entries {
			if f := e.family(); f != "" && !slices.Contains(o.Protocols, f) {
				o.Protocols = append(o.Protocols, f)
			}
		}
	}
}
