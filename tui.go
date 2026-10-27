package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

type tui struct {
	r     *Server
	xray  string
	theme *huh.Theme
}

func tuiPrompt(label string, secret bool) (string, error) {
	var v string
	in := huh.NewInput().Title(label).Value(&v)
	if secret {
		in = in.EchoMode(huh.EchoModePassword)
	}
	return v, in.Run()
}

func pause() {
	fmt.Print(dimStyle.Render("\n[enter] back to menu "))
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func showErr(err error) {
	if err != nil && !errors.Is(err, huh.ErrUserAborted) {
		fmt.Println(errStyle.Render("error: ") + err.Error())
		pause()
	}
}

func runTUI() {
	// Skip the OSC 11 background-colour query: terminals that don't answer it (some Windows
	// consoles, tmux setups) would stall before the first frame.
	lipgloss.SetHasDarkBackground(os.Getenv("XTUNE_LIGHT") == "")
	t := &tui{theme: huh.ThemeCharm()}
	defer func() {
		if t.r != nil {
			t.r.Close()
		}
	}()
	for {
		if t.r == nil {
			if err := t.connect(); err != nil {
				if errors.Is(err, huh.ErrUserAborted) {
					return
				}
				showErr(err)
				continue
			}
		}
		var action string
		err := huh.NewSelect[string]().
			Title("xtune  -  "+t.r.Name()).
			Options(
				huh.NewOption("Status & Xray versions", "status"),
				huh.NewOption("Install / update Xray", "update"),
				huh.NewOption("Tune: find the configs that get through, deploy them", "tune"),
				huh.NewOption("Check the deployed configs from this network", "check"),
				huh.NewOption("Server console: users, addresses", "console"),
				huh.NewOption("Doctor: diagnose the server", "doctor"),
				huh.NewOption("Backup / move to a new server", "move"),
				huh.NewOption("Logs", "logs"),
				huh.NewOption("Update xtune", "self"),
				huh.NewOption("Switch server", "switch"),
				huh.NewOption("Uninstall xtune from server", "uninstall"),
				huh.NewOption("Quit", "quit"),
			).Value(&action).WithTheme(t.theme).Run()
		if err != nil || action == "quit" {
			return
		}
		switch action {
		case "status":
			showErr(t.status())
		case "update":
			showErr(t.update())
		case "tune":
			showErr(t.tune())
		case "check":
			showErr(t.check())
		case "console":
			showErr(openConsole(t.r))
		case "doctor":
			showErr(t.doctor())
		case "move":
			showErr(t.move())
		case "logs":
			showErr(t.logs())
		case "self":
			showErr(t.selfUpdate())
		case "switch":
			t.r.Close()
			t.r = nil
		case "uninstall":
			showErr(t.uninstall())
		}
	}
}

// runServerTUI is the TUI on the server itself (sudo xtune): users and addresses are managed here.
func runServerTUI() {
	lipgloss.SetHasDarkBackground(os.Getenv("XTUNE_LIGHT") == "")
	r, err := OpenLocal()
	if err != nil {
		fmt.Fprintln(os.Stderr, errStyle.Render("error: ")+err.Error())
		os.Exit(1)
	}
	t := &tui{r: r, theme: huh.ThemeCharm()}
	for {
		var action string
		err := huh.NewSelect[string]().
			Title("xtune server  -  "+r.Conn.Host).
			Options(
				huh.NewOption("Status", "status"),
				huh.NewOption("Users: limits, usage, links", "users"),
				huh.NewOption("Subscription server: public links", "sub"),
				huh.NewOption("Addresses: direct / CDN domain", "addresses"),
				huh.NewOption("Client routing: direct country", "country"),
				huh.NewOption("WARP: sites that refuse the server's IP", "warp"),
				huh.NewOption("Alerts: Telegram / webhook", "notify"),
				huh.NewOption("Manage from Telegram: links for @chthvpnbot", "manage"),
				huh.NewOption("Plans: named limits for adding and renewing", "plans"),
				huh.NewOption("Several servers under one subscription", "peers"),
				huh.NewOption("Cloudflare: DNS records and SSL mode", "cloudflare"),
				huh.NewOption("REALITY targets: check and refresh", "sni"),
				huh.NewOption("Doctor: diagnose this server", "doctor"),
				huh.NewOption("Backup", "backup"),
				huh.NewOption("Update Xray", "update"),
				huh.NewOption("Update xtune", "self"),
				huh.NewOption("Logs", "logs"),
				huh.NewOption("Uninstall xtune from this server", "uninstall"),
				huh.NewOption("Quit", "quit"),
			).Value(&action).WithTheme(t.theme).Run()
		if err != nil || action == "quit" {
			return
		}
		switch action {
		case "status":
			showErr(t.status())
		case "users":
			showErr(t.users())
		case "sub":
			showErr(t.sub())
		case "addresses":
			showErr(t.addresses())
		case "country":
			showErr(t.country())
		case "warp":
			showErr(t.warp())
		case "notify":
			showErr(t.notify())
		case "manage":
			showErr(t.manage())
		case "plans":
			showErr(t.plans())
		case "peers":
			showErr(t.peers())
		case "cloudflare":
			showErr(t.cloudflare())
		case "sni":
			showErr(t.snis())
		case "doctor":
			showErr(t.doctor())
		case "backup":
			showErr(t.backup())
		case "update":
			showErr(t.update())
		case "self":
			showErr(t.selfUpdate())
		case "logs":
			showErr(t.logs())
		case "uninstall":
			if err := t.uninstall(); err != nil || !serverMode() {
				showErr(err)
				return
			}
		}
	}
}

// ---------------------------------------------------------------- connect wizard

func (t *tui) connect() error {
	profiles := loadProfiles()
	choice := "new"
	if len(profiles) > 0 {
		opts := []huh.Option[string]{}
		for _, p := range profiles {
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s  (%s@%s:%d)", p.Name, p.User, p.Host, p.Port), p.Name))
		}
		opts = append(opts, huh.NewOption("New server ...", "new"))
		if err := huh.NewSelect[string]().Title("Server").Options(opts...).Value(&choice).WithTheme(t.theme).Run(); err != nil {
			return err
		}
	}
	var c Conn
	if choice != "new" {
		c, _ = findProfile(choice)
	} else {
		port, auth := "22", "key"
		c.User = "root"
		err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Server address").Value(&c.Host).Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("required")
				}
				return nil
			}),
			huh.NewInput().Title("SSH port").Value(&port),
			huh.NewInput().Title("SSH user").Value(&c.User),
			huh.NewSelect[string]().Title("Authentication").Options(
				huh.NewOption("SSH key / agent", "key"), huh.NewOption("Password", "password")).Value(&auth),
		)).WithTheme(t.theme).Run()
		if err != nil {
			return err
		}
		c.Host, c.Port = strings.TrimSpace(c.Host), atoiOr(port, 22)
		if auth == "key" {
			if err := huh.NewInput().Title("Private key path (empty = default keys / agent)").Value(&c.KeyPath).WithTheme(t.theme).Run(); err != nil {
				return err
			}
		} else {
			pw, err := tuiPrompt("SSH password", true)
			if err != nil {
				return err
			}
			c.Password = pw
		}
	}
	fmt.Println(dimStyle.Render("connecting to " + c.Host + " ..."))
	r, err := Dial(c, tuiPrompt)
	if err != nil {
		return err
	}
	t.r = r
	if choice == "new" {
		save := true
		huh.NewConfirm().Title("Save this server as a profile? (no passwords are stored)").Value(&save).WithTheme(t.theme).Run()
		if save {
			c.Password = ""
			c.Name = c.Host
			saveProfile(c)
		}
	}
	return nil
}

