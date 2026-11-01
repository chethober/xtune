package protocols

import (
	"encoding/hex"
	"slices"

	"github.com/chethober/xtune/internal/state"
)

// TunnelConf is what the tunnel server runs.
type TunnelConf struct {
	Entries   []state.Entry `json:"entries"`
	Users     []TunnelUser  `json:"users"`
	HostKey   string        `json:"hostKey"`
	CertPEM   string        `json:"certPem"`
	KeyPEM    string        `json:"keyPem"`
	DNSTTKey  string        `json:"dnsttKey,omitempty"`
	DNSDomain string        `json:"dnsDomain,omitempty"`
	Stats     string        `json:"stats"` // where the server writes its counters
}

type TunnelUser struct {
	Name    string `json:"name"`
	Pass    string `json:"pass"`
	MT      string `json:"mt,omitempty"` // MTProto proxy secret (hex), when an mtproto config is deployed
	Devices int    `json:"devices,omitempty"`
}

// TunnelServerConf builds the tunnel server's config for these users.
func TunnelServerConf(st *state.State, entries []state.Entry, users []state.User, statsPath string) TunnelConf {
	c := TunnelConf{Entries: state.FilterEntries(entries, state.Entry.SSH), HostKey: st.SSHHostKey, CertPEM: st.CertPEM, KeyPEM: st.KeyPEM,
		DNSTTKey: st.DNSTTKey, DNSDomain: st.DNSDomain, Stats: statsPath, Users: []TunnelUser{}}
	mt := state.HasEngine(entries, "tunnel") && slices.ContainsFunc(entries, func(e state.Entry) bool { return e.Kind == "mtproto" })
	for _, u := range users {
		if u.Enabled {
			tu := TunnelUser{Name: u.Name, Pass: TunnelPassword(u), Devices: u.Devices}
			if mt {
				tu.MT = hex.EncodeToString(MTProtoKey(u))
			}
			c.Users = append(c.Users, tu)
		}
	}
	return c
}

// DNSResolvers are the resolvers DNS tunnel variants are tried through: plain UDP to public
// resolvers (often the only thing a censor lets out), and DNS over HTTPS and TLS, which look
// like web traffic.
var DNSResolvers = []string{"udp:8.8.8.8:53", "udp:1.1.1.1:53", "udp:9.9.9.9:53",
	"doh:https://cloudflare-dns.com/dns-query", "doh:https://dns.google/dns-query", "dot:dns.quad9.net:853"}
