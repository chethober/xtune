package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Management links: users and plans from Telegram, through the community VPN bot @chthvpnbot
// (github.com/chethober/bots/tree/main/chthvpnbot) instead of a bot running on the server.
// `xtune manage link` prints a secret link on the subscription server; whoever holds it can
// manage users through POST <link> {"op": ...}. A reseller's link (`xtune manage reseller add`)
// sees and changes only the users it added, within a user count and a total quota. Backups,
// keys, domains and the server's other settings stay on the command line.

type Manage struct {
	Token     string               `json:"token"`
	Resellers map[string]*Reseller `json:"resellers,omitempty"`
}

// A Reseller manages their own users only, within limits. 0 = no limit.
type Reseller struct {
	Token    string  `json:"token"`
	MaxUsers int     `json:"maxUsers,omitempty"`
	QuotaGB  float64 `json:"quotaGB,omitempty"` // total quota of their users; each then needs a quota
}

func (r Reseller) String() string {
	users, quota := "any number of users", "any quota"
	if r.MaxUsers > 0 {
		users = fmt.Sprintf("up to %d users", r.MaxUsers)
	}
	if r.QuotaGB > 0 {
		quota = fmt.Sprintf("%g GB in total", r.QuotaGB)
	}
	return users + ", " + quota
}

func orUsage(uu *UserUsage) *UserUsage {
	if uu == nil {
		return &UserUsage{}
	}
	return uu
}

// startServices starts the subscription server after a deploy or restore.
func startServices(r *Server, st *State) error { return restartSub(r, st) }

// manageURL is the link for a token, or "" when the subscription server is off.
func (st *State) manageURL(token string) string {
	if st.botBase() == "" || token == "" {
		return ""
	}
	return st.botBase() + "/adm/" + token
}

// manager is who a management link belongs to: the admin (reseller "") or a reseller.
type manager struct {
	reseller string
	res      *Reseller
}

func (st *State) managerByToken(token string) (*manager, bool) {
	m := st.Manage
	if m == nil || token == "" {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(m.Token), []byte(token)) == 1 {
		return &manager{}, true
	}
	for name, r := range m.Resellers {
		if subtle.ConstantTimeCompare([]byte(r.Token), []byte(token)) == 1 {
			return &manager{reseller: name, res: r}, true
		}
	}
	return nil, false
}

func (m *manager) admin() bool { return m.reseller == "" }

func (m *manager) manages(u User) bool { return m.admin() || u.Reseller == m.reseller }

// room checks a reseller's change against their user count and total quota.
func (m *manager) room(st *State, name string, l Limits, adding bool) error {
	if m.admin() {
		return nil
	}
	l, err := l.withPlan(st)
	if err != nil {
		return err
	}
	owned, used, cur := 0, int64(0), int64(0)
	for _, u := range st.Users {
		if u.Reseller != m.reseller {
			continue
		}
		owned++
		if u.Name == name {
			cur = u.Quota
		} else {
			used += u.Quota
		}
	}
	if adding && m.res.MaxUsers > 0 && owned >= m.res.MaxUsers {
		return fmt.Errorf("you can have at most %d users", m.res.MaxUsers)
	}
	if m.res.QuotaGB <= 0 {
		return nil
	}
	quota := cur
	if l.QuotaGB != nil {
		quota = int64(*l.QuotaGB * (1 << 30))
	}
	total := int64(m.res.QuotaGB * (1 << 30))
	if quota <= 0 {
		return errors.New("every user needs a quota")
	}
	if used+quota > total {
		return fmt.Errorf("only %s of your total quota is left", fmtBytes(max(0, total-used)))
	}
	return nil
}

// ---------------------------------------------------------------- the API

type manageReq struct {
	Op      string   `json:"op"`
	Name    string   `json:"name,omitempty"`
	QuotaGB *float64 `json:"quotaGB,omitempty"`
	Days    *int     `json:"days,omitempty"`
	Devices *int     `json:"devices,omitempty"`
	Reset   *string  `json:"reset,omitempty"` // monthly | weekly | "" (none)
	Plan    *string  `json:"plan,omitempty"`
}