func atoiOr(s string, d int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return d
}

func (t *tui) localXray() (string, error) {
	if t.xray == "" {
		b, err := EnsureLocalXray("", t.r, func(s string) { fmt.Println(s) })
		if err != nil {
			return "", err
		}
		t.xray = b
	}
	return t.xray, nil
}

// ---------------------------------------------------------------- status / update / uninstall

func (t *tui) status() error {
	fmt.Println(titleStyle.Render("\nXray versions"))
	if err := xrayStatus(os.Stdout, t.r, "", false, !t.r.Local); err != nil {
		return err
	}
	st, err := t.r.Status()
	if err != nil {
		return err
	}
	fmt.Println(titleStyle.Render("\nServer"))
	mismatch := ""
	if !t.r.Local && version != "dev" && st.Xtune != "none" && st.Xtune != version {
		mismatch = " (this computer has " + version + "; opening the console updates it)"
	}
	fmt.Printf("  arch %s, xray-autotune: %s, xtune on server: %s%s\n", st.Arch, orStr(st.Unit, "-"), orStr(st.Xtune, "none"), mismatch)
	fmt.Printf("  enforce timer: %s, subscription server: %s\n", orStr(st.Timer, "-"), orStr(st.Sub, "-"))
	s, err := LoadState(t.r)
	if err != nil {
		return err
	}
	if s == nil {
		fmt.Println("  nothing deployed yet")
		pause()
		return nil
	}
	fmt.Println()
	printAddresses(os.Stdout, s)
	fmt.Println()
	for _, e := range s.Entries {
		fmt.Printf("  - %-14s %-15s port %d/%s\n", e.ID, e.Kind, e.Port, e.Proto())
	}
	us, err := LoadUsage(t.r)
	if err != nil {
		return err
	}
	online := 0
	for _, u := range s.Users {
		if uu := us.Users[u.Name]; uu != nil && len(uu.Online) > 0 {
			online++
		}
	}
	fmt.Printf("  users: %d, %d active, %d online now\n", len(s.Users), len(s.activeUsers(us, time.Now())), online)
	pause()
	return nil
}

