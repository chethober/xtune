package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

var version = "dev"

const usage = `xtune - Xray tuner and manager

Usage:
  xtune                         interactive wizard (TUI)
  xtune tune      [conn] [tune flags]           find the configs that get through from this network
  xtune xray      [conn] status|update [flags]  check / update Xray locally, on the server and in 3x-ui
  xtune users     [conn] [--backend B] list|add|del|enable|disable|links|rotate|quota|expiry ...
  xtune logs      [conn] [--unit U] [-n N] [-f]
  xtune uninstall [conn]                        remove everything xtune installed on the server
  xtune version

Connection flags (conn):
  --host H --port 22 --user root --key PATH --password-env VAR --profile NAME
  sudo password: prompted, or XTUNE_SUDO_PASSWORD
`

func main() {
	if len(os.Args) < 2 {
		runTUI()
		return
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "tui":
		runTUI()
	case "tune":
		err = cmdTune(args)
	case "xray":
		err = cmdXray(args)
	case "users":
		err = cmdUsers(args)
	case "logs":
		err = cmdLogs(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "version", "--version":
		fmt.Println("xtune", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- connection

type connFlags struct {
	host, user, key, passwordEnv, profile, xray *string
	port                                        *int
}

func addConnFlags(fs *flag.FlagSet) connFlags {
	return connFlags{
		host: fs.String("host", "", "server address"), port: fs.Int("port", 0, "SSH port (default 22)"),
		user: fs.String("user", "", "SSH user (default root)"), key: fs.String("key", "", "SSH private key"),
		passwordEnv: fs.String("password-env", "", "env var holding the SSH password"),
		profile:     fs.String("profile", "", "saved profile name"), xray: fs.String("xray", "", "local xray binary"),
	}
}

func (c connFlags) conn() (Conn, error) {
	var cn Conn
	if *c.profile != "" {
		p, ok := findProfile(*c.profile)
		if !ok {
			return cn, fmt.Errorf("no profile %q", *c.profile)
		}
		cn = p
	}
	if *c.host != "" {
		cn.Host = *c.host
	}
	if *c.port != 0 {
		cn.Port = *c.port
	}
	if *c.user != "" {
		cn.User = *c.user
	}
	if *c.key != "" {
		cn.KeyPath = *c.key
	}
	if *c.passwordEnv != "" {
		cn.Password = os.Getenv(*c.passwordEnv)
	}
	if cn.Host == "" {
		return cn, fmt.Errorf("--host or --profile is required")
	}
	return cn, nil
}

// cliPrompt reads from the terminal; secrets are not echoed.
func cliPrompt(label string, secret bool) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("%s needed but stdin is not a terminal", label)
	}
	fmt.Fprint(os.Stderr, label+": ")
	if secret {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	s, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(s), err
}

func connect(c connFlags) (*Remote, error) {
	cn, err := c.conn()
	if err != nil {
		return nil, err
	}
	return Dial(cn, cliPrompt)
}

func parse(fs *flag.FlagSet, args []string) []string {
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	// allow flags after positional args: "users add bob --quota 5"
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && i+1 < len(args) {
				if f := fs.Lookup(name); f != nil {
					if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bf.IsBoolFlag() {
						flags = append(flags, args[i+1])
						i++
					}
				}
			}
			continue
		}
		pos = append(pos, a)
	}
	fs.Parse(flags)
	return pos
}

// ---------------------------------------------------------------- interrupt safety

var (
	activeMu      sync.Mutex
	activeHarness *harness
)

func setActiveHarness(h *harness) { activeMu.Lock(); activeHarness = h; activeMu.Unlock() }

// guarded runs fn; on Ctrl+C / SIGTERM it stops the local harness and the server test
// instance before exiting, so an interrupted tune never leaves ports open.
func guarded(r *Remote, fn func() error) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-sig:
		fmt.Fprintln(os.Stderr, "\ninterrupted - stopping test instances ...")
		activeMu.Lock()
		if activeHarness != nil {
			activeHarness.stop()
		}
		activeMu.Unlock()
		if _, err := r.Helper("stop-test"); err != nil {
			fmt.Fprintln(os.Stderr, "server cleanup failed:", err)
		}
		os.Exit(130)
	}
	return nil
}

