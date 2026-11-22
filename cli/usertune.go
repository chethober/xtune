package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// Per-user tuning (state.UserTune): an admin sets a user's policy with `xtune users tune` (or the
// app's Manage tab, or a management link); the user sets their own preferences from the app's
// Check tab through GET/PUT /PATH/TOKEN/prefs. What the user gets is userView.

// userView is the deployment as user u sees it from network asn (its ranking, if fresh) at local
// hour (-1: unknown): their configs only, pinned ones first, with their client settings.
func userView(st *state.State, u state.User, asn string, hour int, now time.Time) (*state.State, string) {
	view, netName := st, ""
	if len(st.Networks) > 0 && asn != "" {
		view, netName = forNetworkAt(st, asn, now, hour)
	}
	view = personalRank(view, u, asn, hour, now)
	if u.Tune == nil {
		return view, netName
	}
	cp := *view
	p, prefs := st.Policy(u), u.Tune.Prefs
	cp.EntryView = func(es []state.Entry, via string) []state.Entry {
		return state.TrimTo(state.EntryOrder(p, prefs, es, via), p.Max)
	}
	return &cp, netName
}

// needsASN says whether serving u depends on the network they ask from (an ASN lookup).
func needsASN(st *state.State, u state.User) bool { return len(st.Networks) > 0 }

// personalRank is where a user's own measurements reorder their configs (see ranks.go); without
// any, the view as it is.
var personalRank = func(st *state.State, u state.User, asn string, hour int, now time.Time) *state.State { return st }

// ---------------------------------------------------------------- validation

var (
	tuneFPs     = []string{"chrome", "firefox", "safari", "edge", "ios", "android", "random", "randomized", "360", "qq"}
	tuneModes   = []string{"auto", "packet-up", "stream-up", "stream-one", "ws", "junk", "quic"}
	selectorRe  = regexp.MustCompile(`^(via:)?[a-z0-9][a-z0-9._-]{0,62}$`)
	resolverRe  = regexp.MustCompile(`^(udp:[0-9a-fA-F.:\[\]]+:[0-9]{1,5}|doh:https://[^\s]{4,200}|dot:[a-zA-Z0-9.-]{1,200}:[0-9]{1,5})$`)
	errTuneLock = errors.New("your admin set this; it cannot be changed")
)

func checkSelectors(what string, sels []string) error {
	if len(sels) > 64 {
		return fmt.Errorf("%s: at most 64", what)
	}
	for _, s := range sels {
		if !selectorRe.MatchString(s) {
			return fmt.Errorf("%s: %q is not a config ID, kind, family (%s) or via:RELAY", what, s, strings.Join(state.TuneFamilies, ", "))
		}
	}
	return nil
}

// checkOverride accepts only client settings the apps and links know.
func checkOverride(o state.KnobOverride) error {
	switch {
	case o.FP != "" && !slices.Contains(tuneFPs, o.FP):
		return fmt.Errorf("fp %q: one of %s", o.FP, strings.Join(tuneFPs, ", "))
	case o.Frag != "" && !protocols.ValidFrag(o.Frag):
		return fmt.Errorf("frag %q: none, frag-mild or frag-aggr", o.Frag)
	case o.Mode != "" && !slices.Contains(tuneModes, o.Mode):
		return fmt.Errorf("mode %q: one of %s", o.Mode, strings.Join(tuneModes, ", "))
	case o.Split != "" && o.Split != "cdn-up" && o.Split != "none":
		return fmt.Errorf("split %q: cdn-up or none", o.Split)
	case o.MTU != 0 && (o.MTU < 1200 || o.MTU > 1500):
		return fmt.Errorf("mtu %d: 1200 to 1500", o.MTU)
	case o.Resolver != "" && !resolverRe.MatchString(o.Resolver):
		return fmt.Errorf("resolver %q: udp:IP:53, doh:URL or dot:HOST:853", o.Resolver)
	case o.Addr != "" && net.ParseIP(o.Addr) == nil:
		return fmt.Errorf("addr %q: an IP address", o.Addr)
	case o.ALPN != "" && o.ALPN != "h3" && o.ALPN != "h2":
		return fmt.Errorf("alpn %q: h2 or h3", o.ALPN)
	}
	return nil
}

