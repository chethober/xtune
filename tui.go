package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

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
	r     *Remote
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
			Title(fmt.Sprintf("xtune  -  %s@%s", t.r.Conn.User, t.r.Conn.Host)).
			Options(
				huh.NewOption("Status & Xray versions", "status"),
				huh.NewOption("Install / update Xray", "update"),
				huh.NewOption("Tune: find the configs that get through", "tune"),
				huh.NewOption("Users", "users"),
				huh.NewOption("Logs", "logs"),
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
		case "users":
			showErr(t.users())
		case "logs":
			showErr(t.logs())
		case "switch":
			t.r.Close()
			t.r = nil
		case "uninstall":
			showErr(t.uninstall())
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
	if err := xrayStatus(os.Stdout, t.r, "", false); err != nil {
		return err
	}
	st, err := t.r.Status()
	if err != nil {
		return err
	}
	fmt.Println(titleStyle.Render("\nServer"))
	fmt.Printf("  arch %s, xtune deployment: %v (unit %s), 3x-ui: %v\n", st.Arch, st.HasState, orStr(st.Unit, "-"), st.XUI)
	if st.HasState {
		if s, err := LoadState(t.r); err == nil && s != nil {
			for _, e := range s.Entries {
				fmt.Printf("  - %-14s %-15s port %d/%s\n", e.ID, e.Kind, e.Port, e.Proto())
			}
			fmt.Printf("  users: %d\n", len(s.Users))
		}
	}
	pause()
	return nil
}

func (t *tui) update() error {
	st, err := t.r.Status()
	if err != nil {
		return err
	}
	targets := []string{"local", "server"}
	opts := []huh.Option[string]{huh.NewOption("This computer", "local").Selected(true),
		huh.NewOption("Server (xtune's Xray)", "server").Selected(true)}
	if st.XUI {
		opts = append(opts, huh.NewOption("Server (3x-ui's Xray - restarts the panel's Xray)", "panel"))
	}
	channel := "pre"
	err = huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().Title("Update which Xray?").Options(opts...).Value(&targets),
		huh.NewSelect[string]().Title("Version").Options(
			huh.NewOption("Newest release (Xray tags most releases as pre-release)", "pre"),
			huh.NewOption("Newest stable release", "stable"),
			huh.NewOption("Specific tag ...", "tag")).Value(&channel),
	)).WithTheme(t.theme).Run()
	if err != nil {
		return err
	}
	var tag string
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
	err = xrayUpdate(t.r, "", normTag(tag), has("local"), has("server"), has("panel"), tuiPrompt)
	t.xray = ""
	if err == nil {
		pause()
	}
	return err
}

func (t *tui) uninstall() error {
	ok := false
	if err := huh.NewConfirm().Title("Remove xtune's Xray, its deployment, users and firewall rules from the server?").
		Description("3x-ui and anything else on the server are not touched.").Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
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
	o := TuneOpts{XrayBin: bin}
	rounds := "2"
	err = huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Tune").Description("Starts a temporary test matrix on the server (REALITY on several SNIs/ports,\n"+
			"XHTTP, gRPC, VLESS Encryption, Hysteria2, Shadowsocks-2022) and measures each\n"+
			"variant from THIS computer. Run it from the network your users are on."),
		huh.NewSelect[string]().Title("Depth").Options(
			huh.NewOption("Smart (~50 variants, ~8 min)", "smart"), huh.NewOption("Deep (~75 variants, ~20 min)", "deep")).Value(&o.Depth),
		huh.NewInput().Title("Benchmark rounds").Value(&rounds),
	)).WithTheme(t.theme).Run()
	if err != nil {
		return err
	}
	o.Rounds = atoiOr(rounds, 2)
	fmt.Println(dimStyle.Render("Ctrl+C stops the test and cleans up the server."))
	var res *TuneResult
	if err := guarded(t.r, func() error { var e error; res, e = Tune(t.r, o); return e }); err != nil {
		t.r.Helper("cleanup")
		return err
	}
	n, users := "3", ""
	if old, _ := LoadState(t.r); old != nil {
		fmt.Println(dimStyle.Render(fmt.Sprintf("A deployment already exists; its %d users keep their names but get new links.", len(old.Users))))
	}
	err = huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Keep the top N configs running on the server (0 = remove all)").Value(&n),
		huh.NewInput().Title("User names for the deployment (comma-separated, default user1)").Value(&users),
	)).WithTheme(t.theme).Run()
	if err != nil || atoiOr(n, 0) <= 0 {
		out, _ := t.r.Helper("cleanup")
		fmt.Println("server cleanup:", out)
		pause()
		return nil
	}
	st, err := Deploy(t.r, res, atoiOr(n, 0), strings.Split(users, ","))
	if err != nil {
		return err
	}
	printDeployment(st)
	pause()
	return nil
}

// ---------------------------------------------------------------- users

