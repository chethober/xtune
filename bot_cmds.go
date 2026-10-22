package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"
)

// ---------------------------------------------------------------- the daemon

// RunBot runs the bot until it fails; systemd starts it as `xtune bot run` and restarts it.
func RunBot() error {
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if !st.Bot.On() {
		return errors.New("the Telegram bot is turned off (xtune bot enable)")
	}
	b := &botServer{r: r, tg: newTG(st.Bot.Token), now: time.Now}
	if b.name, err = b.tg.me(); err != nil {
		return err
	}
	// getUpdates does not work while a webhook is set
	if err := b.tg.call("deleteWebhook", J{}, nil); err != nil {
		return err
	}
	if err := b.tg.call("setMyCommands", J{"commands": botCommands(st.Bot.Lang)}, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Printf("telegram bot @%s running: %s\n", b.name, st.Bot.describe())
	offset := int64(0)
	for {
		ups, err := b.tg.updates(offset, 50)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			time.Sleep(5 * time.Second)
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			b.handle(u)
		}
		b.maybeBackup()
	}
}

func botCommands(lang string) []J {
	var out []J
	for _, c := range []string{"users", "user", "add", "renew", "links", "status", "help"} {
		out = append(out, J{"command": c, "description": tr(lang, "cmd_"+c)})
	}
	return out
}

// staleAfter is how old a message may be when the bot sees it: commands sent while it was down
// for longer are dropped rather than run late.
const staleAfter = 10 * time.Minute

type botServer struct {
	r       *Server
	tg      *tgClient
	now     func() time.Time
	name    string    // the bot's username, for invite links
	triedAt time.Time // last scheduled backup attempt
}

type role int

const (
	roleUser role = iota // self-service, when on
	roleReseller
	roleAdmin
)

// botReq is one update being answered: who sent it, in which language.
type botReq struct {
	*botServer
	st   *State
	from tgUser
	role role
	res  *Reseller
	lang string
}

func (q *botReq) t(key string, args ...any) string { return tr(q.lang, key, args...) }

// num writes a formatted number in the bot's language.
func (q *botReq) num(s string) string {
	if q.lang == "fa" {
		return faDigits(s)
	}
	return s
}

// reply is one message: HTML text with optional buttons, or a photo or file with text as caption.
type reply struct {
	text     string
	kb       [][]btn
	photo    []byte
	file     []byte
	fileName string
}

func (b *botServer) handle(u tgUpdate) {
	var from tgUser
	var chat, msgID int64
	var text string
	private := false
	switch {
	case u.Message != nil && u.Message.From != nil:
		m := u.Message
		if b.now().Sub(time.Unix(m.Date, 0)) > staleAfter {
			return
		}
		from, chat, text, private = *m.From, m.Chat.ID, m.Text, m.Chat.Type == "private"
	case u.Callback != nil && u.Callback.Message != nil:
		cb := u.Callback
		from, chat, msgID, text = cb.From, cb.Message.Chat.ID, cb.Message.MessageID, cb.Data
		private = cb.Message.Chat.Type == "private" || cb.Message.Chat.ID == cb.From.ID
		b.tg.call("answerCallbackQuery", J{"callback_query_id": cb.ID}, nil)
	default:
		return
	}
	st, err := LoadState(b.r)
	if err != nil || st == nil {
		fmt.Fprintln(os.Stderr, "load state:", err)
		return
	}
	q := &botReq{botServer: b, st: st, from: from, lang: orStr(st.Bot.Lang, "en")}
	switch {
	case slices.Contains(st.Bot.Admins, from.ID):
		q.role = roleAdmin
	case st.Bot.reseller(from.ID) != nil:
		q.role, q.res = roleReseller, st.Bot.reseller(from.ID)
	}
	var replies []reply
	if q.role == roleUser {
		if !private {
			return
		}
		replies, err = q.selfService(text)
	} else {
		if msgID == 0 && !strings.HasPrefix(text, "/") {
			if !private {
				return // group chatter
			}
			text = "help"
		}
		name, args := parseCommand(text)
		if name == "" {
			return
		}
		if mutates[name] {
			fmt.Printf("%d %s: %s %s\n", from.ID, from, name, strings.Join(args, " "))
		}
		replies, err = q.run(name, args)
	}
	if err != nil {
		replies = []reply{{text: "⚠️ " + esc(err.Error())}}
	}
	for i, rp := range replies {
		switch {
		case rp.photo != nil:
			err = b.tg.sendPhoto(chat, rp.photo, rp.text)
		case rp.file != nil:
			err = b.tg.sendDocument(chat, rp.fileName, rp.file, rp.text)
		case i == 0 && msgID != 0 && len(replies) == 1:
			err = b.tg.edit(chat, msgID, rp.text, rp.kb) // a button updates its own message
		default:
			err = b.tg.send(chat, rp.text, rp.kb)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
}

// parseCommand splits "/limit@xtune_bot carol quota=5" into "limit" and its arguments. Button
// data has the same form, without the slash.
func parseCommand(s string) (string, []string) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return "", nil
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(f[0], "/"), "@")
	return strings.ToLower(name), f[1:]
}