func (q manageReq) limits() Limits {
	return Limits{QuotaGB: q.QuotaGB, Days: q.Days, Devices: q.Devices, Reset: q.Reset, Plan: q.Plan}
}

type mUser struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // active | disabled | expired | over quota
	Used     int64  `json:"used"`
	Quota    int64  `json:"quota"`
	Expires  int64  `json:"expires"`
	Devices  int    `json:"devices"`
	Online   int    `json:"online"`
	Plan     string `json:"plan,omitempty"`
	Reset    string `json:"reset,omitempty"`
	Pool     string `json:"pool,omitempty"`
	Reseller string `json:"reseller,omitempty"`
}

type mUserDetail struct {
	mUser
	Sub   string     `json:"sub,omitempty"`
	Links []pageLink `json:"links"`
	Files []string   `json:"files,omitempty"` // profile files on the subscription page, for apps without links
	Daily []int64    `json:"daily"`           // traffic per day, the last 30 days, oldest first
}

type mConfig struct {
	ID     string `json:"id"`
	State  string `json:"state"` // active | quiet (probably blocked) | idle | follower
	Since  int64  `json:"since,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	Server string `json:"server,omitempty"`
}

type mStatus struct {
	Role     string    `json:"role"` // admin | reseller
	Reseller string    `json:"reseller,omitempty"`
	Addr     string    `json:"addr,omitempty"` // admin only
	Version  string    `json:"version"`
	Xray     string    `json:"xray,omitempty"`
	Sub      string    `json:"sub,omitempty"`
	Users    int       `json:"users"`
	Active   int       `json:"active"`
	Online   int       `json:"online"`
	Traffic  int64     `json:"traffic"`
	Configs  []mConfig `json:"configs,omitempty"`
	MaxUsers int       `json:"maxUsers,omitempty"` // a reseller's limits
	QuotaGB  float64   `json:"quotaGB,omitempty"`
	Assigned int64     `json:"assigned,omitempty"` // a reseller's users' quotas, added up
	Plans    []string  `json:"plans"`
}

var errManage = errors.New("not found")

func userRow(u User, uu *UserUsage, now time.Time) mUser {
	uu = orUsage(uu)
	return mUser{Name: u.Name, Status: userStatus(u, uu, now), Used: uu.Up + uu.Down, Quota: u.Quota, Expires: u.Expires,
		Devices: u.Devices, Online: len(uu.Online), Plan: u.Plan, Reset: u.Reset, Pool: u.Pool, Reseller: u.Reseller}
}

func planNamesOf(st *State) []string {
	ns := []string{}
	for n := range st.Plans {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return ns
}

// ManageOp runs one management request for the holder of a management link.
func ManageOp(r *Server, m *manager, q manageReq, now time.Time) (any, error) {
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	us, err := LoadUsage(r)
	if err != nil {
		return nil, err
	}
	// the user a request names, if this link manages them
	target := func() (*User, error) {
		if _, u := st.User(q.Name); u != nil && m.manages(*u) {
			return u, nil
		}
		return nil, fmt.Errorf("no user %q", q.Name)
	}
	detail := func(name string) (any, error) {
		st, err := requireState(r)
		if err != nil {
			return nil, err
		}
		us, err := LoadUsage(r)
		if err != nil {
			return nil, err
		}
		_, u := st.User(name)
		if u == nil {
			return nil, fmt.Errorf("no user %q", name)
		}
		d := mUserDetail{mUser: userRow(*u, us.Users[name], now), Sub: st.SubURL(*u), Links: st.NamedLinks(*u),
			Daily: orUsage(us.Users[name]).lastDays(now, 30)}
		for _, p := range st.Profiles(*u) {
			d.Files = append(d.Files, p.File)
		}
		return d, nil
	}
	ok := func(err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return detail(q.Name)
	}
	switch q.Op {
	case "status":
		return manageStatus(r, st, us, m, now), nil
	case "users":
		out := []mUser{}
		for _, u := range st.Users {
			if m.manages(u) {
				out = append(out, userRow(u, us.Users[u.Name], now))
			}
		}
		return J{"users": out}, nil
	case "user":
		if _, err := target(); err != nil {
			return nil, err
		}
		return detail(q.Name)
	case "plans":
		return J{"plans": st.Plans}, nil
	case "add":
		l := q.limits()
		if !m.admin() {
			l.Reseller = &m.reseller
		}
		if err := m.room(st, q.Name, l, true); err != nil {
			return nil, err
		}
		_, _, err := AddUser(r, q.Name, l)
		return ok(err)
	case "setPlan", "delPlan":
		if !m.admin() {
			return nil, errManage
		}
		if q.Op == "delPlan" {
			return J{"ok": true}, DeletePlan(r, q.Name)
		}
		p := Plan{}
		if q.QuotaGB != nil {
			p.QuotaGB = *q.QuotaGB
		}
		if q.Days != nil {
			p.Days = *q.Days
		}
		if q.Devices != nil {
			p.Devices = *q.Devices
		}
		if q.Reset != nil {
			p.Reset = *q.Reset
		}
		return J{"ok": true}, SetPlan(r, q.Name, p)
	}
	if _, err := target(); err != nil {
		return nil, err
	}
	switch q.Op {
	case "limit":
		l := q.limits()
		if l == (Limits{}) {
			return nil, errors.New("nothing to change")
		}
		if err := m.room(st, q.Name, l, false); err != nil {
			return nil, err
		}
		_, err := SetLimits(r, q.Name, l)
		return ok(err)
	case "renew":
		plan, days := "", 0
		if q.Plan != nil {
			plan = *q.Plan
			if err := m.room(st, q.Name, Limits{Plan: &plan}, false); err != nil {
				return nil, err
			}
		}
		if q.Days != nil {
			days = *q.Days
		}
		_, _, err := Renew(r, q.Name, plan, days)
		return ok(err)
	case "enable", "disable":
		return ok(SetUserEnabled(r, q.Name, q.Op == "enable"))
	case "resetUsage":
		return ok(ResetUsage(r, q.Name))
	case "rotate":
		_, _, err := RotateUser(r, q.Name)
		return ok(err)
	case "resetSub":
		_, err := ResetSubLink(r, q.Name)
		return ok(err)
	case "delete":
		return J{"ok": true}, DeleteUser(r, q.Name)
	}
	return nil, errManage
}

func manageStatus(r *Server, st *State, us *Usage, m *manager, now time.Time) mStatus {
	s := mStatus{Role: "admin", Addr: st.Addr(), Version: version, Plans: planNamesOf(st)}
	for _, u := range st.Users {
		if !m.manages(u) {
			continue
		}
		uu := orUsage(us.Users[u.Name])
		s.Users++
		if userStatus(u, uu, now) == "active" {
			s.Active++
		}
		if len(uu.Online) > 0 {
			s.Online++
		}
		s.Traffic += uu.Up + uu.Down
		s.Assigned += u.Quota
	}
	if !m.admin() {
		s.Role, s.Reseller, s.MaxUsers, s.QuotaGB = "reseller", m.reseller, m.res.MaxUsers, m.res.QuotaGB
		s.Addr = "" // resellers get users, not the server
		return s
	}
	s.Assigned = 0
	if ss, err := r.Status(); err == nil {
		s.Xray = orStr(ss.Unit, "?") + " (" + orStr(ss.Xray, "?") + ")"
		s.Sub = st.Sub.Mode + ", " + orStr(ss.Sub, "?")
	}
	quiet := quietEntries(st, us, now)
	for _, srv := range st.servers() {
		for _, e := range srv.Entries {
			c := mConfig{ID: e.ID, State: "idle"}
			eu := us.Entries[entryKey(e)]
			switch {
			case srv != st:
				c.State, c.Server = "follower", srv.Host
			case quiet[e.ID] > 0:
				c.State, c.Since = "quiet", now.Add(-quiet[e.ID]).Unix()
			case eu != nil && eu.Active > 0:
				c.State, c.Since, c.Bytes = "active", eu.Active, eu.Bytes
			}
			s.Configs = append(s.Configs, c)
		}
	}
	return s
}

// serveManage answers POST /PATH/adm/TOKEN with {"op": ...}.
func (h subHandler) serveManage(w http.ResponseWriter, req *http.Request, st *State, token string) {
	m, found := st.managerByToken(token)
	if h.r == nil || st.Follow != nil || !found {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	var q manageReq
	if err := json.NewDecoder(io.LimitReader(req.Body, 16<<10)).Decode(&q); err != nil || q.Op == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(J{"error": "bad request"})
		return
	}
	out, err := ManageOp(h.r, m, q, time.Now())
	switch {
	case errors.Is(err, errManage):
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(J{"error": "not found"})
	case err != nil:
		// the errors are xtune's own validation messages (no user, quota left…), meant for the person
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(J{"error": err.Error()})
	default:
		json.NewEncoder(w).Encode(out)
	}
}

// ---------------------------------------------------------------- settings

func changeManage(r *Server, fn func(st *State, m *Manage) error) (*State, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	if st.Follow != nil {
		return nil, errFollower(st)
	}
	if st.Sub.Mode == "" {
		return nil, errors.New("management links are on the subscription server: turn it on first (xtune sub enable)")
	}
	if st.Manage == nil {
		st.Manage = &Manage{Token: randHex(16)}
	}
	if err := fn(st, st.Manage); err != nil {
		return nil, err
	}
	if err := saveState(r, st); err != nil {
		return nil, err
	}
	if ok, err := ensurePort443(r, st); err != nil {
		return nil, err
	} else if !ok {
		fmt.Fprintln(os.Stderr, port443Note)
	}
	return st, nil
}

// removeLegacyBot stops and removes the Telegram bot of xtune before v0.7.0. Users it added
// as a reseller keep working, now managed by the admin.
func removeLegacyBot(r *Server, st *State) (bool, error) {
	if len(st.LegacyBot) == 0 {
		return false, nil
	}
	if _, err := r.Helper("bot-remove"); err != nil {
		return false, err
	}
	st.LegacyBot = nil
	return true, nil
}

const manageHelp = "manage [status | link | token | off | reseller list | reseller add|limit NAME [--users N] [--quota GB] | reseller link|token|del NAME]"

func cmdManage(args []string) error {
	if err := serverOnly("manage", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("manage", flag.ExitOnError)
	users := fs.Int("users", 0, "reseller: most users (0 = no limit)")
	quota := fs.Float64("quota", 0, "reseller: total quota of their users in GB (0 = no limit)")
	pos := parse(fs, args)
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		pos = []string{"status"}
	}
	show := func(st *State, err error) error {
		if err != nil {
			return err
		}
		fmt.Printf("management link (send it to @chthvpnbot: 🛠 My servers → 🖥 Manage a server; keep it private):\n%s\n",
			st.manageURL(st.Manage.Token))
		return nil
	}
	switch pos[0] {
	case "status":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		printManage(os.Stdout, st)
		return nil
	case "link", "on", "enable":
		return show(changeManage(r, func(*State, *Manage) error { return nil }))
	case "token", "rotate":
		return show(changeManage(r, func(_ *State, m *Manage) error { m.Token = randHex(16); return nil }))
	case "off", "disable":
		defer r.lock()()
		st, err := requireState(r)
		if err != nil {
			return err
		}
		st.Manage = nil
		for i := range st.Users {
			st.Users[i].Reseller = ""
		}
		fmt.Println("management links: off (resellers' users are now yours)")
		return saveState(r, st)
	case "reseller", "resellers":
	default:
		return errors.New(manageHelp)
	}
	if len(pos) < 2 || pos[1] == "list" {
		st, err := requireState(r)
		if err != nil {
			return err
		}
		printManage(os.Stdout, st)
		return nil
	}
	if len(pos) < 3 {
		return errors.New(manageHelp)
	}
	name := pos[2]
	if err := validUserName(name); err != nil || len(name) > 32 {
		return fmt.Errorf("invalid reseller name %q", name)
	}
	if *users < 0 || *quota < 0 {
		return errors.New("limits cannot be negative")
	}
	link := func(st *State, err error) error {
		if err != nil {
			return err
		}
		res := st.Manage.Resellers[name]
		fmt.Printf("reseller %s: %s\n\nmanagement link for them (they send it to @chthvpnbot):\n%s\n", name, res,
			st.manageURL(res.Token))
		return nil
	}
	set := flagsSet(fs)
	switch pos[1] {
	case "add", "limit":
		return link(changeManage(r, func(_ *State, m *Manage) error {
			res := m.Resellers[name]
			switch {
			case res == nil && pos[1] == "limit":
				return fmt.Errorf("no reseller %q", name)
			case res != nil && pos[1] == "add":
				return fmt.Errorf("reseller %q already exists (manage reseller limit %s ...)", name, name)
			case res == nil:
				res = &Reseller{Token: randHex(16)}
			}
			if set["users"] {
				res.MaxUsers = *users
			}
			if set["quota"] {
				res.QuotaGB = *quota
			}
			if m.Resellers == nil {
				m.Resellers = map[string]*Reseller{}
			}
			m.Resellers[name] = res
			return nil
		}))
	case "link", "show":
		return link(changeManage(r, func(_ *State, m *Manage) error {
			if m.Resellers[name] == nil {
				return fmt.Errorf("no reseller %q", name)
			}
			return nil
		}))
	case "token", "rotate":
		return link(changeManage(r, func(_ *State, m *Manage) error {
			if m.Resellers[name] == nil {
				return fmt.Errorf("no reseller %q", name)
			}
			m.Resellers[name].Token = randHex(16)
			return nil
		}))
	case "del", "delete", "rm":
		_, err := changeManage(r, func(st *State, m *Manage) error {
			if m.Resellers[name] == nil {
				return fmt.Errorf("no reseller %q", name)
			}
			delete(m.Resellers, name)
			for i := range st.Users {
				if st.Users[i].Reseller == name {
					st.Users[i].Reseller = ""
				}
			}
			return nil
		})
		if err == nil {
			fmt.Printf("reseller %s removed; their users are now yours\n", name)
		}
		return err
	}
	return errors.New(manageHelp)
}

func printManage(w io.Writer, st *State) {
	if st.Manage == nil {
		fmt.Fprintln(w, "management links: off (xtune manage link)")
		return
	}
	fmt.Fprintf(w, "management link: %s\n", st.manageURL(st.Manage.Token))
	if len(st.Manage.Resellers) == 0 {
		return
	}
	var names []string
	for n := range st.Manage.Resellers {
		names = append(names, n)
	}
	sort.Strings(names)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nRESELLER\tUSERS\tLIMITS\tLINK")
	for _, n := range names {
		owned := 0
		for _, u := range st.Users {
			if u.Reseller == n {
				owned++
			}
		}
		res := st.Manage.Resellers[n]
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", n, owned, res, st.manageURL(res.Token))
	}
	tw.Flush()
}

// cmdBot is what is left of the Telegram bot removed in v0.7.0.
func cmdBot([]string) error {
	return errors.New(strings.TrimSpace(`the Telegram bot was removed in xtune v0.7.0.
Manage users from @chthvpnbot instead: run "xtune manage link" and send the link to the bot
(🛠 My servers → 🖥 Manage a server). Resellers: xtune manage reseller add NAME --users N --quota GB`))
}
