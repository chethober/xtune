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
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// Public profiles: a pool of users for sharing a server with a community, through a bot such as
// the community VPN bot (github.com/chethober/bots/tree/main/chthvpnbot). `xtune public add NAME`
// prints a connect link on the subscription server; whoever holds it can join members to the pool
// and remove them. Every member is an ordinary user named NAME-LABEL with the pool's limits, so
// quotas, expiry, device limits and peers work as for any user, and one member can be removed
// without touching the others. The bot picks the labels; the server never learns who a member is.

type PublicPool struct {
	Token   string  `json:"token"`             // secret part of the connect link
	QuotaGB float64 `json:"quotaGB,omitempty"` // per member
	Days    int     `json:"days,omitempty"`
	Devices int     `json:"devices,omitempty"`
	Reset   string  `json:"reset,omitempty"`
	Plan    string  `json:"plan,omitempty"`    // limits come from this plan instead, when set
	Members int     `json:"members,omitempty"` // most active members at once, 0 = unlimited
	Paused  bool    `json:"paused,omitempty"`  // no new members; existing ones keep working
	Created int64   `json:"created"`
}

// memberLabel is the part of a member's user name the bot chooses.
var memberLabel = regexp.MustCompile(`^[a-z0-9]{4,16}$`)

// prunePublic is how long an expired member stays before a join or info removes it.
const prunePublic = 7 * 24 * time.Hour

func memberName(pool, label string) string { return pool + "-" + label }

// limits are what a new member gets.
func (p PublicPool) limits() Limits {
	if p.Plan != "" {
		return Limits{Plan: &p.Plan}
	}
	return Limits{QuotaGB: &p.QuotaGB, Days: &p.Days, Devices: &p.Devices, Reset: &p.Reset}
}

func (p PublicPool) String() string {
	s := "plan " + p.Plan
	if p.Plan == "" {
		s = Plan{QuotaGB: p.QuotaGB, Days: p.Days, Devices: p.Devices, Reset: p.Reset}.String()
	}
	if p.Members > 0 {
		s += fmt.Sprintf(", at most %d members", p.Members)
	}
	if p.Paused {
		s += ", paused"
	}
	return s
}

// PublicURL is the pool's connect link, or "" when the subscription server is off.
func (st *State) PublicURL(name string) string {
	p := st.Public[name]
	if p == nil || st.botBase() == "" {
		return ""
	}
	return st.botBase() + "/pub/" + p.Token
}

// members are the pool's users.
func (st *State) members(pool string) []User {
	var out []User
	for _, u := range st.Users {
		if u.Pool == pool {
			out = append(out, u)
		}
	}
	return out
}