func (t *tui) update() error {
	targets := []string{"server"}
	channel := "pre"
	fields := []huh.Field{huh.NewSelect[string]().Title("Version").Options(
		huh.NewOption("Newest release (Xray tags most releases as pre-release)", "pre"),
		huh.NewOption("Newest stable release", "stable"),
		huh.NewOption("Specific tag ...", "tag")).Value(&channel)}
	if !t.r.Local {
		targets = append(targets, "local")
		fields = append([]huh.Field{huh.NewMultiSelect[string]().Title("Update which Xray?").Options(
			huh.NewOption("This computer", "local").Selected(true),
			huh.NewOption("Server", "server").Selected(true)).Value(&targets)}, fields...)
	}
	if err := huh.NewForm(huh.NewGroup(fields...)).WithTheme(t.theme).Run(); err != nil {
		return err
	}
	var tag string
	var err error
	if channel == "tag" {
		if tag, err = tuiPrompt("Tag (e.g. v26.9.30)", false); err != nil {
			return err
		}
	} else if tag, err = LatestXray(channel == "stable", t.r); err != nil {
		return err
	}
	has := func(s string) bool {
		for _, x := range targets {
			if x == s {
				return true
			}
		}
		return false
	}
	err = xrayUpdate(t.r, "", normTag(tag), has("local"), has("server"))
	t.xray = ""
	if err == nil {
		pause()
	}
	return err
}

func (t *tui) uninstall() error {
	ok := false
	if err := huh.NewConfirm().Title("Remove xtune, its Xray, deployment, users and firewall rules from the server?").
		Description("Nothing else on the server is touched.").Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
		return err
	}
	out, err := t.r.Helper("uninstall")
	fmt.Println(out)
	pause()
	return err
}

// ---------------------------------------------------------------- tune

