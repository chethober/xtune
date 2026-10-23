package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Usage is what the enforce job measures, stored in usage.json next to state.json. Traffic
// counters in Xray reset when it restarts, so totals are accumulated here every minute.
type Usage struct {
	Users     map[string]*UserUsage  `json:"users"`
	Blocks    []IPBlock              `json:"blocks,omitempty"`    // device-limit blocks
	ActiveKey string                 `json:"activeKey,omitempty"` // which users config.json was last written for
	Updated   int64                  `json:"updated,omitempty"`
	Notified  map[string]int64       `json:"notified,omitempty"` // alerts sent, so each is sent once
	Failures  int                    `json:"failures,omitempty"` // enforce runs in a row the Xray API failed
	Entries   map[string]*EntryUsage `json:"entries,omitempty"`  // traffic per deployed config, by entryKey
	BackupAt  int64                  `json:"backupAt,omitempty"` // last backup the bot sent
	SNIAt     int64                  `json:"sniAt,omitempty"`    // last check of the REALITY targets
	Synced    map[string][2]int64    `json:"synced,omitempty"`   // follower: traffic already reported to the primary
	Extra     map[string][2]int64    `json:"extra,omitempty"`    // SSH tunnel and VPN counters at the last run
}

// EntryUsage is the traffic of one deployed config. A config that stops carrying anything while
// the others still do is probably blocked on the users' networks.
type EntryUsage struct {
	Bytes  int64 `json:"bytes"`
	First  int64 `json:"first"`            // when it was first seen deployed
	Active int64 `json:"active,omitempty"` // last run it carried traffic
}

type UserUsage struct {
	Up       int64            `json:"up"`
	Down     int64            `json:"down"`
	LastSeen int64            `json:"lastSeen,omitempty"`
	Online   []string         `json:"online,omitempty"` // client IPs at the last check
	Daily    map[string]int64 `json:"daily,omitempty"`  // bytes per day (server date), the last historyDays days
}

// historyDays is how many days of per-user traffic are kept.
const historyDays = 60

func dayKey(t time.Time) string { return t.Format("2006-01-02") }

// addDaily counts n bytes on now's day and drops days older than historyDays.
func (uu *UserUsage) addDaily(now time.Time, n int64) {
	if uu.Daily == nil {
		uu.Daily = map[string]int64{}
	}
	uu.Daily[dayKey(now)] += n
	if len(uu.Daily) > historyDays {
		cut := dayKey(now.AddDate(0, 0, -historyDays))
		for d := range uu.Daily {
			if d < cut {
				delete(uu.Daily, d)
			}
		}
	}
}

// lastDays is the user's traffic on each of the n days up to now, oldest first.
func (uu *UserUsage) lastDays(now time.Time, n int) []int64 {
	out := make([]int64, n)
	for i := range n {
		out[i] = uu.Daily[dayKey(now.AddDate(0, 0, i-n+1))]
	}
	return out
}

// entryKey identifies a deployed config: a redeploy that moves it to another port starts over.
func entryKey(e Entry) string { return fmt.Sprintf("%s/%d", e.ID, e.Port) }

type IPBlock struct {
	IP    string `json:"ip"`
	User  string `json:"user"`
	Until int64  `json:"until"`
}

const (
	deviceRule  = "xtune-devices" // Xray routing rule tag for device-limit blocks
	deviceBlock = 10 * time.Minute
)

func (us *Usage) of(name string) *UserUsage {
	if us.Users == nil {
		us.Users = map[string]*UserUsage{}
	}
	if us.Users[name] == nil {
		us.Users[name] = &UserUsage{}
	}
	return us.Users[name]
}