// mutates are the commands that change something; they are logged with the sender.
var mutates = map[string]bool{"add": true, "limit": true, "renew": true, "enable": true, "disable": true,
	"resetusage": true, "rotate!": true, "resetsub!": true, "del!": true, "backup": true}

// confirmed are the commands that break a user's links or delete them: they ask first.
var confirmed = map[string]string{"rotate": "confirm_rotate", "resetsub": "confirm_resetsub", "del": "confirm_del"}

// userCommands take a user name.
var userCommands = map[string]bool{"user": true, "add": true, "limit": true, "renew": true, "enable": true,
	"disable": true, "resetusage": true, "links": true, "rotate": true, "resetsub": true, "del": true, "invite": true,
	"rotate!": true, "resetsub!": true, "del!": true}

func (q *botReq) run(cmd string, args []string) ([]reply, error) {
	r := q.r
	switch cmd {
	case "start", "help":
		return []reply{{text: q.help()}}, nil
	case "status":
		return q.status()
	case "users":
		page := 0
		if len(args) > 0 {
			page, _ = strconv.Atoi(args[0])
		}
		return q.users(page)
	case "plans":
		return q.plans(), nil
	case "resellers":
		if q.role != roleAdmin {
			break
		}
		return q.resellers(), nil
	case "backup":
		if q.role != roleAdmin {
			break
		}
		rp, err := q.backup()
		if err != nil {
			return nil, err
		}
		return []reply{rp}, nil
	}
	if !userCommands[cmd] {
		return nil, errors.New(q.t("err_unknown", cmd))
	}
	if len(args) == 0 {
		return nil, errors.New(q.t("err_needs_name", cmd, cmd))
	}
	name := args[0]
	if cmd != "add" {
		if _, u := q.st.User(name); u == nil || !q.manages(*u) {
			return nil, errors.New(q.t("err_no_user", name))
		}
	}
	if key, ok := confirmed[cmd]; ok {
		if _, ok := data(cmd+"!", name); !ok {
			return nil, errors.New(q.t("err_name_long"))
		}
		return []reply{{text: q.t(key, "<b>"+esc(name)+"</b>"),
			kb: [][]btn{{{Text: q.t("b_yes"), Data: cmd + "! " + name}, {Text: q.t("b_cancel"), Data: "user " + name}}}}}, nil
	}
	var err error
	done := ""
	switch cmd {
	case "user":
	case "add":
		l, err := q.addLimits(args[1:])
		if err != nil {
			return nil, err
		}
		if err := q.resellerRoom(name, l, true); err != nil {
			return nil, err
		}
		if _, _, err = AddUser(r, name, l); err != nil {
			return nil, err
		}
		return q.links(name, q.t("added", esc(name))+"\n\n")
	case "limit":
		var l Limits
		if l, err = parseLimits(args[1:], false); err != nil {
			return nil, err
		}
		if l == (Limits{}) {
			return nil, errors.New(q.t("err_limit_usage"))
		}
		if err := q.resellerRoom(name, l, false); err != nil {
			return nil, err
		}
		err, done = noState(SetLimits(r, name, l)), q.t("done_limits")
	case "renew":
		plan := ""
		if len(args) > 1 {
			plan = args[1]
			if err := q.resellerRoom(name, Limits{Plan: &plan}, false); err != nil {
				return nil, err
			}
		}
		var u User
		if _, u, err = Renew(r, name, plan, 0); err == nil {
			done = q.t("renewed", q.expiry(u.Expires))
		}
	case "enable", "disable":
		err, done = SetUserEnabled(r, name, cmd == "enable"), q.t("done_"+cmd)
	case "resetusage":
		err, done = ResetUsage(r, name), q.t("done_resetusage")
	case "links":
		return q.links(name, "")
	case "invite":
		return q.invite(name)
	case "rotate!":
		if _, _, err = RotateUser(r, name); err != nil {
			return nil, err
		}
		return q.links(name, q.t("new_keys", esc(name))+"\n\n")
	case "resetsub!":
		if _, err = ResetSubLink(r, name); err != nil {
			return nil, err
		}
		return q.links(name, q.t("new_sub", esc(name))+"\n\n")
	case "del!":
		if err = DeleteUser(r, name); err != nil {
			return nil, err
		}
		return []reply{{text: q.t("deleted", esc(name)), kb: [][]btn{{{Text: q.t("b_users"), Data: "users"}}}}}, nil
	}
	if err != nil {
		return nil, err
	}
	return q.card(name, done)
}

