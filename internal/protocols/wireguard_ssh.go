package protocols

import (
	"crypto/ecdh"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/url"
	"strings"

	"github.com/chethober/xtune/internal/state"
)

// secret derives a per-user secret for one protocol from the user's UUID, so rotating a user
// changes every credential at once and followers, which only get the UUID, agree on them.
func secret(u state.User, proto string) []byte {
	h := sha256.Sum256([]byte("xtune/" + proto + "/" + u.UUID))
	return h[:]
}

// TunnelPassword is the user's password for SSH, OpenVPN and IKEv2.
func TunnelPassword(u state.User) string {
	return base64.RawURLEncoding.EncodeToString(secret(u, "password"))[:20]
}

func WGUserKey(u state.User) (priv, pub string) {
	k, err := ecdh.X25519().NewPrivateKey(secret(u, "wireguard"))
	if err != nil {
		panic(err) // any 32 bytes are an X25519 key
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

func WGMTU(k state.Knobs) int {
	if k.MTU > 0 {
		return k.MTU
	}
	return 1420
}

func wgInbound(st *state.State, ib J, users []state.User) {
	var peers []J
	for _, u := range users {
		_, pub := WGUserKey(u)
		peers = append(peers, J{"publicKey": pub, "allowedIPs": []string{u.WGIP + "/32"}, "email": u.Name})
	}
	ib["protocol"] = "wireguard"
	ib["settings"] = J{"secretKey": st.WGPriv, "peers": peers, "mtu": 1420}
}

func wgOutbound(st *state.State, e state.Entry, u state.User, k state.Knobs, tag string) J {
	priv, _ := WGUserKey(u)
	return J{"tag": tag, "protocol": "wireguard", "settings": J{"secretKey": priv, "address": []string{u.WGIP + "/32"},
		"peers":       []J{{"publicKey": st.WGPub, "endpoint": HostPort(st.Addr(), e.Port), "keepAlive": 25}},
		"mtu":         WGMTU(k),
		"noKernelTun": true}}
}

func wgLink(st *state.State, e state.Entry, u state.User, k state.Knobs, name string) string {
	priv, _ := WGUserKey(u)
	q := url.Values{"publickey": {st.WGPub}, "address": {u.WGIP + "/32"}, "mtu": {fmt.Sprint(WGMTU(k))}}
	return fmt.Sprintf("wireguard://%s@%s?%s#%s", url.QueryEscape(priv), HostPort(st.Addr(), e.Port), q.Encode(), name)
}

// WGConf is the config file for the official WireGuard apps.
func WGConf(st *state.State, e state.Entry, u state.User) string {
	priv, _ := WGUserKey(u)
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
`, priv, u.WGIP, WGMTU(e.Knobs()), st.WGPub, HostPort(st.Addr(), e.Port))
}

// sshLink is the ssh:// link NekoBox, Hiddify and sing-box based apps import, for plain SSH. The
// host key is pinned with the public_key parameter.
func sshLink(st *state.State, e state.Entry, u state.User, name string) string {
	q := url.Values{"public_key": {st.SSHHostPub}}
	return fmt.Sprintf("ssh://%s:%s@%s?%s#%s", url.PathEscape(u.Name), url.PathEscape(TunnelPassword(u)),
		HostPort(st.Addr(), e.Port), q.Encode(), name)
}

// SSHInfo is what HTTP Injector, NetMod and similar apps need for an SSH entry: where to connect,
// whether to wrap it in TLS (with which SNI), and the payload to send first.
func SSHInfo(st *state.State, e state.Entry, u state.User) string {
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
	fmt.Fprintf(&b, "SSH user name: %s\nSSH password: %s\nSSH host key: %s\n", u.Name, TunnelPassword(u), st.SSHHostPub)
	return b.String()
}

// ParseCertPEM decodes the first certificate in a PEM block.
func ParseCertPEM(s string) (*x509.Certificate, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		return nil, fmt.Errorf("no PEM certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}
