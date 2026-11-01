package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/chethober/xtune/internal/protocols"
	"github.com/chethober/xtune/internal/remote"
	"github.com/chethober/xtune/internal/state"
)

// Several servers under one subscription. One server is the primary: it owns the users, their
// limits and their subscription links. Followers take their users from it: every minute their
// enforce job reports the traffic they carried and their public settings to the primary's
// subscription server, and gets the primary's active users back. The primary's subscriptions then
// list every server's configs, and the failover config balances across all of them, so users keep
// working when one server's IP is blocked. Quotas count traffic on every server, and so do device
// limits: followers report who is online, the primary counts each user's devices on all servers
// and tells followers which IPs to block.

type peerReport struct {
	Name   string              `json:"name"`
	State  *state.State        `json:"state"`
	Usage  map[string][2]int64 `json:"usage,omitempty"`  // up, down since the last report
	Online map[string][]string `json:"online,omitempty"` // client IPs per user, for device limits across servers
}

type peerUser struct {
	Name    string   `json:"name"`
	UUID    string   `json:"uuid"`
	SSKey   string   `json:"ssKey"`
	WGIP    string   `json:"wgIp,omitempty"`    // the follower's WireGuard peers must use the primary's addresses
	Devices int      `json:"devices,omitempty"` // the user's device limit, counted across all servers
	Block   []string `json:"block,omitempty"`   // IPs over that limit: the follower blocks them too
}

type peerReply struct {
	Users []peerUser `json:"users"`
}

// peerState is what clients need to connect to this deployment, and nothing secret.
func peerState(st *state.State) *state.State {
	p := &state.State{Host: st.Host, ClientIP: st.ClientIP, Domain: st.Domain, CDNDomain: st.CDNDomain, RealityPub: st.RealityPub,
		ShortID: st.ShortID, VencEnc: st.VencEnc, SSKey: st.SSKey, CertSNI: st.CertSNI, CertHash: st.CertHash,
		WGPub: st.WGPub, SSHHostPub: st.SSHHostPub, DNSTTPub: st.DNSTTPub, DNSDomain: st.DNSDomain,
		VPNCA: st.VPNCA, VPNName: st.VPNName, Entries: st.Entries, Naming: st.Naming}
	if st.AWG != nil {
		awg := *st.AWG
		awg.Priv = ""
		p.AWG = &awg
	}
	return p
}

// errFollower is returned for user changes on a follower: the primary owns the users.
func errFollower(st *state.State) error {
	host := st.Follow.URL
	if u, err := url.Parse(host); err == nil {
		host = u.Hostname()
	}
	return fmt.Errorf("this server follows %s: users are managed there (or stop following: xtune follow off)", host)
}

// ---------------------------------------------------------------- primary

// PeerURL is the URL followers report to, or "" when the subscription server or peers are off.
func PeerURL(st *state.State) string {
	if st.Peers == nil || subHost(st) == "" {
		return ""
	}
	return "https://" + protocols.HostPort(subHost(st), st.Sub.Port) + "/" + st.Sub.Path + "/peer/" + st.Peers.Secret
}

// EnablePeers lets followers join; it needs the subscription server, which they report to.
func EnablePeers(r *remote.Server) (*state.State, error) {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	if st.Follow != nil {
		return nil, errors.New("this server follows another one; only a primary takes followers")
	}
	if st.Sub.Mode == "" {
		return nil, errors.New("followers report to the subscription server: turn it on first (xtune sub enable)")
	}
	if st.Peers == nil {
		st.Peers = &state.PeerHub{Secret: randHex(16)}
	}
	return st, state.Save(r, st)
}

// DisablePeers drops the followers from subscriptions and makes their URL stop working.
func DisablePeers(r *remote.Server) error {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	st.Peers = nil
	return state.Save(r, st)
}

