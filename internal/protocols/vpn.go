package protocols

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"net"
	"sort"
	"strings"

	"github.com/chethober/xtune/internal/remote"
	"github.com/chethober/xtune/internal/state"
)

// VPNSubnet is each VPN entry's client address range.
func VPNSubnet(e state.Entry) string {
	switch e.Kind {
	case "openvpn-udp":
		return "10.68.0.0/20"
	case "openvpn-tcp":
		return "10.68.16.0/20"
	}
	return "10.69.0.0/16" // ikev2
}

// VPNUser is what `xtune vpn-auth` checks an OpenVPN login against.
type VPNUser struct {
	Pass    string `json:"pass"`
	Devices int    `json:"devices,omitempty"`
}

func VPNUsers(users []state.User) map[string]VPNUser {
	out := map[string]VPNUser{}
	for _, u := range users {
		if u.Enabled {
			out[u.Name] = VPNUser{Pass: TunnelPassword(u), Devices: u.Devices}
		}
	}
	return out
}

// OpenVPNConf is the server config of one OpenVPN entry; files live in dir.
func OpenVPNConf(st *state.State, e state.Entry, dir string) string {
	proto := "udp"
	if e.Kind == "openvpn-tcp" {
		proto = "tcp-server"
	}
	_, n, _ := net.ParseCIDR(VPNSubnet(e))
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
`, e.Port, proto, n.IP, net.IP(n.Mask), remote.Dir, dir, dir, e.ID, dir, e.ID)
	if proto == "udp" {
		b.WriteString("explicit-exit-notify 1\n")
	}
	fmt.Fprintf(&b, "<ca>\n%s</ca>\n<cert>\n%s</cert>\n<key>\n%s</key>\n", st.VPNCA, st.VPNCert, st.VPNKey)
	return b.String()
}

// SwanctlConf is strongSwan's config for the ikev2 entry and the active users' EAP secrets. The
// helper installs the certificate and key into strongSwan's x509 and private directories, the
// only place it loads unencrypted keys from.
func SwanctlConf(st *state.State, users []state.User) string {
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
`, st.VPNName, VPNSubnet(state.Entry{Kind: "ikev2"}))
	names := []string{}
	pass := map[string]string{}
	for n, u := range VPNUsers(users) {
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

// Profile is a file a user imports into an app.
type Profile struct {
	Name string // shown to the user
	File string
	Type string // MIME type
	Body string
}

// Profiles are the user's files for the entries that have no share link, and a WireGuard config
// for the official app.
func Profiles(st *state.State, u state.User) []Profile {
	var out []Profile
	for _, s := range PeerServers(st) {
		for _, e := range s.Entries {
			out = append(out, profiles(s, e, u)...)
		}
	}
	return out
}

func profiles(st *state.State, e state.Entry, u state.User) []Profile {
	base := u.Name + "-" + e.ID
	switch e.Kind {
	case "wireguard":
		return []Profile{{Name: "WireGuard app (" + e.ID + ")", File: base + ".conf", Type: "text/plain", Body: WGConf(st, e, u)}}
	case "amneziawg":
		return []Profile{{Name: "AmneziaWG / AmneziaVPN app (" + e.ID + ")", File: base + ".conf", Type: "text/plain", Body: AWGConfFile(st, e, u)}}
	case "openvpn-udp", "openvpn-tcp":
		return []Profile{{Name: "OpenVPN (" + e.ID + ")", File: base + ".ovpn", Type: "application/x-openvpn-profile", Body: OVPNProfile(st, e, u)}}
	case "ikev2":
		return []Profile{
			{Name: "IKEv2, strongSwan app (Android)", File: base + ".sswan", Type: "application/vnd.strongswan.profile", Body: SswanProfile(st, u)},
			{Name: "IKEv2, iPhone / iPad / Mac", File: base + ".mobileconfig", Type: "application/x-apple-aspen-config", Body: MobileConfig(st, u)},
		}
	case "dnstt":
		return []Profile{{Name: "DNS tunnel (SlowDNS / dnstt)", File: base + ".txt", Type: "text/plain", Body: DNSTTInfo(st, e, u)}}
	case "ssh", "ssh-tls", "cdn-ssh":
		return []Profile{{Name: "SSH apps, HTTP Injector / NetMod (" + e.ID + ")", File: base + ".txt", Type: "text/plain", Body: SSHInfo(st, e, u)}}
	case "mtproto":
		return []Profile{{Name: "Telegram MTProto proxy (" + e.ID + ")", File: base + ".txt", Type: "text/plain", Body: MTProtoInfo(st, e, u)}}
	}
	return nil
}

// MobileConfig is an Apple configuration profile (iOS, macOS) with the CA and the IKEv2 VPN.
func MobileConfig(st *state.State, u state.User) string {
	ca, _ := pem.Decode([]byte(st.VPNCA))
	caCert, _ := ParseCertPEM(st.VPNCA)
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
		id("vpn"), uuidFmt(id("vpn")), x(u.Name), x(st.VPNName), x(st.VPNName), x(u.Name), x(u.Name), x(TunnelPassword(u)), x(cn),
		x(u.Name), id("root"), uuidFmt(id("root")))
}

func uuidFmt(h string) string {
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// OVPNProfile is an OpenVPN client profile; the app asks for the user name and password.
func OVPNProfile(st *state.State, e state.Entry, u state.User) string {
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
`, e.ID, u.Name, TunnelPassword(u), proto, st.VPNName, e.Port, st.VPNName, st.VPNCA)
}

// SswanProfile is a strongSwan app profile: the CA is included, the app asks for the password.
func SswanProfile(st *state.State, u state.User) string {
	ca, _ := pem.Decode([]byte(st.VPNCA))
	b, _ := json.MarshalIndent(J{"uuid": uuidFmt(hex.EncodeToString(secret(u, "sswan")[:16])), "name": "xtune " + u.Name,
		"type": "ikev2-eap", "remote": J{"addr": st.VPNName, "id": st.VPNName, "cert": base64.StdEncoding.EncodeToString(ca.Bytes)},
		"local": J{"eap_id": u.Name}}, "", "  ")
	return string(b)
}

// DNSTTInfo is what SlowDNS/dnstt apps (HTTP Injector, NetMod, ...) need.
func DNSTTInfo(st *state.State, e state.Entry, u state.User) string {
	return fmt.Sprintf(`DNS tunnel (dnstt / SlowDNS) for %s

Name server (NS) domain: %s
Public key: %s
DNS resolver: %s (or any resolver that works on your network, e.g. 8.8.8.8, 1.1.1.1)
SSH host: 127.0.0.1 or %s, port 22 (the tunnel ends at the SSH server)
SSH user name: %s
SSH password: %s

It is slow (tens of KB/s): use it when nothing else gets through.
`, u.Name, st.DNSDomain, st.DNSTTPub, strings.SplitN(orStr(e.Resolver, DNSResolvers[0]), ":", 2)[1], st.Addr(), u.Name, TunnelPassword(u))
}
