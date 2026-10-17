package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
)

// ---------------------------------------------------------------- client

func (t *tui) check() error {
	bin, err := t.localXray()
	if err != nil {
		return err
	}
	network := ""
	if err := huh.NewInput().Title("Name of this network (optional, for the history)").Description("e.g. the ISP").
		Value(&network).WithTheme(t.theme).Run(); err != nil {
		return err
	}
	var res []CheckResult
	if err := guarded(nil, func() error {
		var e error
		res, _, e = Check(t.r, bin, 5, 10, "xtune-results", func(s string) { fmt.Println(s) })
		return e
	}); err != nil {
		return err
	}
	fmt.Println()
	bad := printCheck(os.Stdout, res)
	appendHistory(Record{Time: time.Now().Unix(), Host: t.r.Conn.Host, Network: strings.TrimSpace(network), Kind: "check", Results: res})
	if bad > 0 {
		fmt.Println(errStyle.Render(fmt.Sprintf("\n%d of %d configs have problems from here: run a tune from this network to replace them.", bad, len(res))))
	} else {
		fmt.Println(okStyle.Render("\nevery deployed config works from here"))
	}
	pause()
	return nil
}

func (t *tui) doctor() error {
	fmt.Println(titleStyle.Render("\nDoctor: " + t.r.Name()))
	Doctor(t.r).print(os.Stdout)
	pause()
	return nil
}

func (t *tui) backup() error {
	b, err := TakeBackup(t.r)
	if err != nil {
		return err
	}
	path := backupName(b.State, time.Now())
	if err := huh.NewInput().Title("Save the backup as").
		Description("It holds the server's private keys and every user's credentials: keep it private.").
		Value(&path).WithTheme(t.theme).Run(); err != nil {
		return err
	}
	if err := writeBackup(strings.TrimSpace(path), b); err != nil {
		return err
	}
	fmt.Println(okStyle.Render(fmt.Sprintf("saved %d users and the server keys to %s", len(b.State.Users), path)))
	pause()
	return nil
}

func (t *tui) move() error {
	action := "backup"
	if err := huh.NewSelect[string]().Title("Backup / move").Options(
		huh.NewOption("Save a backup of this server to this computer", "backup"),
		huh.NewOption("Move to a new server, tuned from here (recommended when the IP is blocked)", "tune"),
		huh.NewOption("Move to a new server with the configs as they are", "same"),
		huh.NewOption("Back", "back")).Value(&action).WithTheme(t.theme).Run(); err != nil || action == "back" {
		return err
	}
	if action == "backup" {
		return t.backup()
	}
	b, err := TakeBackup(t.r)
	if err != nil {
		return err
	}
	c, err := t.askServer("New server")
	if err != nil {
		return err
	}
	if c.Host == t.r.Conn.Host {
		return errors.New("the new server is this one")
	}
	fmt.Println(dimStyle.Render("connecting to " + c.Host + " ..."))
	nr, err := Dial(c, tuiPrompt)
	if err != nil {
		return err
	}
	defer nr.Close()
	bin, err := t.localXray()
	if err != nil {
		return err
	}
	o := TuneOpts{XrayBin: bin, Depth: "smart", Out: "xtune-results", Log: func(s string) { fmt.Println(s) }}
	fmt.Println(dimStyle.Render(fmt.Sprintf("moving %d users to %s; Ctrl+C stops and cleans up the new server", len(b.State.Users), nr.Name())))
	st, err := MoveTo(nr, b, action == "tune", 3, o, bin)
	if err != nil {
		return err
	}
	afterDeploy(nr, st)
	printNotes(st, t.r.Conn.Host)
	save := true
	huh.NewConfirm().Title("Save the new server as a profile?").Value(&save).WithTheme(t.theme).Run()
	if save {
		c.Password, c.Name = "", c.Host
		saveProfile(c)
	}
	pause()
	return nil
}

// askServer asks for another server's SSH details.
func (t *tui) askServer(title string) (Conn, error) {
	c := Conn{User: "root"}
	port, auth := "22", "key"
	err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title(title+": address").Value(&c.Host).Validate(func(s string) error {
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
		return c, err
	}
	c.Host, c.Port = strings.TrimSpace(c.Host), atoiOr(port, 22)
	if auth == "password" {
		c.Password, err = tuiPrompt("SSH password", true)
	} else {
		err = huh.NewInput().Title("Private key path (empty = default keys / agent)").Value(&c.KeyPath).WithTheme(t.theme).Run()
	}
	return c, err
}

func (t *tui) selfUpdate() error {
	tag, err := LatestXtune()
	if err != nil {
		return err
	}
	if !newer(tag, version) && version != "dev" {
		fmt.Println(okStyle.Render("xtune is current: " + version))
		pause()
		return nil
	}
	ok := true
	if err := huh.NewConfirm().Title(fmt.Sprintf("Update xtune %s to %s?", version, tag)).Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
		return err
	}
	if err := cmdSelfUpdate([]string{"--version", tag}); err != nil {
		return err
	}
	fmt.Println(dimStyle.Render("restart xtune to use the new version"))
	pause()
	return nil
}