func checkKnobs(ks map[string]state.KnobOverride) error {
	if len(ks) > 64 {
		return errors.New("knobs: at most 64")
	}
	for sel, o := range ks {
		if err := checkSelectors("knobs", []string{sel}); err != nil {
			return err
		}
		if err := checkOverride(o); err != nil {
			return fmt.Errorf("knobs %s: %w", sel, err)
		}
	}
	return nil
}

func checkPolicy(st *state.State, p state.TunePolicy) error {
	for what, sels := range map[string][]string{"allow": p.Allow, "deny": p.Deny, "pin": p.Pin} {
		if err := checkSelectors(what, sels); err != nil {
			return err
		}
	}
	for _, l := range p.Lock {
		if !slices.Contains(state.Lockable, l) {
			return fmt.Errorf("lock %q: one of %s", l, strings.Join(state.Lockable, ", "))
		}
	}
	if p.Max < 0 || p.Max > 500 {
		return errors.New("max: 0 (all) to 500")
	}
	return checkKnobs(p.Knobs)
}

func checkPrefs(pr state.TunePrefs) error {
	for what, sels := range map[string][]string{"pin": pr.Pin, "hide": pr.Hide} {
		if err := checkSelectors(what, sels); err != nil {
			return err
		}
	}
	return checkKnobs(pr.Knobs)
}

// ---------------------------------------------------------------- changes

// TuneChange is a change to a user's policy: nil fields stay as they are.
type TuneChange struct {
	Preset   *string                       `json:"preset,omitempty"`
	Allow    *[]string                     `json:"allow,omitempty"`
	Deny     *[]string                     `json:"deny,omitempty"`
	Pin      *[]string                     `json:"pin,omitempty"`
	Max      *int                          `json:"max,omitempty"`
	Knobs    map[string]state.KnobOverride `json:"knobs,omitempty"` // added; an empty override removes one
	Lock     *[]string                     `json:"lock,omitempty"`
	SelfTune *bool                         `json:"selfTune,omitempty"`
	Clear    bool                          `json:"clear,omitempty"`      // the policy back to none first
	Prefs    *state.TunePrefs              `json:"prefs,omitempty"`      // the user's preferences, replaced
	ClearPre bool                          `json:"clearPrefs,omitempty"` // forget the user's preferences
}

func (c TuneChange) apply(st *state.State, u *state.User) error {
	t := u.Tune
	if t == nil {
		t = &state.UserTune{}
	}
	if c.Clear {
		t.Policy, t.Preset = state.TunePolicy{}, ""
	}
	if c.ClearPre {
		t.Prefs = state.TunePrefs{}
	}
	p := &t.Policy
	if c.Preset != nil {
		if *c.Preset != "" && !hasPreset(st, *c.Preset) {
			return fmt.Errorf("no preset %q (xtune presets)", *c.Preset)
		}
		t.Preset = *c.Preset
	}
	for _, f := range []struct {
		src *[]string
		dst *[]string
	}{{c.Allow, &p.Allow}, {c.Deny, &p.Deny}, {c.Pin, &p.Pin}, {c.Lock, &p.Lock}} {
		if f.src != nil {
			*f.dst = cleanList(*f.src)
		}
	}
	if c.Max != nil {
		p.Max = *c.Max
	}
	if c.SelfTune != nil {
		p.SelfTune = *c.SelfTune
	}
	for sel, o := range c.Knobs {
		if p.Knobs == nil {
			p.Knobs = map[string]state.KnobOverride{}
		}
		if o.IsZero() {
			delete(p.Knobs, sel)
		} else {
			p.Knobs[sel] = o
		}
	}
	if len(p.Knobs) == 0 {
		p.Knobs = nil
	}
	if c.Prefs != nil {
		if err := checkPrefs(*c.Prefs); err != nil {
			return err
		}
		t.Prefs = *c.Prefs
	}
	if err := checkPolicy(st, *p); err != nil {
		return err
	}
	if t.Preset == "" && isZeroPolicy(t.Policy) && isZeroPrefs(t.Prefs) {
		u.Tune = nil
	} else {
		u.Tune = t
	}
	return nil
}

