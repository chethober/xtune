package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chethober/xtune/internal/protocols"

	"github.com/chethober/xtune/internal/remote"
	"github.com/chethober/xtune/internal/state"
)

// Multi-hop: a relay, usually in the users' own country, forwards ports to this server
// (client → relay → server). The relay runs only an Xray port forwarder (unit
// xray-autotune-relay): no keys, no users, so a seized relay gives nothing away, and REALITY,
// Hysteria2, WireGuard and the SSH tunnels still end at this server. Configs through a relay are
// this server's configs at the relay's address, listed in subscriptions like a follower's,
// named RELAY-ID and with this server's flag: traffic still leaves the internet from here.

var relayNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// planForwards picks the relay's port for each relayable entry: the same port when it is free
// there, else a free high port. Ports a relay already uses for an entry are kept.
func planForwards(st *state.State, prev []state.Forward, used map[string]map[int]bool, only []string) []state.Forward {
	taken := map[string]map[int]bool{"tcp": {}, "udp": {}}
	for _, f := range prev { // the relay's own ports show as used: they are ours to reuse
		delete(used[f.Net], f.Port)
	}
	free := func(net string, p int) bool { return !used[net][p] && !taken[net][p] }
	var out []state.Forward
	for _, e := range st.Entries {
		if !protocols.Relayable(e) || (len(only) > 0 && !slices.Contains(only, e.ID)) {
			continue
		}
		f := state.Forward{ID: e.ID, To: e.Port, Net: e.Proto()}
		if i := slices.IndexFunc(prev, func(p state.Forward) bool { return p.ID == e.ID && p.Net == f.Net }); i >= 0 && free(f.Net, prev[i].Port) {
			f.Port = prev[i].Port
		} else if free(f.Net, e.Port) {
			f.Port = e.Port
		} else {
			for f.Port == 0 || !free(f.Net, f.Port) {
				f.Port = 20000 + rand.IntN(40000)
			}
		}
		taken[f.Net][f.Port] = true
		out = append(out, f)
	}
	return out
}

// relayConfig is the relay's Xray config: one forwarder per port, nothing else.
func relayConfig(target string, fs []state.Forward) J {
	var ins []J
	for _, f := range fs {
		ins = append(ins, J{"tag": "fwd-" + f.ID, "listen": "0.0.0.0", "port": f.Port, "protocol": "dokodemo-door",
			"settings": J{"address": target, "port": f.To, "network": f.Net}})
	}
	return J{"log": J{"loglevel": "warning"}, "inbounds": ins,
		"outbounds": []J{{"tag": "out", "protocol": "freedom"}},
		"policy":    J{"levels": J{"0": J{"handshake": 8, "connIdle": 600}}}}
}

func relaySpecs(fs []state.Forward) []string {
	var specs []string
	for _, f := range fs {
		specs = append(specs, fmt.Sprintf("%d/%s", f.Port, f.Net))
	}
	return specs
}

// parseRelayCheck reads relay-check's "TCP PORT MS|fail" lines.
func parseRelayCheck(out string) map[int]int {
	ms := map[int]int{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 3 || f[0] != "TCP" {
			continue
		}
		p, _ := strconv.Atoi(f[1])
		if v, err := strconv.Atoi(f[2]); err == nil {
			ms[p] = max(v, 1)
		} else {
			ms[p] = -1
		}
	}
	return ms
}

// RelayOpts are what `xtune relay add` and `sync` take.
type RelayOpts struct {
	Name string
	Addr string   // the address clients dial, default the relay's SSH host
	Only []string // entry IDs to forward, default every relayable one
	Log  func(string)
}

