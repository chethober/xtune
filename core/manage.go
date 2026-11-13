package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// Manage mode: the operator's servers, over SSH, with the xtune that runs on them. Changes go
// through the server's own `xtune` command (so its locking, live Xray updates and checks apply);
// reads come straight from its state files.

// ServerConn is how the app reaches one of the operator's servers. Secrets stay in the app's
// secure storage and are passed in each call; the core keeps only the open connection.
type ServerConn struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
	Key      string `json:"key,omitempty"` // a private key, PEM / OpenSSH format
	KeyPass  string `json:"keyPass,omitempty"`
	// HostKey pins the server's key (authorized_keys format). Empty on the first connection: the
	// key seen is returned, for the app to keep.
	HostKey string `json:"hostKey,omitempty"`
}

func (c ServerConn) addr() string {
	p := c.Port
	if p == 0 {
		p = 22
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(p))
}

func (c ServerConn) cacheKey() string {
	return c.User + "@" + c.addr() + "#" + c.HostKey
}

// sshTransport implements remote.Transport, so the state code runs over it.
type sshTransport struct {
	c        *ssh.Client
	root     bool
	sudoPass *string
}

func (t *sshTransport) wrap(cmd, stdin string, sudo bool) (string, string) {
	if !sudo || t.root {
		return cmd, stdin
	}
	if t.sudoPass != nil {
		return "sudo -S -p '' bash -c " + remote.Quote(cmd), *t.sudoPass + "\n" + stdin
	}
	return "sudo -n bash -c " + remote.Quote(cmd), stdin
}

func (t *sshTransport) Exec(cmd, stdin string, sudo bool) (string, string, error) {
	s, err := t.c.NewSession()
	if err != nil {
		return "", "", err
	}
	defer s.Close()
	cmd, stdin = t.wrap(cmd, stdin, sudo)
	var out, errb bytes.Buffer
	s.Stdin, s.Stdout, s.Stderr = strings.NewReader(stdin), &out, &errb
	if err := s.Run(cmd); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errb.String(), errors.New(msg)
	}
	return out.String(), errb.String(), nil
}

func (t *sshTransport) Stream(ctx context.Context, cmd string, fn func(string)) error {
	out, _, err := t.Exec(cmd, "", true)
	for _, l := range strings.Split(out, "\n") {
		fn(l)
	}
	return err
}

func (t *sshTransport) Close() { t.c.Close() }

var (
	connsMu sync.Mutex
	conns   = map[string]*remote.Server{}
)

// Connected is what connecting found.
type Connected struct {
	HostKey string `json:"hostKey"`
	Version string `json:"version"` // xtune on the server; "" when it is not installed
	Root    bool   `json:"root"`
}

func dialServer(c ServerConn) (*remote.Server, string, error) {
	if c.User == "" {
		c.User = "root"
	}
	var auth []ssh.AuthMethod
	if c.Key != "" {
		var signer ssh.Signer
		var err error
		if c.KeyPass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.Key), []byte(c.KeyPass))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(c.Key))
		}
		if err != nil {
			return nil, "", fmt.Errorf("private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if c.Password != "" {
		pw := c.Password
		auth = append(auth, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range ans {
				ans[i] = pw
			}
			return ans, nil
		}))
	}
	if len(auth) == 0 {
		return nil, "", errors.New("give a password or a private key")
	}
	seen := ""
	hostKeys := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		seen = got
		if c.HostKey == "" {
			return nil // first use: the app keeps it
		}
		want, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.HostKey))
		if err != nil {
			return fmt.Errorf("pinned host key: %w", err)
		}
		if !bytes.Equal(want.Marshal(), key.Marshal()) {
			return errors.New("the server's host key changed: if you reinstalled the server, remove it and add it again; otherwise someone may be intercepting the connection")
		}
		return nil
	}
	cl, err := ssh.Dial("tcp", c.addr(), &ssh.ClientConfig{User: c.User, Auth: auth, HostKeyCallback: hostKeys, Timeout: 20 * time.Second})
	if err != nil {
		return nil, seen, fmt.Errorf("ssh %s: %w", c.addr(), err)
	}
	t := &sshTransport{c: cl, root: c.User == "root"}
	if !t.root {
		if _, _, err := t.Exec("true", "", true); err != nil {
			pw := c.Password
			t.sudoPass = &pw
			if pw == "" {
				cl.Close()
				return nil, seen, fmt.Errorf("%s needs a password for sudo: log in as root, or give the password", c.User)
			}
			if _, _, err := t.Exec("true", "", true); err != nil {
				cl.Close()
				return nil, seen, fmt.Errorf("%s cannot run sudo: %v", c.User, err)
			}
		}
	}
	go func() { // keepalive; a dead connection leaves the cache
		tk := time.NewTicker(30 * time.Second)
		defer tk.Stop()
		for range tk.C {
			if _, _, err := cl.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				cl.Close()
				return
			}
		}
	}()
	return &remote.Server{Transport: t, Conn: remote.Conn{Name: c.Name, Host: c.Host, Port: c.Port, User: c.User}}, seen, nil
}

