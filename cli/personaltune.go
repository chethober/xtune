package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// Personal tunes: a user whose policy allows it (SelfTune) tunes the client settings of their own
// configs from the app's Check tab. Client settings need nothing on the server, so this measures
// the deployed configs with the user's own credentials, no test instance:
//
//	GET  /PATH/TOKEN/tune   a client profile of variants of the user's configs: their current
//	                        settings, the usual ones, and samples from each config's client-side
//	                        search space (ClientSpace), guided by what worked on this network
//	POST /PATH/TOKEN/tune   the app's measurements (a ClientReport keyed by variant); the best
//	                        settings of each config become the user's own (TunePrefs.Knobs), and the
//	                        results count for the network's ranking like a report
//
// A variant's key is "ENTRY_ID|fp=..,frag=..": the server re-checks the settings it names.

// personalTuneLimit is how often a user's personal tune results are taken.
const personalTuneLimit = 10 * time.Minute

// personalTuneMax is the most variants one personal tune measures.
const personalTuneMax = 40

var lastPersonal sync.Map // user name -> time.Time

// personalPrior is what guides a personal tune's samples (memory.go fills it in).
var personalPrior = func(st *state.State, u state.User, asn string) Prior { return Prior{} }

// overrideString is a knob override as a variant key carries it.
func overrideString(k state.Knobs) string {
	var parts []string
	add := func(n, v string) {
		if v != "" {
			parts = append(parts, n+"="+v)
		}
	}
	add("fp", k.FP)
	add("frag", k.Frag)
	add("mode", k.Mode)
	add("split", k.Split)
	if k.MTU != 0 {
		add("mtu", strconv.Itoa(k.MTU))
	}
	add("resolver", k.Resolver)
	add("addr", k.Addr)
	add("alpn", k.ALPN)
	return strings.Join(parts, ",")
}

// personalVariants are the variants a personal tune measures for user u.
func personalVariants(st *state.State, u state.User, prior Prior, seed uint64) []*Variant {
	view, _ := userView(st, u, "", -1, time.Now())
	es := st.Entries
	if view.EntryView != nil {
		es = view.EntryView(st.Entries, "")
	}
	rng := TuneOpts{Seed: seed}.rng()
	var vs []*Variant
	seen := map[string]bool{}
	for _, e := range es {
		if e.HandshakeOnly() || e.Kind == "mtproto" {
			continue // other apps run these; nothing to tune client-side here
		}
		sp := ClientSpace(e)
		ks := slices.Clone(sp.Seeds[:min(len(sp.Seeds), 8)]) // the current settings, then the usual ones
		if sp.Searchable() {
			ks = append(ks, sp.Sample(rng, 3, prior)...)
		}
		for _, k := range ks {
			v := variantFor(e, k)
			key := e.ID + "|" + overrideString(k)
			if seen[key] {
				continue
			}
			seen[key] = true
			v.Label = key
			vs = append(vs, v)
		}
	}
	// at most personalTuneMax, keeping every config's current settings
	if len(vs) > personalTuneMax {
		cur := map[string]bool{}
		var keep, rest []*Variant
		for _, v := range vs {
			if !cur[v.Entry.ID] {
				cur[v.Entry.ID] = true
				keep = append(keep, v)
			} else {
				rest = append(rest, v)
			}
		}
		rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
		vs = append(keep, rest[:max(0, personalTuneMax-len(keep))]...)
	}
	return vs
}

