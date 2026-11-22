package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// What tunes learn and keep (state.Memory, memory.json on the server): every tune, shared-test
// report, personal tune and app report teaches each network's setting weights, which guide the
// next tune's samples (Prior), and each user's own results, which order their subscription
// (personalRank) before the network's ranking.

// knobArms are a variant's setting values as memory names them: a fragment range is "range" (its
// spec is remembered on its own when it works).
func knobArms(k state.Knobs) [][2]string {
	frag := orStr(k.Frag, "none")
	if _, spec := protocols.FragSpec(frag); spec {
		frag = "range"
	}
	out := [][2]string{{"frag", frag}}
	if k.FP != "" {
		out = append(out, [2]string{"fp", k.FP})
	}
	if k.Mode != "" {
		out = append(out, [2]string{"mode", k.Mode})
	}
	out = append(out, [2]string{"alpn", k.ALPN})
	return out
}

// rewardOf is a measured variant's reward, 0 to 1: how reliably it got through, and how it did
// against the best of the same measurement. -1: not measured.
func rewardOf(v *Variant, top float64) float64 {
	if v.N == 0 {
		return -1
	}
	if v.Ok == 0 {
		return 0
	}
	speed := 0.5
	if top > 0 {
		speed = min(v.Score/top, 1)
	}
	return float64(v.Ok) / float64(v.N) * (0.5 + 0.5*speed)
}

// learnVariants teaches memory what a measurement from network asn found, for that network and
// for all networks together ("").
func learnVariants(m *state.Memory, asn, name string, vs []*Variant, now int64) {
	top := 0.0
	for _, v := range vs {
		top = max(top, v.Score)
	}
	nets := []string{""}
	if asn != "" {
		nets = []string{asn, ""}
	}
	for _, net := range nets {
		nm := m.Net(net)
		if net != "" && name != "" {
			nm.Name = name
		}
		for _, v := range vs {
			r := rewardOf(v, top)
			if r < 0 || v.Entry.HandshakeOnly() {
				continue
			}
			for _, a := range knobArms(v.K) {
				nm.Observe(v.Entry.Kind, a[0], a[1], r, now)
			}
			if _, spec := protocols.FragSpec(v.K.Frag); spec && v.Ok > 0 {
				nm.AddFrag(v.Entry.Kind, v.K.Frag, r, now)
			}
		}
	}
}

// priorFrom is what memory knows about network asn: its own weights, else all networks'.
func priorFrom(m *state.Memory, asn string, now int64) Prior {
	if m == nil || len(m.Nets) == 0 {
		return Prior{}
	}
	net, all := m.Nets[asn], m.Nets[""]
	if asn == "" {
		net = nil
	}
	return Prior{
		W: func(kind, dim, value string) float64 {
			w, ok := net.Weight(kind, dim, value, now)
			g, gok := all.Weight(kind, dim, value, now)
			switch {
			case ok && gok:
				return 0.75*w + 0.25*g
			case ok:
				return w
			}
			return g
		},
		Frags: func(kind string) []string {
			var out []string
			for _, nm := range []*state.NetMemory{net, all} {
				if nm == nil {
					continue
				}
				for _, f := range nm.Frags {
					if f.Kind == kind && !slices.Contains(out, f.Spec) {
						out = append(out, f.Spec)
					}
				}
			}
			return out
		},
	}
}

// changeMemory loads memory.json under the server's lock, changes it and saves it.
func changeMemory(r *remote.Server, fn func(m *state.Memory, st *state.State)) error {
	defer r.Lock()()
	m, err := state.LoadMemory(r)
	if err != nil {
		return err
	}
	st, _ := state.Load(r)
	fn(m, st)
	pruneMemory(m, st, time.Now().Unix())
	return state.SaveMemory(r, m)
}

// pruneMemory forgets what no longer matters: old networks, users that are gone, configs that are
// not deployed.
func pruneMemory(m *state.Memory, st *state.State, now int64) {
	if st == nil {
		m.Prune(now, nil, nil)
		return
	}
	users, keys := map[string]bool{}, map[string]bool{}
	for _, u := range st.Users {
		users[u.Name] = true
	}
	for _, e := range st.Entries {
		keys[state.EntryKey(e)] = true
		for _, rl := range st.Relays {
			keys[state.RouteKey(rl.Name, e)] = true
		}
	}
	m.Prune(now, users, keys)
}