// RemovePeer drops one follower from subscriptions; it comes back if it reports again.
func RemovePeer(r *remote.Server, name string) error {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if st.Peers == nil || st.Peers.Servers[name] == nil {
		return fmt.Errorf("no follower %q", name)
	}
	delete(st.Peers.Servers, name)
	return state.Save(r, st)
}

// acceptPeer records a follower's report and returns the users it should serve.
func acceptPeer(r *remote.Server, in peerReport, now time.Time) (*peerReply, error) {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	if st.Peers == nil {
		return nil, errors.New("peers are off")
	}
	us, err := state.LoadUsage(r)
	if err != nil {
		return nil, err
	}
	for name, d := range in.Usage {
		if _, u := st.User(name); u == nil || d[0] < 0 || d[1] < 0 {
			continue
		}
		uu := us.Of(name)
		uu.Up += d[0]
		uu.Down += d[1]
		uu.AddDaily(now, d[0]+d[1])
	}
	if st.Peers.Servers == nil {
		st.Peers.Servers = map[string]*state.PeerServer{}
	}
	st.Peers.Servers[in.Name] = &state.PeerServer{State: peerState(in.State), Seen: now.Unix(), Online: in.Online}
	if err := state.Save(r, st); err != nil {
		return nil, err
	}
	if err := state.SaveUsage(r, us); err != nil {
		return nil, err
	}
	rep := &peerReply{Users: []peerUser{}}
	for _, u := range activeUsers(st, us, now) {
		if st.Check == nil || u.Name != st.Check.Name {
			rep.Users = append(rep.Users, peerUser{Name: u.Name, UUID: u.UUID, SSKey: u.SSKey, WGIP: u.WGIP,
				Devices: u.Devices, Block: blocksFor(us, u.Name, now)})
		}
	}
	return rep, nil
}

// servePeer answers a follower's POST to /PATH/peer/SECRET.
func (h subHandler) servePeer(w http.ResponseWriter, req *http.Request, st *state.State, secret string) {
	if st.Peers == nil || h.r == nil || subtle.ConstantTimeCompare([]byte(st.Peers.Secret), []byte(secret)) != 1 {
		http.NotFound(w, req)
		return
	}
	var in peerReport
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&in); err != nil || in.State == nil ||
		in.Name == "" || len(in.Name) > 40 || strings.ContainsAny(in.Name, " /\\\"'#?&%<>") {
		http.Error(w, "bad report", http.StatusBadRequest)
		return
	}
	rep, err := acceptPeer(h.r, in, time.Now())
	if err != nil {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rep)
}

// ---------------------------------------------------------------- follower

// peerClient is a variable so tests can reach a local primary.
var peerClient = &http.Client{Timeout: 20 * time.Second}

