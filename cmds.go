package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------- several networks

func shareTune(r *Server, res *TuneResult, o TuneOpts, path string) error {
	b, err := Share(r, res, o.Depth, o.Network)
	if err != nil {
		return err
	}
	if err := writeJSON(path, b); err != nil {
		return err
	}
	until := time.Unix(b.Expires, 0).Format("15:04")
	fmt.Printf("\n[+] the test stays up until %s. Send %s to testers on other networks; each runs:\n", until, path)
	fmt.Printf("      xtune measure %s --network NAME\n", filepath.Base(path))
	fmt.Println("    and sends back the report it writes (no SSH access needed; the bundle only works on the test).")
	fmt.Printf("    Then deploy what works everywhere:  xtune deploy --host %s -n 3 REPORT ...\n", r.Conn.Host)
	return nil
}

func cmdMeasure(args []string) error {
	fs := flag.NewFlagSet("measure", flag.ExitOnError)
	xray := fs.String("xray", "", "local xray binary")
	o := TuneOpts{}
	fs.StringVar(&o.Network, "network", "", "name of this network (ISP), shown in the merged deploy")
	fs.IntVar(&o.Probes, "probes", 5, "screening requests per variant")
	fs.IntVar(&o.Parallel, "parallel", 6, "parallel screening")
	fs.IntVar(&o.Rounds, "rounds", 2, "benchmark rounds (median)")
	fs.StringVar(&o.Out, "out", "xtune-results", "results directory")
	report := fs.String("report", "", "report file (default xtune-report-NETWORK.json)")
	pos := parse(fs, args)
	if len(pos) != 1 {
		return errors.New("measure: pass the bundle file")
	}
	var b Bundle
	if err := readJSONFile(pos[0], &b); err != nil {
		return err
	}
	if b.State == nil || len(b.Entries) == 0 {
		return fmt.Errorf("%s is not an xtune test bundle", pos[0])
	}
	if o.Network == "" {
		o.Network, _ = cliPrompt("Name of this network (e.g. the ISP)", false)
	}
	var err error
	if o.XrayBin, err = EnsureLocalXray(*xray, nil, func(s string) { fmt.Println(s) }); err != nil {
		return err
	}
	var rep *NetReport
	if err := guarded(nil, func() error { var e error; rep, e = Measure(&b, o); return e }); err != nil {
		return err
	}
	path := *report
	if path == "" {
		name := strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == ' ' || r == ':' {
				return '_'
			}
			return r
		}, orStr(o.Network, "network"))
		path = "xtune-report-" + name + ".json"
	}
	if err := writeJSON(path, rep); err != nil {
		return err
	}
	appendHistory(Record{Time: rep.Time, Host: b.State.Host, Network: o.Network, Kind: "measure", Results: bestResults(rep.Results)})
	fmt.Printf("\n[+] report written to %s: send it back to whoever sent you the bundle\n", path)
	return nil
}

// bestResults keeps the best-scoring result per config, for the history.
func bestResults(rs []CheckResult) []CheckResult {
	best := map[string]int{}
	var out []CheckResult
	for _, r := range rs {
		if r.Ok > 0 {
			r.Status = "ok"
		} else {
			r.Status = "blocked"
		}
		if i, ok := best[r.ID]; !ok {
			best[r.ID] = len(out)
			out = append(out, r)
		} else if r.Score > out[i].Score {
			out[i] = r
		}
	}
	return out
}

func cmdDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	c := addConnFlags(fs)
	n := fs.Int("n", 3, "configs to deploy (more when needed so every network has one)")
	reports := parse(fs, args)
	var reps []*NetReport
	for _, p := range reports {
		var rep NetReport
		if err := readJSONFile(p, &rep); err != nil {
			return err
		}
		reps = append(reps, &rep)
	}
	r, err := connect(c)
	if err != nil {
		return err
	}
	defer r.Close()
	st, err := DeployPending(r, reps, *n, os.Stdout)
	if err != nil {
		return err
	}
	afterDeploy(r, st)
	return nil
}