func (t *tui) tune() error {
	bin, err := t.localXray()
	if err != nil {
		return err
	}
	o := TuneOpts{XrayBin: bin, CleanIPs: defaultCleanIPs}
	old, err := LoadState(t.r)
	if err != nil {
		return err
	}
	o.inherit(old, nil)
	rounds := "2"
	err = huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Tune").Description("Starts a temporary test matrix on the server (REALITY on several SNIs/ports,\n"+
			"XHTTP, gRPC, VLESS Encryption, Hysteria2, Shadowsocks-2022, WireGuard, CDN) and measures each\n"+
			"variant from THIS computer. Run it from the network your users are on."),
		huh.NewSelect[string]().Title("Depth").Options(
			huh.NewOption("Smart (~50 variants, ~8 min)", "smart"), huh.NewOption("Deep (~75 variants, ~20 min)", "deep")).Value(&o.Depth),
		huh.NewInput().Title("Benchmark rounds").Value(&rounds),
	), huh.NewGroup(
		huh.NewInput().Title("Direct domain (optional)").
			Description("A DNS-only (grey cloud) record pointing at the server. Links use it instead of the IP.").Value(&o.Domain),
		huh.NewInput().Title("CDN domain (optional)").
			Description("A CDN-proxied (orange cloud) record for the server, SSL/TLS mode \"Full\".\n"+
				"Adds a VLESS + XHTTP + TLS entry that goes through the CDN.").Value(&o.CDNDomain),
	), huh.NewGroup(
		huh.NewMultiSelect[string]().Title("Other protocols (optional)").
			Description("WireGuard is always tested. These run besides Xray and serve their own apps.").
			Options(
				huh.NewOption("SSH tunnel (HTTP Injector, NetMod, NapsternetV; also over TLS and the CDN)", "ssh"),
				huh.NewOption("DNS tunnel (dnstt / SlowDNS; slow, for when only DNS gets through)", "dnstt"),
				huh.NewOption("AmneziaWG (WireGuard with a disguised handshake; AmneziaVPN / AmneziaWG apps)", "awg"),
				huh.NewOption("Cloudflare Worker relay (VLESS over workers.dev; needs a Cloudflare token)", "worker"),
				huh.NewOption("OpenVPN (UDP and TCP; handshake checked only)", "openvpn"),
				huh.NewOption("IKEv2 (built into phones and Windows; handshake checked only)", "ikev2"),
			).Value(&o.Protocols),
		huh.NewInput().Title("DNS tunnel zone (for the DNS tunnel)").
			Description("A subdomain delegated to the server: t.example.com NS ns-t.example.com,\n"+
				"and ns-t.example.com A <server IP>. xtune adds both with a Cloudflare token.").Value(&o.DNSDomain),
	), huh.NewGroup(
		huh.NewInput().Title("Cloudflare zone (optional)").
			Description("With a Cloudflare token, domains left empty get random names in this zone\n"+
				"(example.com), and new domains get their DNS records made.").Value(&o.Zone),
		huh.NewInput().Title("Cloudflare API token").EchoMode(huh.EchoModePassword).
			Description("Empty: the server's (xtune cloudflare token). Zone:Read, DNS:Edit, Zone Settings:Edit,\n"+
				"and Account: Workers Scripts:Edit for the Worker relay.").Value(&o.Cloudflare),
	)).WithTheme(t.theme).Run()
	if err != nil {
		return err
	}
	o.Domain, o.CDNDomain, o.DNSDomain = strings.TrimSpace(o.Domain), strings.TrimSpace(o.CDNDomain), strings.TrimSpace(o.DNSDomain)
	o.Zone, o.Cloudflare = strings.TrimSpace(o.Zone), strings.TrimSpace(o.Cloudflare)
	o.Rounds = atoiOr(rounds, 2)
	fmt.Println(dimStyle.Render("Ctrl+C stops the test and cleans up the server."))
	var res *TuneResult
	if err := guarded(t.r, func() error { var e error; res, e = Tune(t.r, o); return e }); err != nil {
		t.r.Helper("cleanup")
		return err
	}
	appendHistory(tuneRecord(t.r.Conn.Host, res.NetName, res.Best))
	n := "3"
	if old != nil {
		fmt.Println(dimStyle.Render(fmt.Sprintf("A deployment already exists: its %d users and server keys are kept.", len(old.Users))))
	}
	if err := huh.NewInput().Title("Keep the top N configs running on the server (0 = remove all)").
		Description("A working CDN config is always kept as well.").Value(&n).
		WithTheme(t.theme).Run(); err != nil || atoiOr(n, 0) <= 0 {
		out, _ := t.r.Helper("cleanup")
		fmt.Println("server cleanup:", out)
		pause()
		return nil
	}
	st, err := Deploy(t.r, res, atoiOr(n, 0))
	if err != nil {
		return err
	}
	printDeployment(st)
	if err := EnsureServerXtune(t.r, func(s string) { fmt.Println(s) }); err != nil {
		return fmt.Errorf("deployed, but xtune could not be installed on the server to manage users: %w", err)
	}
	open := true
	title := "Open the server console to manage users?"
	if len(st.Users) == 0 {
		title = "No users yet. Open the server console to add them?"
	}
	if huh.NewConfirm().Title(title).Value(&open).WithTheme(t.theme).Run() != nil || !open {
		return nil
	}
	return openConsole(t.r)
}