// ---------------------------------------------------------------- server

func (t *tui) warp() error {
	st, err := requireState(t.r)
	if err != nil {
		return err
	}
	fmt.Println(titleStyle.Render("\nWARP") + "  " + st.Warp.describe())
	fmt.Println(dimStyle.Render("Many services refuse datacenter IPs or whole countries. With WARP, Xray reaches them\n" +
		"through Cloudflare's free WARP network instead of this server's IP."))
	choice := "ai"
	opts := []huh.Option[string]{huh.NewOption("On for AI services ("+strings.Join(defaultWarpDomains, ", ")+")", "ai"),
		huh.NewOption("On for chosen sites ...", "custom"), huh.NewOption("On for all traffic", "all")}
	if st.Warp != nil {
		opts = append(opts, huh.NewOption("Off", "off"))
	}
	opts = append(opts, huh.NewOption("Back", "back"))
	if err := huh.NewSelect[string]().Title("WARP").Options(opts...).Value(&choice).WithTheme(t.theme).Run(); err != nil || choice == "back" {
		return err
	}
	var domains []string
	if choice == "custom" {
		list := ""
		if st.Warp != nil {
			list = strings.Join(st.Warp.Domains, ", ")
		}
		if err := huh.NewInput().Title("Sites, comma-separated").
			Description("Xray routing names: geosite:netflix, domain:example.com, full:www.example.com ...").
			Value(&list).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		for _, d := range strings.Split(list, ",") {
			if d = strings.TrimSpace(d); d != "" {
				domains = append(domains, d)
			}
		}
		if len(domains) == 0 {
			return errors.New("no sites given")
		}
	}
	fmt.Println(dimStyle.Render("restarting Xray: connected users reconnect by themselves"))
	st, err = SetWarp(t.r, choice != "off", domains, choice == "all")
	if err != nil {
		return err
	}
	fmt.Println(okStyle.Render("WARP: " + st.Warp.describe()))
	pause()
	return nil
}

func (t *tui) notify() error {
	st, err := requireState(t.r)
	if err != nil {
		return err
	}
	fmt.Println(titleStyle.Render("\nAlerts") + "  " + st.Notify.describe())
	fmt.Println(dimStyle.Render("Sent once each: a user at 80% and 100% of their quota, 3 days before and at expiry,\n" +
		"and when the deployed Xray stops answering."))
	n := st.Notify
	choice := "telegram"
	opts := []huh.Option[string]{huh.NewOption("Telegram bot", "telegram"), huh.NewOption("Webhook (Slack, Discord, ...)", "webhook")}
	if n.On() {
		opts = append(opts, huh.NewOption("Send a test alert", "test"), huh.NewOption("Turn off", "off"))
	}
	opts = append(opts, huh.NewOption("Back", "back"))
	if err := huh.NewSelect[string]().Title("Alerts").Options(opts...).Value(&choice).WithTheme(t.theme).Run(); err != nil || choice == "back" {
		return err
	}
	switch choice {
	case "telegram":
		tok, chat := "", n.TelegramChat
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Bot token").Description("Create a bot with @BotFather and paste its token.").
				EchoMode(huh.EchoModePassword).Value(&tok),
			huh.NewInput().Title("Chat ID").Description("Send /start to your bot, then open\n"+
				"https://api.telegram.org/bot<token>/getUpdates and copy chat.id").Value(&chat),
		)).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		if strings.TrimSpace(tok) != "" {
			n.TelegramToken = strings.TrimSpace(tok)
		}
		n.TelegramChat = strings.TrimSpace(chat)
		if n.TelegramToken == "" || n.TelegramChat == "" {
			return errors.New("both the token and the chat ID are needed")
		}
	case "webhook":
		u := ""
		if err := huh.NewInput().Title("Webhook URL").Description("Receives a JSON POST with text, content, event and server.").
			EchoMode(huh.EchoModePassword).Value(&u).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		n.Webhook = strings.TrimSpace(u)
	case "test":
		if err := sendAlert(n, st.Addr(), "test", "this is a test alert"); err != nil {
			return err
		}
		fmt.Println(okStyle.Render("sent"))
		pause()
		return nil
	case "off":
		n = Notify{}
	}
	if err := SetNotify(t.r, n); err != nil {
		return err
	}
	fmt.Println(okStyle.Render("alerts: " + n.describe()))
	pause()
	return nil
}