func noState(_ *State, err error) error { return err }

func (q *botReq) help() string {
	s := q.t("help")
	if q.role == roleAdmin {
		s += "\n" + q.t("help_admin")
	}
	return s + "\n\n" + q.t("help_examples")
}

// manages reports whether the sender may see and change u.
func (q *botReq) manages(u User) bool {
	return q.role == roleAdmin || q.role == roleReseller && u.Owner == q.res.ID
}

// visible are the users the sender manages.
func (q *botReq) visible() []User {
	var out []User
	for _, u := range q.st.Users {
		if q.manages(u) {
			out = append(out, u)
		}
	}
	return out
}

// addLimits reads /add's arguments: a plan name and/or limits.
func (q *botReq) addLimits(args []string) (Limits, error) {
	plan := ""
	if len(args) > 0 {
		if _, ok := q.st.Plans[args[0]]; ok {
			plan, args = args[0], args[1:]
		}
	}
	l, err := parseLimits(args, plan == "")
	if err != nil {
		return l, err
	}
	if plan != "" {
		l.Plan = &plan
	}
	if q.role == roleReseller {
		l.Owner = &q.res.ID
	}
	return l, nil
}

// resellerRoom checks a reseller's change against their user count and total quota.
func (q *botReq) resellerRoom(name string, l Limits, adding bool) error {
	if q.role != roleReseller {
		return nil
	}
	l, err := l.withPlan(q.st)
	if err != nil {
		return err
	}
	res := q.res
	owned, used, cur := 0, int64(0), int64(0)
	for _, u := range q.st.Users {
		if u.Owner != res.ID {
			continue
		}
		owned++
		if u.Name == name {
			cur = u.Quota
		} else {
			used += u.Quota
		}
	}
	if adding && res.MaxUsers > 0 && owned >= res.MaxUsers {
		return errors.New(q.t("err_res_users", res.MaxUsers))
	}
	if res.QuotaGB <= 0 {
		return nil
	}
	quota := cur
	if l.QuotaGB != nil {
		quota = int64(*l.QuotaGB * (1 << 30))
	}
	total := int64(res.QuotaGB * (1 << 30))
	if quota <= 0 {
		return errors.New(q.t("err_res_unlimited"))
	}
	if used+quota > total {
		return errors.New(q.t("err_res_quota", q.num(fmtBytes(max(0, total-used)))))
	}
	return nil
}

func esc(s string) string { return html.EscapeString(s) }

// data is a button's callback data; Telegram allows 64 bytes, so a long user name gets no button.
func data(cmd, name string) (string, bool) {
	d := cmd + " " + name
	return d, len(d) <= 64
}