// ---------------------------------------------------------------- check / history / doctor

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	c := addConnFlags(fs)
	network := fs.String("network", "", "name of this network (ISP), for the history")
	probes := fs.Int("probes", 5, "requests per config")
	secs := fs.Int("secs", 10, "seconds of download per config")
	out := fs.String("out", "xtune-results", "working directory")
	parse(fs, args)
	if *c.host == "" && *c.profile == "" && serverMode() {
		return errors.New("check measures from where it runs: run it on your computer, on your users' network (xtune check --host H)")
	}
	r, err := connect(c)
	if err != nil {
		return err
	}
	defer r.Close()
	bin, err := EnsureLocalXray(*c.xray, r, func(s string) { fmt.Println(s) })
	if err != nil {
		return err
	}
	var res []CheckResult
	if err := guarded(nil, func() error { // nil: a shared test running on the server must survive Ctrl+C here
		var e error
		res, _, e = Check(r, bin, *probes, *secs, *out, func(s string) { fmt.Println(s) })
		return e
	}); err != nil {
		return err
	}
	fmt.Println()
	bad := printCheck(os.Stdout, res)
	if err := appendHistory(Record{Time: time.Now().Unix(), Host: r.Conn.Host, Network: *network, Kind: "check", Results: res}); err != nil {
		fmt.Println("[!] history:", err)
	}
	if bad > 0 {
		fmt.Printf("\n%d of %d configs have problems from this network: run `xtune tune --host %s` from here to find replacements\n",
			bad, len(res), r.Conn.Host)
	}
	return nil
}

func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	host := fs.String("host", "", "only this server")
	n := fs.Int("n", 10, "records")
	parse(fs, args)
	recs, err := loadHistory(*host)
	if err != nil {
		return err
	}
	printHistory(os.Stdout, recs, *n)
	return nil
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	c := addConnFlags(fs)
	parse(fs, args)
	r, err := target(c)
	if err != nil {
		return err
	}
	defer r.Close()
	if fails := Doctor(r).print(os.Stdout); fails > 0 {
		return fmt.Errorf("%d problem(s) found", fails)
	}
	return nil
}

// ---------------------------------------------------------------- backup / restore / migrate

func cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	c := addConnFlags(fs)
	out := fs.String("o", "", "backup file (default xtune-backup-HOST-DATE.json)")
	encrypt := fs.Bool("encrypt", false, "encrypt with a passphrase (asked, or XTUNE_BACKUP_PASSWORD)")
	parse(fs, args)
	pass := ""
	if *encrypt {
		var err error
		if pass, err = newPassphrase(); err != nil {
			return err
		}
	}
	r, err := target(c)
	if err != nil {
		return err
	}
	defer r.Close()
	b, err := TakeBackup(r)
	if err != nil {
		return err
	}
	path := orStr(*out, backupName(b.State, time.Now()))
	if err := writeBackup(path, b, pass); err != nil {
		return err
	}
	fmt.Printf("[+] backup of %s (%d users) written to %s\n", hostsOf(b.State), len(b.State.Users), path)
	fmt.Println("    It holds the server's private keys and every user's credentials: keep it private.")
	fmt.Println("    Rebuild it on a new server with: xtune migrate --backup " + path + " --to NEWHOST")
	return nil
}

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	c := addConnFlags(fs)
	pos := parse(fs, args)
	if len(pos) != 1 {
		return errors.New("restore: pass the backup file")
	}
	b, err := readBackup(pos[0])
	if err != nil {
		return err
	}
	log := func(s string) { fmt.Println("[*] " + s) }
	warnOldVersion(b, log)
	r, err := target(c)
	if err != nil {
		return err
	}
	defer r.Close()
	oldHost := b.State.Host
	st, err := Restore(r, b, log)
	if err != nil {
		return err
	}
	afterDeploy(r, st)
	printNotes(r, st, oldHost)
	return nil
}