// ---------------------------------------------------------------- tune

func cmdTune(args []string) error {
	fs := flag.NewFlagSet("tune", flag.ExitOnError)
	c := addConnFlags(fs)
	o := TuneOpts{}
	fs.StringVar(&o.Depth, "depth", "deep", "smart | deep")
	fs.IntVar(&o.Probes, "probes", 5, "screening requests per variant")
	fs.IntVar(&o.Parallel, "parallel", 6, "parallel screening")
	fs.IntVar(&o.Top, "top", 30, "max variants benchmarked")
	fs.IntVar(&o.Rounds, "rounds", 2, "benchmark rounds (median)")
	fs.IntVar(&o.Secs, "secs", 8, "seconds per transfer")
	fs.StringVar(&o.Out, "out", "xtune-results", "results directory")
	fs.StringVar(&o.ServerTag, "server-version", "", "install this Xray tag on the server first")
	only := fs.String("only", "", "test only these entries, e.g. hy2,r-tcp,venc (prefix match)")
	deploy := fs.Int("deploy", -1, "keep the top N configs running (0 = none; asks when omitted)")
	users := fs.String("users", "", "comma-separated user names for a new deployment")
	parse(fs, args)
	if *only != "" {
		o.Only = strings.Split(*only, ",")
	}
	r, err := connect(c)
	if err != nil {
		return err
	}
	defer r.Close()
	o.XrayBin, err = EnsureLocalXray(*c.xray, r, func(s string) { fmt.Println(s) })
	if err != nil {
		return err
	}
	var res *TuneResult
	if err := guarded(r, func() error { var e error; res, e = Tune(r, o); return e }); err != nil {
		r.Helper("cleanup")
		return err
	}
	n := *deploy
	if n < 0 {
		s, _ := cliPrompt(fmt.Sprintf("Keep the top N configs running on the server? N (0 = none) [0]"), false)
		n, _ = strconv.Atoi(s)
	}
	if n > 0 {
		st, err := Deploy(r, res, n, strings.Split(*users, ","))
		if err != nil {
			return err
		}
		printDeployment(st)
	} else {
		out, _ := r.Helper("cleanup")
		fmt.Println("[*] server cleanup:", out)
	}
	return nil
}

func printDeployment(st *State) {
	fmt.Printf("\n[+] deployed %d configs (systemd unit xray-autotune)\n", len(st.Entries))
	for _, u := range st.Users {
		fmt.Printf("\n# %s\n", u.Name)
		for _, l := range st.Links(u) {
			fmt.Println(l)
		}
	}
}

// ---------------------------------------------------------------- xray

func cmdXray(args []string) error {
	fs := flag.NewFlagSet("xray", flag.ExitOnError)
	c := addConnFlags(fs)
	ver := fs.String("version", "", "tag to install (default: newest)")
	stable := fs.Bool("stable", false, "newest non-prerelease instead of newest")
	local := fs.Bool("local", false, "only the local binary")
	server := fs.Bool("server", false, "only xtune's server binary")
	panelOnly := fs.Bool("panel", false, "only 3x-ui's Xray")
	pos := parse(fs, args)
	action := "status"
	if len(pos) > 0 {
		action = pos[0]
	}
	var r *Remote
	if *c.host != "" || *c.profile != "" {
		var err error
		if r, err = connect(c); err != nil {
			return err
		}
		defer r.Close()
	}
	all := !*local && !*server && !*panelOnly
	switch action {
	case "status":
		return xrayStatus(os.Stdout, r, *c.xray, *stable)
	case "update":
		tag := *ver
		if tag == "" {
			var err error
			if tag, err = LatestXray(*stable, r); err != nil {
				return err
			}
		}
		return xrayUpdate(r, *c.xray, normTag(tag), all || *local, (all || *server) && r != nil, (all || *panelOnly) && r != nil, cliPrompt)
	}
	return fmt.Errorf("unknown xray action %q", action)
}