func (q *botReq) usage() (*Usage, error) { return LoadUsage(q.r) }

func statusIcon(s string) string {
	switch s {
	case "active":
		return "🟢"
	case "disabled":
		return "⚪️"
	}
	return "🔴"
}

func orUsage(uu *UserUsage) *UserUsage {
	if uu == nil {
		return &UserUsage{}
	}
	return uu
}

// expiry is an expiry date in the bot's language and calendar.
func (q *botReq) expiry(ts int64) string {
	if ts == 0 {
		return q.t("never")
	}
	t, now := time.Unix(ts, 0), q.now()
	if now.After(t) {
		return q.t("expired_on", fmtDate(t, q.lang))
	}
	return q.t("days_left", fmtDate(t, q.lang), int(t.Sub(now).Hours()/24))
}

const usersPerPage = 20

func (q *botReq) users(page int) ([]reply, error) {
	us, err := q.usage()
	if err != nil {
		return nil, err
	}
	users := q.visible()
	if len(users) == 0 {
		return []reply{{text: q.t("no_users")}}, nil
	}
	pages := (len(users) + usersPerPage - 1) / usersPerPage
	page = max(0, min(page, pages-1))
	now := q.now()
	var sb strings.Builder
	sb.WriteString(q.t("users_title", len(users)))
	if pages > 1 {
		sb.WriteString(q.t("page", page+1, pages))
	}
	sb.WriteString("\n\n")
	var kb [][]btn
	var row []btn
	for _, u := range users[page*usersPerPage : min(len(users), (page+1)*usersPerPage)] {
		uu := orUsage(us.Users[u.Name])
		used := q.num(fmtBytes(uu.Up + uu.Down))
		if u.Quota > 0 {
			used += " / " + q.num(fmtBytes(u.Quota))
		}
		online := ""
		if len(uu.Online) > 0 {
			online = q.t("online_n", len(uu.Online))
		}
		fmt.Fprintf(&sb, "%s <b>%s</b> · %s%s\n", statusIcon(userStatus(u, uu, now)), esc(u.Name), used, online)
		if d, ok := data("user", u.Name); ok {
			if row = append(row, btn{Text: u.Name, Data: d}); len(row) == 3 {
				kb, row = append(kb, row), nil
			}
		}
	}
	if row != nil {
		kb = append(kb, row)
	}
	var nav []btn
	if page > 0 {
		nav = append(nav, btn{Text: q.t("b_prev"), Data: fmt.Sprintf("users %d", page-1)})
	}
	if page < pages-1 {
		nav = append(nav, btn{Text: q.t("b_next"), Data: fmt.Sprintf("users %d", page+1)})
	}
	if nav != nil {
		kb = append(kb, nav)
	}
	return []reply{{text: sb.String(), kb: kb}}, nil
}

// cardText describes one user: status, usage, limits and the last 7 days.
func (q *botReq) cardText(u User, uu *UserUsage) string {
	now := q.now()
	s := userStatus(u, uu, now)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s <b>%s</b> · %s\n\n", statusIcon(s), esc(u.Name), q.t("st_"+s))
	used := q.num(fmtBytes(uu.Up + uu.Down))
	if u.Quota > 0 {
		used = q.t("used_of", used, q.num(fmtBytes(u.Quota)), int((uu.Up+uu.Down)*100/u.Quota))
	} else {
		used += " " + q.t("no_quota")
	}
	sb.WriteString(q.t("used", used) + "\n")
	if u.Reset != "" && u.ResetAt > 0 {
		sb.WriteString(q.t("resets", q.t(u.Reset, fmtDate(time.Unix(u.ResetAt, 0), q.lang))) + "\n")
	}
	sb.WriteString(q.t("expires", q.expiry(u.Expires)) + "\n")
	devices := q.t("devices_online", len(uu.Online))
	if u.Devices > 0 {
		devices += q.t("devices_limit", u.Devices)
	}
	sb.WriteString(q.t("devices", devices) + "\n")
	last := q.t("never")
	if len(uu.Online) > 0 {
		last = q.t("now")
	} else if uu.LastSeen > 0 {
		t := time.Unix(uu.LastSeen, 0)
		last = fmtDate(t, q.lang) + " " + q.num(t.Format("15:04"))
	}
	sb.WriteString(q.t("last_seen", last) + "\n")
	days := uu.lastDays(now, 7)
	week := int64(0)
	for _, v := range days {
		week += v
	}
	sb.WriteString(q.t("last7", sparkline(days), q.num(fmtBytes(week))) + "\n")
	if u.Plan != "" {
		sb.WriteString(q.t("plan", esc(u.Plan)) + "\n")
	}
	if q.role == roleAdmin && u.Owner != 0 {
		owner := strconv.FormatInt(u.Owner, 10)
		if res := q.st.Bot.reseller(u.Owner); res != nil && res.Name != "" {
			owner = res.Name
		}
		sb.WriteString(q.t("owner", esc(owner)) + "\n")
	}
	return sb.String()
}