// SetPublic adds a pool, or changes the limits of one (its token and members stay).
func SetPublic(r *Server, name string, p PublicPool) (*State, error) {
	if err := validUserName(name); err != nil || len(name) > 32 {
		return nil, fmt.Errorf("invalid pool name %q", name)
	}
	if err := validReset(p.Reset); err != nil {
		return nil, err
	}
	if p.QuotaGB < 0 || p.Days < 0 || p.Devices < 0 || p.Members < 0 {
		return nil, errors.New("limits cannot be negative")
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	if st.Follow != nil {
		return nil, errFollower(st)
	}
	if st.Sub.Mode == "" {
		return nil, errors.New("the connect link is on the subscription server: turn it on first (xtune sub enable)")
	}
	if _, ok := st.Plans[p.Plan]; p.Plan != "" && !ok {
		return nil, fmt.Errorf("no plan %q (%s)", p.Plan, planNames(st))
	}
	if st.Public == nil {
		st.Public = map[string]*PublicPool{}
	}
	if old := st.Public[name]; old != nil {
		p.Token, p.Created, p.Paused = old.Token, old.Created, old.Paused
	} else {
		p.Token, p.Created = randHex(16), time.Now().Unix()
	}
	st.Public[name] = &p
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

// changePublic loads the state under the server lock and lets fn change the pool.
func changePublic(r *Server, name string, fn func(st *State, p *PublicPool) error) (*State, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	p := st.Public[name]
	if p == nil {
		return nil, fmt.Errorf("no public profile %q", name)
	}
	if err := fn(st, p); err != nil {
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

// RotatePublic gives the pool a new connect link; the old one stops working, members stay.
func RotatePublic(r *Server, name string) (*State, error) {
	return changePublic(r, name, func(_ *State, p *PublicPool) error { p.Token = randHex(16); return nil })
}

func PausePublic(r *Server, name string, paused bool) (*State, error) {
	return changePublic(r, name, func(_ *State, p *PublicPool) error { p.Paused = paused; return nil })
}

// DeletePublic removes the pool and all its members.
func DeletePublic(r *Server, name string) error {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if st.Public[name] == nil {
		return fmt.Errorf("no public profile %q", name)
	}
	delete(st.Public, name)
	st.Users = slices.DeleteFunc(st.Users, func(u User) bool { return u.Pool == name })
	return commit(r, st)
}

// ---------------------------------------------------------------- the connect link's API

type pubMember struct {
	Label   string `json:"label"`
	Status  string `json:"status"` // active | disabled | expired | over quota
	Used    int64  `json:"used"`   // bytes
	Quota   int64  `json:"quota"`  // bytes, 0 = unlimited
	Expires int64  `json:"expires"`
}

type pubInfo struct {
	Name      string      `json:"name"`
	Paused    bool        `json:"paused"`
	Active    int         `json:"active"`
	Max       int         `json:"max"` // 0 = unlimited
	Protocols []string    `json:"protocols"`
	QuotaGB   float64     `json:"quotaGB"`
	Days      int         `json:"days"`
	Devices   int         `json:"devices"`
	Members   []pubMember `json:"members"`
	Version   string      `json:"version"`
}

type pubJoined struct {
	Label   string `json:"label"`
	Sub     string `json:"sub"`  // subscription link: page in a browser, list for apps
	JSON    string `json:"json"` // failover config
	Status  string `json:"status"`
	Quota   int64  `json:"quota"`
	Expires int64  `json:"expires"`
}

var errPoolFull = errors.New("the pool is full")
var errPoolPaused = errors.New("the pool takes no new members")

// poolByToken finds the pool a connect link's token belongs to.
func (st *State) poolByToken(token string) (string, *PublicPool) {
	for name, p := range st.Public {
		if subtle.ConstantTimeCompare([]byte(p.Token), []byte(token)) == 1 {
			return name, p
		}
	}
	return "", nil
}

// protocolNames are the kinds of configs every member gets, for the bot's listing.
func (st *State) protocolNames() []string {
	labels := []struct{ prefix, name string }{
		{"reality", "VLESS REALITY"}, {"vlessenc", "VLESS Encryption"}, {"cdn", "CDN"}, {"hysteria2", "Hysteria2"},
		{"ss2022", "Shadowsocks"}, {"wireguard", "WireGuard"}, {"ssh", "SSH"}, {"dnstt", "DNS tunnel"},
		{"openvpn", "OpenVPN"}, {"ikev2", "IKEv2"}, {"amneziawg", "AmneziaWG"}, {"worker", "CF Worker"},
	}
	var out []string
	for _, s := range st.servers() {
		for _, e := range s.Entries {
			for _, l := range labels {
				if strings.HasPrefix(e.Kind, l.prefix) && !slices.Contains(out, l.name) {
					out = append(out, l.name)
				}
			}
		}
	}
	return out
}

func (st *State) poolInfo(name string, p *PublicPool, us *Usage, now time.Time) pubInfo {
	info := pubInfo{Name: name, Paused: p.Paused, Max: p.Members, Protocols: st.protocolNames(),
		QuotaGB: p.QuotaGB, Days: p.Days, Devices: p.Devices, Members: []pubMember{}, Version: version}
	if pl, ok := st.Plans[p.Plan]; p.Plan != "" && ok {
		info.QuotaGB, info.Days, info.Devices = pl.QuotaGB, pl.Days, pl.Devices
	}
	for _, u := range st.members(name) {
		uu := us.Users[u.Name]
		m := pubMember{Label: strings.TrimPrefix(u.Name, name+"-"), Status: userStatus(u, uu, now),
			Quota: u.Quota, Expires: u.Expires}
		if uu != nil {
			m.Used = uu.Up + uu.Down
		}
		if m.Status == "active" {
			info.Active++
		}
		info.Members = append(info.Members, m)
	}
	return info
}

// pruneMembers drops members whose time ran out more than a week ago.
// It reports whether any were dropped.
func (st *State) pruneMembers(us *Usage, now time.Time) bool {
	n := len(st.Users)
	st.Users = slices.DeleteFunc(st.Users, func(u User) bool {
		if u.Pool == "" {
			return false
		}
		return userStatus(u, us.Users[u.Name], now) == "expired" && now.Sub(time.Unix(u.Expires, 0)) > prunePublic
	})
	return len(st.Users) != n
}

// PublicInfo is what GET /PATH/pub/TOKEN answers.
func PublicInfo(r *Server, token string, now time.Time) (*pubInfo, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	name, p := st.poolByToken(token)
	if p == nil {
		return nil, nil
	}
	us, err := LoadUsage(r)
	if err != nil {
		return nil, err
	}
	if st.pruneMembers(us, now) {
		if err := commit(r, st); err != nil {
			return nil, err
		}
	}
	info := st.poolInfo(name, p, us, now)
	return &info, nil
}

// JoinPublic adds the member LABEL to the pool the token belongs to. Joining again with the same
// label returns the same user, so a bot can safely retry.
func JoinPublic(r *Server, token, label string, now time.Time) (*pubJoined, error) {
	if !memberLabel.MatchString(label) {
		return nil, errors.New("bad label")
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	name, p := st.poolByToken(token)
	if p == nil {
		return nil, nil
	}
	us, err := LoadUsage(r)
	if err != nil {
		return nil, err
	}
	joined := func(u User) *pubJoined {
		sub := st.SubURL(u)
		return &pubJoined{Label: label, Sub: sub, JSON: sub + "/json", Status: userStatus(u, us.Users[u.Name], now),
			Quota: u.Quota, Expires: u.Expires}
	}
	if _, u := st.User(memberName(name, label)); u != nil {
		if u.Pool != name {
			return nil, errors.New("bad label")
		}
		return joined(*u), nil
	}
	if p.Paused {
		return nil, errPoolPaused
	}
	st.pruneMembers(us, now)
	if p.Members > 0 && st.poolInfo(name, p, us, now).Active >= p.Members {
		return nil, errPoolFull
	}
	l, err := p.limits().withPlan(st)
	if err != nil {
		return nil, err
	}
	u := NewUser(memberName(name, label))
	u.Pool = name
	l.apply(&u)
	u.SubToken = randHex(16)
	st.Users = append(st.Users, u)
	if err := resetUsage(r, u.Name); err != nil {
		return nil, err
	}
	if err := commit(r, st); err != nil {
		return nil, err
	}
	return joined(u), nil
}

// LeavePublic removes the member LABEL from the pool. A member that is already gone is not an error.
func LeavePublic(r *Server, token, label string) (bool, error) {
	if !memberLabel.MatchString(label) {
		return false, errors.New("bad label")
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return false, err
	}
	name, p := st.poolByToken(token)
	if p == nil {
		return false, nil
	}
	i, u := st.User(memberName(name, label))
	if u == nil || u.Pool != name {
		return true, nil
	}
	st.Users = append(st.Users[:i], st.Users[i+1:]...)
	return true, commit(r, st)
}

// joinLimiter caps how fast one pool takes new members, so a leaked connect link cannot flood the
// server with users.
var joinLimiter = struct {
	sync.Mutex
	seen map[string][]time.Time
}{seen: map[string][]time.Time{}}

const joinsPerMinute = 30

func allowJoin(token string, now time.Time) bool {
	joinLimiter.Lock()
	defer joinLimiter.Unlock()
	recent := slices.DeleteFunc(joinLimiter.seen[token], func(t time.Time) bool { return now.Sub(t) > time.Minute })
	if len(recent) >= joinsPerMinute {
		joinLimiter.seen[token] = recent
		return false
	}
	joinLimiter.seen[token] = append(recent, now)
	return true
}

// servePublic answers /PATH/pub/TOKEN (GET: info) and /PATH/pub/TOKEN/join|leave (POST {"label"}).
func (h subHandler) servePublic(w http.ResponseWriter, req *http.Request, st *State, parts []string) {
	token := parts[2]
	if h.r == nil || st.Follow != nil {
		http.NotFound(w, req)
		return
	}
	if _, p := st.poolByToken(token); p == nil {
		http.NotFound(w, req)
		return
	}
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	fail := func(code int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	now := time.Now()
	if len(parts) == 3 {
		info, err := PublicInfo(h.r, token, now)
		switch {
		case err != nil:
			fail(http.StatusServiceUnavailable, "temporarily unavailable")
		case info == nil:
			http.NotFound(w, req)
		default:
			reply(info)
		}
		return
	}
	var in struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 4<<10)).Decode(&in); err != nil || !memberLabel.MatchString(in.Label) {
		fail(http.StatusBadRequest, "bad label")
		return
	}
	switch parts[3] {
	case "join":
		if !allowJoin(token, now) {
			fail(http.StatusTooManyRequests, "too many joins, try again in a minute")
			return
		}
		j, err := JoinPublic(h.r, token, in.Label, now)
		switch {
		case errors.Is(err, errPoolFull):
			fail(http.StatusConflict, "full")
		case errors.Is(err, errPoolPaused):
			fail(http.StatusConflict, "paused")
		case err != nil:
			fail(http.StatusServiceUnavailable, "temporarily unavailable")
		case j == nil:
			http.NotFound(w, req)
		default:
			reply(j)
		}
	case "leave":
		ok, err := LeavePublic(h.r, token, in.Label)
		switch {
		case err != nil:
			fail(http.StatusServiceUnavailable, "temporarily unavailable")
		case !ok:
			http.NotFound(w, req)
		default:
			reply(map[string]bool{"ok": true})
		}
	default:
		http.NotFound(w, req)
	}
}

// ---------------------------------------------------------------- CLI

func cmdPublic(args []string) error {
	if err := serverOnly("public", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("public", flag.ExitOnError)
	quota := fs.Float64("quota", 0, "traffic quota per member in GB (0 = unlimited)")
	days := fs.Int("days", 0, "members expire N days after joining (0 = never)")
	devices := fs.Int("devices", 0, "simultaneous devices per member, 0 = unlimited")
	reset := fs.String("reset", "", "start members' usage from zero every period: monthly, weekly or none")
	plan := fs.String("plan", "", "members get this plan's limits (xtune plans)")
	members := fs.Int("members", 0, "most active members at once (0 = unlimited)")
	yes := fs.Bool("yes", false, "del: do not ask for confirmation")
	pos := parse(fs, args)
	if len(pos) == 0 {
		pos = []string{"list"}
	}
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	if len(pos) < 2 && pos[0] != "list" && pos[0] != "ls" {
		return fmt.Errorf("public %s: missing profile name", pos[0])
	}
	showLink := func(st *State, err error) error {
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s\n\nconnect link (send it to the community VPN bot; keep it private, it adds users):\n%s\n",
			pos[1], st.Public[pos[1]], st.PublicURL(pos[1]))
		return nil
	}
	switch pos[0] {
	case "list", "ls":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		us, err := LoadUsage(r)
		if err != nil {
			return err
		}
		return printPublic(os.Stdout, st, us)
	case "add", "limit":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		p := PublicPool{}
		if old := st.Public[pos[1]]; old != nil {
			if pos[0] == "add" {
				return fmt.Errorf("public profile %q already exists (public limit %s ...)", pos[1], pos[1])
			}
			p = *old
		} else if pos[0] == "limit" {
			return fmt.Errorf("no public profile %q", pos[1])
		}
		set := flagsSet(fs)
		if set["quota"] {
			p.QuotaGB = *quota
		}
		if set["days"] {
			p.Days = *days
		}
		if set["devices"] {
			p.Devices = *devices
		}
		if set["reset"] {
			p.Reset = strings.TrimSpace(*reset)
			if p.Reset == "none" {
				p.Reset = ""
			}
		}
		if set["plan"] {
			p.Plan = *plan
		}
		if set["members"] {
			p.Members = *members
		}
		return showLink(SetPublic(r, pos[1], p))
	case "link", "show":
		// a pool made before v0.7.1 gets its link moved to port 443 here
		return showLink(changePublic(r, pos[1], func(*State, *PublicPool) error { return nil }))
	case "token", "rotate":
		return showLink(RotatePublic(r, pos[1]))
	case "pause", "resume":
		_, err := PausePublic(r, pos[1], pos[0] == "pause")
		return err
	case "del", "delete", "rm":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		if st.Public[pos[1]] == nil {
			return fmt.Errorf("no public profile %q", pos[1])
		}
		if n := len(st.members(pos[1])); !*yes && n > 0 {
			a, err := cliPrompt(fmt.Sprintf("delete %q and its %d members? [y/N]", pos[1], n), false)
			if err != nil || !strings.EqualFold(strings.TrimSpace(a), "y") {
				return errors.New("cancelled")
			}
		}
		return DeletePublic(r, pos[1])
	}
	return fmt.Errorf("unknown public action %q", pos[0])
}

func printPublic(w io.Writer, st *State, us *Usage) error {
	if len(st.Public) == 0 {
		fmt.Fprintln(w, "no public profiles yet: xtune public add NAME --quota GB --days N --members N")
		return nil
	}
	var names []string
	for n := range st.Public {
		names = append(names, n)
	}
	sort.Strings(names)
	now := time.Now()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tMEMBERS\tLIMITS\tCONNECT LINK")
	for _, n := range names {
		p := st.Public[n]
		info := st.poolInfo(n, p, us, now)
		max := "∞"
		if p.Members > 0 {
			max = fmt.Sprint(p.Members)
		}
		fmt.Fprintf(tw, "%s\t%d/%s active (%d)\t%s\t%s\n", n, info.Active, max, len(info.Members), p, st.PublicURL(n))
	}
	return tw.Flush()
}