// syncPrimary reports this follower's traffic and settings to the primary and takes its users.
// It reports whether the users changed.
func syncPrimary(st *state.State, us *state.Usage) (bool, error) {
	deltas := map[string][2]int64{}
	for name, uu := range us.Users {
		if name == checkUserName {
			continue
		}
		s := us.Synced[name]
		d := [2]int64{uu.Up - s[0], uu.Down - s[1]}
		if d[0] < 0 || d[1] < 0 { // counted again from zero
			d = [2]int64{uu.Up, uu.Down}
		}
		if d != [2]int64{} {
			deltas[name] = d
		}
	}
	online := map[string][]string{}
	for name, uu := range us.Users {
		if name != checkUserName && len(uu.Online) > 0 {
			online[name] = uu.Online
		}
	}
	b, _ := json.Marshal(peerReport{Name: st.Follow.Name, State: peerState(st), Usage: deltas, Online: online})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", st.Follow.URL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "xtune/"+version)
	resp, err := peerClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("report to the primary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("report to the primary: HTTP %d (removed, or peers turned off there?)", resp.StatusCode)
	}
	var rep peerReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rep); err != nil {
		return false, fmt.Errorf("primary's reply: %w", err)
	}
	if us.Synced == nil {
		us.Synced = map[string][2]int64{}
	}
	for name := range deltas {
		uu := us.Users[name]
		us.Synced[name] = [2]int64{uu.Up, uu.Down}
	}
	var users []state.User
	now := time.Now()
	blocked := map[string]bool{}
	for _, b := range us.Blocks {
		blocked[b.IP] = true
	}
	for _, p := range rep.Users {
		if validUserName(p.Name) != nil || p.UUID == "" {
			continue
		}
		u := state.User{Name: p.Name, UUID: p.UUID, SSKey: p.SSKey, WGIP: p.WGIP, Devices: max(p.Devices, 0), Enabled: true, Created: now.Unix()}
		if _, old := st.User(p.Name); old != nil {
			u.Created, u.SubToken = old.Created, old.SubToken
		}
		users = append(users, u)
		for _, ip := range p.Block { // over the user's limit across all servers
			if net.ParseIP(ip) != nil && !blocked[ip] {
				blocked[ip] = true
				us.Blocks = append(us.Blocks, state.IPBlock{IP: ip, User: p.Name, Until: now.Add(deviceBlock).Unix()})
			}
		}
	}
	same := len(users) == len(st.Users)
	for i := 0; same && i < len(users); i++ {
		a, b := users[i], st.Users[i]
		same = a.Name == b.Name && a.UUID == b.UUID && a.SSKey == b.SSKey && a.WGIP == b.WGIP && b.Enabled && b.Quota == 0 &&
			b.Expires == 0 && b.Devices == a.Devices
	}
	st.Users = users
	return !same, nil
}

// SetFollow makes this server follow a primary (url "" stops following; the users stay).
func SetFollow(r *remote.Server, peerURL, name string) (*state.State, error) {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	if peerURL == "" {
		st.Follow = nil
		return st, state.Save(r, st)
	}
	u, err := url.Parse(peerURL)
	if err != nil || u.Scheme != "https" || !strings.Contains(u.Path, "/peer/") {
		return nil, errors.New("pass the peer URL the primary shows (xtune peers on the primary)")
	}
	if st.Peers != nil && len(st.Peers.Servers) > 0 {
		return nil, errors.New("this server has followers of its own; a follower cannot be a primary too")
	}
	name = orStr(name, strings.NewReplacer(":", "-", ".", "-").Replace(st.Addr()))
	if validUserName(name) != nil {
		return nil, fmt.Errorf("invalid server name %q", name)
	}
	prev := st.Follow
	st.Follow = &state.Follow{URL: peerURL, Name: name}
	us, err := state.LoadUsage(r)
	if err != nil {
		return nil, err
	}
	if _, err := syncPrimary(st, us); err != nil {
		st.Follow = prev
		return nil, err
	}
	st.Peers = nil
	if err := state.SaveUsage(r, us); err != nil {
		return nil, err
	}
	return st, commit(r, st)
}

// ---------------------------------------------------------------- commands