// card shows one user with buttons for the common actions; note is shown above it.
func (q *botReq) card(name, note string) ([]reply, error) {
	st, err := requireState(q.r) // after a change
	if err != nil {
		return nil, err
	}
	q.st = st
	us, err := q.usage()
	if err != nil {
		return nil, err
	}
	_, u := st.User(name)
	if u == nil || !q.manages(*u) {
		return nil, errors.New(q.t("err_no_user", name))
	}
	text := q.cardText(*u, orUsage(us.Users[u.Name]))
	if note != "" {
		text = note + "\n\n" + text
	}
	text += "\n<i>" + q.t("hint_limit", esc(u.Name)) + "</i>"
	var kb [][]btn
	if _, ok := data("resetsub!", u.Name); ok {
		n := u.Name
		toggle := btn{Text: q.t("b_disable"), Data: "disable " + n}
		if !u.Enabled {
			toggle = btn{Text: q.t("b_enable"), Data: "enable " + n}
		}
		kb = [][]btn{
			{{Text: q.t("b_links"), Data: "links " + n}, toggle},
			{{Text: q.t("b_renew"), Data: "renew " + n}, {Text: q.t("b_resetusage"), Data: "resetusage " + n}},
			{{Text: q.t("b_rotate"), Data: "rotate " + n}, {Text: q.t("b_delete"), Data: "del " + n}},
		}
		var extra []btn
		if st.Sub.Mode != "" {
			extra = append(extra, btn{Text: q.t("b_resetsub"), Data: "resetsub " + n})
		}
		if st.Bot.SelfService && q.name != "" {
			extra = append(extra, btn{Text: q.t("b_invite"), Data: "invite " + n})
		}
		if extra != nil {
			kb = append(kb, extra)
		}
	}
	kb = append(kb, []btn{{Text: q.t("b_users"), Data: "users"}})
	return []reply{{text: text, kb: kb}}, nil
}

// links sends the user's subscription link and configs as tap-to-copy text, and a QR code of the
// subscription link (or of each config when the subscription server is off).
func (q *botReq) links(name, note string) ([]reply, error) {
	st, err := requireState(q.r)
	if err != nil {
		return nil, err
	}
	_, u := st.User(name)
	if u == nil {
		return nil, errors.New(q.t("err_no_user", name))
	}
	var out []reply
	var sb strings.Builder
	sb.WriteString(note)
	fmt.Fprintf(&sb, "<b>%s</b>", esc(u.Name))
	if !u.Enabled {
		sb.WriteString(q.t("disabled_tag"))
	}
	sb.WriteString("\n\n")
	sub := st.SubURL(*u)
	if sub != "" {
		fmt.Fprintf(&sb, "%s\n<code>%s</code>\n\n", q.t("sub_link"), esc(sub))
	}
	flush := func() {
		if sb.Len() > 0 {
			out = append(out, reply{text: sb.String()})
			sb.Reset()
		}
	}
	for _, l := range st.NamedLinks(*u) {
		item := fmt.Sprintf("%s:\n<code>%s</code>\n\n", esc(l.Name), esc(l.URL))
		if sb.Len()+len(item) > 4000 { // Telegram's limit is 4096 characters a message
			flush()
		}
		sb.WriteString(item)
	}
	if st.hasLogin() {
		fmt.Fprintf(&sb, "SSH / VPN: <code>%s</code> / <code>%s</code>\n", esc(u.Name), esc(tunnelPassword(*u)))
	}
	flush()
	for _, p := range st.Profiles(*u) { // OpenVPN, IKEv2, WireGuard and DNS tunnel apps import files
		out = append(out, reply{file: []byte(p.Body), fileName: p.File, text: esc(p.Name)})
	}
	qrOf := func(text, caption string) {
		c, err := qr.Encode(text, qr.M)
		if err != nil {
			return
		}
		c.Scale = 8
		out = append(out, reply{photo: c.PNG(), text: caption})
	}
	if sub != "" {
		qrOf(sub, q.t("qr_sub", esc(u.Name)))
	} else {
		for _, l := range st.NamedLinks(*u) {
			qrOf(l.URL, esc(u.Name)+": "+esc(l.Name))
		}
	}
	return out, nil
}

