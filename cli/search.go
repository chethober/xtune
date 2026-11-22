package main

import (
	"fmt"
	mrand "math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/state"
)

// Searching client settings instead of trying a fixed grid. Each kind of config has a search space
// (SpaceFor): typed dimensions, each a choice (fingerprint, XHTTP mode, ALPN) or a range
// (fragment length and interval), marked client-side (any user can change it, see personal tunes)
// or server-side (a redeploy changes it). A tune starts from seeds (the settings that usually
// work, so nothing the old grid found is lost), adds samples from the space, guided by what worked
// before on this network (Prior, see memory.go), and, after the first round, mutates the best
// (one dimension at a time, Geneva-style) for a second round. RunTests screens cheaply and only
// gives the rest of the probes and the throughput test to the survivors (successive halving).

// Dim is one dimension of a config's search space.
type Dim struct {
	Name   string   // fp | frag | mode | alpn | split | mtu | resolver
	Server bool     // changing it means a redeploy (only admin tunes)
	Values []string // the choices; for frag, "range" samples a spec
}

// Space is what tuning may change on one config.
type Space struct {
	Entry state.Entry
	Dims  []Dim
	Seeds []state.Knobs // tried first, always
}

var (
	fpChoices     = []string{"chrome", "firefox", "safari", "edge", "randomized", "ios", "android"}
	xhttpModes    = []string{"auto", "packet-up", "stream-up", "stream-one"}
	fragPackets   = []string{"tlshello", "tlshello", "1-2", "1-3"} // the ClientHello most often
	searchSamples = map[string]int{"smart": 3, "deep": 6}          // random samples per searchable config
)

// SpaceFor is entry e's search space; seeds are what the fixed grid tried (ExpandVariants), so the
// search never tries less than it did.
func SpaceFor(e state.Entry, depth string, resolvers []string) Space {
	sp := Space{Entry: e}
	for _, v := range ExpandVariants([]state.Entry{e}, depth, resolvers...) {
		sp.Seeds = append(sp.Seeds, v.K)
	}
	fp := Dim{Name: "fp", Values: fpChoices}
	frag := Dim{Name: "frag", Values: []string{"none", "frag-mild", "frag-aggr", "range", "range"}}
	switch {
	case e.Kind == "reality-tcp", e.Kind == "trojan-reality", e.Kind == "reality-grpc", e.Kind == "worker-ws":
		sp.Dims = []Dim{fp, frag}
	case e.Kind == "reality-xhttp", e.Kind == "cdn-xhttp":
		sp.Dims = []Dim{fp, frag, {Name: "mode", Values: xhttpModes}}
		if e.CDN() {
			sp.Dims = append(sp.Dims, Dim{Name: "alpn", Values: []string{"", "", "h3"}})
		}
	case e.Kind == "vlessenc-xhttp":
		sp.Dims = []Dim{{Name: "mode", Values: xhttpModes}}
	}
	// the server side of every config: tuning finds them with new entries (planEntries), so they
	// are listed for what a personal tune must leave alone
	sp.Dims = append(sp.Dims, Dim{Name: "sni", Server: true}, Dim{Name: "port", Server: true}, Dim{Name: "path", Server: true})
	return sp
}

// Searchable says whether the space has client-side dimensions to sample.
func (sp Space) Searchable() bool {
	return slices.ContainsFunc(sp.Dims, func(d Dim) bool { return !d.Server && len(d.Values) > 0 })
}

// Prior is what is known about the network being tuned for (memory.go); the zero Prior knows
// nothing.
type Prior struct {
	// W says how likely a setting value is to work there, from 0 to 1 (0.5: nothing known).
	W func(kind, dim, value string) float64
	// Frags are fragment ranges that worked there for a kind, best first.
	Frags func(kind string) []string
}

// sampleValue picks a value of d: by the prior's weights, with a floor so every value keeps a
// chance (exploration).
func sampleValue(rng *mrand.Rand, kind string, d Dim, prior Prior) string {
	if prior.W == nil {
		return d.Values[rng.IntN(len(d.Values))]
	}
	ws := make([]float64, len(d.Values))
	total := 0.0
	for i, v := range d.Values {
		w := prior.W(kind, d.Name, v)
		ws[i] = 0.15 + w // explore: a value that never worked still gets picked now and then
		total += ws[i]
	}
	x := rng.Float64() * total
	for i, w := range ws {
		if x -= w; x <= 0 {
			return d.Values[i]
		}
	}
	return d.Values[len(d.Values)-1]
}