// ---------------------------------------------------------------- users (server)

func (t *tui) users() error {
	for {
		st, err := requireState(t.r)
		if err != nil {
			return err
		}
		us, err := LoadUsage(t.r)
		if err != nil {
			return err
		}
		fmt.Println(titleStyle.Render("\nUsers"))
		printUsers(os.Stdout, st, us)
		opts := []huh.Option[string]{huh.NewOption("Add user", "add")}
		if len(st.Users) > 0 {
			opts = append(opts, huh.NewOption("Show links / subscription", "links"),
				huh.NewOption("Set limits: quota, expiry, devices", "limits"),
				huh.NewOption("Renew: extend expiry, usage from zero", "renew"),
				huh.NewOption("Usage per day", "history"),
				huh.NewOption("Enable / disable", "toggle"), huh.NewOption("Reset usage", "reset"),
				huh.NewOption("Rotate credentials", "rotate"), huh.NewOption("New subscription link", "resub"),
				huh.NewOption("Delete user", "del"))
		}
		opts = append(opts, huh.NewOption("Back", "back"))
		var action string
		if err := huh.NewSelect[string]().Title("Action").Options(opts...).Value(&action).WithTheme(t.theme).Run(); err != nil || action == "back" {
			return nil
		}
		if err := t.userAction(st, action); err != nil {
			showErr(err)
		}
	}
}

// limitsForm asks for quota, expiry and device limits, prefilled from u.
func (t *tui) limitsForm(u User, name *string) (Limits, error) {
	quota, days, devices, reset := "", "", "", u.Reset
	if u.Quota > 0 {
		quota = strconv.FormatFloat(float64(u.Quota)/(1<<30), 'f', -1, 64)
	}
	if u.Expires > 0 {
		days = strconv.Itoa(max(0, int(time.Until(time.Unix(u.Expires, 0)).Hours()/24+0.5)))
	}
	if u.Devices > 0 {
		devices = strconv.Itoa(u.Devices)
	}
	num := func(s string) error {
		if s = strings.TrimSpace(s); s != "" {
			if f, err := strconv.ParseFloat(s, 64); err != nil || f < 0 {
				return errors.New("a number, or empty")
			}
		}
		return nil
	}
	var fields []huh.Field
	if name != nil {
		fields = append(fields, huh.NewInput().Title("User name").Value(name).Validate(func(s string) error {
			return validUserName(strings.TrimSpace(s))
		}))
	}
	fields = append(fields,
		huh.NewInput().Title("Traffic quota in GB (empty = unlimited)").Value(&quota).Validate(num),
		huh.NewInput().Title("Expires in days from now (empty = never)").Value(&days).Validate(num),
		huh.NewInput().Title("Devices at the same time (empty = unlimited)").
			Description("Counted by client IP. Extra devices are blocked for 10 minutes at a time.").Value(&devices).Validate(num),
		huh.NewSelect[string]().Title("Usage starts from zero").Options(huh.NewOption("Never", ""),
			huh.NewOption("Every month (from today)", "monthly"), huh.NewOption("Every week (from today)", "weekly")).Value(&reset))
	if err := huh.NewForm(huh.NewGroup(fields...)).WithTheme(t.theme).Run(); err != nil {
		return Limits{}, err
	}
	q, _ := strconv.ParseFloat(strings.TrimSpace(quota), 64)
	d, dv := atoiOr(days, 0), atoiOr(devices, 0)
	l := Limits{QuotaGB: &q, Days: &d, Devices: &dv}
	if reset != u.Reset { // keep the running period when the setting does not change
		l.Reset = &reset
	}
	return l, nil
}