func LoadUsage(r *Server) (*Usage, error) {
	out, err := r.Root("cat " + remoteDir + "/usage.json 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	us := &Usage{Users: map[string]*UserUsage{}}
	if strings.TrimSpace(out) == "" {
		return us, nil
	}
	if err := json.Unmarshal([]byte(out), us); err != nil {
		return nil, fmt.Errorf("parse usage.json: %w", err)
	}
	if us.Users == nil {
		us.Users = map[string]*UserUsage{}
	}
	return us, nil
}

func saveUsage(r *Server, us *Usage) error {
	b, _ := json.MarshalIndent(us, "", " ")
	return r.Upload(remoteDir+"/usage.json", b, "600")
}

// userStatus is why a user can or cannot connect.
func userStatus(u User, uu *UserUsage, now time.Time) string {
	switch {
	case !u.Enabled:
		return "disabled"
	case u.Expires > 0 && now.Unix() >= u.Expires:
		return "expired"
	case u.Quota > 0 && uu != nil && uu.Up+uu.Down >= u.Quota:
		return "over quota"
	}
	return "active"
}

// checkUserName is the hidden user `xtune check` measures the deployed configs with. It has no
// limits and is not listed with the other users.
const checkUserName = "xtune-check"

// activeUsers are the users who can connect now, plus the check user.
func (st *State) activeUsers(us *Usage, now time.Time) []User {
	var out []User
	for _, u := range st.Users {
		if userStatus(u, us.Users[u.Name], now) == "active" {
			out = append(out, u)
		}
	}
	if st.Check != nil {
		out = append(out, *st.Check)
	}
	return out
}

// applyResets starts the usage of users whose reset time has come from zero. It reports whether
// any user's next reset time changed, so the state needs saving.
func applyResets(st *State, us *Usage, now time.Time) bool {
	changed := false
	for i := range st.Users {
		u := &st.Users[i]
		if u.Reset == "" || u.ResetAt == 0 || now.Unix() < u.ResetAt {
			continue
		}
		if uu := us.Users[u.Name]; uu != nil {
			uu.Up, uu.Down = 0, 0
		}
		next := time.Unix(u.ResetAt, 0)
		for !next.After(now) {
			next = nextReset(u.Reset, next)
		}
		u.ResetAt, changed = next.Unix(), true
	}
	return changed
}

// activeKey identifies a set of active users and their credentials, plus the entries they are on.
func activeKey(st *State, active []User) string {
	var parts []string
	for _, u := range active {
		parts = append(parts, u.Name+":"+u.UUID+":"+u.SSKey)
	}
	sort.Strings(parts)
	for _, e := range st.Entries {
		parts = append(parts, fmt.Sprintf("%s/%d", e.ID, e.Port))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8])
}

// ---------------------------------------------------------------- Xray API

// xrayAPI runs `xray api CMD` against the deployment's loopback API.
func (r *Server) xrayAPI(st *State, cmd string, args ...string) (string, error) {
	if st.APIPort == 0 {
		return "", fmt.Errorf("this deployment has no Xray API yet")
	}
	q := []string{remoteDir + "/xray", "api", cmd, fmt.Sprintf("--server=127.0.0.1:%d", st.APIPort)}
	for _, a := range args {
		q = append(q, shq(a))
	}
	return r.Root(strings.Join(q, " "))
}

func credential(e Entry, u User) string {
	switch e.Kind {
	case "ss2022":
		return u.SSKey
	case "wireguard":
		_, pub := wgUserKey(u)
		return pub
	}
	return u.UUID // VLESS id, Hysteria2 auth
}

// wgLive reports whether the running WireGuard inbound has exactly these users. Xray cannot add
// WireGuard users through its API, so a difference means a restart.
func wgLive(out string, want map[string]string) bool {
	var res struct {
		Users []map[string]any `json:"users"`
	}
	if json.Unmarshal([]byte(out), &res) != nil || len(res.Users) != len(want) {
		return false
	}
	for _, u := range res.Users {
		email, _ := u["email"].(string)
		w, ok := want[email]
		if !ok {
			return false
		}
		if key := findKey(u, "publicKey", "public_key"); key != "" && key != w {
			return false
		}
	}
	return true
}

// findKey returns the first string value under one of the names, anywhere in v.
func findKey(v any, names ...string) string {
	switch x := v.(type) {
	case map[string]any:
		for _, n := range names {
			if s, ok := x[n].(string); ok {
				return s
			}
		}
		for _, y := range x {
			if s := findKey(y, names...); s != "" {
				return s
			}
		}
	case []any:
		for _, y := range x {
			if s := findKey(y, names...); s != "" {
				return s
			}
		}
	}
	return ""
}

var (
	addedRe   = regexp.MustCompile(`Added (\d+) user`)
	removedRe = regexp.MustCompile(`Removed (\d+) user`)
)

func countIs(re *regexp.Regexp, out string, want int) bool {
	m := re.FindStringSubmatch(out)
	n := -1
	if m != nil {
		n, _ = strconv.Atoi(m[1])
	}
	return n == want
}