// learnTune keeps what a tune measured from network asn.
func learnTune(r *remote.Server, asn, name string, vs []*Variant, log func(string)) {
	err := changeMemory(r, func(m *state.Memory, _ *state.State) { learnVariants(m, asn, name, vs, time.Now().Unix()) })
	if err != nil {
		log("[!] keeping what this tune learned: " + err.Error())
	}
}

// ---------------------------------------------------------------- users

// learnUser keeps how a user's configs did for them on network asn (scores by ranking key), from
// a report or a personal tune: a fresh score moves the old one halfway.
func learnUser(m *state.Memory, name, asn string, scores map[string]float64, now int64) {
	if asn == "" || len(scores) == 0 {
		return
	}
	un := m.UserNet(name, asn)
	fresh := now-un.Updated <= state.MemoryHalfLife
	for k, s := range scores {
		if old, ok := un.Scores[k]; ok && fresh {
			s = round2((old + s) / 2)
		}
		un.Scores[k] = s
	}
	un.Updated = now
}

// The subscription server reads memory.json from disk for every request that needs it, so it is
// cached by modification time.
var (
	memCacheMu   sync.Mutex
	memCache     *state.Memory
	memCacheTime time.Time
)

// loadMemory is the server's memory as the subscription server sees it (tests replace it).
var loadMemory = func() *state.Memory {
	fi, err := os.Stat(state.MemoryPath)
	if err != nil {
		return nil
	}
	memCacheMu.Lock()
	defer memCacheMu.Unlock()
	if memCache != nil && fi.ModTime().Equal(memCacheTime) {
		return memCache
	}
	b, err := os.ReadFile(state.MemoryPath)
	if err != nil {
		return nil
	}
	memCache, memCacheTime = state.DecodeMemory(b), fi.ModTime()
	return memCache
}

// userWeight is how much a user's own results count over their network's ranking.
const userWeight = 0.7

func init() {
	personalRank = func(st *state.State, u state.User, asn string, hour int, now time.Time) *state.State {
		if asn == "" || u.Tune != nil && u.Tune.Prefs.Auto != nil && !*u.Tune.Prefs.Auto {
			return st
		}
		m := loadMemory()
		if m == nil || m.Users[u.Name] == nil {
			return st
		}
		un := m.Users[u.Name].Nets[asn]
		if un == nil || now.Unix()-un.Updated > state.MemoryKeep/2 {
			return st
		}
		cp := *st
		base := st.RouteRank
		byID := map[string]state.Entry{}
		for _, e := range st.Entries {
			byID[e.ID] = e
		}
		mine := func(via string, e state.Entry) (float64, bool) {
			orig := e
			if via != "" {
				o, ok := byID[trimVia(e.ID, via)]
				if !ok {
					return 0, false
				}
				orig = o
			}
			s, ok := un.Scores[state.RouteKey(via, orig)]
			return s, ok
		}
		cp.RouteRank = func(via string, e state.Entry) float64 {
			b := -1.0
			if base != nil {
				b = base(via, e)
			}
			s, ok := mine(via, e)
			switch {
			case !ok:
				return b
			case s == 0:
				return -2 // blocked for them there
			case b > 0:
				return userWeight*s + (1-userWeight)*b
			}
			return s
		}
		return &cp
	}
	needsASNHook = func(st *state.State, u state.User) bool {
		m := loadMemory()
		return m != nil && m.Users[u.Name] != nil
	}
	recordPersonal = func(st *state.State, u state.User, in ClientReport, groups map[string][]*Variant, now time.Time) func(*state.Memory) {
		var all []*Variant
		scores := map[string]float64{}
		for id, g := range groups {
			all = append(all, g...)
			e, ok := findEntry(st, id)
			if !ok {
				continue
			}
			scores[state.EntryKey(e)] = 0 // nothing got through for them there
			if b := pick(g); b != nil {
				scores[state.EntryKey(e)] = b.Score
			}
		}
		return func(m *state.Memory) {
			learnVariants(m, in.ASN, in.Name, all, now.Unix())
			learnUser(m, u.Name, in.ASN, scores, now.Unix())
		}
	}
	personalPrior = func(st *state.State, u state.User, asn string) Prior {
		return priorFrom(loadMemory(), asn, time.Now().Unix())
	}
}

func trimVia(id, via string) string {
	if len(id) > len(via)+1 && id[:len(via)+1] == via+"-" {
		return id[len(via)+1:]
	}
	return id
}

