package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chethober/xtune/internal/state"

	"github.com/chethober/xtune/internal/remote"
)

// A backup is a deployment's identity (server keys, certificate), its users with their usage, and
// its addresses: enough to rebuild it on another server with the same links and subscriptions.

type Backup struct {
	Version string       `json:"xtuneVersion"`
	Created int64        `json:"created"`
	State   *state.State `json:"state"`
	Usage   *state.Usage `json:"usage"`
}

func TakeBackup(r *remote.Server) (*Backup, error) {
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	us, err := state.LoadUsage(r)
	if err != nil {
		return nil, err
	}
	return &Backup{Version: version, Created: time.Now().Unix(), State: st, Usage: us}, nil
}

// writeBackup saves b readable by the owner only, encrypted when pass is set. It holds every
// secret of the deployment.
func writeBackup(path string, b *Backup, pass string) error {
	data, err := backupBytes(b, pass)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func backupBytes(b *Backup, pass string) ([]byte, error) {
	data, err := json.MarshalIndent(b, "", " ")
	if err != nil || pass == "" {
		return data, err
	}
	return seal(data, pass)
}

func readBackup(path string) (*Backup, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if raw, err = unseal(raw, backupPassphrase); err != nil {
		return nil, err
	}
	return parseBackup(path, raw)
}

// parseBackup reads a backup from any release: its state and usage go through the same
// migrations as the files on a server.
func parseBackup(path string, raw []byte) (*Backup, error) {
	var in struct {
		Version string          `json:"xtuneVersion"`
		Created int64           `json:"created"`
		State   json.RawMessage `json:"state"`
		Usage   json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("%s is not an xtune backup: %w", path, err)
	}
	if len(in.State) == 0 || string(in.State) == "null" {
		return nil, fmt.Errorf("%s is not an xtune backup", path)
	}
	st, err := state.Decode(in.State)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st.RealityPriv == "" {
		return nil, fmt.Errorf("%s is not an xtune backup", path)
	}
	b := &Backup{Version: in.Version, Created: in.Created, State: st, Usage: &state.Usage{Users: map[string]*state.UserUsage{}}}
	if len(in.Usage) > 0 && string(in.Usage) != "null" {
		if b.Usage, err = state.DecodeUsage(in.Usage); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return b, nil
}

func backupName(st *state.State, now time.Time) string {
	host := strings.NewReplacer(":", "_", "/", "_").Replace(st.Addr())
	return fmt.Sprintf("xtune-backup-%s-%s.json", host, now.Format("20060102-1504"))
}

// seed puts a backup on a server that has no deployment yet: the state (with the new server's
// address) and the usage. Nothing runs yet; Restore or a tune and deploy follow. probeOut is the
// helper's probe output for the new server.
func seed(r *remote.Server, b *Backup, probeOut string, log func(string)) (*state.State, error) {
	old, err := state.Load(r)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return nil, fmt.Errorf("%s already has a deployment; run `xtune uninstall` there first if it should be replaced", r.Name())
	}
	st := *b.State
	st.Host, st.ClientIP = r.Conn.Host, "" // a reserved IP stays with the old server
	forgetDetectedFlag(&st)                // the new server may be elsewhere
	if st.Autopilot != nil {
		ap := *st.Autopilot
		ap.Droplet, ap.Owned = 0, "" // found again on the new server
		st.Autopilot = &ap
	}
	if usedPorts(probeOut)["tcp"][st.APIPort] {
		st.APIPort = 0 // Apply picks another
	}
	us := *b.Usage
	us.Blocks, us.ActiveKey, us.Notified = nil, "", nil
	for _, uu := range us.Users {
		uu.Online = nil
	}
	if _, err := EnsureServerXray(r, "", log); err != nil {
		return nil, err
	}
	if err := state.Save(r, &st); err != nil {
		return nil, err
	}
	if err := state.SaveUsage(r, &us); err != nil {
		return nil, err
	}
	return &st, nil
}

// portConflicts lists the backup's ports that are in use on the new server.
func portConflicts(st *state.State, probeOut string) []string {
	used := usedPorts(probeOut)
	var c []string
	for _, e := range st.Entries {
		if used[e.Proto()][e.Port] {
			c = append(c, fmt.Sprintf("%s %d/%s", e.ID, e.Port, e.Proto()))
		}
	}
	if st.Sub.Mode != "" && used["tcp"][st.Sub.Port] {
		c = append(c, fmt.Sprintf("subscription server %d/tcp", st.Sub.Port))
	}
	if st.Sub.Mode == "direct" && used["tcp"][80] {
		c = append(c, "Let's Encrypt 80/tcp")
	}
	return c
}

// Restore rebuilds a backup on r with its configs as they were: same ports, SNIs and client
// settings. Links change only where they contain the server's IP.
func Restore(r *remote.Server, b *Backup, log func(string)) (*state.State, error) {
	out, err := r.Helper("probe")
	if err != nil {
		return nil, err
	}
	if c := portConflicts(b.State, out); len(c) > 0 {
		return nil, fmt.Errorf("in use on %s: %s; migrate with a tune instead, which picks free ports", r.Name(), strings.Join(c, ", "))
	}
	st, err := seed(r, b, out, log)
	if err != nil {
		return nil, err
	}
	if err := installXtune(r, log); err != nil {
		return nil, err
	}
	if err := Apply(r, st); err != nil {
		return nil, err
	}
	return st, startServices(r, st)
}

// installXtune puts xtune on the server, where users are managed and limits enforced.
func installXtune(r *remote.Server, log func(string)) error {
	if err := EnsureServerXtune(r, log); err != nil {
		return fmt.Errorf("xtune could not be installed on the server (users, limits and subscriptions need it): %w", err)
	}
	return nil
}

// moveNotes tells the admin what is left to do after a deployment moved to a new server.
func moveNotes(st *state.State, oldAddr string, dnsDone bool) []string {
	ip := st.Host
	var notes []string
	if st.Domain != "" && !dnsDone {
		notes = append(notes, fmt.Sprintf("point the DNS record of %s (DNS only) at %s", st.Domain, ip))
	}
	if st.CDNDomain != "" && !dnsDone {
		notes = append(notes, fmt.Sprintf("point the CDN record of %s (proxied) at %s", st.CDNDomain, ip))
	}
	switch {
	case st.Sub.Mode != "":
		notes = append(notes, "users' subscription links stay the same: their apps get the new configs on the next update")
	case st.Domain == "":
		notes = append(notes, "links contain the server's IP, so send users their new links (sudo xtune users links NAME)")
	default:
		notes = append(notes, "once DNS points at the new server, users' links work again; send new links to users of configs that changed")
	}
	if oldAddr != "" {
		notes = append(notes, "when everyone has moved, remove xtune from the old server: xtune uninstall --host "+oldAddr)
	}
	return notes
}

// parseTarget reads a saved profile name or [user@]host[:port].
func parseTarget(s string) (remote.Conn, error) {
	if p, ok := findProfile(s); ok {
		return p, nil
	}
	var c remote.Conn
	if u, h, ok := strings.Cut(s, "@"); ok {
		c.User, s = u, h
	}
	c.Host = s
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil {
			return c, fmt.Errorf("bad port in %q", s)
		}
		c.Host, c.Port = h, n
	}
	if c.Host == "" {
		return c, fmt.Errorf("no host in %q", s)
	}
	return c, nil
}

// warnOldVersion notes backups taken by a newer xtune, whose fields this one may not know.
func warnOldVersion(b *Backup, log func(string)) {
	if newer(b.Version, version) && version != "dev" {
		log(fmt.Sprintf("[!] this backup was made by xtune %s; this is %s: update first (xtune self-update) to keep every setting", b.Version, version))
	}
}

// hostsOf lists the addresses a state answers to, for messages.
func hostsOf(st *state.State) string {
	hs := []string{st.Host}
	for _, d := range []string{st.Domain, st.CDNDomain} {
		if d != "" && !slices.Contains(hs, d) {
			hs = append(hs, d)
		}
	}
	return strings.Join(hs, ", ")
}