func hasPreset(st *state.State, name string) bool { _, ok := st.TunePresets[name]; return ok }

func isZeroPolicy(p state.TunePolicy) bool {
	return len(p.Allow)+len(p.Deny)+len(p.Pin)+len(p.Knobs)+len(p.Lock) == 0 && p.Max == 0 && !p.SelfTune
}

func isZeroPrefs(p state.TunePrefs) bool {
	return len(p.Pin)+len(p.Hide)+len(p.Knobs) == 0 && p.Auto == nil && p.Tuned == 0
}

func cleanList(xs []string) []string {
	var out []string
	for _, x := range xs {
		for _, s := range strings.Split(x, ",") {
			if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// SetUserTune changes a user's tuning and applies it on the server: configs a policy denies stop
// letting them in.
func SetUserTune(r *remote.Server, name string, c TuneChange) (*state.State, error) {
	return changeUser(r, name, func(st *state.State, u *state.User) error { return c.apply(st, u) })
}

// SetPreset adds or replaces a named policy; DeletePreset removes one no user has.
func SetPreset(r *remote.Server, name string, p state.TunePolicy) error {
	if !planNameRe.MatchString(name) {
		return fmt.Errorf("preset name %q: letters, digits, dashes", name)
	}
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if err := checkPolicy(st, p); err != nil {
		return err
	}
	if st.TunePresets == nil {
		st.TunePresets = map[string]state.TunePreset{}
	}
	st.TunePresets[name] = p
	return commit(r, st) // users with the preset may lose (or get back) configs
}

func DeletePreset(r *remote.Server, name string) error {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if !hasPreset(st, name) {
		return fmt.Errorf("no preset %q", name)
	}
	for _, u := range st.Users {
		if u.Tune != nil && u.Tune.Preset == name {
			return fmt.Errorf("%s uses preset %q: change their tuning first", u.Name, name)
		}
	}
	delete(st.TunePresets, name)
	return state.Save(r, st)
}

var planNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)

// ---------------------------------------------------------------- what a user's tuning gives

// tuneConfig is one config as a user's tuning sees it.
type tuneConfig struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Via     string `json:"via,omitempty"`     // the relay it goes through
	Allowed bool   `json:"allowed"`           // the policy lets the user have it
	Listed  bool   `json:"listed"`            // in their subscription (not hidden, within max)
	Order   int    `json:"order,omitempty"`   // its place there, from 1
	Knobs   string `json:"knobs,omitempty"`   // its client settings, as tuning labels them
	Pinned  bool   `json:"pinned,omitempty"`  // pinned by the policy or the user
	Message string `json:"message,omitempty"` // why it is not listed
}

// userTuneView is `xtune users tune NAME --json` and the app's view of a user's tuning.
type userTuneView struct {
	Name    string             `json:"name"`
	Preset  string             `json:"preset,omitempty"`
	Policy  state.TunePolicy   `json:"policy"`    // the user's own policy
	Applied state.TunePolicy   `json:"effective"` // with the preset's
	Prefs   state.TunePrefs    `json:"prefs"`
	Configs []tuneConfig       `json:"configs"`
	Presets []string           `json:"presets,omitempty"`
	Kinds   []string           `json:"families"` // the selector words besides IDs and kinds
	Locks   []string           `json:"lockable"`
	Ranked  map[string]float64 `json:"ranked,omitempty"` // their own scores, by config ID (personal ranking)
}

func tuneViewOf(st *state.State, u state.User) userTuneView {
	v := userTuneView{Name: u.Name, Applied: st.Policy(u), Configs: []tuneConfig{}, Kinds: state.TuneFamilies, Locks: state.Lockable}
	if u.Tune != nil {
		v.Preset, v.Policy, v.Prefs = u.Tune.Preset, u.Tune.Policy, u.Tune.Prefs
	}
	for n := range st.TunePresets {
		v.Presets = append(v.Presets, n)
	}
	sort.Strings(v.Presets)
	view, _ := userView(st, u, "", -1, time.Now())
	listed := map[string]int{}
	for i, c := range protocols.ClientProfileFor(view, u).Configs {
		listed[c.ID] = i + 1
	}
	pins := append(slices.Clone(v.Applied.Pin), v.Prefs.Pin...)
	for _, s := range protocols.SubStates(st) {
		var shown []state.Entry
		if view.EntryView != nil {
			shown = view.EntryView(s.Entries, s.Naming.Via)
		} else {
			shown = s.Entries
		}
		for _, e := range s.Entries {
			c := tuneConfig{ID: e.ID, Kind: e.Kind, Label: protocols.KindLabel(e.Kind), Via: s.Naming.Via,
				Allowed: v.Applied.Allowed(e, s.Naming.Via)}
			if i := slices.IndexFunc(shown, func(x state.Entry) bool { return x.ID == e.ID }); i >= 0 {
				c.Knobs = knobLabel(shown[i].Knobs())
			} else {
				c.Knobs = knobLabel(e.Knobs())
			}
			c.Order = listed[e.ID]
			c.Listed = c.Order > 0
			c.Pinned = slices.ContainsFunc(pins, func(p string) bool { return state.MatchEntry(p, e, s.Naming.Via) })
			switch {
			case !c.Allowed:
				c.Message = "not allowed"
			case !c.Listed && slices.ContainsFunc(v.Prefs.Hide, func(p string) bool { return state.MatchEntry(p, e, s.Naming.Via) }):
				c.Message = "hidden by the user"
			case !c.Listed && e.Kind != "openvpn-udp" && e.Kind != "openvpn-tcp" && e.Kind != "ikev2":
				c.Message = "over the most configs"
			}
			v.Configs = append(v.Configs, c)
		}
	}
	return v
}

// knobLabel is a config's client settings the way tune results show them.
func knobLabel(k state.Knobs) string {
	var parts []string
	add := func(name, v string) {
		if v != "" && v != "none" {
			parts = append(parts, name+"="+v)
		}
	}
	add("fp", k.FP)
	add("frag", k.Frag)
	add("mode", k.Mode)
	add("split", k.Split)
	if k.MTU != 0 {
		add("mtu", strconv.Itoa(k.MTU))
	}
	add("via", k.Resolver)
	add("ip", k.Addr)
	add("alpn", k.ALPN)
	return strings.Join(parts, " ")
}

func printUserTune(w io.Writer, v userTuneView) {
	p := v.Applied
	fmt.Fprintf(w, "# %s\n", v.Name)
	line := func(k, val string) {
		if val != "" {
			fmt.Fprintf(w, "  %-10s %s\n", k, val)
		}
	}
	line("preset", v.Preset)
	line("allow", strings.Join(p.Allow, ", "))
	line("deny", strings.Join(p.Deny, ", "))
	line("pin", strings.Join(p.Pin, ", "))
	if p.Max > 0 {
		line("max", strconv.Itoa(p.Max))
	}
	for _, sel := range sortedKeys(p.Knobs) {
		line("knobs", sel+": "+knobLabel(knobsOf(p.Knobs[sel])))
	}
	line("locked", strings.Join(p.Lock, ", "))
	if p.SelfTune {
		line("self-tune", "on (the user can tune their configs from the app)")
	}
	if len(v.Prefs.Pin)+len(v.Prefs.Hide)+len(v.Prefs.Knobs) > 0 || v.Prefs.Tuned > 0 {
		fmt.Fprintln(w, "  the user's own:")
		line("  pin", strings.Join(v.Prefs.Pin, ", "))
		line("  hide", strings.Join(v.Prefs.Hide, ", "))
		for _, sel := range sortedKeys(v.Prefs.Knobs) {
			line("  knobs", sel+": "+knobLabel(knobsOf(v.Prefs.Knobs[sel])))
		}
		if v.Prefs.Tuned > 0 {
			line("  tuned", time.Unix(v.Prefs.Tuned, 0).Format("2006-01-02 15:04"))
		}
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tCONFIG\tVIA\tKIND\tSETTINGS\tNOTE")
	for _, c := range v.Configs {
		n := "-"
		if c.Order > 0 {
			n = strconv.Itoa(c.Order)
		}
		note := c.Message
		if c.Pinned {
			note = strings.TrimSpace("pinned " + note)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", n, c.ID, orStr(c.Via, "-"), c.Kind, orStr(c.Knobs, "-"), note)
	}
	tw.Flush()
}

func knobsOf(o state.KnobOverride) state.Knobs {
	e := state.Entry{}
	o.Apply(&e)
	return e.Knobs()
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---------------------------------------------------------------- CLI

// knobFlag is --knob SEL:key=value,key=value (repeatable).
type knobFlag map[string]state.KnobOverride

func (k knobFlag) String() string { return "" }

func (k knobFlag) Set(s string) error {
	sel, kvs, ok := strings.Cut(s, ":")
	if !ok || sel == "" {
		return errors.New("--knob SELECTOR:fp=chrome,frag=frag-aggr (or SELECTOR:none to remove)")
	}
	if strings.HasPrefix(sel, "via") { // via:RELAY:fp=...
		rest, more, ok2 := strings.Cut(kvs, ":")
		if !ok2 {
			return errors.New("--knob via:RELAY:key=value")
		}
		sel, kvs = sel+":"+rest, more
	}
	o, err := parseOverride(kvs)
	if err != nil {
		return err
	}
	k[sel] = o
	return nil
}

// parseOverride reads "fp=chrome,frag=frag-aggr,mtu=1280" ("none": no override).
func parseOverride(s string) (state.KnobOverride, error) {
	var o state.KnobOverride
	if s == "none" || s == "" {
		return o, nil
	}
	for _, kv := range splitKV(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return o, fmt.Errorf("knob %q: key=value", kv)
		}
		switch k {
		case "fp":
			o.FP = v
		case "frag":
			o.Frag = v
		case "mode":
			o.Mode = v
		case "split":
			o.Split = v
		case "mtu":
			n, err := strconv.Atoi(v)
			if err != nil {
				return o, fmt.Errorf("mtu %q", v)
			}
			o.MTU = n
		case "resolver", "via":
			o.Resolver = v
		case "addr", "ip":
			o.Addr = v
		case "alpn":
			o.ALPN = v
		default:
			return o, fmt.Errorf("knob %q: fp, frag, mode, split, mtu, resolver, addr or alpn", k)
		}
	}
	return o, checkOverride(o)
}

// splitKV splits on commas that start a new key=value (a DoH URL may hold commas).
func splitKV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if len(out) > 0 && !strings.Contains(p, "=") {
			out[len(out)-1] += "," + p
			continue
		}
		out = append(out, p)
	}
	return out
}

// tuneFlags are the policy flags `users tune` and `presets set` share.
type tuneFlags struct {
	allow, deny, pin, lock, preset, selfTune *string
	max                                      *int
	knobs                                    knobFlag
	clear, clearPrefs                        *bool
}

func addTuneFlags(fs *flag.FlagSet) *tuneFlags {
	t := &tuneFlags{knobs: knobFlag{}}
	t.allow = fs.String("allow", "", "tune: only these configs (IDs, kinds, families, via:RELAY; \"\" = all)")
	t.deny = fs.String("deny", "", "tune: never these configs (the server refuses them too)")
	t.pin = fs.String("pin", "", "tune: these configs first, in this order")
	t.lock = fs.String("lock", "", "tune: what the user cannot change: order, hide, knobs")
	t.preset = fs.String("preset", "", "tune: a preset's policy under the user's (\"\" = none)")
	t.selfTune = fs.String("self-tune", "", "tune: on lets the user tune their configs from the app")
	t.max = fs.Int("max", 0, "tune: most configs in the subscription (0 = all)")
	fs.Var(t.knobs, "knob", "tune: SELECTOR:fp=chrome,frag=frag-aggr,mode=packet-up (repeat; SELECTOR:none removes)")
	t.clear = fs.Bool("clear", false, "tune: drop the policy first")
	t.clearPrefs = fs.Bool("clear-prefs", false, "tune: forget the user's own preferences")
	return t
}

func (t *tuneFlags) change(set map[string]bool) (TuneChange, error) {
	c := TuneChange{Clear: *t.clear, ClearPre: *t.clearPrefs}
	list := func(name string, v *string) *[]string {
		if !set[name] {
			return nil
		}
		l := cleanList([]string{*v})
		return &l
	}
	c.Allow, c.Deny, c.Pin, c.Lock = list("allow", t.allow), list("deny", t.deny), list("pin", t.pin), list("lock", t.lock)
	if set["preset"] {
		c.Preset = t.preset
	}
	if set["max"] {
		c.Max = t.max
	}
	if set["self-tune"] {
		on, err := onOff(*t.selfTune)
		if err != nil {
			return c, fmt.Errorf("--self-tune: %w", err)
		}
		c.SelfTune = &on
	}
	if len(t.knobs) > 0 {
		c.Knobs = t.knobs
	}
	return c, nil
}

func onOff(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "yes", "true", "1":
		return true, nil
	case "off", "no", "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("%q: on or off", s)
}

// usersTune is `xtune users tune NAME [flags] [--json]`: show, or change, a user's tuning.
func usersTune(r *remote.Server, name string, t *tuneFlags, set map[string]bool, asJSON bool) error {
	c, err := t.change(set)
	if err != nil {
		return err
	}
	var st *state.State
	if c.Clear || c.ClearPre || c.Allow != nil || c.Deny != nil || c.Pin != nil || c.Lock != nil || c.Preset != nil ||
		c.Max != nil || c.SelfTune != nil || len(c.Knobs) > 0 {
		st, err = SetUserTune(r, name, c)
	} else {
		st, err = requireState(r)
	}
	if err != nil {
		return err
	}
	_, u := st.User(name)
	if u == nil {
		return fmt.Errorf("no user %q", name)
	}
	v := tuneViewOf(st, *u)
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	printUserTune(os.Stdout, v)
	return nil
}

// usersTuneJSON is `xtune users tune NAME --set JSON`: a whole TuneChange, for the app.
func usersTuneSet(r *remote.Server, name, body string) error {
	var c TuneChange
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return fmt.Errorf("--set: %w", err)
	}
	st, err := SetUserTune(r, name, c)
	if err != nil {
		return err
	}
	_, u := st.User(name)
	return json.NewEncoder(os.Stdout).Encode(tuneViewOf(st, *u))
}