func xrayStatus(w *os.File, r *Remote, xrayFlag string, stable bool) error {
	latest, lerr := LatestXray(stable, r)
	if lerr != nil {
		latest = "unknown"
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "WHERE\tVERSION\tLATEST\tSTATE\n")
	mark := func(v string) string {
		if v == "none" || v == "" {
			return latest
		}
		if newer(latest, v) {
			return latest + " (update available)"
		}
		return latest
	}
	lb := FindLocalXray(xrayFlag)
	lv := XrayVersion(lb)
	fmt.Fprintf(tw, "local\t%s\t%s\t%s\n", lv, mark(lv), orStr(lb, "not installed"))
	if r != nil {
		st, err := r.Status()
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "server (xtune)\t%s\t%s\tunit %s\n", st.Xray, mark(st.Xray), orStr(st.Unit, "-"))
		if st.XUI {
			fmt.Fprintf(tw, "server (3x-ui)\t%s\t%s\tx-ui %s\n", st.XUIXray, mark(st.XUIXray), st.XUIUnit)
		}
	}
	return tw.Flush()
}

func xrayUpdate(r *Remote, xrayFlag, tag string, local, server, panel bool, ask Prompter) error {
	logf := func(s string) { fmt.Println(s) }
	if local {
		cur := XrayVersion(FindLocalXray(xrayFlag))
		if newer(tag, cur) || cur == "none" || cur == "broken" {
			p, err := InstallLocalXray(tag, logf)
			if err != nil {
				return fmt.Errorf("local update: %w", err)
			}
			fmt.Println("local xray:", XrayVersion(p), "->", p)
		} else {
			fmt.Println("local xray is current:", cur)
		}
	}
	if r == nil {
		return nil
	}
	st, err := r.Status()
	if err != nil {
		return err
	}
	if server {
		if st.Xray == "none" && !st.HasState {
			fmt.Println("server: xtune's Xray is not installed (it is installed on first tune)")
		} else if newer(tag, st.Xray) {
			out, err := r.Helper("install", tag)
			if err != nil {
				return fmt.Errorf("server update: %w", err)
			}
			fmt.Println("server xray:", out)
		} else {
			fmt.Println("server xray is current:", st.Xray)
		}
	}
	if panel && st.XUI {
		if !newer(tag, st.XUIXray) {
			fmt.Println("3x-ui xray is current:", st.XUIXray)
			return nil
		}
		p, err := OpenPanel(r, st, ask)
		if err != nil {
			return err
		}
		defer p.Close()
		fmt.Println("3x-ui: installing", tag, "(Xray restarts; connections drop briefly)")
		if err := p.InstallXray(tag); err != nil {
			return err
		}
		time.Sleep(3 * time.Second)
		fmt.Println("3x-ui xray:", p.XrayVersion())
	}
	return nil
}

// ---------------------------------------------------------------- users

type backend struct {
	r     *Remote
	st    ServerStatus
	panel *Panel
	kind  string // standalone | panel
}

