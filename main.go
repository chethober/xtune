package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
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

On your computer (client):
  xtune                                    interactive TUI: connect, tune, deploy, open the server console
  xtune tune      [conn] [tune flags]      find the configs that get through from this network, deploy them
                  --domain D --cdn C       direct (DNS-only) and CDN-proxied domains for the links
                  --network NAME           name of this network (ISP), for the history and merged deploys
                  --protocols LIST         also test ssh, dnstt, awg (AmneziaWG), worker (Cloudflare Worker relay),
                                           openvpn, ikev2 (vpn = both VPNs, all = every one)
                  --zone Z                 a Cloudflare zone: domains not given get random names in it, with
                                           their DNS records (token: XTUNE_CLOUDFLARE_TOKEN, or the server's)
                  --dns-domain Z           zone delegated to the server (NS record): the DNS tunnel
                  --resolvers LIST         DNS tunnel: also try these (udp:IP:53, doh:URL, dot:HOST:853)
                  --share FILE             keep the test running 2 hours and write FILE for testers elsewhere
  xtune measure   FILE [--network NAME] [--report OUT]   measure a shared test from this network (no SSH)
  xtune deploy    [conn] [-n N] [REPORT ...]   deploy the shared test, so every network gets a config that works
  xtune check     [conn] [--network NAME]  quick check of the deployed configs from this network
  xtune history   [--host H] [-n N]        past tunes and checks
  xtune doctor    [conn]                   diagnose services, ports, DNS, the CDN, clocks and certificates
  xtune backup    [conn] [-o FILE] [--encrypt]   save the deployment (keys, users, usage) to a local file
  xtune restore   [conn] FILE              rebuild a backup on a new server, configs as they were
  xtune migrate   [conn | --backup FILE] --to [user@]host[:port] [--no-tune] [tune flags]
                                           move the deployment to a new server, keeping users and links
  xtune console   [conn]                   open the server's TUI (users, addresses) over SSH
  xtune xray      [conn] status|update     check / update Xray here and on the server
  xtune logs      [conn] [-n N] [-f]
  xtune uninstall [conn]                   remove everything xtune installed on the server
  xtune self-update [--version vX.Y.Z]     update this xtune (on the server: its copy)
  xtune version

On the server (as root):
  sudo xtune                               server TUI: users, subscriptions, addresses, Xray, logs
  xtune users     list [--json]
  xtune users     add NAME [--quota GB] [--days N] [--devices N] [--reset monthly|weekly]
  xtune users     limit NAME [--quota GB] [--days N] [--devices N] [--reset monthly|weekly|none]
                                           (0 = unlimited / never; --reset starts usage from zero each period)
  xtune users     links NAME [--json] [--qr]
  xtune users     profiles NAME [--out DIR]   files for apps without share links (OpenVPN, IKEv2, WireGuard, DNS tunnel)
  xtune users     add|limit NAME --plan P  a plan's limits (flags given override them)
  xtune users     renew NAME [--plan P] [--days N]   extend expiry, usage from zero, enable
  xtune users     usage NAME               traffic per day, the last 30 days
  xtune users     enable|disable|rotate|reset-usage|reset-sub|del NAME
  xtune plans     [list | add NAME --quota GB --days N [--devices N] [--reset monthly] | del NAME]
  xtune public    [list | add NAME [--quota GB] [--days N] [--devices N] [--plan P] [--members N]]
  xtune public    limit|link|token|pause|resume|del NAME   pools of users shared through the community VPN bot
  xtune sub       [status | enable [--mode cdn|direct] [--port P] | disable | lang en|fa|auto]
  xtune domains   [--direct D] [--cdn C]   show or change the addresses used in links
  xtune country   [ir|cn|ru|none]          sites of this country go direct in client configs
  xtune notify    [status | telegram --chat ID | webhook [URL] | test | off]   admin alerts
  xtune warp      [status | on [--domains D,D] [--all] | off]   chosen sites (default: AI services) via WARP
  xtune manage    [status | link | token | off]   a link to manage users and plans from @chthvpnbot
  xtune manage    reseller [list | add|limit NAME [--users N] [--quota GB] | link|token|del NAME]
  xtune peers     [status | enable | remove NAME | disable]   let other servers join this one's subscriptions
  xtune follow    [PEER_URL [--name N] | off]   take users from a primary, add this server's configs to it
  xtune cloudflare [status | token | dns [--ip IP] | ssl | off]   DNS records and SSL mode via the API
  xtune cloudflare worker [status | sync | off]   the Worker relay's routes; off deletes it and its configs
  xtune sni       [check | refresh]        REALITY targets: re-probe, replace failing or slow ones
  xtune xray|logs|doctor|backup|uninstall|self-update   as above, for this server
  (internal, run by systemd: xtune enforce, xtune serve, xtune tunnels run, xtune awg run, xtune vpn-auth)

Connection flags (conn):
  --host H --port 22 --user root --key PATH --password-env VAR --profile NAME
  sudo password: prompted, or XTUNE_SUDO_PASSWORD
`

func main() {
	if len(os.Args) < 2 {
		if serverMode() {
			runServerTUI()
		} else {
			runTUI()
		}
		return
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "tui", "client":
		runTUI()
	case "server":
		runServerTUI()
	case "tune":
		err = cmdTune(args)
	case "console":
		err = cmdConsole(args)
	case "xray":
		err = cmdXray(args)
	case "users":
		err = cmdUsers(args)
	case "domains":
		err = cmdDomains(args)
	case "sub":
		err = cmdSub(args)
	case "country":
		err = cmdCountry(args)
	case "notify":
		err = cmdNotify(args)
	case "warp":
		err = cmdWarp(args)
	case "bot":
		err = cmdBot(args)
	case "peers":
		err = cmdPeers(args)
	case "follow":
		err = cmdFollow(args)
	case "plans":
		err = cmdPlans(args)
	case "public":
		err = cmdPublic(args)
	case "manage":
		err = cmdManage(args)
	case "cloudflare", "cf":
		err = cmdCloudflare(args)
	case "sni":
		err = cmdSNI(args)
	case "check":
		err = cmdCheck(args)
	case "history":
		err = cmdHistory(args)
	case "measure":
		err = cmdMeasure(args)
	case "deploy":
		err = cmdDeploy(args)
	case "doctor":
		err = cmdDoctor(args)
	case "backup":
		err = cmdBackup(args)
	case "restore":
		err = cmdRestore(args)
	case "migrate":
		err = cmdMigrate(args)
	case "self-update":
		err = cmdSelfUpdate(args)
	case "enforce":
		var r *Server
		if r, err = OpenLocal(); err == nil {
			err = Enforce(r)
		}
	case "serve":
		err = Serve()
	case "tunnels":
		err = cmdTunnels(args)
	case "awg":
		err = cmdAWG(args)
	case "vpn-auth":
		err = cmdVPNAuth(args)
	case "vpn-mgmt":
		err = cmdVPNMgmt(args)
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

func connect(c connFlags) (*Server, error) {
	cn, err := c.conn()
	if err != nil {
		return nil, err
	}
	return Dial(cn, cliPrompt)
}

// target connects to --host/--profile, or manages this machine when xtune runs on the server.
func target(c connFlags) (*Server, error) {
	if *c.host == "" && *c.profile == "" && serverMode() {
		return OpenLocal()
	}
	return connect(c)
}

func flagsSet(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
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
// instance (when r is set) before exiting, so an interrupted tune never leaves ports open.
func guarded(r *Server, fn func() error) error {
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
		if r != nil {
			if _, err := r.Helper("stop-test"); err != nil {
				fmt.Fprintln(os.Stderr, "server cleanup failed:", err)
			}
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
	fs.StringVar(&o.Domain, "domain", "", "direct (DNS-only) domain used in links instead of the IP (default: the deployed one)")
	fs.StringVar(&o.CDNDomain, "cdn", "", "CDN-proxied domain: adds a VLESS+XHTTP+TLS entry through the CDN (default: the deployed one)")
	fs.StringVar(&o.Network, "network", "", "name of this network (ISP), for the history and merged deploys")
	fs.StringVar(&o.DNSDomain, "dns-domain", "", "zone delegated to the server with an NS record: adds a DNS tunnel (default: the deployed one)")
	resolvers := fs.String("resolvers", "", "DNS tunnel: also try these resolvers, e.g. udp:10.0.0.1:53,doh:https://dns.example/dns-query")
	protocols := fs.String("protocols", "", "also test: ssh, dnstt, awg, worker, openvpn, ikev2, vpn (= openvpn,ikev2) or all (default: what is deployed)")
	fs.StringVar(&o.Zone, "zone", "", "Cloudflare zone: the direct and CDN domains (and the DNS tunnel's) not given get random names in it")
	only := fs.String("only", "", "test only these entries, e.g. hy2,r-tcp,venc,cdn (prefix match)")
	deploy := fs.Int("deploy", -1, "keep the top N configs running (0 = none; asks when omitted)")
	share := fs.String("share", "", "keep the test running and write this bundle for testers on other networks")
	parse(fs, args)
	o.KeepTest = *share != ""
	if *only != "" {
		o.Only = strings.Split(*only, ",")
	}
	var err error
	if o.Protocols, err = parseProtocols(*protocols); err != nil {
		return err
	}
	if o.Resolvers, err = parseResolvers(*resolvers); err != nil {
		return err
	}
	r, err := connect(c)
	if err != nil {
		return err
	}
	defer r.Close()
	old, _ := LoadState(r)
	o.inherit(old, flagsSet(fs))
	o.Cloudflare = os.Getenv("XTUNE_CLOUDFLARE_TOKEN")
	if o.Cloudflare == "" && (old == nil || old.Cloudflare.Token == "") && (o.Zone != "" || slices.Contains(o.Protocols, "worker")) {
		if o.Cloudflare, err = cliPrompt(cfTokenPrompt, true); err != nil {
			return err
		}
	}
	o.XrayBin, err = EnsureLocalXray(*c.xray, r, func(s string) { fmt.Println(s) })
	if err != nil {
		return err
	}
	var res *TuneResult
	if err := guarded(r, func() error { var e error; res, e = Tune(r, o); return e }); err != nil {
		r.Helper("cleanup")
		return err
	}
	if err := appendHistory(tuneRecord(r.Conn.Host, o.Network, res.Best)); err != nil {
		fmt.Println("[!] history:", err)
	}
	if *share != "" {
		return shareTune(r, res, o, *share)
	}
	n := *deploy
	if n < 0 {
		s, _ := cliPrompt("Keep the top N configs running on the server? N (0 = none) [0]", false)
		n, _ = strconv.Atoi(s)
	}
	if n <= 0 {
		out, _ := r.Helper("cleanup")
		fmt.Println("[*] server cleanup:", out)
		return nil
	}
	st, err := Deploy(r, res, n)
	if err != nil {
		return err
	}
	afterDeploy(r, st)
	return nil
}

// afterDeploy shows the deployment and installs xtune on the server, where users are managed.
func afterDeploy(r *Server, st *State) {
	printDeployment(st)
	if err := EnsureServerXtune(r, func(s string) { fmt.Println("[*] " + s) }); err != nil {
		fmt.Println("[!] xtune could not be installed on the server, so users cannot be managed there yet:", err)
	} else if len(st.Users) == 0 {
		fmt.Printf("\nNo users yet. Add them on the server: xtune console --host %s  (or ssh in and run sudo xtune)\n", r.Conn.Host)
	}
}

func printDeployment(st *State) {
	fmt.Printf("\n[+] deployed %d configs (systemd unit xray-autotune), address %s", len(st.Entries), st.Addr())
	if st.CDNDomain != "" {
		fmt.Printf(", CDN %s", st.CDNDomain)
	}
	fmt.Println()
	for _, u := range st.Users {
		if !u.Enabled {
			continue
		}
		if sub := st.SubURL(u); sub != "" {
			fmt.Printf("  %-16s %s  (refresh to get the new configs)\n", u.Name, sub)
			continue
		}
		fmt.Printf("\n# %s\n", u.Name)
		for _, l := range st.Links(u) {
			fmt.Println(l)
		}
	}
}

func cmdConsole(args []string) error {
	fs := flag.NewFlagSet("console", flag.ExitOnError)
	c := addConnFlags(fs)
	parse(fs, args)
	r, err := connect(c)
	if err != nil {
		return err
	}
	defer r.Close()
	return openConsole(r)
}

// openConsole makes sure the server has this xtune, then runs its server TUI over SSH.
func openConsole(r *Server) error {
	if err := EnsureServerXtune(r, func(s string) { fmt.Println(s) }); err != nil {
		return err
	}
	return r.Console(remoteDir + "/xtune server")
}

// ---------------------------------------------------------------- xray

func cmdXray(args []string) error {
	fs := flag.NewFlagSet("xray", flag.ExitOnError)
	c := addConnFlags(fs)
	ver := fs.String("version", "", "tag to install (default: newest)")
	stable := fs.Bool("stable", false, "newest non-prerelease instead of newest")
	local := fs.Bool("local", false, "only the local binary")
	server := fs.Bool("server", false, "only xtune's server binary")
	pos := parse(fs, args)
	action := "status"
	if len(pos) > 0 {
		action = pos[0]
	}
	var r *Server
	if *c.host != "" || *c.profile != "" || serverMode() {
		var err error
		if r, err = target(c); err != nil {
			return err
		}
		defer r.Close()
	}
	onServer := r != nil && r.Local
	all := !*local && !*server
	switch action {
	case "status":
		return xrayStatus(os.Stdout, r, *c.xray, *stable, !onServer)
	case "update":
		tag := *ver
		if tag == "" {
			var err error
			if tag, err = LatestXray(*stable, r); err != nil {
				return err
			}
		}
		return xrayUpdate(r, *c.xray, normTag(tag), (all || *local) && !onServer, (all || *server) && r != nil)
	}
	return fmt.Errorf("unknown xray action %q", action)
}

func xrayStatus(w io.Writer, r *Server, xrayFlag string, stable, local bool) error {
	latest, lerr := LatestXray(stable, r)
	if lerr != nil {
		latest = "unknown"
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "WHERE\tVERSION\tLATEST\tSTATE\n")
	mark := func(v string) string {
		if v != "none" && v != "" && newer(latest, v) {
			return latest + " (update available)"
		}
		return latest
	}
	if local {
		lb := FindLocalXray(xrayFlag)
		lv := XrayVersion(lb)
		fmt.Fprintf(tw, "local\t%s\t%s\t%s\n", lv, mark(lv), orStr(lb, "not installed"))
	}
	if r != nil {
		st, err := r.Status()
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "server\t%s\t%s\tunit %s\n", st.Xray, mark(st.Xray), orStr(st.Unit, "-"))
	}
	return tw.Flush()
}

func xrayUpdate(r *Server, xrayFlag, tag string, local, server bool) error {
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
	if r == nil || !server {
		return nil
	}
	st, err := r.Status()
	if err != nil {
		return err
	}
	switch {
	case st.Xray == "none" && !st.HasState:
		fmt.Println("server: xtune's Xray is not installed (it is installed on first tune)")
	case newer(tag, st.Xray):
		out, err := r.Helper("install", tag)
		if err != nil {
			return fmt.Errorf("server update: %w", err)
		}
		fmt.Println("server xray:", out)
	default:
		fmt.Println("server xray is current:", st.Xray)
	}
	return nil
}

// ---------------------------------------------------------------- users and addresses (server only)

func serverOnly(cmd string, args []string) error {
	for _, a := range args {
		n := strings.TrimLeft(a, "-")
		if strings.HasPrefix(a, "-") && (strings.HasPrefix(n, "host") || strings.HasPrefix(n, "profile")) {
			return fmt.Errorf("%s are managed on the server only: run `xtune console --host H`, "+
				"or ssh in and run `sudo xtune %s`", cmd, cmd)
		}
	}
	return nil
}

func cmdUsers(args []string) error {
	if err := serverOnly("users", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("users", flag.ExitOnError)
	quota := fs.Float64("quota", 0, "traffic quota in GB (0 = unlimited)")
	days := fs.Int("days", 0, "expire N days from now (0 = never)")
	devices := fs.Int("devices", 0, "simultaneous devices (client IPs), 0 = unlimited")
	reset := fs.String("reset", "", "start usage from zero every period: monthly, weekly or none")
	plan := fs.String("plan", "", "add, limit, renew: a plan's limits (xtune plans); flags given override them")
	asJSON := fs.Bool("json", false, "machine-readable output (list, links)")
	asQR := fs.Bool("qr", false, "links: show QR codes in the terminal")
	outDir := fs.String("out", ".", "profiles: directory to write the files to")
	pos := parse(fs, args)
	if len(pos) == 0 {
		pos = []string{"list"}
	}
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	if len(pos) < 2 && pos[0] != "list" && pos[0] != "ls" {
		return fmt.Errorf("users %s: missing user name", pos[0])
	}
	var l Limits
	set := flagsSet(fs)
	if set["quota"] {
		l.QuotaGB = quota
	}
	if set["days"] {
		l.Days = days
	}
	if set["devices"] {
		l.Devices = devices
	}
	if set["reset"] {
		p := strings.TrimSpace(*reset)
		if p == "none" {
			p = ""
		}
		l.Reset = &p
	}
	if set["plan"] {
		l.Plan = plan
	}
	show := func(st *State, err error) error {
		switch {
		case err != nil:
			return err
		case *asJSON:
			return printLinksJSON(os.Stdout, st, pos[1])
		case *asQR:
			return printLinksQR(os.Stdout, st, pos[1])
		}
		return printLinks(os.Stdout, st, pos[1])
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
		if *asJSON {
			return printUsersJSON(os.Stdout, st, us)
		}
		return printUsers(os.Stdout, st, us)
	case "add":
		st, _, err := AddUser(r, pos[1], l)
		return show(st, err)
	case "limit":
		if l == (Limits{}) {
			return fmt.Errorf("users limit: pass --quota, --days, --devices and/or --reset")
		}
		_, err := SetLimits(r, pos[1], l)
		return err
	case "del", "delete", "rm":
		return DeleteUser(r, pos[1])
	case "enable", "disable":
		return SetUserEnabled(r, pos[1], pos[0] == "enable")
	case "links", "show":
		return show(requireState(r))
	case "rotate":
		st, _, err := RotateUser(r, pos[1])
		return show(st, err)
	case "reset-usage":
		return ResetUsage(r, pos[1])
	case "renew":
		d := 0
		if set["days"] {
			d = *days
		}
		_, u, err := Renew(r, pos[1], *plan, d)
		if err != nil {
			return err
		}
		fmt.Printf("%s renewed: expires %s, usage from zero\n", u.Name, fmtExpiry(u.Expires, time.Now()))
		return nil
	case "usage":
		return printUsageHistory(os.Stdout, r, pos[1])
	case "profiles":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		return writeProfiles(os.Stdout, st, pos[1], *outDir)
	case "reset-sub":
		return show(ResetSubLink(r, pos[1]))
	}
	return fmt.Errorf("unknown users action %q", pos[0])
}

func printUsers(w io.Writer, st *State, us *Usage) error {
	if len(st.Users) == 0 {
		fmt.Fprintln(w, "no users yet")
		return nil
	}
	now := time.Now()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "USER\tSTATUS\tUSED\tQUOTA\tRESETS\tEXPIRES\tDEVICES\tLAST SEEN\n")
	for _, u := range st.Users {
		uu := us.Users[u.Name]
		if uu == nil {
			uu = &UserUsage{}
		}
		quota, devices, last := "-", fmt.Sprintf("%d/-", len(uu.Online)), "never"
		if u.Quota > 0 {
			quota = fmtBytes(u.Quota)
		}
		if u.Devices > 0 {
			devices = fmt.Sprintf("%d/%d", len(uu.Online), u.Devices)
		}
		if len(uu.Online) > 0 {
			last = "online"
		} else if uu.LastSeen > 0 {
			last = time.Unix(uu.LastSeen, 0).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", u.Name, userStatus(u, uu, now), fmtBytes(uu.Up+uu.Down),
			quota, fmtReset(u), fmtExpiry(u.Expires, now), devices, last)
	}
	return tw.Flush()
}

func printLinks(w io.Writer, st *State, name string) error {
	_, u := st.User(name)
	if u == nil {
		return fmt.Errorf("no user %q", name)
	}
	state := ""
	if !u.Enabled {
		state = ", DISABLED"
	}
	if sub := st.SubURL(*u); sub != "" {
		fmt.Fprintf(w, "# %s%s\nsubscription (page, links, usage):  %s\nfailover config (Xray JSON):        %s/json\n\n", u.Name, state, sub, sub)
	} else {
		fmt.Fprintf(w, "# %s (%d configs%s, base64 subscription body below the links)\n", u.Name, len(st.Entries), state)
	}
	for _, l := range st.Links(*u) {
		fmt.Fprintln(w, l)
	}
	if st.SubURL(*u) == "" {
		fmt.Fprintf(w, "\n%s\n", st.Subscription(*u))
	}
	if ps := st.Profiles(*u); len(ps) > 0 {
		fmt.Fprintf(w, "\nFiles for apps without share links (xtune users profiles %s writes them):\n", u.Name)
		for _, p := range ps {
			where := p.File
			if sub := st.SubURL(*u); sub != "" {
				where = sub + "/f/" + url.PathEscape(p.File)
			}
			fmt.Fprintf(w, "  %-34s %s\n", p.Name, where)
		}
		if st.hasLogin() {
			fmt.Fprintf(w, "SSH / VPN login: user name %s, password %s\n", u.Name, tunnelPassword(*u))
		}
	}
	return nil
}

// writeProfiles writes the user's profile files into dir.
func writeProfiles(w io.Writer, st *State, name, dir string) error {
	_, u := st.User(name)
	if u == nil {
		return fmt.Errorf("no user %q", name)
	}
	ps := st.Profiles(*u)
	if len(ps) == 0 {
		return fmt.Errorf("the deployment has no configs that need profile files")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, p := range ps {
		path := filepath.Join(dir, p.File)
		if err := os.WriteFile(path, []byte(p.Body), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(w, "%-34s %s\n", p.Name, path)
	}
	return nil
}

func cmdSub(args []string) error {
	if err := serverOnly("sub", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("sub", flag.ExitOnError)
	mode := fs.String("mode", "", "cdn (through the CDN domain) or direct (Let's Encrypt on the direct domain)")
	port := fs.Int("port", 0, "HTTPS port (default: a free CDN-compatible port)")
	pos := parse(fs, args)
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	action := "status"
	if len(pos) > 0 {
		action = pos[0]
	}
	switch action {
	case "status":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		return printSub(os.Stdout, st)
	case "enable":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		m := *mode
		if m == "" {
			m = defaultSubMode(st)
		}
		if st, err = EnableSub(r, m, *port); err != nil {
			return err
		}
		return printSub(os.Stdout, st)
	case "disable":
		return DisableSub(r)
	case "lang":
		if len(pos) < 2 {
			return fmt.Errorf("sub lang: en, fa or auto (from the browser)")
		}
		l := strings.ToLower(pos[1])
		if l == "auto" {
			l = ""
		}
		return SetSubLang(r, l)
	}
	return fmt.Errorf("unknown sub action %q", action)
}

// defaultSubMode prefers the CDN: it hides the server's IP and keeps working when the IP is blocked.
func defaultSubMode(st *State) string {
	if st.CDNDomain == "" && st.Domain != "" {
		return "direct"
	}
	return "cdn"
}

func printSub(w io.Writer, st *State) error {
	if st.Sub.Mode == "" {
		fmt.Fprintln(w, "subscription server: off (xtune sub enable)")
		return nil
	}
	fmt.Fprintf(w, "subscription server: %s mode, https://%s, page language %s\n\n", st.Sub.Mode,
		hostPort(subHost(st), st.Sub.Port), orStr(st.Sub.Lang, "from the browser"))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, u := range st.Users {
		fmt.Fprintf(tw, "%s\t%s\n", u.Name, st.SubURL(u))
	}
	return tw.Flush()
}

func cmdCountry(args []string) error {
	if err := serverOnly("country", args); err != nil {
		return err
	}
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		st, err := requireState(r)
		if err != nil {
			return err
		}
		fmt.Println("direct country in client configs:", orStr(st.Country, "none"), " (supported:", countryList()+")")
		return nil
	}
	code := strings.ToLower(args[0])
	if code == "none" {
		code = ""
	}
	return SetCountry(r, code)
}

func cmdDomains(args []string) error {
	if err := serverOnly("domains", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("domains", flag.ExitOnError)
	direct := fs.String("direct", "", "direct (DNS-only) domain; --direct= clears it")
	cdn := fs.String("cdn", "", "CDN-proxied domain; --cdn= clears it and removes the CDN entry")
	dnsZone := fs.String("dns", "", "zone delegated to the server for the DNS tunnel; --dns= clears it")
	parse(fs, args)
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	set := flagsSet(fs)
	if set["dns"] {
		if st, err = SetDNSDomain(r, strings.TrimSpace(*dnsZone), func(s string) { fmt.Println(s) }); err != nil {
			return err
		}
	}
	if !set["direct"] && !set["cdn"] {
		printAddresses(os.Stdout, st)
		return nil
	}
	d, c := st.Domain, st.CDNDomain
	if set["direct"] {
		d = strings.TrimSpace(*direct)
	}
	if set["cdn"] {
		c = strings.TrimSpace(*cdn)
	}
	if st.Cloudflare.Token != "" && (d != "" || c != "") {
		ip, err := serverIP(st.Host)
		if err != nil {
			return err
		}
		next := *st
		next.Domain, next.CDNDomain = d, c
		if err := CloudflarePoint(&next, ip, func(s string) { fmt.Println("DNS " + s) }); err != nil {
			return err
		}
	}
	st, added, err := SetDomains(r, d, c, func(s string) { fmt.Println(s) })
	if err != nil {
		return err
	}
	printAddresses(os.Stdout, st)
	if added {
		fmt.Println("added a CDN entry with default settings; re-run a tune from your users' network to measure it")
	}
	fmt.Println("links changed: send users their new links or have them refresh their subscription")
	return nil
}

func printAddresses(w io.Writer, st *State) {
	fmt.Fprintf(w, "server IP / host: %s\n", st.Host)
	fmt.Fprintf(w, "direct domain:    %s\n", orStr(st.Domain, "-  (links use the IP)"))
	fmt.Fprintf(w, "CDN domain:       %s\n", orStr(st.CDNDomain, "-"))
	for _, e := range st.Entries {
		if e.CDN() {
			fmt.Fprintf(w, "CDN entry:        %s port %d\n", e.ID, e.Port)
		}
	}
	if st.CDNDomain != "" && !hasCDN(st.Entries) {
		fmt.Fprintln(w, "CDN entry:        none deployed (save the CDN domain again to add one)")
	}
	if st.DNSDomain != "" {
		fmt.Fprintf(w, "DNS tunnel zone:  %s (NS record pointing at %s, an A record for the server)\n", st.DNSDomain, nsHost(st.DNSDomain))
	}
}

// ---------------------------------------------------------------- logs / uninstall

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
	unit := fs.String("unit", "xray-autotune", "systemd unit (xray-autotune or xray-autotune-test)")
	n := fs.Int("n", 100, "lines")
	follow := fs.Bool("f", false, "follow")
	parse(fs, args)
	r, err := target(c)
	if err != nil {
		return err
	}
	defer r.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return r.Stream(ctx, logCmd(*unit, *n, *follow), func(l string) { fmt.Println(l) })
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	c := addConnFlags(fs)
	parse(fs, args)
	r, err := target(c)
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