func cmdPresets(args []string) error {
	if err := serverOnly("presets", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("presets", flag.ExitOnError)
	t := addTuneFlags(fs)
	asJSON := fs.Bool("json", false, "machine-readable output")
	pos := parse(fs, args)
	if len(pos) == 0 {
		pos = []string{"list"}
	}
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	switch pos[0] {
	case "list", "ls":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(J{"presets": orMapP(st.TunePresets)})
		}
		if len(st.TunePresets) == 0 {
			fmt.Println("no presets (xtune presets set NAME --allow cdn --knob cdn:frag=frag-aggr)")
			return nil
		}
		for _, n := range sortedKeys(st.TunePresets) {
			users := 0
			for _, u := range st.Users {
				if u.Tune != nil && u.Tune.Preset == n {
					users++
				}
			}
			printUserTune(os.Stdout, userTuneView{Name: fmt.Sprintf("%s (%d users)", n, users), Applied: st.TunePresets[n]})
		}
		return nil
	case "set", "add":
		if len(pos) < 2 {
			return errors.New("presets set NAME [--allow ..] [--deny ..] [--pin ..] [--max N] [--knob ..] [--lock ..] [--self-tune on]")
		}
		st, err := requireState(r)
		if err != nil {
			return err
		}
		u := state.User{Name: pos[1]}
		if p, ok := st.TunePresets[pos[1]]; ok && !*t.clear {
			u.Tune = &state.UserTune{Policy: p}
		}
		c, err := t.change(flagsSet(fs))
		if err != nil {
			return err
		}
		c.Preset = nil
		if err := c.apply(st, &u); err != nil {
			return err
		}
		var p state.TunePolicy
		if u.Tune != nil {
			p = u.Tune.Policy
		}
		if err := SetPreset(r, pos[1], p); err != nil {
			return err
		}
		fmt.Printf("preset %s saved; give it to users with: xtune users tune NAME --preset %s\n", pos[1], pos[1])
		return nil
	case "del", "delete", "rm":
		if len(pos) < 2 {
			return errors.New("presets del NAME")
		}
		return DeletePreset(r, pos[1])
	}
	return fmt.Errorf("unknown presets action %q", pos[0])
}