func (t *tui) userAction(st *State, action string) error {
	if action == "add" {
		var name string
		plan, err := t.pickPlan(st, "Plan", "Custom limits")
		if err != nil {
			return err
		}
		var l Limits
		if plan != "" {
			if err := huh.NewInput().Title("User name").Value(&name).Validate(func(s string) error {
				return validUserName(strings.TrimSpace(s))
			}).WithTheme(t.theme).Run(); err != nil {
				return err
			}
			l.Plan = &plan
		} else if l, err = t.limitsForm(User{}, &name); err != nil {
			return err
		}
		st, u, err := AddUser(t.r, strings.TrimSpace(name), l)
		if err != nil {
			return err
		}
		fmt.Println(okStyle.Render("added " + u.Name))
		printLinks(os.Stdout, st, u.Name)
		pause()
		return nil
	}
	var names []string
	for _, u := range st.Users {
		names = append(names, u.Name)
	}
	var name string
	if err := huh.NewSelect[string]().Title("User").Options(huh.NewOptions(names...)...).Value(&name).WithTheme(t.theme).Run(); err != nil {
		return err
	}
	_, u := st.User(name)
	switch action {
	case "links":
		if err := printLinks(os.Stdout, st, name); err != nil {
			return err
		}
		pause()
	case "limits":
		l, err := t.limitsForm(*u, nil)
		if err != nil {
			return err
		}
		_, err = SetLimits(t.r, name, l)
		return err
	case "renew":
		plan, err := t.pickPlan(st, "Renew "+name+" with", "Their current limits ("+orStr(u.Plan, fmt.Sprintf("+%d days", renewDays))+")")
		if err != nil {
			return err
		}
		_, nu, err := Renew(t.r, name, plan, 0)
		if err != nil {
			return err
		}
		fmt.Println(okStyle.Render(fmt.Sprintf("%s renewed: expires %s, usage from zero", name, fmtExpiry(nu.Expires, time.Now()))))
		pause()
	case "history":
		if err := printUsageHistory(os.Stdout, t.r, name); err != nil {
			return err
		}
		pause()
	case "toggle":
		on := true
		if err := huh.NewSelect[bool]().Title(name).Options(huh.NewOption("Enable", true), huh.NewOption("Disable", false)).
			Value(&on).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		return SetUserEnabled(t.r, name, on)
	case "reset":
		ok := false
		if err := huh.NewConfirm().Title("Reset " + name + "'s traffic to zero?").Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
			return err
		}
		return ResetUsage(t.r, name)
	case "rotate":
		ok := false
		if err := huh.NewConfirm().Title("New credentials for " + name + "? Old links stop working.").
			Description("Subscription users get the new ones on their next update.").Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
			return err
		}
		st, u, err := RotateUser(t.r, name)
		if err != nil {
			return err
		}
		printLinks(os.Stdout, st, u.Name)
		pause()
	case "resub":
		ok := false
		if err := huh.NewConfirm().Title("New subscription link for " + name + "? The old link stops working.").Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
			return err
		}
		st, err := ResetSubLink(t.r, name)
		if err != nil {
			return err
		}
		printLinks(os.Stdout, st, name)
		pause()
	case "del":
		ok := false
		if err := huh.NewConfirm().Title("Delete " + name + "?").Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
			return err
		}
		return DeleteUser(t.r, name)
	}
	return nil
}

// ---------------------------------------------------------------- subscription server / client routing (server)

