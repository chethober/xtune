package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"time"
)

// The deployment (systemd unit "xray-autotune") and its users live in state.json on the server.
// Users are managed only there: in the server TUI (sudo xtune) or with `xtune users` on the server.

func LoadState(r *Server) (*State, error) {
	out, err := r.Root("cat " + remoteDir + "/state.json 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var st State
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, fmt.Errorf("parse state.json: %w", err)
	}
	return &st, nil
}

// saveState writes state.json and the certificate. Users get a subscription token if they lack one.
func saveState(r *Server, st *State) error {
	for i := range st.Users {
		if st.Users[i].SubToken == "" {
			st.Users[i].SubToken = randHex(16)
		}
	}
	stJSON, _ := json.MarshalIndent(st, "", " ")
	for _, f := range []struct{ path, body string }{
		{remoteDir + "/state.json", string(stJSON)},
		{remoteDir + "/hy.crt", st.CertPEM},
		{remoteDir + "/hy.key", st.KeyPEM},
	} {
		if err := r.Upload(f.path, []byte(f.body), "600"); err != nil {
			return err
		}
	}
	return nil
}

// writeConfig writes config.json for the active users and checks it with Xray, without a restart.
func writeConfig(r *Server, st *State, active []User) error {
	cfgJSON, _ := json.MarshalIndent(st.ServerConfig(st.Entries, active), "", " ")
	if err := r.Upload(remoteDir+"/config.json", cfgJSON, "600"); err != nil {
		return err
	}
	_, err := r.Helper("check")
	return err
}

// Apply saves everything and restarts the service; used when entries or ports change.
func Apply(r *Server, st *State) error {
	if st.APIPort == 0 {
		st.APIPort = pickAPIPort(st)
	}
	us, err := LoadUsage(r)
	if err != nil {
		return err
	}
	if err := saveState(r, st); err != nil {
		return err
	}
	active := st.activeUsers(us, time.Now())
	if err := writeConfig(r, st, active); err != nil {
		return err
	}
	if err := restart(r, st); err != nil {
		return err
	}
	us.ActiveKey = activeKey(st, active)
	return saveUsage(r, us)
}

// commit saves a user change and applies it to the running Xray through its API, so nobody else is
// disconnected. It falls back to a restart when the API cannot do it (older deployments, or the
// first user on an inbound).
func commit(r *Server, st *State) error {
	if st.APIPort == 0 {
		return Apply(r, st)
	}
	us, err := LoadUsage(r)
	if err != nil {
		return err
	}
	if err := saveState(r, st); err != nil {
		return err
	}
	active := st.activeUsers(us, time.Now())
	if err := writeConfig(r, st, active); err != nil {
		return err
	}
	if err := reconcile(r, st, active); err != nil {
		if err := restart(r, st); err != nil {
			return err
		}
	}
	us.ActiveKey = activeKey(st, active)
	return saveUsage(r, us)
}

// pickAPIPort chooses a loopback port for the Xray API that no entry uses.
func pickAPIPort(st *State) int {
	for {
		p := 40000 + rand.IntN(20000)
		clash := p == st.Sub.Port
		for _, e := range st.Entries {
			clash = clash || e.Port == p
		}
		if !clash {
			return p
		}
	}
}

// restart (re)starts xray-autotune with the saved config and opens the entries' firewall ports.
func restart(r *Server, st *State) error {
	out, err := r.Helper(append([]string{"apply"}, portSpecs(st.Entries)...)...)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(out, "active") || strings.HasSuffix(out, "inactive") {
		return fmt.Errorf("xray-autotune did not start: %s", out)
	}
	return nil
}

// Deploy keeps the top n tuned configs running, plus the CDN entry when it connected: it is the
// fallback for when the server's IP gets blocked, so it is kept even when slower. On a server that
// already has a deployment, the server keys, certificate and users are kept: only ports, SNIs and
// client settings change, so subscriptions keep working.
func Deploy(r *Server, res *TuneResult, n int) (*State, error) {
	return DeployVariants(r, res.State, deployChoice(res.Best, n))
}