// server returns an open connection to c, opening one when needed.
func server(c ServerConn) (*remote.Server, error) {
	if c.HostKey == "" {
		return nil, errors.New("connect to the server first")
	}
	connsMu.Lock()
	r := conns[c.cacheKey()]
	connsMu.Unlock()
	if r != nil {
		if _, _, err := r.Transport.Exec("true", "", false); err == nil {
			return r, nil
		}
		r.Close()
	}
	r, _, err := dialServer(c)
	if err != nil {
		return nil, err
	}
	connsMu.Lock()
	conns[c.cacheKey()] = r
	connsMu.Unlock()
	return r, nil
}

// ConnectServer opens c (pinning its host key on first use) and tells what is installed there.
func ConnectServer(c ServerConn) (Connected, error) {
	r, seen, err := dialServer(c)
	if err != nil {
		return Connected{HostKey: seen}, err
	}
	c.HostKey = seen
	connsMu.Lock()
	if old := conns[c.cacheKey()]; old != nil {
		old.Close()
	}
	conns[c.cacheKey()] = r
	connsMu.Unlock()
	out := Connected{HostKey: seen, Root: c.User == "root" || c.User == ""}
	if v, err := r.Root("xtune version 2>/dev/null || true"); err == nil {
		out.Version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "xtune "))
	}
	return out, nil
}

func DisconnectServer(c ServerConn) {
	connsMu.Lock()
	defer connsMu.Unlock()
	if r := conns[c.cacheKey()]; r != nil {
		r.Close()
		delete(conns, c.cacheKey())
	}
}

// ---------------------------------------------------------------- reads

type EntryInfo struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Port  int    `json:"port"`
	Users int    `json:"users"` // users online on it at the last count
}

type NetworkInfo struct {
	ASN     string   `json:"asn"`
	Name    string   `json:"name"`
	Updated int64    `json:"updated"`
	Best    []string `json:"best"`    // config IDs, best first
	Blocked []string `json:"blocked"` // config IDs that failed there
}

// Overview is the server's dashboard.
type Overview struct {
	Host       string        `json:"host"`
	Domain     string        `json:"domain,omitempty"`
	DNSDomain  string        `json:"dnsDomain,omitempty"` // the DNS tunnel's zone
	Country    string        `json:"country,omitempty"`
	Version    string        `json:"version"`
	Entries    []EntryInfo   `json:"entries"`
	Users      int           `json:"users"`
	Enabled    int           `json:"enabled"`
	Online     int           `json:"online"`
	Devices    int           `json:"devices"`
	TodayBytes int64         `json:"todayBytes"`
	MonthBytes int64         `json:"monthBytes"`
	Sub        bool          `json:"sub"`
	Relays     []string      `json:"relays,omitempty"`
	Networks   []NetworkInfo `json:"networks,omitempty"`
}