func printNotes(r *Server, st *State, oldAddr string) {
	if oldAddr == st.Host {
		oldAddr = ""
	}
	dns := pointAfterMove(r, st, func(s string) { fmt.Println(s) })
	fmt.Println("\nNext:")
	for _, n := range moveNotes(st, oldAddr, dns) {
		fmt.Println("  - " + n)
	}
}

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	c := addConnFlags(fs)
	to := fs.String("to", "", "new server: saved profile or [user@]host[:port]")
	toKey := fs.String("to-key", "", "SSH key for the new server")
	toPassEnv := fs.String("to-password-env", "", "env var holding the new server's SSH password")
	from := fs.String("backup", "", "move this backup instead of reading the old server (when it is unreachable)")
	noTune := fs.Bool("no-tune", false, "keep the configs as they are instead of tuning the new server")
	n := fs.Int("deploy", 3, "configs to keep running after the tune")
	o := TuneOpts{}
	fs.StringVar(&o.Depth, "depth", "deep", "smart | deep")
	fs.IntVar(&o.Rounds, "rounds", 2, "benchmark rounds (median)")
	fs.StringVar(&o.Network, "network", "", "name of this network (ISP), for the history")
	parse(fs, args)
	if *to == "" {
		return errors.New("migrate: --to NEWHOST is required")
	}
	var b *Backup
	oldAddr := ""
	if *from != "" {
		var err error
		if b, err = readBackup(*from); err != nil {
			return err
		}
		oldAddr = b.State.Host
	} else {
		src, err := connect(c)
		if err != nil {
			return fmt.Errorf("%w (when the old server is unreachable, migrate from a backup: --backup FILE)", err)
		}
		b, err = TakeBackup(src)
		src.Close()
		if err != nil {
			return err
		}
		oldAddr = src.Conn.Host
	}
	warnOldVersion(b, func(s string) { fmt.Println(s) })
	tc, err := parseTarget(*to)
	if err != nil {
		return err
	}
	if *toKey != "" {
		tc.KeyPath = *toKey
	}
	if *toPassEnv != "" {
		tc.Password = os.Getenv(*toPassEnv)
	}
	if tc.Host == oldAddr {
		return errors.New("the new server is the old one")
	}
	r, err := Dial(tc, cliPrompt)
	if err != nil {
		return err
	}
	defer r.Close()
	fmt.Printf("moving %s (%d users) to %s\n", hostsOf(b.State), len(b.State.Users), r.Name())
	o.Out, o.Log = "xtune-results", func(s string) { fmt.Println(s) }
	st, err := MoveTo(r, b, !*noTune, *n, o, *c.xray)
	if err != nil {
		return err
	}
	afterDeploy(r, st)
	printNotes(r, st, oldAddr)
	return nil
}

// MoveTo rebuilds a backup on r, a server without a deployment. With tune, the new server is
// tuned from here first and the best n configs deployed; without, the configs are kept as they
// were (their ports must be free).
func MoveTo(r *Server, b *Backup, tune bool, n int, o TuneOpts, xrayFlag string) (*State, error) {
	log := func(s string) { o.Log("[*] " + s) }
	if !tune {
		return Restore(r, b, log)
	}
	out, err := r.Helper("probe")
	if err != nil {
		return nil, err
	}
	subOnly := *b.State
	subOnly.Entries = nil // the tune picks new ports; only the subscription server keeps its own
	if c := portConflicts(&subOnly, out); len(c) > 0 {
		return nil, fmt.Errorf("in use on %s: %s (the subscription links need it)", r.Name(), strings.Join(c, ", "))
	}
	seeded, err := seed(r, b, out, log)
	if err != nil {
		return nil, err
	}
	if o.XrayBin, err = EnsureLocalXray(xrayFlag, r, o.Log); err != nil {
		return nil, err
	}
	o.Domain, o.CDNDomain = seeded.Domain, seeded.CDNDomain
	var res *TuneResult
	if err := guarded(r, func() error { var e error; res, e = Tune(r, o); return e }); err != nil {
		r.Helper("cleanup") // nothing runs yet, so this removes the copied deployment too
		return nil, err
	}
	appendHistory(tuneRecord(r.Conn.Host, o.Network, res.Best))
	return Deploy(r, res, n)
}

func cmdSelfUpdate(args []string) error {
	fs := flag.NewFlagSet("self-update", flag.ExitOnError)
	ver := fs.String("version", "", "release tag to install (default: the latest)")
	parse(fs, args)
	tag := *ver
	if tag == "" {
		var err error
		if tag, err = LatestXtune(); err != nil {
			return err
		}
	}
	tag = normTag(tag)
	updated, err := SelfUpdate(tag, func(s string) { fmt.Println(s) })
	if err != nil {
		return err
	}
	if !updated {
		fmt.Println("xtune is current:", version)
		return nil
	}
	fmt.Println("xtune updated to", tag)
	if exe, err := os.Executable(); err == nil && serverMode() && os.Geteuid() == 0 {
		if p, _ := filepath.EvalSymlinks(exe); p == remoteDir+"/xtune" {
			r, err := OpenLocal()
			if err == nil {
				_, err = r.Helper("xtune-link") // restarts the subscription server on the new binary
			}
			return err
		}
	}
	if !serverMode() {
		fmt.Println("the server's copy is updated the next time you open the console or deploy")
	}
	return nil
}

// ---------------------------------------------------------------- notify / warp (server)