// DeployVariants deploys the chosen variants with their tuned client settings, keeping an
// existing deployment's identity and users (see Deploy).
func DeployVariants(r *Server, st *State, chosen []*Variant) (*State, error) {
	if len(chosen) == 0 {
		return nil, fmt.Errorf("nothing to deploy")
	}
	st.Entries = nil
	for _, v := range chosen {
		e := v.Entry
		e.FP, e.Mode, e.Frag, e.Split = v.K.FP, v.K.Mode, v.K.Frag, v.K.Split
		st.Entries = append(st.Entries, e)
	}
	old, err := LoadState(r)
	if err != nil {
		return nil, err
	}
	if old != nil {
		st.RealityPriv, st.RealityPub, st.ShortID = old.RealityPriv, old.RealityPub, old.ShortID
		st.VencDec, st.VencEnc, st.SSKey = old.VencDec, old.VencEnc, old.SSKey
		st.CertSNI, st.CertHash, st.CertPEM, st.KeyPEM = old.CertSNI, old.CertHash, old.CertPEM, old.KeyPEM
		st.Users, st.APIPort, st.Country, st.Sub = old.Users, old.APIPort, old.Country, old.Sub
		st.Check, st.Warp, st.Notify = old.Check, old.Warp, old.Notify
	}
	if err := Apply(r, st); err != nil {
		return st, err
	}
	return st, restartSub(r, st)
}

func deployChoice(best []*Variant, n int) []*Variant {
	if n <= 0 {
		return nil
	}
	if n > len(best) {
		n = len(best)
	}
	chosen := append([]*Variant(nil), best[:n]...)
	for _, v := range best[n:] {
		if v.Entry.CDN() {
			chosen = append(chosen, v)
		}
	}
	return chosen
}

func requireState(r *Server) (*State, error) {
	st, err := LoadState(r)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, fmt.Errorf("nothing is deployed on this server yet - run a tune from your computer and deploy first")
	}
	return st, nil
}

func (st *State) Links(u User) []string {
	var out []string
	for _, e := range st.Entries {
		out = append(out, st.Link(e, u, e.Knobs()))
	}
	return out
}

// Subscription is the base64 body most clients accept as an imported subscription.
func (st *State) Subscription(u User) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(st.Links(u), "\n")))
}

// ---------------------------------------------------------------- users

func validUserName(name string) error {
	if name == "" || strings.ContainsAny(name, " /\\\"'#?&%<>") {
		return fmt.Errorf("invalid user name %q", name)
	}
	if name == checkUserName {
		return fmt.Errorf("%q is reserved for xtune check", name)
	}
	return nil
}

// Limits are optional per-user limits; nil fields are left unchanged.
type Limits struct {
	QuotaGB *float64 // 0 = unlimited
	Days    *int     // expiry N days from now, 0 = never
	Devices *int     // 0 = unlimited
	Reset   *string  // usage reset period: monthly | weekly | "" (never)
}

var resetPeriods = []string{"monthly", "weekly"}

func validReset(p string) error {
	if p != "" && !slices.Contains(resetPeriods, p) {
		return fmt.Errorf("unknown reset period %q (monthly, weekly or none)", p)
	}
	return nil
}

// nextReset is when a usage period that starts at from ends.
func nextReset(period string, from time.Time) time.Time {
	if period == "weekly" {
		return from.AddDate(0, 0, 7)
	}
	return from.AddDate(0, 1, 0)
}

func (l Limits) apply(u *User) {
	if l.Reset != nil {
		u.Reset, u.ResetAt = *l.Reset, 0
		if u.Reset != "" {
			u.ResetAt = nextReset(u.Reset, time.Now()).Unix()
		}
	}
	if l.QuotaGB != nil {
		u.Quota = int64(*l.QuotaGB * (1 << 30))
	}
	if l.Days != nil {
		u.Expires = 0
		if *l.Days > 0 {
			u.Expires = time.Now().AddDate(0, 0, *l.Days).Unix()
		}
	}
	if l.Devices != nil {
		u.Devices = *l.Devices
	}
}

// changeUser loads the state under the server lock, lets fn change it and commits the result.
func changeUser(r *Server, name string, fn func(st *State, u *User) error) (*State, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	_, u := st.User(name)
	if u == nil {
		return nil, fmt.Errorf("no user %q", name)
	}
	if err := fn(st, u); err != nil {
		return nil, err
	}
	return st, commit(r, st)
}

func AddUser(r *Server, name string, l Limits) (*State, User, error) {
	if err := validUserName(name); err != nil {
		return nil, User{}, err
	}
	if l.Reset != nil {
		if err := validReset(*l.Reset); err != nil {
			return nil, User{}, err
		}
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, User{}, err
	}
	if _, u := st.User(name); u != nil {
		return nil, User{}, fmt.Errorf("user %q already exists", name)
	}
	u := NewUser(name)
	l.apply(&u)
	st.Users = append(st.Users, u)
	if err := resetUsage(r, name); err != nil { // a re-added name starts from zero
		return nil, User{}, err
	}
	return st, u, commit(r, st)
}