// reconcile makes the running Xray's users match active, through the API, so user changes need no
// restart and other users keep their connections. Users whose credentials changed are re-added.
func reconcile(r *Server, st *State, active []User) error {
	var add []J
	adds := 0
	for _, e := range filterEntries(st.Entries, Entry.Xray) {
		out, err := r.xrayAPI(st, "inbounduser", "-tag="+e.ID)
		if err != nil {
			return err
		}
		if e.Kind == "wireguard" {
			want := map[string]string{}
			for _, u := range active {
				want[u.Name] = credential(e, u)
			}
			if !wgLive(out, want) {
				return fmt.Errorf("WireGuard users changed: Xray needs a restart")
			}
			continue
		}
		var res struct {
			Users []struct {
				Email   string `json:"email"`
				Account struct{ ID, Auth, Key string }
			} `json:"users"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			return fmt.Errorf("inbounduser %s: %w (%s)", e.ID, err, tail(out, 200))
		}
		live := map[string]string{}
		for _, u := range res.Users {
			live[u.Email] = u.Account.ID + u.Account.Auth + u.Account.Key
		}
		want := map[string]string{}
		var missing []User
		for _, u := range active {
			want[u.Name] = credential(e, u)
			if c, ok := live[u.Name]; !ok || c != want[u.Name] {
				missing = append(missing, u)
			}
		}
		var stale []string
		for email, c := range live {
			if w, ok := want[email]; !ok || w != c {
				stale = append(stale, email)
			}
		}
		if len(stale) > 0 {
			sort.Strings(stale)
			out, err := r.xrayAPI(st, "rmu", append([]string{"-tag=" + e.ID}, stale...)...)
			if err != nil || !countIs(removedRe, out, len(stale)) {
				return fmt.Errorf("remove users from %s: %v %s", e.ID, err, tail(out, 300))
			}
		}
		if len(missing) > 0 {
			add = append(add, st.inbound(e, missing))
			adds += len(missing)
		}
	}
	if adds == 0 {
		return nil
	}
	b, _ := json.Marshal(J{"inbounds": add})
	f := remoteDir + "/adu.json"
	if err := r.Upload(f, b, "600"); err != nil {
		return err
	}
	out, err := r.xrayAPI(st, "adu", f)
	r.Root("rm -f " + shq(f))
	if err != nil || !countIs(addedRe, out, adds) {
		return fmt.Errorf("add users: %v %s", err, tail(out, 300))
	}
	return nil
}

// ---------------------------------------------------------------- enforce (every minute, on the server)

// Enforce accumulates traffic, records who is online, applies device limits and brings the running
// Xray's users in line with quotas, expiry and the state file. The xray-autotune-enforce timer
// runs it every minute.
func Enforce(r *Server) error {
	defer r.lock()()
	st, err := LoadState(r)
	if err != nil || st == nil || st.APIPort == 0 {
		return err
	}
	us, err := LoadUsage(r)
	if err != nil {
		return err
	}
	if removed, err := removeLegacyBot(r, st); err != nil {
		fmt.Println("removing the old Telegram bot:", err) // retried at the next run
	} else if removed {
		if err := saveState(r, st); err != nil {
			return err
		}
	}
	now := time.Now()
	prev, sessions, err := collect(r, st, us, now)
	notifyXray(st, us, err)
	if err != nil {
		saveUsage(r, us) // keeps the alert from repeating
		return err
	}
	if st.Follow != nil {
		changed, err := syncPrimary(st, us)
		if err != nil {
			fmt.Println(err) // the current users keep working; the next run retries
		} else if changed {
			if err := saveState(r, st); err != nil {
				return err
			}
		}
	}
	if applyResets(st, us, now) {
		if err := saveState(r, st); err != nil {
			return err
		}
	}
	notifyUsers(st, us, now)
	notifyEntries(st, us, now)
	if err := applyDeviceLimits(r, st, us, prev, now); err != nil {
		return err
	}
	active := st.activeUsers(us, now)
	if err := kickInactive(r, active, sessions); err != nil {
		fmt.Println("disconnecting VPN sessions:", err)
	}
	if key := activeKey(st, active); key != us.ActiveKey {
		if err := writeConfig(r, st, active); err != nil {
			return err
		}
		if err := reconcile(r, st, active); err != nil {
			if err := restart(r, st); err != nil {
				return err
			}
		}
		us.ActiveKey = key
	} else if err := reconcile(r, st, active); err != nil {
		fmt.Println("reconcile:", err) // config.json is right; the next restart fixes the running set
	}
	us.Updated = now.Unix()
	due := sniDue(st, us, now)
	if err := saveUsage(r, us); err != nil {
		return err
	}
	if due {
		if err := checkSNIs(r); err != nil {
			fmt.Println("REALITY targets:", err)
		}
	}
	return nil
}

// collect adds the traffic since the last run and records online IPs. It returns the IPs each user
// had online at the previous run.
func collect(r *Server, st *State, us *Usage, now time.Time) (map[string][]string, []vpnSession, error) {
	out, err := r.xrayAPI(st, "statsquery", "-reset") // users and inbounds
	if err != nil {
		return nil, nil, err
	}
	var stats struct {
		Stat []struct {
			Name  string `json:"name"`
			Value int64  `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		return nil, nil, fmt.Errorf("statsquery: %w", err)
	}
	byTag := map[string]int64{}
	for _, s := range stats.Stat {
		p := strings.Split(s.Name, ">>>") // user>>>NAME>>>traffic>>>uplink, inbound>>>TAG>>>traffic>>>downlink
		if len(p) != 4 || p[2] != "traffic" || s.Value <= 0 {
			continue
		}
		switch p[0] {
		case "user":
			uu := us.of(p[1])
			if p[3] == "uplink" {
				uu.Up += s.Value
			} else {
				uu.Down += s.Value
			}
			uu.addDaily(now, s.Value)
		case "inbound":
			byTag[p[1]] += s.Value
		}
	}
	extraOnline, sessions := collectExtra(r, st, us, now, byTag)
	countEntries(st, us, byTag, now)
	out, err = r.xrayAPI(st, "statsonlineiplist", "-all")
	if err != nil {
		return nil, nil, err
	}
	var online struct {
		Users []struct {
			Email string `json:"email"`
			IPs   []struct {
				IP       string `json:"ip"`
				LastSeen int64  `json:"lastSeen"`
			} `json:"ips"`
		} `json:"users"`
	}
	if err := json.Unmarshal([]byte(out), &online); err != nil {
		return nil, nil, fmt.Errorf("statsonlineiplist: %w", err)
	}
	prev := map[string][]string{}
	for name, uu := range us.Users {
		prev[name], uu.Online = uu.Online, nil
	}
	for _, u := range online.Users {
		uu := us.of(u.Email)
		sort.Slice(u.IPs, func(i, j int) bool { return u.IPs[i].LastSeen < u.IPs[j].LastSeen })
		for _, ip := range u.IPs {
			uu.Online = append(uu.Online, ip.IP)
		}
		if len(uu.Online) > 0 {
			uu.LastSeen = now.Unix()
		}
	}
	for name, ips := range extraOnline {
		uu := us.of(name)
		for _, ip := range ips {
			if !contains2s(uu.Online, ip) {
				uu.Online = append(uu.Online, ip)
			}
		}
		if len(uu.Online) > 0 {
			uu.LastSeen = now.Unix()
		}
	}
	return prev, sessions, nil
}

// applyDeviceLimits blocks, for a while, the client IPs a user has beyond their device limit. IPs
// that were already connected at the previous check keep their place; newcomers are blocked.
func applyDeviceLimits(r *Server, st *State, us *Usage, prev map[string][]string, now time.Time) error {
	had := len(us.Blocks) > 0
	var keep []IPBlock
	blocked := map[string]bool{}
	for _, b := range us.Blocks {
		if _, u := st.User(b.User); u != nil && u.Devices > 0 && b.Until > now.Unix() {
			keep = append(keep, b)
			blocked[b.IP] = true
		}
	}
	for _, u := range st.Users {
		uu := us.Users[u.Name]
		if u.Devices <= 0 || uu == nil {
			continue
		}
		var order []string
		was := map[string]bool{}
		for _, ip := range prev[u.Name] {
			was[ip] = true
		}
		for _, ip := range uu.Online {
			if was[ip] && !blocked[ip] {
				order = append(order, ip)
			}
		}
		for _, ip := range uu.Online {
			if !was[ip] && !blocked[ip] {
				order = append(order, ip)
			}
		}
		for i := u.Devices; i < len(order); i++ {
			keep = append(keep, IPBlock{IP: order[i], User: u.Name, Until: now.Add(deviceBlock).Unix()})
			blocked[order[i]] = true
		}
	}
	us.Blocks = keep
	if len(keep) == 0 {
		if had {
			_, err := r.xrayAPI(st, "rmrules", deviceRule)
			return err
		}
		return nil
	}
	args := []string{"-outbound=block", "-ruletag=" + deviceRule, "-reset"}
	for _, b := range keep {
		args = append(args, b.IP)
	}
	_, err := r.xrayAPI(st, "sib", args...)
	return err
}