// invite is a t.me link that links the user's Telegram to their account when they open it.
func (q *botReq) invite(name string) ([]reply, error) {
	_, u := q.st.User(name)
	if !q.st.Bot.SelfService || q.name == "" {
		return nil, errors.New(q.t("err_no_selfservice"))
	}
	link := "https://t.me/" + q.name + "?start=" + u.SubToken
	return []reply{{text: q.t("invite", esc(name), esc(link)), kb: [][]btn{{{Text: q.t("b_back"), Data: "user " + name}}}}}, nil
}

func (q *botReq) status() ([]reply, error) {
	st := q.st
	us, err := q.usage()
	if err != nil {
		return nil, err
	}
	now := q.now()
	var sb strings.Builder
	if q.role == roleReseller {
		users := q.visible()
		var quota, used int64
		for _, u := range users {
			quota += u.Quota
			uu := orUsage(us.Users[u.Name])
			used += uu.Up + uu.Down
		}
		limit, qlimit := q.t("unlimited"), q.t("unlimited")
		if q.res.MaxUsers > 0 {
			limit = strconv.Itoa(q.res.MaxUsers)
		}
		if q.res.QuotaGB > 0 {
			qlimit = fmtBytes(int64(q.res.QuotaGB * (1 << 30)))
		}
		sb.WriteString(q.t("res_status", len(users), q.num(limit), q.num(fmtBytes(quota)), q.num(qlimit), q.num(fmtBytes(used))))
		return []reply{{text: sb.String(), kb: [][]btn{{{Text: q.t("b_users"), Data: "users"}}}}}, nil
	}
	ss, _ := q.r.Status()
	active, online := 0, 0
	var total int64
	for _, u := range st.Users {
		uu := orUsage(us.Users[u.Name])
		if userStatus(u, uu, now) == "active" {
			active++
		}
		if len(uu.Online) > 0 {
			online++
		}
		total += uu.Up + uu.Down
	}
	fmt.Fprintf(&sb, "<b>%s</b>\n\n", esc(st.Addr()))
	fmt.Fprintf(&sb, "Xray: %s (%s)\n", orStr(ss.Unit, "?"), orStr(ss.Xray, "?"))
	sb.WriteString(q.t("users_line", len(st.Users), active, online) + "\n")
	sb.WriteString(q.t("traffic", q.num(fmtBytes(total))) + "\n")
	sub := q.t("off")
	if st.Sub.Mode != "" {
		sub = st.Sub.Mode + ", " + orStr(ss.Sub, "?")
	}
	sb.WriteString(q.t("sub_server", esc(sub)) + "\n\n")
	sb.WriteString(q.t("configs") + "\n")
	quiet := quietEntries(st, us, now)
	for _, s := range st.servers() {
		for _, e := range s.Entries {
			line := "• " + esc(e.ID) + ": "
			eu := us.Entries[entryKey(e)]
			switch {
			case s != st:
				line += q.t("follower")
			case quiet[e.ID] > 0:
				line += "⚠️ " + q.t("quiet", q.num(fmtSince(quiet[e.ID])))
			case eu != nil && eu.Active > 0:
				line += q.t("active_ago", q.num(fmtSince(now.Sub(time.Unix(eu.Active, 0)))), q.num(fmtBytes(eu.Bytes)))
			default:
				line += q.t("no_traffic")
			}
			sb.WriteString(line + "\n")
		}
	}
	fmt.Fprintf(&sb, "\nxtune %s", esc(version))
	return []reply{{text: sb.String(), kb: [][]btn{{{Text: q.t("b_users"), Data: "users"}}}}}, nil
}

