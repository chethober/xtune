package protocols

import (
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/chethober/xtune/pkg/state"
)

// Share links and subscriptions: every server a subscription lists (this one, its relays and the
// followers) and the links of each config.

// NamedLink is a link and what to call it: a config's share link, a profile file, an app's import link.
type NamedLink struct{ Name, URL string }

// Subscription is the base64 body most clients accept as an imported subscription.
func Subscription(st *state.State, u state.User) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(Links(st, u), "\n")))
}

// PeerStale is how long a follower that stopped reporting stays in subscriptions.
const PeerStale = 24 * time.Hour

// PeerServers are this deployment and, on a primary, the followers that reported recently, with
// their entries named after them.
func PeerServers(st *state.State) []*state.State {
	out := []*state.State{st}
	if st.Peers == nil {
		return out
	}
	names := make([]string, 0, len(st.Peers.Servers))
	for n := range st.Peers.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := st.Peers.Servers[n]
		if p.State == nil || time.Since(time.Unix(p.Seen, 0)) > PeerStale {
			continue
		}
		ps := *p.State
		ps.Entries = nil
		for _, e := range p.State.Entries {
			e.ID = n + "-" + e.ID
			ps.Entries = append(ps.Entries, e)
		}
		out = append(out, &ps)
	}
	return out
}

// NamedLinks are the user's share links on every server, named by config.
func NamedLinks(st *state.State, u state.User) []NamedLink {
	var out []NamedLink
	for _, s := range subServers(st) {
		for _, e := range linkEntries(s) {
			if l := Link(s, e, u, e.Knobs()); l != "" { // VPNs and the DNS tunnel come as profile files
				out = append(out, NamedLink{Name: e.ID, URL: l})
			}
		}
	}
	return out
}

func Links(st *state.State, u state.User) []string {
	var out []string
	for _, l := range NamedLinks(st, u) {
		out = append(out, l.URL)
	}
	return out
}

// Relayable entries can go through a port forwarder: the others are reached by domain (CDN,
// Worker), through DNS resolvers (dnstt), or are VPNs whose certificate or profile names this server.
func Relayable(e state.Entry) bool {
	switch {
	case e.Reality(), e.VlessEnc():
		return !e.CDN()
	}
	switch e.Kind {
	case "hysteria2", "ss2022", "wireguard", "ssh", "ssh-tls", "mtproto":
		return true
	}
	return false
}

// RelayTarget is the address of this server relays forward to.
func RelayTarget(st *state.State) string { return orStr(st.ClientIP, st.Host) }

// RelayStates are this server's configs as clients reach them through each relay. A relay that
// forwards to an old address (after a move) is left out until it is synced.
func RelayStates(st *state.State) []*state.State {
	var out []*state.State
	for _, rl := range st.Relays {
		if rl.Target != RelayTarget(st) {
			continue
		}
		rs := *st
		rs.Domain, rs.CDNDomain, rs.ClientIP, rs.Host = "", "", rl.Addr, rl.Addr
		rs.Relays, rs.Peers, rs.Worker = nil, nil, nil
		rs.Naming.Via, rs.Naming.ViaFlag = rl.Name, rl.Flag
		rs.Entries = nil
		for _, e := range st.Entries {
			i := slices.IndexFunc(rl.Forwards, func(f state.Forward) bool { return f.ID == e.ID && f.To == e.Port })
			if i < 0 || !Relayable(e) {
				continue // redeployed since the relay was synced
			}
			e.ID, e.Port, e.Hop, e.Addr = rl.Name+"-"+e.ID, rl.Forwards[i].Port, "", ""
			rs.Entries = append(rs.Entries, e)
		}
		if len(rs.Entries) > 0 {
			out = append(out, &rs)
		}
	}
	return out
}

// subServers are the servers whose configs subscriptions list: this one, its relays, then the
// followers. Usage and statistics use PeerServers: relays carry this server's own configs.
func subServers(st *state.State) []*state.State {
	ps := PeerServers(st)
	out := []*state.State{ps[0]}
	out = append(out, RelayStates(st)...)
	return append(out, ps[1:]...)
}

// workerPath is the WebSocket path clients use: the entry's path, with Xray early data (the
// first bytes go in a header, saving a round trip) that the Worker decodes.
func workerPath(e state.Entry) string { return e.Path + "?ed=2560" }

// linkEntries are the entries clients get: the deployed ones, plus a copy of each workers.dev
// config for every extra Worker.
func linkEntries(st *state.State) []state.Entry {
	if st.Worker == nil || len(st.Worker.Extra) == 0 {
		return st.Entries
	}
	var out []state.Entry
	for _, e := range st.Entries {
		out = append(out, e)
		if !e.Worker() || e.SNI != st.Worker.Host {
			continue
		}
		for i, x := range st.Worker.Extra {
			c := e
			c.ID, c.SNI = fmt.Sprintf("%s-%d", e.ID, i+2), x.Host
			out = append(out, c)
		}
	}
	return out
}