func cmdPeers(args []string) error {
	if err := serverOnly("peers", args); err != nil {
		return err
	}
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "status", "list":
	case "enable", "on":
		if _, err := EnablePeers(r); err != nil {
			return err
		}
	case "disable", "off":
		if err := DisablePeers(r); err != nil {
			return err
		}
		fmt.Println("followers: off")
		return nil
	case "remove", "rm":
		if len(args) < 2 {
			return errors.New("peers remove NAME")
		}
		if err := RemovePeer(r, args[1]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown peers action %q", action)
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	printPeers(st)
	return nil
}

func printPeers(st *state.State) {
	switch {
	case st.Follow != nil:
		primary, _, _ := strings.Cut(st.Follow.URL, "/peer/")
		fmt.Printf("this server follows %s as %q\n", primary, st.Follow.Name)
		return
	case st.Peers == nil:
		fmt.Println("followers: off (xtune peers enable)")
		return
	}
	fmt.Println("followers: on. On each other xtune server, run:")
	fmt.Printf("  sudo xtune follow %s\n\n", orStr(PeerURL(st), "(turn the subscription server on first)"))
	if len(st.Peers.Servers) == 0 {
		fmt.Println("no followers yet")
		return
	}
	names := make([]string, 0, len(st.Peers.Servers))
	for n := range st.Peers.Servers {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		p := st.Peers.Servers[n]
		var ids []string
		for _, e := range p.State.Entries {
			ids = append(ids, e.ID)
		}
		seen := time.Since(time.Unix(p.Seen, 0))
		state := "reporting"
		if seen > 5*time.Minute {
			state = "last report " + fmtSince(seen) + " ago"
		}
		if seen > protocols.PeerStale {
			state += ", left out of subscriptions"
		}
		fmt.Printf("%-20s %-28s %s  (%s)\n", n, p.State.Addr(), strings.Join(ids, ", "), state)
	}
}

func cmdFollow(args []string) error {
	if err := serverOnly("follow", args); err != nil {
		return err
	}
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	pos, name := []string{}, ""
	for i := 0; i < len(args); i++ {
		if a := args[i]; a == "--name" && i+1 < len(args) {
			name, i = args[i+1], i+1
		} else if n, ok := strings.CutPrefix(a, "--name="); ok {
			name = n
		} else {
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		st, err := requireState(r)
		if err != nil {
			return err
		}
		if st.Follow == nil {
			fmt.Println("this server manages its own users (follow a primary: xtune follow PEER_URL)")
			return nil
		}
		printPeers(st)
		return nil
	}
	if pos[0] == "off" {
		if _, err := SetFollow(r, "", ""); err != nil {
			return err
		}
		fmt.Println("stopped following; the users stay and are managed here now")
		return nil
	}
	st, err := SetFollow(r, pos[0], name)
	if err != nil {
		return err
	}
	fmt.Printf("following as %q: %d users. The primary's subscriptions now include this server's configs.\n",
		st.Follow.Name, len(st.Users))
	return nil
}

// ---------------------------------------------------------------- device limits across servers

// peerOnlineFresh is how recent a follower's report must be for its online IPs to count.
const peerOnlineFresh = 5 * time.Minute

// remoteOnline are the IPs each user has online on the followers that reported recently.
func remoteOnline(st *state.State, now time.Time) map[string][]string {
	out := map[string][]string{}
	if st.Peers == nil {
		return out
	}
	for _, p := range st.Peers.Servers {
		if now.Sub(time.Unix(p.Seen, 0)) > peerOnlineFresh {
			continue
		}
		for name, ips := range p.Online {
			for _, ip := range ips {
				if !slices.Contains(out[name], ip) {
					out[name] = append(out[name], ip)
				}
			}
		}
	}
	return out
}

// trackFirstSeen records when each user's IP was first online on any server (on a primary with
// followers): the device limit keeps established devices and blocks newcomers.
func trackFirstSeen(st *state.State, us *state.Usage, remote map[string][]string, now time.Time) {
	if len(remote) == 0 && len(us.Online) == 0 {
		return
	}
	if us.Online == nil {
		us.Online = map[string]int64{}
	}
	keep := map[string]bool{}
	for _, u := range st.Users {
		if u.Devices <= 0 {
			continue
		}
		ips := slices.Clone(remote[u.Name])
		if uu := us.Users[u.Name]; uu != nil {
			ips = append(ips, uu.Online...)
		}
		for _, ip := range ips {
			k := u.Name + "|" + ip
			keep[k] = true
			if us.Online[k] == 0 {
				us.Online[k] = now.Unix()
			}
		}
	}
	for k := range us.Online {
		if !keep[k] {
			delete(us.Online, k)
		}
	}
	if len(us.Online) == 0 {
		us.Online = nil
	}
}

// blocksFor are the IPs blocked for a user over their device limit, for a follower to block too.
func blocksFor(us *state.Usage, name string, now time.Time) []string {
	var out []string
	for _, b := range us.Blocks {
		if b.User == name && b.Until > now.Unix() {
			out = append(out, b.IP)
		}
	}
	return out
}
