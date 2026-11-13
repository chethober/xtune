package state

import "net/netip"

// Kinds describes each kind: what serves it and how it is measured.
var Kinds = map[string]struct {
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
	if k, ok := Kinds[e.Kind]; ok {
		return k.Engine
	}
	return "xray"
}

func (e Entry) Xray() bool { return e.Engine() == "xray" }

// HandshakeOnly entries are VPNs: tuning sees whether the handshake answers, not throughput.
func (e Entry) HandshakeOnly() bool { return Kinds[e.Kind].Handshake }

// SSH entries are served by xtune's SSH server; dnstt carries that server too.
func (e Entry) SSH() bool { return e.Engine() == "tunnel" }

func FilterEntries(es []Entry, keep func(Entry) bool) []Entry {
	var out []Entry
	for _, e := range es {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

func HasEngine(es []Entry, engine string) bool {
	for _, e := range es {
		if e.Engine() == engine {
			return true
		}
	}
	return false
}

// WgSubnet holds the users' tunnel addresses; the server never needs one of its own.
var WgSubnet = netip.MustParsePrefix("10.66.0.0/16")

// AssignWGIPs gives every user (and the check user) a tunnel address that stays theirs.
func (st *State) AssignWGIPs() {
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
	next := WgSubnet.Addr().Next().Next() // .0.2
	for _, u := range users {
		if u.WGIP != "" {
			continue
		}
		for used[next.String()] && WgSubnet.Contains(next) {
			next = next.Next()
		}
		u.WGIP = next.String()
		used[u.WGIP] = true
	}
}

// Family groups kinds into the opt-in protocols of --protocols ("" for the default matrix).
func (e Entry) Family() string {
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
