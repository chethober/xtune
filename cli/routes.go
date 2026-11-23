package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// Routes: every way a client can reach a config, measured in the same tune. A route is a config
// direct, or through one of the deployment's relays (client → relay → server). During a tune each
// relay forwards the test instance's ports too (a temporary forwarder, xray-autotune-test-relay,
// gone after 2 hours at most), so every variant is also measured through every relay, in
// parallel with the direct ones: the two legs are measured together, from the network that
// matters, instead of the relay reusing the direct tune. Each relay also measures its own leg to
// the server (TCP connect to every test port), so a route that fails says which leg failed.
//
// The results are a connectivity matrix, configs × routes, kept per network: subscriptions list
// the best route for the network first, whichever server or relay it goes through.

// relayRoute is one relay taking part in a tune.
type relayRoute struct {
	Relay state.Relay
	conn  *remote.Server
	fwd   map[string]int // test entry ID -> the relay's test port
	legB  map[int]int    // test entry port -> relay → server TCP connect ms (-1: fails)
	err   error
}

// RouteDialer connects to a relay (with the admin's SSH credentials); nil: no routes are tuned.
type RouteDialer func(rl state.Relay) (*remote.Server, error)

// openRoutes connects to the relays (one at a time: SSH may ask for a password), then sets each
// up to forward the test instance's ports and measures its leg to the server, all relays at once.
func openRoutes(o TuneOpts, target string, entries []state.Entry) []*relayRoute {
	if o.RelayDial == nil || o.deployed == nil || len(o.deployed.Relays) == 0 || slices.Contains(o.Relays, "none") {
		return nil
	}
	var routes []*relayRoute
	for _, rl := range o.deployed.Relays {
		if len(o.Relays) > 0 && !slices.Contains(o.Relays, "all") && !slices.Contains(o.Relays, rl.Name) {
			continue
		}
		conn, err := o.RelayDial(rl)
		if err != nil {
			o.Log(fmt.Sprintf("[!] relay %s: %v; its routes are left out of this tune", rl.Name, err))
			continue
		}
		routes = append(routes, &relayRoute{Relay: rl, conn: conn})
	}
	var relayable []state.Entry
	for _, e := range entries {
		if protocols.Relayable(e) {
			relayable = append(relayable, e)
		}
	}
	if len(routes) == 0 || len(relayable) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	for _, rt := range routes {
		wg.Add(1)
		go func(rt *relayRoute) {
			defer wg.Done()
			rt.err = rt.start(target, relayable)
		}(rt)
	}
	wg.Wait()
	var ok []*relayRoute
	for _, rt := range routes {
		if rt.err != nil {
			o.Log(fmt.Sprintf("[!] relay %s: %v; its routes are left out of this tune", rt.Relay.Name, rt.err))
			rt.conn.Close()
			continue
		}
		var reach []string
		for _, e := range relayable {
			if ms, done := rt.legB[e.Port]; done && e.Proto() == "tcp" {
				reach = append(reach, fmt.Sprintf("%s %s", e.ID, msOrFail(ms)))
			}
		}
		o.Log(fmt.Sprintf("[*] relay %s: forwarding %d test configs; relay → server: %s", rt.Relay.Name, len(rt.fwd), strings.Join(reach, ", ")))
		ok = append(ok, rt)
	}
	return ok
}

func msOrFail(ms int) string {
	if ms <= 0 {
		return "fails"
	}
	return strconv.Itoa(ms) + "ms"
}

// start forwards the test entries' ports on the relay and measures the relay's leg.
func (rt *relayRoute) start(target string, entries []state.Entry) error {
	if _, err := EnsureServerXray(rt.conn, "", func(string) {}); err != nil {
		return fmt.Errorf("xray on the relay: %w", err)
	}
	probe, err := rt.conn.Helper("probe")
	if err != nil {
		return err
	}
	used := usedPorts(probe)
	for _, f := range rt.Relay.Forwards { // the relay's own forwards stay
		used[f.Net][f.Port] = true
	}
	var fs []state.Forward
	rt.fwd = map[string]int{}
	for _, e := range entries {
		f := state.Forward{ID: e.ID, To: e.Port, Net: e.Proto()}
		for f.Port == 0 || used[f.Net][f.Port] {
			f.Port = 20000 + rand.IntN(40000)
		}
		used[f.Net][f.Port] = true
		rt.fwd[e.ID] = f.Port
		fs = append(fs, f)
	}
	var tcp []string
	for _, e := range entries {
		if e.Proto() == "tcp" {
			tcp = append(tcp, strconv.Itoa(e.Port))
		}
	}
	rt.legB = map[int]int{}
	if len(tcp) > 0 {
		out, err := rt.conn.Helper(append([]string{"relay-check", target}, tcp...)...)
		if err != nil {
			return fmt.Errorf("relay-check: %w", err)
		}
		rt.legB = parseRelayCheck(out)
	}
	cfg, _ := json.MarshalIndent(relayConfig(target, fs), "", " ")
	if err := rt.conn.Upload(remote.Dir+"/relay-test.json", cfg, "600"); err != nil {
		return err
	}
	out, err := rt.conn.Helper(append([]string{"relay-test-start"}, relaySpecs(fs)...)...)
	if err != nil || !strings.HasSuffix(out, "active") {
		return fmt.Errorf("the relay's test forwarder did not start: %v %s", err, tail(out, 200))
	}
	return nil
}

