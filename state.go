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

// saveState writes state.json and the certificate. Users get a subscription token and a
// WireGuard address if they lack one.
func saveState(r *Server, st *State) error {
	for i := range st.Users {
		if st.Users[i].SubToken == "" {
			st.Users[i].SubToken = randHex(16)
		}
	}
	st.assignWGIPs()
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

// writeConfig writes config.json for the active users and checks it with Xray, without a restart,
// and the active users of the SSH tunnels and VPNs, which pick them up by themselves.
func writeConfig(r *Server, st *State, active []User) error {
	cfgJSON, _ := json.MarshalIndent(st.ServerConfig(st.Entries, active), "", " ")
	if err := r.Upload(remoteDir+"/config.json", cfgJSON, "600"); err != nil {
		return err
	}
	if _, err := r.Helper("check"); err != nil {
		return err
	}
	if hasEngine(st.Entries, "tunnel") {
		tc, _ := json.MarshalIndent(st.TunnelConf(st.Entries, active, tunnelStatsFile), "", " ")
		if err := r.Upload(remoteDir+"/tunnels.json", tc, "600"); err != nil {
			return err
		}
	}
	if hasEngine(st.Entries, "awg") {
		ac, _ := json.MarshalIndent(st.AWGConf(st.Entries, active, awgStatsFile), "", " ")
		if err := r.Upload(remoteDir+"/awg.json", ac, "600"); err != nil {
			return err
		}
	}
	if !hasEngine(st.Entries, "openvpn") && !hasEngine(st.Entries, "ikev2") {
		return nil
	}
	for p, c := range st.vpnFiles(st.Entries, active, vpnDir) {
		if err := r.Upload(p, []byte(c), "600"); err != nil {
			return err
		}
	}
	if hasEngine(st.Entries, "ikev2") {
		_, err := r.Helper("vpn-reload")
		return err
	}
	return nil
}

const tunnelStatsFile = remoteDir + "/tunnels-stats.json"

// Apply saves everything and restarts the service; used when entries or ports change. The
// Cloudflare Worker, if any, is pointed at the deployed worker-ws inbounds first.
func Apply(r *Server, st *State) error {
	if st.APIPort == 0 {
		st.APIPort = pickAPIPort(st)
	}
	if _, err := st.ensureKeys(); err != nil {
		return err
	}
	if err := syncWorker(st); err != nil {
		return err
	}
	if hasEngine(st.Entries, "openvpn") || hasEngine(st.Entries, "ikev2") {
		if err := st.ensureVPNCert(); err != nil { // the address may have changed
			return err
		}
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

// restart (re)starts xray-autotune with the saved config and opens the entries' firewall ports,
// then the tunnel server and VPNs the entries need (stopping those they no longer need).
func restart(r *Server, st *State) error {
	out, err := r.Helper(append([]string{"apply"}, portSpecs(st.Entries)...)...)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(out, "active") || strings.HasSuffix(out, "inactive") {
		return fmt.Errorf("xray-autotune did not start: %s", out)
	}
	specs := vpnSpecs(st.Entries)
	if len(specs) > 0 { // a restored or migrated deployment may land on a server without them
		if out, err := r.Helper("vpn-install"); err != nil || !strings.HasSuffix(out, "ok") {
			return fmt.Errorf("installing OpenVPN/strongSwan: %v %s", err, tail(out, 200))
		}
	}
	specs = append(specs, awgSpecs(st.Entries)...)
	if hasEngine(st.Entries, "tunnel") {
		specs = append([]string{"tunnels"}, specs...)
	}
	out, err = r.Helper(append([]string{"extras"}, specs...)...)
	if err != nil {
		return fmt.Errorf("tunnels and VPNs: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] != "active" {
			return fmt.Errorf("%s did not start (%s)", f[0], f[1])
		}
	}
	return nil
}

// Deploy keeps the top n tuned configs running, plus the CDN entry when it connected: it is the
// fallback for when the server's IP gets blocked, so it is kept even when slower. On a server that
// already has a deployment, the server keys, certificate and users are kept: only ports, SNIs and
// client settings change, so subscriptions keep working.
func Deploy(r *Server, res *TuneResult, n int) (*State, error) {
	st, err := DeployVariants(r, res.State, deployChoice(res.Best, n))
	if err != nil {
		return st, err
	}
	scores, addrs := variantRank(st.Entries, res.All)
	if err := RecordNetwork(r, res.Network, scores, addrs); err != nil {
		fmt.Println("[!] ranking the configs for this network:", err) // subscriptions just stay unranked for it
	}
	return st, nil
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
		e.FP, e.Mode, e.Frag, e.Split, e.MTU, e.Resolver = v.K.FP, v.K.Mode, v.K.Frag, v.K.Split, v.K.MTU, v.K.Resolver
		e.Addr = v.K.Addr
		st.Entries = append(st.Entries, e)
	}
	renumberAWG(st.Entries) // off the test instance's ranges
	old, err := LoadState(r)
	if err != nil {
		return nil, err
	}
	if old != nil {
		st.keepIdentity(old)
		st.Users, st.APIPort, st.Country, st.Sub = old.Users, old.APIPort, old.Country, old.Sub
		st.Check, st.Warp, st.Notify, st.LegacyBot = old.Check, old.Warp, old.Notify, old.LegacyBot
		st.Public, st.Manage = old.Public, old.Manage
		st.Plans, st.Peers, st.Follow = old.Plans, old.Peers, old.Follow
		st.Backups, st.Networks = old.Backups, old.Networks
		st.forgetStaleRanks()          // configs that moved are measured anew
		if st.Cloudflare.Token == "" { // a token given for this tune replaces the deployment's
			st.Cloudflare = old.Cloudflare
		}
		if st.Worker == nil {
			st.Worker = old.Worker
		}
	}
	if err := Apply(r, st); err != nil {
		return st, err
	}
	return st, startServices(r, st)
}

// deployChoice is the top n, plus a working CDN config, plus the best working config of each
// opt-in protocol that was tested (SSH, the DNS tunnel and the VPNs serve their own apps, so they
// do not compete with the Xray configs for a place).
func deployChoice(best []*Variant, n int) []*Variant {
	if n <= 0 {
		return nil
	}
	if n > len(best) {
		n = len(best)
	}
	chosen := append([]*Variant(nil), best[:n]...)
	have := map[string]bool{}
	for _, v := range chosen {
		have[v.Entry.family()] = true
	}
	for _, v := range best[n:] {
		f := v.Entry.family()
		switch {
		case v.Entry.CDN():
			chosen = append(chosen, v)
		case f != "" && !have[f] && v.Ok > 0:
			chosen = append(chosen, v)
			have[f] = true
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
	QuotaGB  *float64 // 0 = unlimited
	Days     *int     // expiry N days from now, 0 = never
	Devices  *int     // 0 = unlimited
	Reset    *string  // usage reset period: monthly | weekly | "" (never)
	Plan     *string  // plan name, recorded on the user; withPlan fills the other fields from it
	Reseller *string  // reseller (management link) who manages the user
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
	if l.Plan != nil {
		u.Plan = *l.Plan
	}
	if l.Reseller != nil {
		u.Reseller = *l.Reseller
	}
}

// changeUser loads the state under the server lock, lets fn change it and commits the result.
func changeUser(r *Server, name string, fn func(st *State, u *User) error) (*State, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	if st.Follow != nil {
		return nil, errFollower(st)
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
	if st.Follow != nil {
		return nil, User{}, errFollower(st)
	}
	if _, u := st.User(name); u != nil {
		return nil, User{}, fmt.Errorf("user %q already exists", name)
	}
	if l, err = l.withPlan(st); err != nil {
		return nil, User{}, err
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
	if st.Follow != nil {
		return errFollower(st)
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
	return changeUser(r, name, func(st *State, u *User) error {
		l, err := l.withPlan(st)
		if err != nil {
			return err
		}
		l.apply(u)
		return nil
	})
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

// SetDNSDomain changes the DNS tunnel's zone ("" removes the DNS tunnel). With a Cloudflare
// token, the zone is delegated to the server.
func SetDNSDomain(r *Server, zone string, log func(string)) (*State, error) {
	zone = strings.TrimSuffix(strings.ToLower(zone), ".")
	if zone != "" && (strings.ContainsAny(zone, "/: ") || strings.Count(zone, ".") < 2) {
		return nil, fmt.Errorf("%q: use a subdomain such as t.example.com", zone)
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	st.DNSDomain = zone
	if zone == "" {
		st.Entries = filterEntries(st.Entries, func(e Entry) bool { return e.Kind != "dnstt" })
	}
	if zone != "" && st.Cloudflare.Token != "" {
		ip, err := serverIP(st.Host)
		if err != nil {
			return nil, err
		}
		next := *st
		next.Domain, next.CDNDomain = "", ""
		if err := CloudflarePoint(&next, ip, func(s string) { log("DNS " + s) }); err != nil {
			return nil, err
		}
	} else if zone != "" {
		log(fmt.Sprintf("add these DNS records: %s A <server IP> (DNS only), and %s NS %s", nsHost(zone), zone, nsHost(zone)))
	}
	if zone != "" && !slices.ContainsFunc(st.Entries, func(e Entry) bool { return e.Kind == "dnstt" }) {
		log("re-run a tune with --protocols dnstt to add the DNS tunnel")
	}
	return st, Apply(r, st)
}

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