// personalProfile is the variants as configs the app measures, each keyed by its variant key.
func personalProfile(st *state.State, u state.User, vs []*Variant, now time.Time) *TestProfile {
	tp := &TestProfile{ID: "personal-" + u.Name, Expires: now.Add(personalTuneLimit * 3).Unix(),
		Profile: protocols.ClientProfile{V: protocols.ClientProfileVersion, Name: "xtune personal tune", User: u.Name,
			Country: st.Country, Probe: protocols.ProbeURL}}
	for i, v := range vs {
		c, ok := protocols.ClientEntryFor(st, v.Entry, u, v.K, "proxy-"+strconv.Itoa(i))
		if !ok {
			tp.Skipped = append(tp.Skipped, v.Label)
			continue
		}
		c.ID, c.Key, c.Name = v.Entry.ID+"#"+strconv.Itoa(i), v.Label, v.Entry.ID+" "+knobLabel(v.K)
		tp.Profile.Configs = append(tp.Profile.Configs, c)
	}
	tp.Profile.Frag = protocols.FragOutbounds(protocols.ConfigFrags(tp.Profile.Configs)...)
	return tp
}

var errNoSelfTune = errors.New("personal tuning is not on for you: ask your admin (xtune users tune NAME --self-tune on)")

// serveTune answers GET and POST /PATH/TOKEN/tune.
func (h subHandler) serveTune(w http.ResponseWriter, req *http.Request, st *state.State, u state.User, now time.Time) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, err error) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(J{"error": err.Error()})
	}
	if !st.Policy(u).SelfTune {
		fail(http.StatusForbidden, errNoSelfTune)
		return
	}
	if req.Method == http.MethodGet {
		asn := clientNetwork(req, st, now)
		vs := personalVariants(st, u, personalPrior(st, u, asn), uint64(now.UnixNano()))
		json.NewEncoder(w).Encode(personalProfile(st, u, vs, now))
		return
	}
	var in ClientReport
	if err := json.NewDecoder(io.LimitReader(req.Body, 128<<10)).Decode(&in); err != nil || !asnRe.MatchString(in.ASN) ||
		len(in.Name) > 80 || len(in.Results) == 0 || len(in.Results) > 200 {
		fail(http.StatusBadRequest, errors.New("bad report"))
		return
	}
	if t, ok := lastPersonal.Load(u.Name); ok && now.Sub(t.(time.Time)) < personalTuneLimit {
		fail(http.StatusTooManyRequests, fmt.Errorf("one personal tune every %d minutes", int(personalTuneLimit.Minutes())))
		return
	}
	if h.r == nil {
		fail(http.StatusServiceUnavailable, errors.New("temporarily unavailable"))
		return
	}
	res, err := applyPersonalTune(h.r, u.Name, in, now)
	if err != nil {
		fail(http.StatusUnprocessableEntity, err)
		return
	}
	lastPersonal.Store(u.Name, now)
	json.NewEncoder(w).Encode(res)
}

// PersonalResult is what a personal tune changed.
type PersonalResult struct {
	Measured int               `json:"measured"`
	Chosen   map[string]string `json:"chosen"` // config ID -> its settings now ("" = its own)
	View     userTuneView      `json:"view"`
}

// personalPick reads a personal tune's results: per config, the measured variants.
func personalPick(st *state.State, u state.User, in ClientReport) (map[string][]*Variant, int) {
	byID := map[string]state.Entry{}
	for _, e := range st.Entries {
		byID[e.ID] = e
	}
	groups := map[string][]*Variant{}
	n := 0
	for _, r := range in.Results {
		id, kv, ok := strings.Cut(r.Key, "|")
		e, known := byID[id]
		if !ok || !known || r.N <= 0 || r.N > 100 || r.Ok < 0 || r.Ok > r.N || r.Down < 0 || r.Up < 0 || r.Down > 10000 || r.Up > 10000 {
			continue
		}
		o, err := parseOverride(kv)
		if err != nil {
			continue
		}
		if !st.Policy(u).Allowed(e, "") {
			continue
		}
		v := variantFor(e, knobsOf(o))
		if kv == "" {
			v.K = e.Knobs()
		}
		v.Ok, v.N, v.Warm, v.Down, v.Up = r.Ok, r.N, r.Warm, r.Down, r.Up
		v.Score = score(v)
		groups[id] = append(groups[id], v)
		n++
	}
	return groups, n
}