// closeRoutes stops the relays' test forwarders.
func closeRoutes(routes []*relayRoute, log func(string)) {
	for _, rt := range routes {
		if _, err := rt.conn.Helper("relay-test-stop"); err != nil {
			log("[!] relay " + rt.Relay.Name + ": stopping its test forwarder: " + err.Error())
		}
		rt.conn.Close()
	}
}

// routeVariants are the variants again through every relay that forwards their config.
func routeVariants(st *state.State, vs []*Variant, routes []*relayRoute) []*Variant {
	var out []*Variant
	for _, rt := range routes {
		rs := *st
		rs.Domain, rs.CDNDomain, rs.ClientIP, rs.Host = "", "", rt.Relay.Addr, rt.Relay.Addr
		for _, v := range vs {
			port, ok := rt.fwd[v.Entry.ID]
			if !ok || v.Via != "" {
				continue
			}
			c := *v
			c.Via, c.viaPort, c.viaSt = rt.Relay.Name, port, &rs
			c.Label = "via " + rt.Relay.Name + " " + v.Label
			c.Downs, c.Ups, c.oks = nil, nil, nil
			out = append(out, &c)
		}
	}
	return out
}

// RouteCell is one config's best result on one route.
type RouteCell struct {
	Score float64 `json:"score"`
	Ok    int     `json:"ok"`
	N     int     `json:"n"`
	LegB  int     `json:"legB,omitempty"` // relay → server ms (-1: fails), TCP only
}

// RouteMatrix is configs × routes ("" = direct, else the relay's name).
type RouteMatrix struct {
	Routes  []string                         `json:"routes"`
	Configs []string                         `json:"configs"`
	Cells   map[string]map[string]*RouteCell `json:"cells"` // config ID -> route -> cell
}

// routeMatrix is the best variant of each config on each route.
func routeMatrix(vs []*Variant, routes []*relayRoute) RouteMatrix {
	m := RouteMatrix{Routes: []string{""}, Cells: map[string]map[string]*RouteCell{}}
	legB := map[string]map[int]int{}
	for _, rt := range routes {
		m.Routes = append(m.Routes, rt.Relay.Name)
		legB[rt.Relay.Name] = rt.legB
	}
	for _, v := range vs {
		if v.Via != "" && !slices.Contains(m.Routes, v.Via) {
			m.Routes = append(m.Routes, v.Via)
		}
		row := m.Cells[v.Entry.ID]
		if row == nil {
			row = map[string]*RouteCell{}
			m.Cells[v.Entry.ID] = row
			m.Configs = append(m.Configs, v.Entry.ID)
		}
		c := row[v.Via]
		if c == nil || v.Score > c.Score || v.Score == c.Score && v.Ok > c.Ok {
			c = &RouteCell{Score: v.Score, Ok: v.Ok, N: v.N}
			if lb, ok := legB[v.Via][v.Entry.Port]; ok && v.Entry.Proto() == "tcp" {
				c.LegB = lb
			}
			row[v.Via] = c
		}
	}
	sort.Strings(m.Configs)
	return m
}

// Best is the best route of config id: its relay ("" direct) and score.
func (m RouteMatrix) Best(id string) (string, float64) {
	via, best := "", -1.0
	for _, r := range m.Routes {
		if c := m.Cells[id][r]; c != nil && c.Score > best {
			via, best = r, c.Score
		}
	}
	return via, best
}

func printRouteMatrix(w io.Writer, m RouteMatrix) {
	if len(m.Routes) < 2 {
		return
	}
	fmt.Fprintln(w, "\nRoutes (best score of each config on each route; relay → server leg in brackets):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	head := []string{"CONFIG"}
	for _, r := range m.Routes {
		head = append(head, strings.ToUpper(orStr(r, "direct")))
	}
	fmt.Fprintln(tw, strings.Join(append(head, "BEST"), "\t"))
	for _, id := range m.Configs {
		row := []string{id}
		for _, r := range m.Routes {
			c := m.Cells[id][r]
			switch {
			case c == nil:
				row = append(row, "-")
			case c.Score == 0:
				cell := "blocked"
				if c.LegB < 0 {
					cell += " [relay→server fails]"
				}
				row = append(row, cell)
			default:
				cell := ff(c.Score)
				if c.LegB > 0 {
					cell += fmt.Sprintf(" [%dms]", c.LegB)
				}
				row = append(row, cell)
			}
		}
		via, s := m.Best(id)
		best := "none"
		if s > 0 {
			best = orStr(via, "direct")
		}
		fmt.Fprintln(tw, strings.Join(append(row, best), "\t"))
	}
	tw.Flush()
}

// routeRank adds route scores to a tune's ranking: each deployed config's score through each relay,
// keyed state.RouteKey.
func routeRank(entries []state.Entry, vs []*Variant, scores map[string]float64) {
	for _, e := range entries {
		best := map[string]float64{}
		exact := map[string]bool{}
		for _, v := range vs {
			if v.Via == "" || v.Entry.ID != e.ID {
				continue
			}
			k := state.RouteKey(v.Via, e)
			switch {
			case sameKnobs(v.K, e.Knobs()):
				scores[k], exact[v.Via] = v.Score, true
			case !exact[v.Via] && v.Score > best[v.Via]:
				best[v.Via] = v.Score
			}
		}
		for via, s := range best {
			if !exact[via] {
				scores[state.RouteKey(via, e)] = s
			}
		}
	}
}