// SyncRelay sets up relay rl (new, or one this server knows) to forward this server's current
// configs, measures each TCP port from the relay, and records it. Ports the relay cannot reach
// are left out; UDP cannot be measured this way and is forwarded as it is.
func SyncRelay(r, relay *remote.Server, o RelayOpts) (*state.State, error) {
	log := o.Log
	if log == nil {
		log = func(string) {}
	}
	if !relayNameRe.MatchString(o.Name) {
		return nil, fmt.Errorf("relay name %q: lowercase letters, digits and dashes", o.Name)
	}
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	target := protocols.RelayTarget(st)
	if relay.Conn.Host == r.Conn.Host || relay.Conn.Host == target {
		return nil, errors.New("the relay must be another server")
	}
	i := slices.IndexFunc(st.Relays, func(x state.Relay) bool { return x.Name == o.Name })
	rl := state.Relay{Name: o.Name, Added: time.Now().Unix()}
	if i >= 0 {
		rl = st.Relays[i]
	}
	rl.Addr = orStr(o.Addr, orStr(rl.Addr, relay.Conn.Host))
	rl.SSH = state.RelaySSH{Host: relay.Conn.Host, Port: relay.Conn.Port, User: relay.Conn.User}
	if rl.SSH.Port == 22 {
		rl.SSH.Port = 0
	}
	if rl.SSH.User == "root" {
		rl.SSH.User = ""
	}
	if _, err := EnsureServerXray(relay, "", log); err != nil {
		return nil, fmt.Errorf("xray on the relay: %w", err)
	}
	probe, err := relay.Helper("probe")
	if err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	fs := planForwards(st, rl.Forwards, usedPorts(probe), o.Only)
	if len(fs) == 0 {
		return nil, errors.New("no config here can go through a relay (CDN, Worker, DNS tunnel and VPN configs cannot)")
	}
	var tcp []string
	for _, f := range fs {
		if f.Net == "tcp" {
			tcp = append(tcp, strconv.Itoa(f.To))
		}
	}
	reach := map[int]int{}
	if len(tcp) > 0 {
		log("measuring this server from the relay")
		out, err := relay.Helper(append([]string{"relay-check", target}, tcp...)...)
		if err != nil {
			return nil, fmt.Errorf("relay-check: %w", err)
		}
		reach = parseRelayCheck(out)
	}
	var kept []state.Forward
	for _, f := range fs {
		if f.Net == "tcp" {
			if reach[f.To] <= 0 {
				log(fmt.Sprintf("  %-18s %5d/tcp  not reachable from the relay: left out", f.ID, f.To))
				continue
			}
			f.Ms = reach[f.To]
			log(fmt.Sprintf("  %-18s %5d/tcp  relay port %d, %d ms to this server", f.ID, f.To, f.Port, f.Ms))
		} else {
			log(fmt.Sprintf("  %-18s %5d/udp  relay port %d (UDP: not measured)", f.ID, f.To, f.Port))
		}
		kept = append(kept, f)
	}
	if len(kept) == 0 {
		return nil, errors.New("the relay reaches none of this server's ports: is it filtered between them?")
	}
	cfg, _ := json.MarshalIndent(relayConfig(target, kept), "", " ")
	if err := relay.Upload(remote.Dir+"/relay.json", cfg, "600"); err != nil {
		return nil, err
	}
	if out, err := relay.Helper(append([]string{"relay-apply"}, relaySpecs(kept)...)...); err != nil {
		return nil, fmt.Errorf("relay-apply: %w", err)
	} else if !strings.Contains(out, "active") {
		return nil, fmt.Errorf("the relay did not start: %s", out)
	}
	rl.Forwards, rl.Target = kept, target
	defer r.Lock()()
	if st, err = requireState(r); err != nil { // reloaded under the lock
		return nil, err
	}
	if i := slices.IndexFunc(st.Relays, func(x state.Relay) bool { return x.Name == rl.Name }); i >= 0 {
		st.Relays[i] = rl
	} else {
		st.Relays = append(st.Relays, rl)
	}
	return st, state.Save(r, st)
}