func (t *tui) sub() error {
	st, err := requireState(t.r)
	if err != nil {
		return err
	}
	fmt.Println(titleStyle.Render("\nSubscription server"))
	printSub(os.Stdout, st)
	fmt.Println(dimStyle.Render("\nEach user gets one link: a page in a browser (usage, QR code, configs), a subscription for apps,\n" +
		"and an automatic-failover config at <link>/json."))
	opts := []huh.Option[string]{}
	if st.CDNDomain != "" {
		opts = append(opts, huh.NewOption("Through the CDN domain "+st.CDNDomain+" (hides the server IP)", "cdn"))
	}
	if st.Domain != "" {
		opts = append(opts, huh.NewOption("On the direct domain "+st.Domain+" (Let's Encrypt, needs port 80)", "direct"))
	}
	if len(opts) == 0 {
		return errors.New("the subscription server needs a domain: set a CDN or direct domain under Addresses first")
	}
	if st.Sub.Mode != "" {
		opts = append(opts, huh.NewOption("Page language", "lang"), huh.NewOption("Turn off", "off"))
	}
	opts = append(opts, huh.NewOption("Back", "back"))
	mode := st.Sub.Mode
	if mode == "" {
		mode = defaultSubMode(st)
	}
	if err := huh.NewSelect[string]().Title("Serve subscriptions").Options(opts...).Value(&mode).WithTheme(t.theme).Run(); err != nil || mode == "back" {
		return err
	}
	if mode == "off" {
		return DisableSub(t.r)
	}
	if mode == "lang" {
		lang := st.Sub.Lang
		if err := huh.NewSelect[string]().Title("Language of users' pages").Options(
			huh.NewOption("From the browser's setting", ""), huh.NewOption("English", "en"),
			huh.NewOption("Persian (فارسی), Solar Hijri dates", "fa"), huh.NewOption("Russian (Русский)", "ru"),
			huh.NewOption("Chinese, Simplified (简体中文)", "zh")).Value(&lang).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		return SetSubLang(t.r, lang)
	}
	port := ""
	if st.Sub.Mode == mode {
		port = strconv.Itoa(st.Sub.Port)
	}
	if err := huh.NewInput().Title("HTTPS port (empty = pick a free one)").Description("Through a CDN: one of " + fmt.Sprint(cdnPorts)).
		Value(&port).WithTheme(t.theme).Run(); err != nil {
		return err
	}
	if st, err = EnableSub(t.r, mode, atoiOr(port, 0)); err != nil {
		return err
	}
	fmt.Println(okStyle.Render("\nsubscription server is on"))
	printSub(os.Stdout, st)
	pause()
	return nil
}

func (t *tui) country() error {
	st, err := requireState(t.r)
	if err != nil {
		return err
	}
	code := st.Country
	if err := huh.NewSelect[string]().Title("Sites that go direct in client configs").
		Description("Used by the automatic-failover config: local sites skip the proxy (faster, less server traffic).").
		Options(huh.NewOption("None: everything through the proxy", ""), huh.NewOption("Iran (ir)", "ir"),
			huh.NewOption("China (cn)", "cn"), huh.NewOption("Russia (ru)", "ru")).
		Value(&code).WithTheme(t.theme).Run(); err != nil {
		return err
	}
	return SetCountry(t.r, code)
}

// ---------------------------------------------------------------- addresses (server)

