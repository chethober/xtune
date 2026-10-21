package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SetBot saves the bot settings and starts, restarts or stops the bot. A new token is checked
// first; the bot's username is returned.
func SetBot(r *Server, b Bot) (string, error) {
	name := ""
	if b.On() {
		var err error
		if name, err = newTG(b.Token).me(); err != nil {
			return "", fmt.Errorf("check the bot token: %w", err)
		}
	}
	if err := validLang(b.Lang); err != nil {
		return "", err
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return "", err
	}
	st.Bot = b
	if err := saveState(r, st); err != nil {
		return "", err
	}
	return name, restartBot(r, st)
}

// changeBot changes bot settings that the running bot reads on every update, without a restart.
func changeBot(r *Server, fn func(b *Bot) error) (*State, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	if !st.Bot.On() {
		return nil, errors.New("the bot is off: xtune bot enable")
	}
	if err := fn(&st.Bot); err != nil {
		return nil, err
	}
	return st, saveState(r, st)
}

// restartBot (re)starts the bot so it uses a new token, or stops it when it is off.
func restartBot(r *Server, st *State) error {
	if !st.Bot.On() {
		_, err := r.Helper("bot-disable")
		return err
	}
	out, err := r.Helper("bot-enable")
	if err != nil {
		return err
	}
	if !strings.HasSuffix(out, "active") || strings.HasSuffix(out, "inactive") {
		return fmt.Errorf("the Telegram bot did not start (see: xtune logs --unit xray-autotune-bot): %s", out)
	}
	return nil
}

// startServices starts the subscription server and the bot after a deploy or restore.
func startServices(r *Server, st *State) error {
	if err := restartSub(r, st); err != nil {
		return err
	}
	if st.Bot.On() {
		return restartBot(r, st)
	}
	return nil
}

// pairAdmin waits for someone to message the bot and asks whether to make them an admin, so
// nobody has to look up their user ID. It needs the bot to be stopped: only one getUpdates
// poller is allowed per token.
func pairAdmin(token string, wait time.Duration, log func(string), confirm func(tgUser) (bool, error)) (int64, error) {
	tg := newTG(token)
	name, err := tg.me()
	if err != nil {
		return 0, fmt.Errorf("check the bot token: %w", err)
	}
	if err := tg.call("deleteWebhook", J{}, nil); err != nil {
		return 0, err
	}
	start := time.Now()
	offset := int64(0)
	log(fmt.Sprintf("open https://t.me/%s in Telegram and press Start (or send it any message); waiting %s...", name, wait))
	for time.Since(start) < wait {
		ups, err := tg.updates(offset, 20)
		if err != nil {
			if strings.Contains(err.Error(), "Conflict") {
				return 0, errors.New("the bot is already running elsewhere; pass the admin IDs instead")
			}
			return 0, err
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			m := u.Message
			if m == nil || m.From == nil || m.Chat.Type != "private" || m.Date < start.Unix()-5 {
				continue
			}
			ok, err := confirm(*m.From)
			if err != nil {
				return 0, err
			}
			if ok {
				tg.updates(offset, 0) // marks the message as seen, so the bot does not answer it later
				tg.send(m.Chat.ID, "You are an admin of this xtune server. Send /help to see what I can do.", nil)
				return m.From.ID, nil
			}
		}
	}
	return 0, errors.New("no message arrived; pass your Telegram user ID with --admins instead")
}

// alertsToBot sends alerts to the first admin through the bot, when no alerts are set up yet.
func alertsToBot(r *Server, st *State, b Bot, log func(string)) error {
	if st.Notify.On() {
		return nil
	}
	n := Notify{TelegramToken: b.Token, TelegramChat: strconv.FormatInt(b.Admins[0], 10)}
	if err := SetNotify(r, n); err != nil {
		return err
	}
	log("alerts (quota, expiry, Xray down) now go to that admin too; `xtune notify off` turns them off")
	return nil
}

// ---------------------------------------------------------------- command