func orMapP(m map[string]state.TunePreset) map[string]state.TunePreset {
	if m == nil {
		return map[string]state.TunePreset{}
	}
	return m
}

// ---------------------------------------------------------------- the user's own preferences

// prefsBody is what PUT /PATH/TOKEN/prefs takes.
type prefsBody struct {
	Pin   []string                      `json:"pin"`
	Hide  []string                      `json:"hide"`
	Knobs map[string]state.KnobOverride `json:"knobs"`
	Auto  *bool                         `json:"auto,omitempty"`
}

// serveUserPrefs answers GET (the user's tuning view) and PUT (their preferences) on
// /PATH/TOKEN/prefs. What the policy locks is refused; the policy itself is the admin's.
func (h subHandler) serveUserPrefs(w http.ResponseWriter, req *http.Request, st *state.State, u state.User) {
	w.Header().Set("Content-Type", "application/json")
	if req.Method == http.MethodGet {
		json.NewEncoder(w).Encode(tuneViewOf(st, u))
		return
	}
	var in prefsBody
	if err := json.NewDecoder(io.LimitReader(req.Body, 32<<10)).Decode(&in); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	if h.r == nil {
		http.Error(w, `{"error":"temporarily unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	st, err := setUserPrefs(h.r, u.Name, in)
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(J{"error": err.Error()})
		return
	}
	_, nu := st.User(u.Name)
	json.NewEncoder(w).Encode(tuneViewOf(st, *nu))
}

// setUserPrefs saves a user's own preferences. They change no server config (only what the
// subscription lists), so this saves state without touching Xray.
func setUserPrefs(r *remote.Server, name string, in prefsBody) (*state.State, error) {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	_, u := st.User(name)
	if u == nil {
		return nil, fmt.Errorf("no user %q", name)
	}
	p := st.Policy(*u)
	pr := state.TunePrefs{}
	if u.Tune != nil {
		pr = u.Tune.Prefs
	}
	locked := func(part string, changed bool) error {
		if changed && p.Locked(part) {
			return fmt.Errorf("%s: %w", part, errTuneLock)
		}
		return nil
	}
	in.Pin, in.Hide = cleanList(in.Pin), cleanList(in.Hide)
	if err := errors.Join(locked("order", !slices.Equal(in.Pin, pr.Pin)), locked("hide", !slices.Equal(in.Hide, pr.Hide)),
		locked("knobs", !knobsEqual(in.Knobs, pr.Knobs))); err != nil {
		return nil, err
	}
	pr.Pin, pr.Hide, pr.Knobs, pr.Auto = in.Pin, in.Hide, in.Knobs, in.Auto
	if len(pr.Knobs) == 0 {
		pr.Knobs = nil
	}
	if err := checkPrefs(pr); err != nil {
		return nil, err
	}
	if u.Tune == nil {
		u.Tune = &state.UserTune{}
	}
	u.Tune.Prefs = pr
	if u.Tune.Preset == "" && isZeroPolicy(u.Tune.Policy) && isZeroPrefs(pr) {
		u.Tune = nil
	}
	return st, state.Save(r, st)
}

func knobsEqual(a, b map[string]state.KnobOverride) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