func cmdNotify(args []string) error {
	if err := serverOnly("notify", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("notify", flag.ExitOnError)
	chat := fs.String("chat", "", "Telegram chat ID that receives the alerts")
	pos := parse(fs, args)
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	n := st.Notify
	action := "status"
	if len(pos) > 0 {
		action = pos[0]
	}
	switch action {
	case "status":
		fmt.Println("alerts:", n.describe())
		return nil
	case "telegram":
		if *chat == "" {
			return errors.New("notify telegram: pass --chat ID (send /start to your bot, then read the ID from getUpdates)")
		}
		tok := os.Getenv("XTUNE_TELEGRAM_TOKEN")
		if tok == "" {
			if tok, err = cliPrompt("Telegram bot token (from @BotFather)", true); err != nil {
				return err
			}
		}
		n.TelegramToken, n.TelegramChat = strings.TrimSpace(tok), *chat
	case "webhook":
		u := ""
		if len(pos) > 1 {
			u = pos[1]
		} else if u, err = cliPrompt("Webhook URL", true); err != nil {
			return err
		}
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return fmt.Errorf("%q is not a URL", u)
		}
		n.Webhook = u
	case "off":
		n = Notify{}
	case "test":
		if !n.On() {
			return errors.New("alerts are off: set up telegram or a webhook first")
		}
		return sendAlert(n, st.Addr(), "test", "this is a test alert")
	default:
		return fmt.Errorf("unknown notify action %q", action)
	}
	if err := SetNotify(r, n); err != nil {
		return err
	}
	fmt.Println("alerts:", n.describe())
	return nil
}

func cmdWarp(args []string) error {
	if err := serverOnly("warp", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("warp", flag.ExitOnError)
	domains := fs.String("domains", "", "routing domains that use WARP, comma-separated (default: "+strings.Join(defaultWarpDomains, ",")+")")
	all := fs.Bool("all", false, "send all traffic through WARP")
	pos := parse(fs, args)
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	action := "status"
	if len(pos) > 0 {
		action = pos[0]
	}
	var st *State
	switch action {
	case "status":
		if st, err = requireState(r); err != nil {
			return err
		}
	case "on":
		var ds []string
		for _, d := range strings.Split(*domains, ",") {
			if d = strings.TrimSpace(d); d != "" {
				ds = append(ds, d)
			}
		}
		fmt.Println("restarting Xray: connected users reconnect by themselves")
		if st, err = SetWarp(r, true, ds, *all); err != nil {
			return err
		}
	case "off":
		if st, err = SetWarp(r, false, nil, false); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown warp action %q", action)
	}
	fmt.Println("WARP:", st.Warp.describe())
	return nil
}

// ---------------------------------------------------------------- users output

type userJSON struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Enabled  bool     `json:"enabled"`
	Up       int64    `json:"up"`
	Down     int64    `json:"down"`
	Quota    int64    `json:"quota"`
	Reset    string   `json:"reset,omitempty"`
	ResetAt  int64    `json:"resetAt,omitempty"`
	Expires  int64    `json:"expires"`
	Devices  int      `json:"devices"`
	Online   []string `json:"online"`
	LastSeen int64    `json:"lastSeen"`
	Sub      string   `json:"subscription,omitempty"`
}

func printUsersJSON(w io.Writer, st *State, us *Usage) error {
	out := []userJSON{}
	now := time.Now()
	for _, u := range st.Users {
		uu := us.Users[u.Name]
		if uu == nil {
			uu = &UserUsage{}
		}
		out = append(out, userJSON{Name: u.Name, Status: userStatus(u, uu, now), Enabled: u.Enabled, Up: uu.Up, Down: uu.Down,
			Quota: u.Quota, Reset: u.Reset, ResetAt: u.ResetAt, Expires: u.Expires, Devices: u.Devices,
			Online: append([]string{}, uu.Online...), LastSeen: uu.LastSeen, Sub: st.SubURL(u)})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func printLinksJSON(w io.Writer, st *State, name string) error {
	_, u := st.User(name)
	if u == nil {
		return fmt.Errorf("no user %q", name)
	}
	out := J{"name": u.Name, "enabled": u.Enabled, "links": st.Links(*u)}
	if sub := st.SubURL(*u); sub != "" {
		out["subscription"], out["failover"] = sub, sub+"/json"
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// printLinksQR shows the subscription link as a QR code, or every link when there is none.
func printLinksQR(w io.Writer, st *State, name string) error {
	_, u := st.User(name)
	if u == nil {
		return fmt.Errorf("no user %q", name)
	}
	if sub := st.SubURL(*u); sub != "" {
		fmt.Fprintf(w, "%s: subscription\n", u.Name)
		return printQR(w, sub)
	}
	for _, l := range st.NamedLinks(*u) {
		fmt.Fprintf(w, "\n%s: %s\n", u.Name, l.Name)
		if err := printQR(w, l.URL); err != nil {
			return err
		}
	}
	return nil
}

func fmtReset(u User) string {
	if u.Reset == "" || u.ResetAt == 0 {
		return "-"
	}
	return u.Reset + " (" + time.Unix(u.ResetAt, 0).Format("01-02") + ")"
}
