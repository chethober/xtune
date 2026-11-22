package state

import (
	"slices"
	"strings"
)

// Per-user tuning: which configs a user gets, in which order, and with which client settings.
// Two layers: the Policy an admin (or the user's reseller) sets, which the user cannot change, and
// the Prefs the user sets from the app (or a personal tune fills in), within the policy. A named
// TunePreset can stand in for a policy and is shared by many users.
//
// Configs are picked with selectors, so a redeploy (new IDs, ports, keys) keeps what was chosen:
//
//	ID          one config, e.g. "r-tcp" (on a follower: "NAME-ID", through a relay: "RELAY-ID")
//	KIND        every config of a kind, e.g. "reality-tcp", "cdn-xhttp"
//	FAMILY      reality | cdn | worker | ssh | vpn | udp | tcp | xray
//	via:RELAY   the configs through that relay; "via:direct" the ones that are not

// UserTune is a user's tuning.
type UserTune struct {
	Preset string     `json:"preset,omitempty"` // a TunePreset whose policy applies under this one
	Policy TunePolicy `json:"policy,omitzero"`
	Prefs  TunePrefs  `json:"prefs,omitzero"`
}

// TunePolicy is what an admin decides for a user.
type TunePolicy struct {
	Allow    []string                `json:"allow,omitempty"` // only these configs (empty: all)
	Deny     []string                `json:"deny,omitempty"`  // never these, on the server too
	Pin      []string                `json:"pin,omitempty"`   // listed first, in this order
	Max      int                     `json:"max,omitempty"`   // most configs in the subscription, 0 = all
	Knobs    map[string]KnobOverride `json:"knobs,omitempty"` // client settings by selector
	Lock     []string                `json:"lock,omitempty"`  // what the user cannot change: order, hide, knobs
	SelfTune bool                    `json:"selfTune,omitempty"`
}

// TunePrefs are the user's own choices, and what their personal tunes found.
type TunePrefs struct {
	Pin   []string                `json:"pin,omitempty"`
	Hide  []string                `json:"hide,omitempty"`
	Knobs map[string]KnobOverride `json:"knobs,omitempty"`
	Auto  *bool                   `json:"auto,omitempty"`  // rank by their own measurements (nil: yes)
	Tuned int64                   `json:"tuned,omitempty"` // their last personal tune, unix seconds
}

// TunePreset is a named policy, for many users at once.
type TunePreset = TunePolicy

// Lockable are the parts of a user's preferences a policy can lock.
var Lockable = []string{"order", "hide", "knobs"}

// TuneFamilies are the selector words for groups of kinds.
var TuneFamilies = []string{"reality", "cdn", "worker", "ssh", "vpn", "udp", "tcp", "xray"}

