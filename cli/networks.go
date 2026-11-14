package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chethober/xtune/pkg/netinfo"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// Configs ranked per network. Every tune, check and measured report knows which network it ran
// from: its autonomous system (ASN), which the client looks up for itself. The scores of the
// deployed configs from there are kept on the server, by ASN. The subscription server looks up
// the ASN of whoever asks for a subscription and lists that network's best configs first, with
// the clean IP that worked best there for the CDN and Worker configs. Configs blocked there go
// last, not away: phones move between networks.

// netRankFresh is how long a network's ranking is used.
const netRankFresh = 30 * 24 * time.Hour

// ---------------------------------------------------------------- the client's network

// ---------------------------------------------------------------- recording

// checkScore scores a check like a tune scores a variant: reliability, then speed and latency.
func checkScore(c CheckResult) float64 {
	return score(&Variant{Ok: c.Ok, N: c.N, Warm: c.Warm, Down: c.Down, Up: c.Up})
}

// variantRank scores the deployed entries from a tune's variants: each entry's own (with the
// knobs it was deployed with), and for CDN and Worker entries the clean IP that beat it.
func variantRank(entries []state.Entry, vs []*Variant) (map[string]float64, map[string]string) {
	scores, addrs := map[string]float64{}, map[string]string{}
	for _, e := range entries {
		best := -1.0
		for _, v := range vs {
			if v.Entry.ID != e.ID {
				continue
			}
			if v.K == e.Knobs() {
				scores[state.EntryKey(e)] = v.Score
			}
			if v.K.Addr != "" && v.Score > best {
				best = v.Score
				addrs[state.EntryKey(e)] = v.K.Addr
			}
		}
		if a, ok := addrs[state.EntryKey(e)]; ok && (best <= scores[state.EntryKey(e)] || a == e.Addr) {
			delete(addrs, state.EntryKey(e))
		}
	}
	return scores, addrs
}

// labelRank does the same from a report's results, which name variants by label: the deployed
// variant of each entry is known by its label in the pending tune.
func labelRank(entries []state.Entry, deployed map[string]string, rs []CheckResult) (map[string]float64, map[string]string) {
	scores, addrs := map[string]float64{}, map[string]string{}
	by := map[string]float64{}
	for _, r := range rs {
		by[r.Label] = r.Score
	}
	for _, e := range entries {
		k := state.EntryKey(e)
		own, ok := by[deployed[e.ID]]
		if !ok {
			continue
		}
		scores[k] = own
		best := own
		for _, r := range rs {
			if _, ip, found := strings.Cut(r.Label, " ip="); found && r.ID == e.ID && r.Score > best && ip != e.Addr {
				best, addrs[k] = r.Score, ip
			}
		}
	}
	return scores, addrs
}

// RecordNetwork stores one network's scores of the deployed configs on the server, over the
// scores it had. addrs nil keeps the clean IPs it had (a check measures only the deployed knobs).
func RecordNetwork(r *remote.Server, n netinfo.Network, scores map[string]float64, addrs map[string]string) error {
	if n.ASN == "" || len(scores) == 0 {
		return nil
	}
	if err := serverXtuneCurrent(r); err != nil {
		return fmt.Errorf("xtune on the server is older than this one, which would drop the ranking, and updating it failed: %w", err)
	}
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if st.Networks == nil {
		st.Networks = map[string]*state.NetRank{}
	}
	nr := st.Networks[n.ASN]
	if nr == nil {
		nr = &state.NetRank{Scores: map[string]float64{}}
		st.Networks[n.ASN] = nr
	}
	nr.Name, nr.Updated = orStr(n.Name, nr.Name), time.Now().Unix()
	for k, s := range scores {
		nr.Scores[k] = s
	}
	if addrs != nil {
		nr.Addrs = addrs
	}
	if len(nr.Addrs) == 0 {
		nr.Addrs = nil
	}
	forgetStaleRanks(st)
	return state.Save(r, st)
}

// recordCheck stores a check's results as its network's ranking.
func recordCheck(r *remote.Server, n netinfo.Network, st *state.State, res []CheckResult) error {
	scores := map[string]float64{}
	for _, c := range res {
		for _, e := range st.Entries {
			if e.ID == c.ID {
				scores[state.EntryKey(e)] = checkScore(c)
			}
		}
	}
	return RecordNetwork(r, n, scores, nil)
}

// forgetStaleRanks drops scores of configs no longer deployed, and networks with none left.
func forgetStaleRanks(st *state.State) {
	keys := map[string]bool{}
	for _, e := range st.Entries {
		keys[state.EntryKey(e)] = true
	}
	for asn, nr := range st.Networks {
		for k := range nr.Scores {
			if !keys[k] {
				delete(nr.Scores, k)
				delete(nr.Addrs, k)
			}
		}
		if len(nr.Scores) == 0 {
			delete(st.Networks, asn)
		}
	}
}