func (q *botReq) plans() []reply {
	if len(q.st.Plans) == 0 {
		return []reply{{text: q.t("no_plans")}}
	}
	var ns []string
	for n := range q.st.Plans {
		ns = append(ns, n)
	}
	slices.Sort(ns)
	var sb strings.Builder
	sb.WriteString(q.t("plans_title") + "\n\n")
	for _, n := range ns {
		fmt.Fprintf(&sb, "<b>%s</b>: %s\n", esc(n), esc(q.st.Plans[n].String()))
	}
	return []reply{{text: sb.String()}}
}

func (q *botReq) resellers() []reply {
	if len(q.st.Bot.Resellers) == 0 {
		return []reply{{text: q.t("no_resellers")}}
	}
	var sb strings.Builder
	for _, res := range q.st.Bot.Resellers {
		n := 0
		for _, u := range q.st.Users {
			if u.Owner == res.ID {
				n++
			}
		}
		fmt.Fprintf(&sb, "<code>%s</code> · %d users\n", esc(res.String()), n)
	}
	return []reply{{text: sb.String()}}
}

// ---------------------------------------------------------------- self-service

var tokenRe = regexp.MustCompile(`[0-9a-f]{32}`)

// byToken finds the user whose subscription token is in text.
func (st *State) byToken(text string) *User {
	for _, tok := range tokenRe.FindAllString(text, 3) {
		for i := range st.Users {
			if t := st.Users[i].SubToken; t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
				return &st.Users[i]
			}
		}
	}
	return nil
}

// selfService answers users: linking with their subscription link (or an invite's /start
// TOKEN), then their own cards and links.
func (q *botReq) selfService(text string) ([]reply, error) {
	id := strconv.FormatInt(q.from.ID, 10)
	if !q.st.Bot.SelfService {
		// the sender's ID is what an admin needs to add them; nothing else is said
		return []reply{{text: q.t("not_authorized", id)}}, nil
	}
	if u := q.st.byToken(text); u != nil {
		name := u.Name
		if _, err := changeUser(q.r, name, func(_ *State, u *User) error { u.Telegram = q.from.ID; return nil }); err != nil {
			return nil, err
		}
		fmt.Printf("%d %s: linked to %s\n", q.from.ID, q.from, name)
		st, err := requireState(q.r)
		if err != nil {
			return nil, err
		}
		q.st = st
		cards, err := q.myCards()
		if err != nil {
			return nil, err
		}
		cards[0].text = q.t("linked", esc(name)) + "\n\n" + cards[0].text
		return cards, nil
	}
	var mine []string
	for _, u := range q.st.Users {
		if u.Telegram == q.from.ID {
			mine = append(mine, u.Name)
		}
	}
	if len(mine) == 0 {
		return []reply{{text: q.t("send_link", id)}}, nil
	}
	cmd, args := parseCommand(text)
	switch cmd {
	case "links":
		name := mine[0]
		if len(args) > 0 && slices.Contains(mine, args[0]) {
			name = args[0]
		}
		return q.links(name, "")
	case "unlink":
		for _, n := range mine {
			if _, err := changeUser(q.r, n, func(_ *State, u *User) error { u.Telegram = 0; return nil }); err != nil {
				return nil, err
			}
		}
		return []reply{{text: q.t("unlinked")}}, nil
	case "help", "start":
		return []reply{{text: q.t("help_user")}}, nil
	}
	return q.myCards()
}