func pickBackend(r *Remote, want string, ask Prompter) (*backend, error) {
	st, err := r.Status()
	if err != nil {
		return nil, err
	}
	b := &backend{r: r, st: st, kind: want}
	if b.kind == "" || b.kind == "auto" {
		switch {
		case st.HasState && st.XUI:
			return nil, fmt.Errorf("both a standalone deployment and 3x-ui exist; pass --backend standalone|panel")
		case st.HasState:
			b.kind = "standalone"
		case st.XUI:
			b.kind = "panel"
		default:
			return nil, fmt.Errorf("no users to manage: deploy with `xtune tune` or install 3x-ui")
		}
	}
	if b.kind == "panel" {
		if b.panel, err = OpenPanel(r, st, ask); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func cmdUsers(args []string) error {
	fs := flag.NewFlagSet("users", flag.ExitOnError)
	c := addConnFlags(fs)
	be := fs.String("backend", "auto", "standalone | panel | auto")
	quota := fs.Float64("quota", 0, "traffic quota in GB (panel, 0 = unlimited)")
	days := fs.Int("days", 0, "expire after N days (panel, 0 = never)")
	inbounds := fs.String("inbounds", "", "panel inbound IDs for add, comma-separated (default: all enabled)")
	pos := parse(fs, args)
	if len(pos) == 0 {
		pos = []string{"list"}
	}
	r, err := connect(c)
	if err != nil {
		return err
	}
	defer r.Close()
	b, err := pickBackend(r, *be, cliPrompt)
	if err != nil {
		return err
	}
	if b.panel != nil {
		defer b.panel.Close()
	}
	need := func(n int) error {
		if len(pos) < n+1 {
			return fmt.Errorf("users %s: missing argument", pos[0])
		}
		return nil
	}
	switch pos[0] {
	case "list", "ls":
		return b.list(os.Stdout)
	case "add":
		if err := need(1); err != nil {
			return err
		}
		var ids []int
		for _, f := range strings.Split(*inbounds, ",") {
			if id, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
				ids = append(ids, id)
			}
		}
		if err := b.add(pos[1], *quota, *days, ids); err != nil {
			return err
		}
		return b.links(os.Stdout, pos[1])
	case "del", "delete", "rm":
		if err := need(1); err != nil {
			return err
		}
		return b.del(pos[1])
	case "enable", "disable":
		if err := need(1); err != nil {
			return err
		}
		return b.setEnabled(pos[1], pos[0] == "enable")
	case "links", "show":
		if err := need(1); err != nil {
			return err
		}
		return b.links(os.Stdout, pos[1])
	case "rotate":
		if err := need(1); err != nil {
			return err
		}
		return b.rotate(os.Stdout, pos[1])
	case "quota":
		if err := need(2); err != nil {
			return err
		}
		gb, err := strconv.ParseFloat(pos[2], 64)
		if err != nil {
			return err
		}
		return b.setQuota(pos[1], gb)
	case "expiry":
		if err := need(2); err != nil {
			return err
		}
		d, err := strconv.Atoi(pos[2])
		if err != nil {
			return err
		}
		return b.setExpiry(pos[1], d)
	}
	return fmt.Errorf("unknown users action %q", pos[0])
}

func (b *backend) list(w *os.File) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer tw.Flush()
	if b.kind == "standalone" {
		st, err := requireState(b.r)
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "USER\tENABLED\tCREATED\tCONFIGS\n")
		for _, u := range st.Users {
			fmt.Fprintf(tw, "%s\t%v\t%s\t%d\n", u.Name, u.Enabled, time.Unix(u.Created, 0).Format("2006-01-02"), len(st.Entries))
		}
		return nil
	}
	us, err := b.panel.Users()
	if err != nil {
		return err
	}
	fmt.Fprintf(tw, "USER\tENABLED\tUSED\tQUOTA\tEXPIRES\tINBOUNDS\tLAST ONLINE\n")
	for _, u := range us {
		quota := "unlimited"
		if u.TotalGB > 0 {
			quota = humanBytes(u.TotalGB)
		}
		last := "never"
		if u.Traffic.LastOnline > 0 {
			last = time.UnixMilli(u.Traffic.LastOnline).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%v\t%s\t%s\t%s\t%d\t%s\n", u.Email, u.Enable, humanBytes(u.Traffic.Up+u.Traffic.Down),
			quota, expiryStr(u.ExpiryTime), len(u.InboundIDs), last)
	}
	return nil
}

func (b *backend) add(name string, quotaGB float64, days int, inboundIDs []int) error {
	if name == "" || strings.ContainsAny(name, " /\\\"'") {
		return fmt.Errorf("invalid user name %q", name)
	}
	if b.kind == "standalone" {
		_, _, err := StandaloneAdd(b.r, name)
		return err
	}
	return b.panel.Add(name, quotaGB, days, inboundIDs)
}

func (b *backend) del(name string) error {
	if b.kind == "standalone" {
		return StandaloneDelete(b.r, name)
	}
	return b.panel.Delete(name)
}

func (b *backend) setEnabled(name string, on bool) error {
	if b.kind == "standalone" {
		return StandaloneSetEnabled(b.r, name, on)
	}
	return b.panel.Update(name, func(c J) { c["enable"] = on })
}