// ---------------------------------------------------------------- serving

// forNetwork is the deployment as a client on network asn should see it: its configs best first
// there, with that network's clean IPs. It returns st itself, and "", when there is no fresh
// ranking for asn.
func forNetwork(st *state.State, asn string, now time.Time) (*state.State, string) {
	nr := st.Networks[asn]
	if nr == nil || now.Sub(time.Unix(nr.Updated, 0)) > netRankFresh {
		return st, ""
	}
	cp := *st
	cp.Entries = slices.Clone(st.Entries)
	for i := range cp.Entries {
		e := &cp.Entries[i]
		if a := nr.Addrs[state.EntryKey(*e)]; a != "" && (e.CDN() || e.Worker()) {
			e.Addr = a
		}
	}
	rank := func(e state.Entry) float64 {
		s, ok := nr.Scores[state.EntryKey(e)]
		switch {
		case !ok:
			return -1 // not measured there: after the ones that work, before the blocked ones
		case s == 0:
			return -2
		}
		return s
	}
	sort.SliceStable(cp.Entries, func(i, j int) bool { return rank(cp.Entries[i]) > rank(cp.Entries[j]) })
	return &cp, orStr(nr.Name, asn)
}

// requesterIP is who asked: through the CDN, the address Cloudflare saw.
func requesterIP(req *http.Request, st *state.State) net.IP {
	if st.Sub.Mode == "cdn" {
		if ip := net.ParseIP(strings.TrimSpace(req.Header.Get("CF-Connecting-IP"))); ip != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	return net.ParseIP(host)
}

// ---------------------------------------------------------------- command

func cmdNetworks(args []string) error {
	if err := serverOnly("networks", args); err != nil {
		return err
	}
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	action := "list"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "list", "ls":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		printNetworks(os.Stdout, st, time.Now())
		return nil
	case "del", "rm", "clear":
		defer r.Lock()()
		st, err := requireState(r)
		if err != nil {
			return err
		}
		if action == "clear" {
			st.Networks = nil
		} else {
			if len(args) < 2 {
				return errors.New("networks del ASN")
			}
			asn := strings.ToUpper(args[1])
			if !strings.HasPrefix(asn, "AS") {
				asn = "AS" + asn
			}
			if st.Networks[asn] == nil {
				return fmt.Errorf("no ranking for %s", asn)
			}
			delete(st.Networks, asn)
		}
		return state.Save(r, st)
	}
	return fmt.Errorf("unknown networks action %q (list, del ASN, clear)", action)
}

func printNetworks(w io.Writer, st *state.State, now time.Time) {
	if len(st.Networks) == 0 {
		fmt.Fprintln(w, "no networks ranked yet: tune, check (xtune check --every 6h) and measure from your users' networks")
		return
	}
	asns := make([]string, 0, len(st.Networks))
	for a := range st.Networks {
		asns = append(asns, a)
	}
	sort.Strings(asns)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ASN\tNETWORK\tMEASURED\tCONFIGS, BEST FIRST\n")
	for _, a := range asns {
		nr := st.Networks[a]
		ranked, _ := forNetwork(st, a, time.Unix(nr.Updated, 0))
		var ids []string
		for _, e := range ranked.Entries {
			s, ok := nr.Scores[state.EntryKey(e)]
			switch {
			case !ok:
				continue
			case s == 0:
				ids = append(ids, e.ID+" (blocked)")
			case e.Addr != "" && nr.Addrs[state.EntryKey(e)] == e.Addr:
				ids = append(ids, fmt.Sprintf("%s via %s", e.ID, e.Addr))
			default:
				ids = append(ids, e.ID)
			}
		}
		when := time.Unix(nr.Updated, 0).Format("2006-01-02")
		if now.Sub(time.Unix(nr.Updated, 0)) > netRankFresh {
			when += " (stale, unused)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a, orStr(nr.Name, "-"), when, strings.Join(ids, ", "))
	}
	tw.Flush()
}

// serverXtuneCurrent updates the server's xtune when it is not this version: an older one would
// drop the fields it does not know (the rankings among them) the next time it saves the state.
func serverXtuneCurrent(r *remote.Server) error {
	if r.Local || version == "dev" {
		return nil
	}
	ss, err := r.Status()
	if err != nil {
		return err
	}
	if ss.Xtune == version || ss.Xtune != "none" && !newer(version, ss.Xtune) {
		return nil // this one or newer
	}
	return EnsureServerXtune(r, func(s string) { fmt.Println("[*] " + s) })
}