// myCards are the cards of the users linked to the sender.
func (q *botReq) myCards() ([]reply, error) {
	us, err := q.usage()
	if err != nil {
		return nil, err
	}
	var out []reply
	for _, u := range q.st.Users {
		if u.Telegram != q.from.ID {
			continue
		}
		var kb [][]btn
		if d, ok := data("links", u.Name); ok {
			kb = [][]btn{{{Text: q.t("b_links"), Data: d}, {Text: q.t("b_refresh"), Data: "me"}}}
		}
		out = append(out, reply{text: q.cardText(u, orUsage(us.Users[u.Name])), kb: kb})
	}
	if out == nil {
		return []reply{{text: q.t("send_link", strconv.FormatInt(q.from.ID, 10))}}, nil
	}
	return out, nil
}

// ---------------------------------------------------------------- backups

// backup is an encrypted backup to send now.
func (q *botReq) backup() (reply, error) {
	if q.st.Bot.Backup.Passphrase == "" {
		return reply{}, errors.New(q.t("err_no_backup_pass"))
	}
	bk, err := TakeBackup(q.r)
	if err != nil {
		return reply{}, err
	}
	b, err := backupBytes(bk, q.st.Bot.Backup.Passphrase)
	if err != nil {
		return reply{}, err
	}
	return reply{file: b, fileName: backupName(q.st, q.now()), text: q.t("backup_caption", esc(q.st.Addr()), len(bk.State.Users))}, nil
}

// maybeBackup sends admins the scheduled backup when it is due. A failed attempt is retried in
// an hour.
func (b *botServer) maybeBackup() {
	st, err := LoadState(b.r)
	if err != nil || st == nil || !st.Bot.On() || st.Bot.Backup.Hours <= 0 || st.Bot.Backup.Passphrase == "" {
		return
	}
	now := b.now()
	if now.Sub(b.triedAt) < time.Hour {
		return
	}
	us, err := LoadUsage(b.r)
	if err != nil || now.Unix()-us.BackupAt < int64(st.Bot.Backup.Hours)*3600 {
		return
	}
	b.triedAt = now
	q := &botReq{botServer: b, st: st, role: roleAdmin, lang: orStr(st.Bot.Lang, "en")}
	rp, err := q.backup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		return
	}
	sent := false
	for _, id := range st.Bot.Admins {
		if err := b.tg.sendDocument(id, rp.fileName, rp.file, rp.text); err != nil {
			fmt.Fprintln(os.Stderr, "backup:", err)
		} else {
			sent = true
		}
	}
	if !sent {
		return
	}
	defer b.r.lock()()
	if us, err = LoadUsage(b.r); err == nil {
		us.BackupAt = now.Unix()
		err = saveUsage(b.r, us)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
	}
}

// ---------------------------------------------------------------- limits

// parseLimits reads key=value limits, or with positional, plain numbers in the order quota, days,
// devices (as in /add carol 50 30 2).
func parseLimits(args []string, positional bool) (Limits, error) {
	var l Limits
	order := []string{"quota", "days", "devices"}
	for _, a := range args {
		k, v, ok := strings.Cut(strings.ToLower(a), "=")
		if !ok {
			if !positional || len(order) == 0 {
				return l, fmt.Errorf("%q: use key=value (quota, days, devices, reset)", a)
			}
			k, v, order = order[0], a, order[1:]
		}
		switch k {
		case "quota", "gb":
			f, err := strconv.ParseFloat(strings.TrimSuffix(v, "gb"), 64)
			if err != nil || f < 0 {
				return l, fmt.Errorf("quota %q: a number of GB, 0 = unlimited", v)
			}
			l.QuotaGB = &f
		case "days", "devices":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return l, fmt.Errorf("%s %q: a whole number, 0 = unlimited", k, v)
			}
			if k == "days" {
				l.Days = &n
			} else {
				l.Devices = &n
			}
		case "reset":
			if v == "none" || v == "never" {
				v = ""
			}
			l.Reset = &v
		case "plan":
			l.Plan = &v
		default:
			return l, fmt.Errorf("unknown limit %q (quota, days, devices, reset, plan)", k)
		}
	}
	return l, nil
}