// randomFrag is a fragment spec from the ranges that get past ClientHello filters: short pieces
// (length 5-200 bytes) a few milliseconds apart.
func randomFrag(rng *mrand.Rand) string {
	lo := 5 + rng.IntN(120)
	hi := lo + 5 + rng.IntN(80)
	ilo := 1 + rng.IntN(15)
	ihi := ilo + rng.IntN(15)
	return protocols.MakeFragSpec(fragPackets[rng.IntN(len(fragPackets))], lo, hi, ilo, ihi)
}

func setDim(k *state.Knobs, kind, dim, v string, rng *mrand.Rand, prior Prior) {
	switch dim {
	case "fp":
		k.FP = v
	case "frag":
		if v == "range" {
			v = randomFrag(rng)
			// half the time, a range that worked here before, moved a little
			if prior.Frags != nil && rng.IntN(2) == 0 {
				if good := prior.Frags(kind); len(good) > 0 {
					if m, ok := protocols.FragSpec(good[rng.IntN(min(len(good), 3))]); ok {
						v = nudgeFrag(rng, m)
					}
				}
			}
		}
		k.Frag = v
	case "mode":
		k.Mode = v
	case "alpn":
		k.ALPN = v
		if v == "h3" {
			k.FP = "" // uTLS fingerprints are for TCP
		}
	}
}