func ServerOverview(c ServerConn) (*Overview, error) {
	r, err := server(c)
	if err != nil {
		return nil, err
	}
	st, err := state.Load(r)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("nothing is deployed on this server yet: tune and deploy from xtune on a computer first")
	}
	us, err := state.LoadUsage(r)
	if err != nil {
		return nil, err
	}
	v, _ := r.Root("xtune version 2>/dev/null || true")
	o := &Overview{Host: st.Host, Domain: st.Domain, DNSDomain: st.DNSDomain, Country: st.Country, Version: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "xtune ")),
		Users: len(st.Users), Sub: st.Sub.Mode != ""}
	now := time.Now()
	today, month := state.DayKey(now), now.Format("2006-01")
	for _, u := range st.Users {
		if u.Enabled {
			o.Enabled++
		}
		uu := us.Users[u.Name]
		if uu == nil {
			continue
		}
		if len(uu.Online) > 0 {
			o.Online++
			o.Devices += len(uu.Online)
		}
		for d, b := range uu.Daily {
			if d == today {
				o.TodayBytes += b
			}
			if strings.HasPrefix(d, month) {
				o.MonthBytes += b
			}
		}
	}
	for _, e := range st.Entries {
		o.Entries = append(o.Entries, EntryInfo{ID: e.ID, Kind: e.Kind, Port: e.Port})
	}
	for _, rl := range st.Relays {
		o.Relays = append(o.Relays, rl.Name)
	}
	for asn, nr := range st.Networks {
		ni := NetworkInfo{ASN: asn, Name: nr.Name, Updated: nr.Updated}
		type kv struct {
			id string
			s  float64
		}
		var ks []kv
		for _, e := range st.Entries {
			if s, ok := nr.Scores[state.EntryKey(e)]; ok {
				ks = append(ks, kv{e.ID, s})
			}
		}
		for i := range ks { // best first
			for j := i + 1; j < len(ks); j++ {
				if ks[j].s > ks[i].s {
					ks[i], ks[j] = ks[j], ks[i]
				}
			}
		}
		for _, k := range ks {
			if k.s == 0 {
				ni.Blocked = append(ni.Blocked, k.id)
			} else {
				ni.Best = append(ni.Best, k.id)
			}
		}
		o.Networks = append(o.Networks, ni)
	}
	return o, nil
}

// xtune runs the server's xtune with args (as root) and returns its output.
func xtune(c ServerConn, args ...string) (string, error) {
	r, err := server(c)
	if err != nil {
		return "", err
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = remote.Quote(a)
	}
	out, _, err := r.Transport.Exec("xtune "+strings.Join(q, " "), "", true)
	return out, err
}

// Users is `xtune users list --json` as is.
func Users(c ServerConn) (json.RawMessage, error) {
	out, err := xtune(c, "users", "--json", "list")
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(out)) {
		return nil, errors.New("unexpected answer from xtune on the server: update it (xtune self-update)")
	}
	return json.RawMessage(out), nil
}

// UserAction changes a user: add, del, enable, disable, renew, reset-usage, rotate, limit,
// reset-sub, links.
type UserAction struct {
	Action  string   `json:"action"`
	Name    string   `json:"name"`
	QuotaGB *float64 `json:"quotaGb,omitempty"`
	Days    *int     `json:"days,omitempty"`
	Devices *int     `json:"devices,omitempty"`
	Reset   *string  `json:"reset,omitempty"`
	Plan    string   `json:"plan,omitempty"`
}

var userActions = map[string]bool{"add": true, "del": true, "enable": true, "disable": true, "renew": true,
	"reset-usage": true, "rotate": true, "limit": true, "reset-sub": true, "links": true}

func DoUser(c ServerConn, a UserAction) (string, error) {
	if !userActions[a.Action] {
		return "", fmt.Errorf("unknown action %q", a.Action)
	}
	if a.Name == "" || strings.ContainsAny(a.Name, " /\\'\"") || strings.HasPrefix(a.Name, "-") {
		return "", errors.New("a user name is letters, digits, - and _")
	}
	args := []string{"users"}
	if a.QuotaGB != nil {
		args = append(args, "--quota", strconv.FormatFloat(*a.QuotaGB, 'f', -1, 64))
	}
	if a.Days != nil {
		args = append(args, "--days", strconv.Itoa(*a.Days))
	}
	if a.Devices != nil {
		args = append(args, "--devices", strconv.Itoa(*a.Devices))
	}
	if a.Reset != nil {
		args = append(args, "--reset", orStr(*a.Reset, "none"))
	}
	if a.Plan != "" {
		args = append(args, "--plan", a.Plan)
	}
	switch a.Action {
	case "add", "links", "rotate", "reset-sub":
		args = append(args, "--json")
	}
	return xtune(c, append(args, a.Action, a.Name)...)
}

// Xtune runs any xtune command on the server: the app's console. Only the operator, who holds
// the server's root login anyway, can call it.
func Xtune(c ServerConn, args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("no command")
	}
	return xtune(c, args...)
}

// Logs are the last lines of a unit's journal.
func Logs(c ServerConn, unit string, lines int) (string, error) {
	if lines <= 0 || lines > 2000 {
		lines = 200
	}
	if unit == "" {
		unit = "xray-autotune"
	}
	if strings.ContainsAny(unit, " ;&|'\"$`") {
		return "", errors.New("bad unit")
	}
	r, err := server(c)
	if err != nil {
		return "", err
	}
	out, _, err := r.Transport.Exec(fmt.Sprintf("journalctl -u %s -n %d --no-pager -o short-iso", unit, lines), "", true)
	return out, err
}