// KnobOverride changes some of a config's client settings; empty fields keep the config's own.
// Frag is a preset name (none, frag-mild, frag-aggr) or a range spec (see protocols.FragSpec).
type KnobOverride struct {
	FP       string `json:"fp,omitempty"`
	Frag     string `json:"frag,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Split    string `json:"split,omitempty"`
	MTU      int    `json:"mtu,omitempty"`
	Resolver string `json:"resolver,omitempty"`
	Addr     string `json:"addr,omitempty"`
	ALPN     string `json:"alpn,omitempty"`
}

func (o KnobOverride) IsZero() bool { return o == KnobOverride{} }

// Apply sets the override's fields on entry e (its tuned client settings).
func (o KnobOverride) Apply(e *Entry) {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&e.FP, o.FP)
	set(&e.Frag, o.Frag)
	set(&e.Mode, o.Mode)
	set(&e.Split, o.Split)
	set(&e.Resolver, o.Resolver)
	set(&e.Addr, o.Addr)
	set(&e.ALPN, o.ALPN)
	if o.MTU != 0 {
		e.MTU = o.MTU
	}
}

// OverrideOf is the override that gives a config knobs k.
func OverrideOf(k Knobs) KnobOverride {
	return KnobOverride{FP: k.FP, Frag: k.Frag, Mode: k.Mode, Split: k.Split, MTU: k.MTU, Resolver: k.Resolver, Addr: k.Addr, ALPN: k.ALPN}
}

// MatchEntry says whether selector sel picks entry e, reached through relay via ("" = direct).
func MatchEntry(sel string, e Entry, via string) bool {
	sel = strings.TrimSpace(sel)
	if v, ok := strings.CutPrefix(sel, "via:"); ok {
		return v == via || v == "direct" && via == ""
	}
	switch sel {
	case "":
		return false
	case e.ID, e.Kind:
		return true
	case "reality":
		return e.Reality()
	case "cdn":
		return e.CDN()
	case "worker":
		return e.Worker()
	case "ssh":
		return e.SSH()
	case "vpn":
		return e.HandshakeOnly() || e.Kind == "wireguard" || e.Kind == "amneziawg"
	case "udp", "tcp":
		return e.Proto() == sel
	case "xray":
		return e.Xray()
	}
	// a relay's or follower's copy of a config: "NAME-ID" also matches the plain ID
	if i := strings.Index(e.ID, "-"); i > 0 && sel == e.ID[i+1:] {
		return true
	}
	return false
}

func matchAny(sels []string, e Entry, via string) bool {
	return slices.ContainsFunc(sels, func(s string) bool { return MatchEntry(s, e, via) })
}

// Policy is the user's policy with its preset's under it: the user's own fields win, lists add up.
func (st *State) Policy(u User) TunePolicy {
	var p TunePolicy
	if u.Tune != nil {
		p = u.Tune.Policy
	}
	if u.Tune == nil || u.Tune.Preset == "" {
		return p
	}
	pre, ok := st.TunePresets[u.Tune.Preset]
	if !ok {
		return p
	}
	out := pre
	out.Allow = orList(p.Allow, pre.Allow)
	out.Deny = append(slices.Clone(pre.Deny), p.Deny...)
	out.Pin = orList(p.Pin, pre.Pin)
	if p.Max > 0 {
		out.Max = p.Max
	}
	out.Knobs = map[string]KnobOverride{}
	for k, v := range pre.Knobs {
		out.Knobs[k] = v
	}
	for k, v := range p.Knobs {
		out.Knobs[k] = v
	}
	out.Lock = append(slices.Clone(pre.Lock), p.Lock...)
	out.SelfTune = pre.SelfTune || p.SelfTune
	return out
}

func orList(a, b []string) []string {
	if len(a) > 0 {
		return a
	}
	return b
}

// Locked says whether the policy keeps the user from changing part ("order", "hide", "knobs").
func (p TunePolicy) Locked(part string) bool { return slices.Contains(p.Lock, part) }

// Allowed says whether the user may use entry e (reached through relay via), on the server too.
func (p TunePolicy) Allowed(e Entry, via string) bool {
	if matchAny(p.Deny, e, via) {
		return false
	}
	if len(p.Allow) == 0 {
		return true
	}
	// "via:" selectors and config selectors are separate filters: both must pass when both are given
	var vias, cfgs []string
	for _, s := range p.Allow {
		if strings.HasPrefix(strings.TrimSpace(s), "via:") {
			vias = append(vias, s)
		} else {
			cfgs = append(cfgs, s)
		}
	}
	return (len(vias) == 0 || matchAny(vias, e, via)) && (len(cfgs) == 0 || matchAny(cfgs, e, via))
}

// ServesUser says whether the server lets user u in through entry e. Relays forward to the same
// inbound, so only config selectors count here: a user may be denied a relay but not its inbound.
func (st *State) ServesUser(u User, e Entry) bool {
	if u.Tune == nil {
		return true
	}
	p := st.Policy(u)
	if matchAny(slices.DeleteFunc(slices.Clone(p.Deny), isVia), e, "") {
		return false
	}
	allow := slices.DeleteFunc(slices.Clone(p.Allow), isVia)
	return len(allow) == 0 || matchAny(allow, e, "")
}

func isVia(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "via:") }

// UsersFor are the users the server lets in through entry e.
func (st *State) UsersFor(e Entry, users []User) []User {
	if !slices.ContainsFunc(users, func(u User) bool { return u.Tune != nil }) {
		return users
	}
	var out []User
	for _, u := range users {
		if st.ServesUser(u, e) {
			out = append(out, u)
		}
	}
	return out
}

// UsersForEngine are the users some entry of engine lets in: the SSH tunnels and AmneziaWG have
// one user list for all their entries.
func (st *State) UsersForEngine(engine string, users []User) []User {
	es := FilterEntries(st.Entries, func(e Entry) bool { return e.Engine() == engine })
	if len(es) == 0 {
		return users
	}
	var out []User
	for _, u := range users {
		if slices.ContainsFunc(es, func(e Entry) bool { return st.ServesUser(u, e) }) {
			out = append(out, u)
		}
	}
	return out
}

// EntryOrder applies a user's tuning to one server's configs (already ranked best first): it
// drops what the policy denies and the user hides, moves pinned configs first (the policy's pins,
// then the user's unless order is locked), sets client settings (the policy's, then the user's
// unless knobs are locked) and keeps at most Max. via is the relay the configs go through.
func EntryOrder(p TunePolicy, prefs TunePrefs, es []Entry, via string) []Entry {
	var out []Entry
	for _, e := range es {
		if !p.Allowed(e, via) || !p.Locked("hide") && matchAny(prefs.Hide, e, via) {
			continue
		}
		for sel, o := range p.Knobs {
			if MatchEntry(sel, e, via) {
				o.Apply(&e)
			}
		}
		if !p.Locked("knobs") {
			for sel, o := range prefs.Knobs {
				if MatchEntry(sel, e, via) && !policyKnob(p, e, via) {
					o.Apply(&e)
				}
			}
		}
		out = append(out, e)
	}
	pins := slices.Clone(p.Pin)
	if !p.Locked("order") {
		pins = append(pins, prefs.Pin...)
	}
	rank := func(e Entry) int {
		for i, s := range pins {
			if MatchEntry(s, e, via) {
				return i
			}
		}
		return len(pins)
	}
	slices.SortStableFunc(out, func(a, b Entry) int { return rank(a) - rank(b) })
	return out
}

// policyKnob says whether the policy sets client settings for e: then the user's do not apply.
func policyKnob(p TunePolicy, e Entry, via string) bool {
	for sel := range p.Knobs {
		if MatchEntry(sel, e, via) {
			return true
		}
	}
	return false
}

// TrimTo keeps at most max configs (0 = all).
func TrimTo[T any](xs []T, max int) []T {
	if max > 0 && len(xs) > max {
		return xs[:max]
	}
	return xs
}