func DeleteUser(r *Server, name string) error {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	i, _ := st.User(name)
	if i < 0 {
		return fmt.Errorf("no user %q", name)
	}
	st.Users = append(st.Users[:i], st.Users[i+1:]...)
	return commit(r, st)
}

func SetUserEnabled(r *Server, name string, on bool) error {
	_, err := changeUser(r, name, func(_ *State, u *User) error { u.Enabled = on; return nil })
	return err
}

func SetLimits(r *Server, name string, l Limits) (*State, error) {
	if l.Reset != nil {
		if err := validReset(*l.Reset); err != nil {
			return nil, err
		}
	}
	return changeUser(r, name, func(_ *State, u *User) error { l.apply(u); return nil })
}

// RotateUser issues new credentials, invalidating the user's old links.
func RotateUser(r *Server, name string) (*State, User, error) {
	var nu User
	st, err := changeUser(r, name, func(_ *State, u *User) error {
		u.UUID, u.SSKey = newUUID(), ssKey()
		nu = *u
		return nil
	})
	return st, nu, err
}

// ResetSubLink gives the user a new subscription URL; the old one stops working.
func ResetSubLink(r *Server, name string) (*State, error) {
	return changeUser(r, name, func(_ *State, u *User) error { u.SubToken = randHex(16); return nil })
}

// ResetUsage sets the user's traffic back to zero, re-enabling them if they were over quota.
func ResetUsage(r *Server, name string) error {
	_, err := changeUser(r, name, func(*State, *User) error { return resetUsage(r, name) })
	return err
}

func resetUsage(r *Server, name string) error {
	us, err := LoadUsage(r)
	if err != nil {
		return err
	}
	delete(us.Users, name)
	return saveUsage(r, us)
}

// SetCountry chooses which country's sites client configs send direct ("" = none).
func SetCountry(r *Server, code string) error {
	if _, ok := countries[code]; code != "" && !ok {
		return fmt.Errorf("unknown country %q (supported: %s)", code, countryList())
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	st.Country = code
	return saveState(r, st) // only client configs change
}

func countryList() string {
	var cs []string
	for c := range countries {
		cs = append(cs, c)
	}
	sort.Strings(cs)
	return strings.Join(cs, ", ")
}

// ---------------------------------------------------------------- addresses

// SetDomains changes the direct and CDN domains used in links. Setting a CDN domain on a deployment
// without a CDN entry adds one with default settings (re-tune to measure it); clearing it removes
// the CDN entries. added reports whether a CDN entry was added.
func SetDomains(r *Server, direct, cdn string, log func(string)) (st *State, added bool, err error) {
	defer r.lock()()
	if st, err = requireState(r); err != nil {
		return nil, false, err
	}
	if err := checkDomains(st.Host, direct, cdn, log); err != nil {
		return nil, false, err
	}
	st.Domain, st.CDNDomain = direct, cdn
	if cdn == "" {
		var keep []Entry
		for _, e := range st.Entries {
			if !e.CDN() {
				keep = append(keep, e)
			}
		}
		st.Entries = keep
	} else if !hasCDN(st.Entries) {
		out, err := r.Helper("probe")
		if err != nil {
			return nil, false, err
		}
		taken := map[int]bool{}
		for _, e := range st.Entries {
			taken[e.Port] = true
		}
		e, ok := cdnEntry(usedPorts(out), taken)
		if !ok {
			return nil, false, fmt.Errorf("none of the CDN ports %v is free on this server", cdnPorts)
		}
		e.FP, e.Frag, e.Mode = "chrome", "none", "packet-up" // the mode CDNs pass most reliably
		st.Entries, added = append(st.Entries, e), true
	}
	if st.Sub.Mode == "cdn" && cdn == "" || st.Sub.Mode == "direct" && direct == "" {
		log("the subscription server's domain was removed; turning the subscription server off")
		if _, err := r.Helper("sub-disable"); err != nil {
			return nil, false, err
		}
		st.Sub.Mode = ""
	}
	if err := Apply(r, st); err != nil {
		return nil, false, err
	}
	return st, added, restartSub(r, st)
}