func (b *backend) setQuota(name string, gb float64) error {
	if b.kind == "standalone" {
		return errors.New("quotas need traffic accounting - only the 3x-ui backend supports them")
	}
	return b.panel.Update(name, func(c J) { c["totalGB"] = int64(gb * (1 << 30)) })
}

func (b *backend) setExpiry(name string, days int) error {
	if b.kind == "standalone" {
		return errors.New("expiry needs enforcement - only the 3x-ui backend supports it")
	}
	return b.panel.Update(name, func(c J) { c["expiryTime"] = expiryMs(days) })
}

func (b *backend) rotate(w *os.File, name string) error {
	if b.kind != "standalone" {
		return errors.New("rotate is for the standalone backend (in 3x-ui: delete and re-add)")
	}
	if _, _, err := StandaloneRotate(b.r, name); err != nil {
		return err
	}
	return b.links(w, name)
}

func (b *backend) links(w *os.File, name string) error {
	if b.kind == "standalone" {
		st, err := requireState(b.r)
		if err != nil {
			return err
		}
		_, u := st.User(name)
		if u == nil {
			return fmt.Errorf("no user %q", name)
		}
		fmt.Fprintf(w, "# %s (%d configs, base64 subscription body below the links)\n", u.Name, len(st.Entries))
		for _, l := range st.Links(*u) {
			fmt.Fprintln(w, l)
		}
		fmt.Fprintf(w, "\n%s\n", st.Subscription(*u))
		return nil
	}
	us, err := b.panel.Users()
	if err != nil {
		return err
	}
	for _, u := range us {
		if u.Email != name {
			continue
		}
		if sub := b.panel.SubURL(u.SubID); sub != "" {
			fmt.Fprintf(w, "subscription: %s\n\n", sub)
		}
		links, err := b.panel.Links(name)
		if err != nil {
			return err
		}
		for _, l := range links {
			fmt.Fprintln(w, l)
		}
		return nil
	}
	return fmt.Errorf("no user %q", name)
}

// ---------------------------------------------------------------- logs / uninstall

func defaultUnit(st ServerStatus) string {
	if st.Unit == "active" || !st.XUI {
		return "xray-autotune"
	}
	return "x-ui"
}

func logCmd(unit string, n int, follow bool) string {
	c := fmt.Sprintf("journalctl -u %s -n %d --no-pager -o short-iso", shq(unit), n)
	if follow {
		c += " -f"
	}
	return c
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	c := addConnFlags(fs)
	unit := fs.String("unit", "", "systemd unit (default: xray-autotune, else x-ui)")
	n := fs.Int("n", 100, "lines")
	follow := fs.Bool("f", false, "follow")
	parse(fs, args)
	r, err := connect(c)
	if err != nil {
		return err
	}
	defer r.Close()
	if *unit == "" {
		st, err := r.Status()
		if err != nil {
			return err
		}
		*unit = defaultUnit(st)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return r.Stream(ctx, logCmd(*unit, *n, *follow), func(l string) { fmt.Println(l) })
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	c := addConnFlags(fs)
	parse(fs, args)
	r, err := connect(c)
	if err != nil {
		return err
	}
	defer r.Close()
	out, err := r.Helper("uninstall")
	fmt.Println(out)
	return err
}

// ---------------------------------------------------------------- profiles

func profilesPath() string { return filepath.Join(dataDir(), "profiles.json") }

func loadProfiles() []Conn {
	var ps []Conn
	if b, err := os.ReadFile(profilesPath()); err == nil {
		json.Unmarshal(b, &ps)
	}
	return ps
}

func findProfile(name string) (Conn, bool) {
	for _, p := range loadProfiles() {
		if p.Name == name || p.Host == name {
			return p, true
		}
	}
	return Conn{}, false
}

// saveProfile stores connection details (never passwords).
func saveProfile(c Conn) error {
	if c.Name == "" {
		c.Name = c.Host
	}
	ps := loadProfiles()
	replaced := false
	for i := range ps {
		if ps[i].Name == c.Name {
			ps[i], replaced = c, true
		}
	}
	if !replaced {
		ps = append(ps, c)
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	return writeJSON(profilesPath(), ps)
}