func (t *tui) users() error {
	st, err := t.r.Status()
	if err != nil {
		return err
	}
	kind := ""
	switch {
	case st.HasState && st.XUI:
		if err := huh.NewSelect[string]().Title("Which users?").Options(
			huh.NewOption("3x-ui panel users", "panel"), huh.NewOption("xtune standalone users", "standalone")).
			Value(&kind).WithTheme(t.theme).Run(); err != nil {
			return err
		}
	case st.HasState:
		kind = "standalone"
	case st.XUI:
		kind = "panel"
	default:
		return errors.New("no users to manage yet: run Tune and deploy, or install 3x-ui")
	}
	b, err := pickBackend(t.r, kind, tuiPrompt)
	if err != nil {
		return err
	}
	if b.panel != nil {
		defer b.panel.Close()
	}
	for {
		fmt.Println(titleStyle.Render("\nUsers (" + kind + ")"))
		if err := b.list(os.Stdout); err != nil {
			return err
		}
		names, err := b.names()
		if err != nil {
			return err
		}
		opts := []huh.Option[string]{huh.NewOption("Add user", "add")}
		if len(names) > 0 {
			opts = append(opts, huh.NewOption("Show links / subscription", "links"), huh.NewOption("Enable / disable", "toggle"))
			if kind == "panel" {
				opts = append(opts, huh.NewOption("Set traffic quota", "quota"), huh.NewOption("Set expiry", "expiry"))
			} else {
				opts = append(opts, huh.NewOption("Rotate credentials", "rotate"))
			}
			opts = append(opts, huh.NewOption("Delete user", "del"))
		}
		opts = append(opts, huh.NewOption("Back", "back"))
		var action string
		if err := huh.NewSelect[string]().Title("Action").Options(opts...).Value(&action).WithTheme(t.theme).Run(); err != nil || action == "back" {
			return nil
		}
		if err := t.userAction(b, action, names); err != nil {
			showErr(err)
		}
	}
}

func (b *backend) names() ([]string, error) {
	var out []string
	if b.kind == "standalone" {
		st, err := requireState(b.r)
		if err != nil {
			return nil, err
		}
		for _, u := range st.Users {
			out = append(out, u.Name)
		}
		return out, nil
	}
	us, err := b.panel.Users()
	for _, u := range us {
		out = append(out, u.Email)
	}
	return out, err
}

func (t *tui) userAction(b *backend, action string, names []string) error {
	if action == "add" {
		var name, quota, days string
		group := []huh.Field{huh.NewInput().Title("User name").Value(&name)}
		if b.kind == "panel" {
			group = append(group, huh.NewInput().Title("Quota GB (empty = unlimited)").Value(&quota),
				huh.NewInput().Title("Expires in days (empty = never)").Value(&days))
		}
		var ids []int
		if b.kind == "panel" {
			ibs, err := b.panel.Inbounds()
			if err != nil {
				return err
			}
			var opts []huh.Option[int]
			for _, ib := range ibs {
				if ib.Enable {
					opts = append(opts, huh.NewOption(fmt.Sprintf("%s  (%s :%d)", ib.Remark, ib.Protocol, ib.Port), ib.ID).Selected(true))
				}
			}
			group = append(group, huh.NewMultiSelect[int]().Title("Inbounds").Options(opts...).Value(&ids))
		}
		if err := huh.NewForm(huh.NewGroup(group...)).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		q, _ := strconv.ParseFloat(strings.TrimSpace(quota), 64)
		if err := b.add(strings.TrimSpace(name), q, atoiOr(days, 0), ids); err != nil {
			return err
		}
		fmt.Println(okStyle.Render("added " + name))
		b.links(os.Stdout, strings.TrimSpace(name))
		pause()
		return nil
	}
	var name string
	opts := huh.NewOptions(names...)
	if err := huh.NewSelect[string]().Title("User").Options(opts...).Value(&name).WithTheme(t.theme).Run(); err != nil {
		return err
	}
	switch action {
	case "links":
		if err := b.links(os.Stdout, name); err != nil {
			return err
		}
		pause()
	case "toggle":
		on := true
		if err := huh.NewSelect[bool]().Title(name).Options(huh.NewOption("Enable", true), huh.NewOption("Disable", false)).
			Value(&on).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		return b.setEnabled(name, on)
	case "quota":
		v, err := tuiPrompt("Quota GB for "+name+" (0 = unlimited)", false)
		if err != nil {
			return err
		}
		gb, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return b.setQuota(name, gb)
	case "expiry":
		v, err := tuiPrompt("Expires in N days from now (0 = never)", false)
		if err != nil {
			return err
		}
		return b.setExpiry(name, atoiOr(v, 0))
	case "rotate":
		ok := false
		if err := huh.NewConfirm().Title("New credentials for " + name + "? Old links stop working.").Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
			return err
		}
		if err := b.rotate(os.Stdout, name); err != nil {
			return err
		}
		pause()
	case "del":
		ok := false
		if err := huh.NewConfirm().Title("Delete " + name + "?").Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
			return err
		}
		return b.del(name)
	}
	return nil
}

// ---------------------------------------------------------------- logs

func (t *tui) logs() error {
	st, err := t.r.Status()
	if err != nil {
		return err
	}
	unit := defaultUnit(st)
	opts := []huh.Option[string]{huh.NewOption("xtune Xray (xray-autotune)", "xray-autotune")}
	if st.XUI {
		opts = append(opts, huh.NewOption("3x-ui panel + its Xray (x-ui)", "x-ui"))
	}
	opts = append(opts, huh.NewOption("xtune test instance (xray-autotune-test)", "xray-autotune-test"))
	if err := huh.NewSelect[string]().Title("Logs").Options(opts...).Value(&unit).WithTheme(t.theme).Run(); err != nil {
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

func viewLogs(r *Remote, unit string) error {
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
