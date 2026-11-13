package protocols

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/chethober/xtune/pkg/state"
	"github.com/chethober/xtune/pkg/tunnel"
)

// The client profile: everything the xtune client app needs to connect as one user, without the
// rest of the deployment's secrets. The subscription server serves it at /PATH/TOKEN/client.

// ClientProfileVersion changes only when an older app would misread the profile.
const ClientProfileVersion = 1

// ClientProfile is one user's configs on every server their subscription lists, in the order
// the subscription lists them (the requester's network's best first, when it is ranked).
type ClientProfile struct {
	V       int           `json:"v"`
	Name    string        `json:"name"`              // the subscription's name in apps
	User    string        `json:"user"`              //
	Country string        `json:"country,omitempty"` // whose sites go direct (ir, cn, ru)
	Network string        `json:"network,omitempty"` // the network the order is for
	Probe   string        `json:"probe"`             // the URL health checks fetch
	Usage   ClientUsage   `json:"usage"`
	Configs []ClientEntry `json:"configs"`
	// Frag are the fragmenting outbounds that Xray outbounds with a "dialerProxy" need.
	Frag []J `json:"frag,omitempty"`
}

// ClientUsage is the user's account, as the subscription headers carry it, and more.
type ClientUsage struct {
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
	Quota    int64  `json:"quota"`   // bytes; 0 = unlimited
	Expires  int64  `json:"expires"` // unix seconds; 0 = never
	Devices  int    `json:"devices,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Resets   int64  `json:"resets,omitempty"` // unix seconds of the next usage reset
	Page     string `json:"page"`             // the subscription page
}

// ClientEntry is one config. Engine says which field holds it: xray (Outbound), ssh (SSH),
// awg (AWG), or handoff (File or Link: another app runs it).
type ClientEntry struct {
	ID       string      `json:"id"`
	Key      string      `json:"key"` // what reports refer to: state.EntryKey on its server
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Label    string      `json:"label"`
	Engine   string      `json:"engine"`
	Server   string      `json:"server"` // the address the config connects to, for routes around a VPN
	Outbound J           `json:"outbound,omitempty"`
	SSH      *tunnel.SSH `json:"ssh,omitempty"`
	AWG      *tunnel.AWG `json:"awg,omitempty"`
	Link     string      `json:"link,omitempty"` // the share link (also what MTProto opens in Telegram)
	File     *Profile    `json:"file,omitempty"`
}

// ClientProfileFor builds u's profile from st, which is already ordered for the requester.
func ClientProfileFor(st *state.State, u state.User) ClientProfile {
	p := ClientProfile{V: ClientProfileVersion, Name: SubName(st, u), User: u.Name, Country: st.Country, Probe: ProbeURL, Frag: FragOutbounds()}
	for _, s := range subServers(st) {
		for _, e := range linkEntries(s) {
			k := e.Knobs()
			c := ClientEntry{ID: e.ID, Key: state.EntryKey(e), Name: ConfigName(s, e, u), Kind: e.Kind, Label: KindLabel(e.Kind),
				Server: DialAddr(s, e, k), Link: Link(s, e, u, k)}
			switch {
			case e.Xray():
				c.Engine, c.Outbound = "xray", Outbound(s, e, u, k, "proxy-"+e.ID)
			case e.Kind == "amneziawg":
				if s.AWG == nil {
					continue
				}
				a := AWGClient(s, e, u, k)
				c.Engine, c.AWG = "awg", &a
			case e.SSH() && e.Kind != "mtproto":
				sc := SSHClient(s, e, u, k)
				c.Engine, c.SSH = "ssh", &sc
				if sc.DNSTT != nil {
					c.Server = ""
				}
			default: // OpenVPN, IKEv2, MTProto
				c.Engine = "handoff"
				if e.Kind == "mtproto" {
					c.Link = MTProtoLink(s, e, u)
				}
				if fs := profiles(s, e, u); len(fs) > 0 {
					c.File = &fs[0]
				}
			}
			p.Configs = append(p.Configs, c)
		}
	}
	return p
}

// SSHClient is u's connection to an SSH entry (ssh, ssh-tls, cdn-ssh, dnstt).
func SSHClient(st *state.State, e state.Entry, u state.User, k state.Knobs) tunnel.SSH {
	c := tunnel.SSH{Host: st.Addr(), Port: e.Port, User: u.Name, Password: TunnelPassword(u), HostKey: st.SSHHostPub}
	switch e.Kind {
	case "dnstt":
		c.DNSTT = &tunnel.DNSTT{Resolver: orStr(k.Resolver, DNSResolvers[0]), Domain: st.DNSDomain, PubKey: st.DNSTTPub}
		return c
	case "ssh-tls":
		c.TLS, c.SNI, c.CertHash = "pinned", orStr(e.SNI, st.CertSNI), st.CertHash
	case "cdn-ssh":
		c.Host, c.TLS, c.SNI = st.CDNDomain, "public", st.CDNDomain
	}
	if k.Mode == "ws" || e.CDN() {
		c.WSHost = orStr(e.SNI, "www.bing.com")
		if e.CDN() {
			c.WSHost = st.CDNDomain
		}
	}
	return c
}

// AWGClient is u's AmneziaWG device config for entry e.
func AWGClient(st *state.State, e state.Entry, u state.User, k state.Knobs) tunnel.AWG {
	p := *st.AWG
	priv, _ := AWGUserKey(u)
	jc, jmin, jmax, i1 := AWGClientParams(p, k.Mode)
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\njc=%d\njmin=%d\njmax=%d\n", B64hex(priv), jc, jmin, jmax)
	UAPIShared(p, &b)
	if i1 != "" {
		fmt.Fprintf(&b, "i1=%s\n", i1)
	}
	fmt.Fprintf(&b, "public_key=%s\nallowed_ip=0.0.0.0/0\npersistent_keepalive_interval=25\n", B64hex(p.Public))
	addr := AWGAddr(e, u)
	if !addr.IsValid() {
		addr = netip.IPv4Unspecified()
	}
	return tunnel.AWG{UAPI: b.String(), Endpoint: HostPort(st.Addr(), e.Port), Address: addr.String(), MTU: WGMTU(k)}
}