// RemoveRelay stops relay name (when it can be reached: relay may be nil) and forgets it.
func RemoveRelay(r, relay *remote.Server, name string) (*state.State, error) {
	if relay != nil {
		if _, err := relay.Helper("relay-remove"); err != nil {
			return nil, fmt.Errorf("relay: %w (to forget it anyway: --forget)", err)
		}
	}
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(st.Relays, func(x state.Relay) bool { return x.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("no relay %q", name)
	}
	st.Relays = slices.Delete(st.Relays, i, i+1)
	return st, state.Save(r, st)
}

func printRelays(st *state.State) {
	if len(st.Relays) == 0 {
		fmt.Println("no relays (add one: xtune relay add NAME --ssh RELAY_HOST)")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tADDRESS\tCONFIGS\tTO THIS SERVER\tSTATE")
	for _, rl := range st.Relays {
		var ms []int
		for _, f := range rl.Forwards {
			if f.Ms > 0 {
				ms = append(ms, f.Ms)
			}
		}
		lat := "-"
		if len(ms) > 0 {
			lat = fmt.Sprintf("%d ms", slices.Min(ms))
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", rl.Name, rl.Addr, len(rl.Forwards), lat, relayProblem(st, rl, "in subscriptions"))
	}
	tw.Flush()
}

// relayProblem says what is wrong with a relay since its last sync, or ok when nothing is.
func relayProblem(st *state.State, rl state.Relay, ok string) string {
	if rl.Target != protocols.RelayTarget(st) {
		return "forwards to " + rl.Target + ", this server is now " + protocols.RelayTarget(st) + ": xtune relay sync " + rl.Name
	}
	stale := 0
	for _, f := range rl.Forwards {
		if !slices.ContainsFunc(st.Entries, func(e state.Entry) bool { return e.ID == f.ID && e.Port == f.To }) {
			stale++
		}
	}
	if stale > 0 {
		return fmt.Sprintf("%d of its configs changed in a redeploy: xtune relay sync %s", stale, rl.Name)
	}
	return ok
}

func cmdRelay(args []string) error {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	c := addConnFlags(fs)
	sshHost := fs.String("ssh", "", "the relay's SSH address (add; sync uses the recorded one)")
	sshUser := fs.String("ssh-user", "", "SSH user on the relay (default root)")
	sshPort := fs.Int("ssh-port", 0, "SSH port on the relay (default 22)")
	sshKey := fs.String("ssh-key", "", "SSH key for the relay")
	sshPass := fs.String("ssh-password-env", "", "env var holding the relay's SSH password")
	addr := fs.String("addr", "", "address clients dial for the relay (default its SSH host)")
	only := fs.String("only", "", "comma-separated config IDs to forward (default every one that can)")
	forget := fs.Bool("forget", false, "del: forget the relay without reaching it")
	pos := parse(fs, args)
	action := "list"
	if len(pos) > 0 {
		action = pos[0]
	}
	r, err := target(c)
	if err != nil {
		return err
	}
	defer r.Close()
	name := ""
	if len(pos) > 1 {
		name = pos[1]
	}
	dialRelay := func(rl *state.Relay) (*remote.Server, error) {
		cn := remote.Conn{Host: *sshHost, Port: *sshPort, User: *sshUser, KeyPath: *sshKey}
		if rl != nil && cn.Host == "" {
			cn.Host, cn.Port, cn.User = rl.SSH.Host, rl.SSH.Port, rl.SSH.User
		}
		if *sshPass != "" {
			cn.Password = os.Getenv(*sshPass)
		}
		if cn.Host == "" {
			return nil, errors.New("--ssh RELAY_HOST is required")
		}
		return remote.Dial(cn, cliPrompt)
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	find := func() (*state.Relay, error) {
		for i := range st.Relays {
			if st.Relays[i].Name == name {
				return &st.Relays[i], nil
			}
		}
		return nil, fmt.Errorf("no relay %q", name)
	}
	logf := func(s string) { fmt.Println(s) }
	var ids []string
	if *only != "" {
		ids = strings.Split(*only, ",")
	}
	switch action {
	case "list", "status":
		printRelays(st)
		return nil
	case "add":
		if name == "" {
			return errors.New("usage: xtune relay add NAME --ssh RELAY_HOST [--addr A] [--only ID,ID] [conn]")
		}
		if _, err := find(); err == nil {
			return fmt.Errorf("relay %q exists: xtune relay sync %s", name, name)
		}
		relay, err := dialRelay(nil)
		if err != nil {
			return err
		}
		defer relay.Close()
		if st, err = SyncRelay(r, relay, RelayOpts{Name: name, Addr: *addr, Only: ids, Log: logf}); err != nil {
			return err
		}
	case "sync":
		var names []string
		if name != "" {
			if _, err := find(); err != nil {
				return err
			}
			names = []string{name}
		}
		for _, rl := range st.Relays {
			if name == "" {
				names = append(names, rl.Name)
			}
		}
		for _, n := range names {
			name = n
			rl, _ := find()
			relay, err := dialRelay(rl)
			if err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
			fmt.Println("relay", n)
			st, err = SyncRelay(r, relay, RelayOpts{Name: n, Addr: *addr, Only: ids, Log: logf})
			relay.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
		}
	case "del", "remove":
		rl, err := find()
		if err != nil {
			return err
		}
		var relay *remote.Server
		if !*forget {
			if relay, err = dialRelay(rl); err != nil {
				return fmt.Errorf("%w (to forget it anyway: --forget)", err)
			}
			defer relay.Close()
		}
		if st, err = RemoveRelay(r, relay, name); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown relay action %q (list, add, sync, del)", action)
	}
	printRelays(st)
	return nil
}
