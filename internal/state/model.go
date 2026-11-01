// Package state is xtune's data model and its storage on the server: the deployment and user
// database in state.json (State, Entry, User and the settings they hold) and the traffic counters
// in usage.json (Usage). It holds the types, how they are read and written, and simple accessors;
// building configs from them and changing them lives with the features that do it.
package state

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Entry is one server inbound. FP/Mode/Frag are the client-side knobs tuning picked for it.
type Entry struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // see kinds
	Port     int    `json:"port"`
	SNI      string `json:"sni,omitempty"`
	Path     string `json:"path,omitempty"`
	Svc      string `json:"svc,omitempty"`
	Primary  bool   `json:"primary,omitempty"`
	FP       string `json:"fp,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Frag     string `json:"frag,omitempty"`
	Split    string `json:"split,omitempty"`    // cdn-xhttp: "cdn-up" uploads through the CDN and downloads straight from the server
	Hop      string `json:"hop,omitempty"`      // hysteria2: UDP port range forwarded to Port, for port hopping ("20000-20999")
	MTU      int    `json:"mtu,omitempty"`      // wireguard: tunnel MTU
	Resolver string `json:"resolver,omitempty"` // dnstt: the resolver clients send queries through
	Subnet   string `json:"subnet,omitempty"`   // amneziawg: the clients' address range
	Addr     string `json:"addr,omitempty"`     // CDN and Worker entries: a Cloudflare edge IP to dial (a clean IP)
	ALPN     string `json:"alpn,omitempty"`     // cdn-xhttp: "h3" reaches the CDN over HTTP/3 (QUIC)
}

// Knobs are the client settings tuning picked for the entry.
func (e Entry) Knobs() Knobs {
	return Knobs{FP: e.FP, Frag: e.Frag, Mode: e.Mode, Split: e.Split, MTU: e.MTU, Resolver: e.Resolver, Addr: e.Addr, ALPN: e.ALPN}
}

func (e Entry) Proto() string {
	switch e.Kind {
	case "hysteria2", "wireguard", "amneziawg", "dnstt", "openvpn-udp", "ikev2":
		return "udp"
	}
	return "tcp"
}

func (e Entry) Reality() bool { return strings.HasPrefix(e.Kind, "reality") || e.Trojan() }

func (e Entry) Trojan() bool { return e.Kind == "trojan-reality" }

func (e Entry) VlessEnc() bool { return strings.HasPrefix(e.Kind, "vlessenc") }

// CDN entries are reached through the CDN domain: TLS ends at the CDN, which forwards to the origin.
func (e Entry) CDN() bool { return strings.HasPrefix(e.Kind, "cdn") }

// Worker entries are reached through a Cloudflare Worker (SNI is its hostname), which relays
// VLESS over WebSocket to a plain TCP inbound here (see worker.go).
func (e Entry) Worker() bool { return e.Kind == "worker-ws" }

func (e Entry) Net() string {
	if e.Worker() {
		return "ws"
	}
	if e.Trojan() {
		return "tcp"
	}
	if i := strings.Index(e.Kind, "-"); i > 0 && e.Xray() && (e.Reality() || e.VlessEnc() || e.CDN()) {
		return e.Kind[i+1:]
	}
	return ""
}

type User struct {
	Name     string `json:"name"`
	UUID     string `json:"uuid"`
	SSKey    string `json:"ssKey"`
	Enabled  bool   `json:"enabled"`
	Created  int64  `json:"created"`
	Quota    int64  `json:"quota,omitempty"`    // traffic in bytes, 0 = unlimited
	Expires  int64  `json:"expires,omitempty"`  // unix seconds, 0 = never
	Devices  int    `json:"devices,omitempty"`  // simultaneous client IPs, 0 = unlimited
	SubToken string `json:"subToken,omitempty"` // secret part of the user's subscription URL
	Reset    string `json:"reset,omitempty"`    // usage starts from zero every period: monthly | weekly
	ResetAt  int64  `json:"resetAt,omitempty"`  // unix seconds of the next reset
	Plan     string `json:"plan,omitempty"`     // plan the user was added or renewed with
	Reseller string `json:"reseller,omitempty"` // reseller (management link) who manages this user
	WGIP     string `json:"wgIp,omitempty"`     // WireGuard tunnel address
	Pool     string `json:"pool,omitempty"`     // public profile the user is a member of

	WGOnly bool `json:"-"` // a WireGuard test peer (wgVariantUser): its UUID is not a real UUID
}

// SubConfig is the subscription server: off when Mode is empty.
type SubConfig struct {
	Mode string `json:"mode,omitempty"` // cdn (through the CDN domain) | direct (Let's Encrypt on the direct domain)
	Port int    `json:"port,omitempty"`
	Path string `json:"path,omitempty"` // secret path prefix
	Lang string `json:"lang,omitempty"` // page language: en | fa ("" = from the browser)
	// Port443: the server also listens on 443, for the connect and management links: bots on
	// serverless platforms (such as @chthvpnbot on tgcloud) can only reach ports 80 and 443.
	Port443 bool   `json:"port443,omitempty"`
	Metrics string `json:"metrics,omitempty"` // token for /PATH/metrics (Prometheus), "" = off
}

// State is the server's deployment and user database, stored as state.json (root, 0600).
type State struct {
	Host        string                 `json:"host"`
	Domain      string                 `json:"domain,omitempty"`    // direct (DNS-only) domain used instead of Host in links
	CDNDomain   string                 `json:"cdnDomain,omitempty"` // CDN-proxied domain for the cdn-* entries
	RealityPriv string                 `json:"realityPrivateKey"`
	RealityPub  string                 `json:"realityPublicKey"`
	ShortID     string                 `json:"shortId"`
	VencDec     string                 `json:"vlessDecryption"`
	VencEnc     string                 `json:"vlessEncryption"`
	SSKey       string                 `json:"ssServerKey"`
	CertSNI     string                 `json:"certSni"`
	CertHash    string                 `json:"certSha256"`
	CertPEM     string                 `json:"certPem"`
	KeyPEM      string                 `json:"keyPem"`
	Entries     []Entry                `json:"entries"`
	Users       []User                 `json:"users"`
	APIPort     int                    `json:"apiPort,omitempty"` // Xray API on 127.0.0.1, for live user changes and stats
	Country     string                 `json:"country,omitempty"` // client configs send this country's sites direct
	Sub         SubConfig              `json:"sub"`
	Check       *User                  `json:"check,omitempty"`     // hidden user `xtune check` measures the deployed configs with
	Warp        *Warp                  `json:"warp,omitempty"`      // Cloudflare WARP outbound for chosen sites
	Notify      Notify                 `json:"notify,omitzero"`     // admin alerts
	LegacyBot   json.RawMessage        `json:"bot,omitempty"`       // the Telegram bot removed in v0.7.0: Enforce removes its unit, then this
	Manage      *Manage                `json:"manage,omitempty"`    // management links, used by the community VPN bot
	Plans       map[string]Plan        `json:"plans,omitempty"`     // named sets of limits for adding and renewing users
	Cloudflare  Cloudflare             `json:"cloudflare,omitzero"` // API token for DNS records and the SSL mode
	Peers       *PeerHub               `json:"peers,omitempty"`     // primary: followers whose configs join the subscriptions
	Follow      *Follow                `json:"follow,omitempty"`    // follower: the users come from a primary
	Public      map[string]*PublicPool `json:"public,omitempty"`    // public profiles: pools of users shared through a bot
	AWG         *AWGParams             `json:"awg,omitempty"`       // AmneziaWG key and obfuscation settings
	Worker      *Worker                `json:"worker,omitempty"`    // Cloudflare Worker relaying the worker-ws entries
	Backups     *BackupPlan            `json:"backups,omitempty"`   // scheduled backups sent off the server
	Networks    map[string]*NetRank    `json:"networks,omitempty"`  // configs ranked per network (ASN), for subscriptions
	Autopilot   *Autopilot             `json:"autopilot,omitempty"` // where to get a new IP when this one is blocked
	ClientIP    string                 `json:"clientIp,omitempty"`  // the address clients use instead of Host (a reserved IP)
	Naming      Naming                 `json:"naming,omitzero"`     // config and subscription names, with the country flag

	WGPriv     string `json:"wgPrivateKey,omitempty"` // WireGuard server key (base64)
	WGPub      string `json:"wgPublicKey,omitempty"`
	SSHHostKey string `json:"sshHostKey,omitempty"` // SSH tunnel host key (OpenSSH PEM)
	SSHHostPub string `json:"sshHostPub,omitempty"` // authorized_keys format; clients pin it
	DNSTTKey   string `json:"dnsttKey,omitempty"`   // DNS tunnel Noise key (hex)
	DNSTTPub   string `json:"dnsttPub,omitempty"`
	DNSDomain  string `json:"dnsDomain,omitempty"` // zone delegated to the server for the DNS tunnel
	VPNCA      string `json:"vpnCa,omitempty"`     // CA for OpenVPN and IKEv2 (PEM); clients trust it
	VPNCAKey   string `json:"vpnCaKey,omitempty"`
	VPNCert    string `json:"vpnCert,omitempty"` // server certificate, for VPNName
	VPNKey     string `json:"vpnKey,omitempty"`
	VPNName    string `json:"vpnName,omitempty"` // the address the server certificate is for
}

// Knobs are client-side settings. Split applies to cdn-xhttp only, MTU to wireguard, Resolver to
// dnstt; Mode is the XHTTP mode, or for SSH entries "ws" when the client opens with a WebSocket
// upgrade request. Addr is the Cloudflare edge IP CDN and Worker entries dial instead of
// resolving their domain; ALPN "h3" has the CDN entry reach the CDN over HTTP/3.
type Knobs struct {
	FP, Frag, Mode, Split string
	MTU                   int
	Resolver              string
	Addr                  string
	ALPN                  string
}

func randHex(n int) string { b := make([]byte, n); rand.Read(b); return hex.EncodeToString(b) }

func NewUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func NewSSKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func NewUser(name string) User {
	return User{Name: name, UUID: NewUUID(), SSKey: NewSSKey(), Enabled: true, Created: time.Now().Unix(), SubToken: randHex(16)}
}

// Addr is the address clients dial for direct (non-CDN) entries.
func (st *State) Addr() string { return orStr(st.Domain, orStr(st.ClientIP, st.Host)) }

func (st *State) User(name string) (int, *User) {
	for i := range st.Users {
		if st.Users[i].Name == name {
			return i, &st.Users[i]
		}
	}
	return -1, nil
}

type J = map[string]any

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