func cmdBot(args []string) error {
	if len(args) == 1 && args[0] == "run" {
		return RunBot()
	}
	if err := serverOnly("bot", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("bot", flag.ExitOnError)
	admins := fs.String("admins", "", "enable: Telegram user IDs with full access, comma-separated (default: message the bot to pair)")
	resName := fs.String("name", "", "resellers add: a name to show")
	resUsers := fs.Int("users", 0, "resellers add: most users they may have (0 = no limit)")
	resQuota := fs.Float64("quota", 0, "resellers add: total quota in GB they may give out (0 = no limit)")
	every := fs.Int("every", 24, "backup on: hours between backups")
	pos := parse(fs, args)
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	action := "status"
	if len(pos) > 0 {
		action = pos[0]
	}
	arg := func(i int) string {
		if i < len(pos) {
			return pos[i]
		}
		return ""
	}
	b := st.Bot
	show := func(st *State, err error) error {
		if err == nil {
			fmt.Println("Telegram bot:", st.Bot.describe())
		}
		return err
	}
	switch action {
	case "status":
		fmt.Println("Telegram bot:", b.describe())
		for _, res := range b.Resellers {
			fmt.Println("  reseller", res)
		}
		return nil
	case "enable":
		tok := os.Getenv("XTUNE_TELEGRAM_TOKEN")
		if tok == "" && b.Token == "" {
			label := "Telegram bot token (from @BotFather)"
			if st.Notify.TelegramToken != "" {
				label += ", empty = the alerts bot"
			}
			if tok, err = cliPrompt(label, true); err != nil {
				return err
			}
			tok = orStr(strings.TrimSpace(tok), st.Notify.TelegramToken)
		}
		if tok != "" {
			b.Token = strings.TrimSpace(tok)
		}
		if b.Token == "" {
			return errors.New("bot enable: a bot token is needed (create a bot with @BotFather)")
		}
		if *admins != "" {
			if b.Admins, err = parseIDs(*admins); err != nil {
				return err
			}
		} else if len(b.Admins) == 0 {
			id, err := pairAdmin(b.Token, 3*time.Minute, func(s string) { fmt.Println(s) }, func(u tgUser) (bool, error) {
				a, err := cliPrompt(fmt.Sprintf("Make %s (ID %d) an admin? [y/N]", u, u.ID), false)
				return strings.EqualFold(a, "y") || strings.EqualFold(a, "yes"), err
			})
			if err != nil {
				return err
			}
			b.Admins = []int64{id}
		}
	case "admins":
		ids, err := parseIDs(strings.Join(pos[1:], ","))
		if err != nil {
			return err
		}
		if len(ids) == 0 || b.Token == "" {
			return errors.New("bot admins: pass one or more user IDs, after `xtune bot enable`")
		}
		b.Admins = ids
	case "disable", "off":
		b = Bot{}
	case "lang":
		l := arg(1)
		if l == "" || validLang(l) != nil || l == "auto" {
			return errors.New("bot lang: en or fa")
		}
		return show(changeBot(r, func(b *Bot) error { b.Lang = l; return nil }))
	case "self-service":
		on := arg(1)
		if on != "on" && on != "off" {
			return errors.New("bot self-service on|off")
		}
		return show(changeBot(r, func(b *Bot) error { b.SelfService = on == "on"; return nil }))
	case "resellers":
		return cmdResellers(r, pos[1:], Reseller{Name: *resName, MaxUsers: *resUsers, QuotaGB: *resQuota})
	case "backup":
		switch arg(1) {
		case "on":
			if *every < 1 {
				return errors.New("bot backup on --every HOURS (1 or more)")
			}
			pass := st.Bot.Backup.Passphrase
			if pass == "" || os.Getenv("XTUNE_BACKUP_PASSWORD") != "" {
				if pass, err = newPassphrase(); err != nil {
					return err
				}
			}
			return show(changeBot(r, func(b *Bot) error { b.Backup = BotBackup{Hours: *every, Passphrase: pass}; return nil }))
		case "off":
			return show(changeBot(r, func(b *Bot) error { b.Backup.Hours = 0; return nil }))
		}
		return errors.New("bot backup on [--every HOURS] | off   (send one now: /backup in the bot)")
	default:
		return fmt.Errorf("unknown bot action %q", action)
	}
	name, err := SetBot(r, b)
	if err != nil {
		return err
	}
	if !b.On() {
		fmt.Println("Telegram bot: off")
		return nil
	}
	fmt.Printf("Telegram bot @%s: %s\n", name, b.describe())
	return alertsToBot(r, st, b, func(s string) { fmt.Println(s) })
}

func cmdResellers(r *Server, args []string, res Reseller) error {
	action := "list"
	if len(args) > 0 {
		action = args[0]
	}
	var id int64
	if action != "list" {
		if len(args) < 2 {
			return fmt.Errorf("bot resellers %s TELEGRAM_ID", action)
		}
		ids, err := parseIDs(args[1])
		if err != nil || len(ids) != 1 {
			return fmt.Errorf("%q is not a Telegram user ID", args[1])
		}
		id = ids[0]
	}
	st, err := changeBot(r, func(b *Bot) error {
		i := slices.IndexFunc(b.Resellers, func(x Reseller) bool { return x.ID == id })
		switch action {
		case "list":
		case "add", "set":
			if slices.Contains(b.Admins, id) {
				return errors.New("that ID is an admin")
			}
			res.ID = id
			if i >= 0 {
				b.Resellers[i] = res
			} else {
				b.Resellers = append(b.Resellers, res)
			}
		case "del", "rm":
			if i < 0 {
				return fmt.Errorf("no reseller %d", id)
			}
			b.Resellers = slices.Delete(b.Resellers, i, i+1)
		default:
			return fmt.Errorf("unknown resellers action %q (list, add, del)", action)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(st.Bot.Resellers) == 0 {
		fmt.Println("no resellers (xtune bot resellers add ID --users N --quota GB)")
	}
	for _, x := range st.Bot.Resellers {
		n := 0
		for _, u := range st.Users {
			if u.Owner == x.ID {
				n++
			}
		}
		fmt.Printf("%s; %d users now\n", x, n)
	}
	if action == "del" || action == "rm" {
		fmt.Println("their users stay; admins manage them now")
	}
	return nil
}