// applyPersonalTune saves the best settings of each measured config as the user's own, and counts
// the results for the network's ranking.
func applyPersonalTune(r *remote.Server, name string, in ClientReport, now time.Time) (*PersonalResult, error) {
	var res *PersonalResult
	var learn func(*state.Memory)
	err := func() error {
		defer r.Lock()()
		st, err := requireState(r)
		if err != nil {
			return err
		}
		_, u := st.User(name)
		if u == nil {
			return fmt.Errorf("no user %q", name)
		}
		p := st.Policy(*u)
		if !p.SelfTune {
			return errNoSelfTune
		}
		groups, n := personalPick(st, *u, in)
		if n == 0 {
			return errors.New("no results for your configs")
		}
		if u.Tune == nil {
			u.Tune = &state.UserTune{}
		}
		pr := &u.Tune.Prefs
		res = &PersonalResult{Measured: n, Chosen: map[string]string{}}
		for _, id := range sortedKeys(groups) {
			best := pick(groups[id])
			if best == nil || p.Locked("knobs") {
				continue
			}
			i := slices.IndexFunc(st.Entries, func(e state.Entry) bool { return e.ID == id })
			if sameKnobs(best.K, st.Entries[i].Knobs()) {
				delete(pr.Knobs, id) // the config's own settings won
				res.Chosen[id] = ""
				continue
			}
			if pr.Knobs == nil {
				pr.Knobs = map[string]state.KnobOverride{}
			}
			pr.Knobs[id] = state.OverrideOf(best.K)
			res.Chosen[id] = knobLabel(best.K)
		}
		if len(pr.Knobs) == 0 {
			pr.Knobs = nil
		}
		pr.Tuned = now.Unix()
		learn = recordPersonal(st, *u, in, groups, now)
		if err := state.Save(r, st); err != nil {
			return err
		}
		res.View = tuneViewOf(st, *u)
		return nil
	}()
	if err != nil {
		return nil, err
	}
	if learn != nil {
		changeMemory(r, func(m *state.Memory, _ *state.State) { learn(m) })
	}
	// the configs' best results count for this network like a report from the app
	rep := ClientReport{ASN: in.ASN, Name: in.Name, Hour: in.Hour}
	st, err := requireState(r)
	if err == nil {
		_, u := st.User(name)
		groups, _ := personalPick(st, *u, in)
		for id, g := range groups {
			best := pick(g)
			i := slices.IndexFunc(st.Entries, func(e state.Entry) bool { return e.ID == id })
			if best == nil || i < 0 {
				if i >= 0 && len(g) > 0 { // nothing worked: blocked there
					rep.Results = append(rep.Results, ClientReportResult{Key: state.EntryKey(st.Entries[i]), Ok: 0, N: g[0].N, Warm: -1})
				}
				continue
			}
			rep.Results = append(rep.Results, ClientReportResult{Key: state.EntryKey(st.Entries[i]), Ok: best.Ok, N: best.N,
				Warm: best.Warm, Down: best.Down, Up: best.Up})
		}
		if len(rep.Results) > 0 {
			applyClientReport(r.Lock, func() (*state.State, error) { return requireState(r) },
				func(st *state.State) error { return state.Save(r, st) }, rep, now)
		}
	}
	return res, nil
}

// sameKnobs compares client settings, "" and "none" being the same fragment setting.
func sameKnobs(a, b state.Knobs) bool {
	if a.Frag == "" {
		a.Frag = "none"
	}
	if b.Frag == "" {
		b.Frag = "none"
	}
	return a == b
}

// recordPersonal is what a personal tune teaches the user's and network's memory (memory.go),
// saved once the state is no longer locked.
var recordPersonal = func(st *state.State, u state.User, in ClientReport, groups map[string][]*Variant, now time.Time) func(*state.Memory) {
	return nil
}