// Sample draws n settings from the space's client-side dimensions, none of them a seed.
func (sp Space) Sample(rng *mrand.Rand, n int, prior Prior) []state.Knobs {
	var out []state.Knobs
	seen := map[state.Knobs]bool{}
	for _, s := range sp.Seeds {
		seen[s] = true
	}
	for tries := 0; len(out) < n && tries < n*20; tries++ {
		k := state.Knobs{Frag: "none"}
		if len(sp.Seeds) > 0 {
			k = sp.Seeds[0]
		}
		for _, d := range sp.Dims {
			if !d.Server && len(d.Values) > 0 {
				setDim(&k, sp.Entry.Kind, d.Name, sampleValue(rng, sp.Entry.Kind, d, prior), rng, prior)
			}
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Mutate changes one client-side dimension of k: a range spec moves its numbers a little, the
// rest take another value.
func (sp Space) Mutate(rng *mrand.Rand, k state.Knobs, prior Prior) state.Knobs {
	var dims []Dim
	for _, d := range sp.Dims {
		if !d.Server && len(d.Values) > 1 {
			dims = append(dims, d)
		}
	}
	if len(dims) == 0 {
		return k
	}
	d := dims[rng.IntN(len(dims))]
	if d.Name == "frag" {
		if m, ok := protocols.FragSpec(k.Frag); ok && rng.IntN(3) > 0 {
			k.Frag = nudgeFrag(rng, m)
			return k
		}
	}
	cur := map[string]string{"fp": k.FP, "frag": k.Frag, "mode": k.Mode, "alpn": k.ALPN}[d.Name]
	for range 10 {
		if v := sampleValue(rng, sp.Entry.Kind, d, prior); v != cur || v == "range" {
			setDim(&k, sp.Entry.Kind, d.Name, v, rng, prior)
			return k
		}
	}
	return k
}

// Cross takes each client-side dimension from a or b.
func (sp Space) Cross(rng *mrand.Rand, a, b state.Knobs) state.Knobs {
	k := a
	if rng.IntN(2) == 0 {
		k.FP = b.FP
	}
	if rng.IntN(2) == 0 {
		k.Frag = b.Frag
	}
	if rng.IntN(2) == 0 {
		k.Mode, k.Split = b.Mode, b.Split
	}
	if rng.IntN(2) == 0 {
		k.ALPN = b.ALPN
		if k.ALPN == "h3" {
			k.FP = ""
		}
	}
	return k
}

// nudgeFrag moves a spec's ranges by up to a third (at least a step).
func nudgeFrag(rng *mrand.Rand, m map[string]string) string {
	rng2 := func(s string) (int, int) {
		a, b, _ := strings.Cut(s, "-")
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)
		return x, y
	}
	nudge := func(x, floor, ceil int) int {
		d := max(1, x/3)
		x += rng.IntN(2*d+1) - d
		return min(max(x, floor), ceil)
	}
	lo, hi := rng2(m["length"])
	ilo, ihi := rng2(m["interval"])
	lo = nudge(lo, 1, 1000)
	hi = max(lo, nudge(hi, 1, 1400))
	ilo = nudge(ilo, 0, 100)
	ihi = max(ilo, nudge(ihi, 0, 200))
	return protocols.MakeFragSpec(m["packets"], lo, hi, ilo, ihi)
}

// variantFor is a variant of e with knobs k, labeled like ExpandVariants labels them.
func variantFor(e state.Entry, k state.Knobs) *Variant {
	v := &Variant{Entry: e, K: k, Cold: -1, Warm: -1}
	v.Label = fmt.Sprintf("%s:%d", e.ID, e.Port)
	if e.Reality() {
		parts := strings.Split(e.SNI, ".")
		if len(parts) >= 2 {
			v.Label += " sni=" + parts[len(parts)-2]
		}
	}
	if k.FP != "" {
		v.Label += " fp=" + k.FP
	}
	if k.Frag != "none" && k.Frag != "" {
		v.Label += " " + k.Frag
	}
	if k.Mode != "" {
		v.Label += " mode=" + k.Mode
	}
	if k.Split != "" {
		v.Label += " split=" + k.Split
	}
	if e.Hop != "" {
		v.Label += " hop=" + e.Hop
	}
	if k.MTU != 0 {
		v.Label += fmt.Sprintf(" mtu=%d", k.MTU)
	}
	if k.Resolver != "" {
		v.Label += " via=" + k.Resolver
	}
	if k.ALPN != "" {
		v.Label += " " + k.ALPN
	}
	return v
}

// SearchVariants is the first round: every config's seeds, plus samples from its space.
func SearchVariants(entries []state.Entry, depth string, resolvers []string, prior Prior, rng *mrand.Rand) []*Variant {
	var out []*Variant
	seen := map[string]bool{}
	add := func(v *Variant) {
		if !seen[v.Label] {
			seen[v.Label] = true
			out = append(out, v)
		}
	}
	n := searchSamples[depth]
	for _, e := range entries {
		sp := SpaceFor(e, depth, resolvers)
		for _, k := range sp.Seeds {
			add(variantFor(e, k))
		}
		if !sp.Searchable() || n == 0 {
			continue
		}
		for _, k := range sp.Sample(rng, n, prior) {
			add(variantFor(e, k))
		}
	}
	return out
}

// Evolve is the next round: for each of the best variants that worked, mutations of it and a cross
// with another good one of the same config. Variants already tried are skipped.
func Evolve(best []*Variant, tried []*Variant, depth string, prior Prior, rng *mrand.Rand, perParent int) []*Variant {
	seen := map[string]bool{}
	for _, v := range tried {
		seen[v.Label] = true
	}
	var out []*Variant
	byEntry := map[string][]*Variant{}
	for _, v := range best {
		byEntry[v.Entry.ID] = append(byEntry[v.Entry.ID], v)
	}
	for _, p := range best {
		sp := SpaceFor(p.Entry, depth, nil)
		if !sp.Searchable() || p.Entry.Kind == "wireguard" || p.Entry.Kind == "amneziawg" {
			continue // their variants are test peers the server was started with
		}
		for i := 0; i < perParent; i++ {
			k := sp.Mutate(rng, p.K, prior)
			if mates := byEntry[p.Entry.ID]; i == perParent-1 && len(mates) > 1 {
				k = sp.Cross(rng, p.K, mates[rng.IntN(len(mates))].K)
			}
			v := variantFor(p.Entry, k)
			if p.K.Addr != "" { // a clean-IP variant keeps its IP
				v.K.Addr = p.K.Addr
				v.Label += " ip=" + p.K.Addr
			}
			if p.Via != "" { // a route keeps its relay
				v.Via, v.viaPort, v.viaSt = p.Via, p.viaPort, p.viaSt
				v.Label = "via " + p.Via + " " + v.Label
			}
			if !seen[v.Label] {
				seen[v.Label] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// ClientSpace is the search space a personal tune may use: client-side dimensions only, the
// config's own settings first.
func ClientSpace(e state.Entry) Space {
	sp := SpaceFor(e, "smart", nil)
	sp.Seeds = append([]state.Knobs{e.Knobs()}, sp.Seeds...)
	sp.Dims = slices.DeleteFunc(sp.Dims, func(d Dim) bool { return d.Server })
	return sp
}