// learnReport keeps what an app's report says about its user's configs on its network, and what
// the configs' settings did there.
func learnReport(r *remote.Server, u state.User, in ClientReport, now time.Time) error {
	return changeMemory(r, func(m *state.Memory, st *state.State) {
		if st == nil {
			return
		}
		keys := map[string]state.Entry{}
		via := map[string]string{}
		for _, e := range st.Entries {
			keys[state.EntryKey(e)] = e
		}
		for _, rs := range protocols.RelayStates(st) {
			for _, e := range rs.Entries {
				if orig, ok := findEntry(st, trimVia(e.ID, rs.Naming.Via)); ok {
					keys[state.EntryKey(e)], via[state.EntryKey(e)] = orig, rs.Naming.Via
				}
			}
		}
		scores := map[string]float64{}
		var vs []*Variant
		for _, res := range in.Results {
			e, ok := keys[res.Key]
			if !ok || res.N <= 0 || res.N > 100 || res.Ok < 0 || res.Ok > res.N {
				continue
			}
			v := &Variant{Entry: e, K: e.Knobs(), Ok: res.Ok, N: res.N, Warm: res.Warm, Down: res.Down, Up: res.Up}
			v.Score = score(v)
			scores[state.RouteKey(via[res.Key], e)] = v.Score
			vs = append(vs, v)
		}
		learnVariants(m, in.ASN, in.Name, vs, now.Unix())
		learnUser(m, u.Name, in.ASN, scores, now.Unix())
	})
}

func findEntry(st *state.State, id string) (state.Entry, bool) {
	for _, e := range st.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return state.Entry{}, false
}

// ---------------------------------------------------------------- xtune memory

func printMemory(w io.Writer, m *state.Memory, st *state.State, now int64) {
	if len(m.Nets) == 0 && len(m.Users) == 0 {
		fmt.Fprintln(w, "nothing learned yet: tunes, checks, personal tunes and app reports fill this in")
		return
	}
	asns := sortedKeys(m.Nets)
	for _, asn := range asns {
		nm := m.Nets[asn]
		title := "all networks"
		if asn != "" {
			title = fmt.Sprintf("AS%s %s", asn, nm.Name)
		}
		fmt.Fprintf(w, "# %s (updated %s)\n", title, time.Unix(nm.Updated, 0).Format("2006-01-02 15:04"))
		type row struct {
			key string
			w   float64
			n   float64
		}
		var rows []row
		for k, a := range nm.Arms {
			kind, dim, val := splitArm(k)
			wt, _ := nm.Weight(kind, dim, val, now)
			rows = append(rows, row{k, wt, a.N})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  KIND\tSETTING\tVALUE\tWORKS\tEVIDENCE")
		for _, r := range rows {
			kind, dim, val := splitArm(r.key)
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%.0f%%\t%.1f\n", kind, dim, orStr(val, "-"), r.w*100, r.n)
		}
		tw.Flush()
		for _, f := range nm.Frags {
			fmt.Fprintf(w, "  good fragment range for %s: %s (%.2f)\n", f.Kind, f.Spec, f.Reward)
		}
		fmt.Fprintln(w)
	}
	if len(m.Users) > 0 {
		fmt.Fprintln(w, "# users' own results (order their subscriptions on these networks)")
		for _, name := range sortedKeys(m.Users) {
			for _, asn := range sortedKeys(m.Users[name].Nets) {
				un := m.Users[name].Nets[asn]
				fmt.Fprintf(w, "  %s on AS%s: %d configs, %s\n", name, asn, len(un.Scores), time.Unix(un.Updated, 0).Format("2006-01-02 15:04"))
			}
		}
	}
}

func splitArm(k string) (kind, dim, val string) {
	parts := [3]string{}
	i := 0
	start := 0
	for j := 0; j < len(k) && i < 2; j++ {
		if k[j] == '|' {
			parts[i] = k[start:j]
			i++
			start = j + 1
		}
	}
	parts[i] = k[start:]
	return parts[0], parts[1], parts[2]
}

func cmdMemory(args []string) error {
	if err := serverOnly("memory", args); err != nil {
		return err
	}
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	action := "show"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "show", "list":
		m, err := state.LoadMemory(r)
		if err != nil {
			return err
		}
		st, _ := state.Load(r)
		printMemory(os.Stdout, m, st, time.Now().Unix())
		return nil
	case "clear":
		defer r.Lock()()
		return state.SaveMemory(r, &state.Memory{})
	}
	return fmt.Errorf("unknown memory action %q (show, clear)", action)
}