func (t *tui) addresses() error {
	st, err := requireState(t.r)
	if err != nil {
		return err
	}
	fmt.Println(titleStyle.Render("\nAddresses"))
	printAddresses(os.Stdout, st)
	direct, cdn := st.Domain, st.CDNDomain
	err = huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Direct domain (empty = use the IP)").
			Description("DNS-only (grey cloud) record pointing at this server.").Value(&direct),
		huh.NewInput().Title("CDN domain (empty = no CDN entry)").
			Description("CDN-proxied (orange cloud) record, SSL/TLS mode \"Full\". Setting one adds a\n"+
				"VLESS + XHTTP + TLS entry with default settings; re-tune to measure it.").Value(&cdn),
	)).WithTheme(t.theme).Run()
	if err != nil {
		return err
	}
	direct, cdn = strings.TrimSpace(direct), strings.TrimSpace(cdn)
	if direct == st.Domain && cdn == st.CDNDomain && (cdn == "" || hasCDN(st.Entries)) {
		return nil
	}
	st, added, err := SetDomains(t.r, direct, cdn, func(s string) { fmt.Println(s) })
	if err != nil {
		return err
	}
	fmt.Println(okStyle.Render("\naddresses updated"))
	printAddresses(os.Stdout, st)
	if added {
		fmt.Println(dimStyle.Render("Added a CDN entry with default settings; re-run a tune from your users' network to measure it."))
	}
	fmt.Println(dimStyle.Render("Users' links changed: send them the new links or have them refresh their subscription."))
	pause()
	return nil
}

// ---------------------------------------------------------------- logs

func (t *tui) logs() error {
	unit := "xray-autotune"
	if err := huh.NewSelect[string]().Title("Logs").Options(
		huh.NewOption("Deployed Xray (xray-autotune)", "xray-autotune"),
		huh.NewOption("Subscription server (xray-autotune-sub)", "xray-autotune-sub"),
		huh.NewOption("Usage and limits job (xray-autotune-enforce)", "xray-autotune-enforce"),
		huh.NewOption("Telegram bot (xray-autotune-bot)", "xray-autotune-bot"),
		huh.NewOption("Test instance (xray-autotune-test)", "xray-autotune-test"),
	).Value(&unit).WithTheme(t.theme).Run(); err != nil {
		return err
	}
	return viewLogs(t.r, unit)
}

type lineMsg string
type endMsg struct{}

type logModel struct {
	vp     viewport.Model
	lines  []string
	ch     chan string
	unit   string
	follow bool
	ready  bool
	ended  bool
}

func waitLine(ch chan string) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-ch
		if !ok {
			return endMsg{}
		}
		return lineMsg(l)
	}
}

func (m logModel) Init() tea.Cmd { return waitLine(m.ch) }

func (m logModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !m.ready {
			m.vp = viewport.New(msg.Width, msg.Height-2)
			m.ready = true
		} else {
			m.vp.Width, m.vp.Height = msg.Width, msg.Height-2
		}
		m.vp.SetContent(strings.Join(m.lines, "\n"))
		if m.follow {
			m.vp.GotoBottom()
		}
	case lineMsg:
		m.lines = append(m.lines, string(msg))
		if len(m.lines) > 5000 {
			m.lines = m.lines[len(m.lines)-5000:]
		}
		if m.ready {
			m.vp.SetContent(strings.Join(m.lines, "\n"))
			if m.follow {
				m.vp.GotoBottom()
			}
		}
		return m, waitLine(m.ch)
	case endMsg:
		m.ended = true
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "f":
			m.follow = !m.follow
			if m.follow {
				m.vp.GotoBottom()
			}
			return m, nil
		case "up", "k", "pgup", "home":
			m.follow = false
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m logModel) View() string {
	if !m.ready {
		return "loading ..."
	}
	state := "following"
	if !m.follow {
		state = "paused"
	}
	if m.ended {
		state = "stream ended"
	}
	head := titleStyle.Render("logs: "+m.unit) + dimStyle.Render(fmt.Sprintf("  %d lines  %s", len(m.lines), state))
	foot := dimStyle.Render("↑/↓ pgup/pgdn scroll · f follow · q back")
	return head + "\n" + m.vp.View() + "\n" + foot
}

func viewLogs(r *Server, unit string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan string, 256)
	go func() {
		defer close(ch)
		r.Stream(ctx, logCmd(unit, 300, true), func(l string) {
			select {
			case ch <- l:
			case <-ctx.Done():
			}
		})
	}()
	_, err := tea.NewProgram(logModel{ch: ch, unit: unit, follow: true}, tea.WithAltScreen()).Run()
	return err
}
