package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Notify sends admin alerts (quota, expiry, Xray down) to Telegram and/or a webhook. The enforce
// job checks for them every minute and sends each alert once, until its condition clears.
type Notify struct {
	TelegramToken string `json:"telegramToken,omitempty"`
	TelegramChat  string `json:"telegramChat,omitempty"`
	Webhook       string `json:"webhook,omitempty"` // receives {"text","content","event","server"} as JSON
}

func (n Notify) On() bool { return n.TelegramToken != "" && n.TelegramChat != "" || n.Webhook != "" }

const expiringSoon = 3 * 24 * time.Hour

// telegramAPI is a variable so tests can point it at a local server.
var telegramAPI = "https://api.telegram.org"

// sendAlert delivers one alert to every configured channel. A variable, so tests can capture alerts.
var sendAlert = func(n Notify, server, event, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg := "xtune (" + server + "): " + text
	var errs []string
	post := func(u, ctype string, body []byte) {
		req, _ := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("User-Agent", "xtune/"+version)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			// the error text can contain the URL, which holds the token
			errs = append(errs, strings.ReplaceAll(err.Error(), n.TelegramToken, "***"))
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			errs = append(errs, fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
	}
	if n.TelegramToken != "" && n.TelegramChat != "" {
		form := url.Values{"chat_id": {n.TelegramChat}, "text": {msg}}
		post(telegramAPI+"/bot"+n.TelegramToken+"/sendMessage", "application/x-www-form-urlencoded", []byte(form.Encode()))
	}
	if n.Webhook != "" {
		// "text" suits Slack-style hooks, "content" Discord
		b, _ := json.Marshal(J{"text": msg, "content": msg, "event": event, "server": server})
		post(n.Webhook, "application/json", b)
	}
	if len(errs) > 0 {
		return fmt.Errorf("alert %s: %s", event, strings.Join(errs, "; "))
	}
	return nil
}

// alert sends text once while cond holds; when cond is false the alert is re-armed.
func alert(st *State, us *Usage, key string, cond bool, text string) {
	if !cond {
		delete(us.Notified, key)
		return
	}
	if us.Notified[key] != 0 || !st.Notify.On() {
		return
	}
	if err := sendAlert(st.Notify, st.Addr(), key, text); err != nil {
		fmt.Println(err) // retried at the next run
		return
	}
	if us.Notified == nil {
		us.Notified = map[string]int64{}
	}
	us.Notified[key] = time.Now().Unix()
}

// sendUserMsg tells a user something through the bot. A variable, so tests can capture messages.
var sendUserMsg = func(token string, chat int64, text string) error { return newTG(token).send(chat, text, nil) }

// userAlert is alert for a user with a linked Telegram account: sent once while cond holds.
func userAlert(st *State, us *Usage, key string, chat int64, cond bool, text string) {
	if !cond {
		delete(us.Notified, key)
		return
	}
	if us.Notified[key] != 0 {
		return
	}
	if err := sendUserMsg(st.Bot.Token, chat, text); err != nil {
		fmt.Println(strings.ReplaceAll(err.Error(), st.Bot.Token, "***")) // retried at the next run
		return
	}
	if us.Notified == nil {
		us.Notified = map[string]int64{}
	}
	us.Notified[key] = time.Now().Unix()
}

// xrayDownAfter is how many enforce runs in a row must fail before an alert: a restart is not one.
const xrayDownAfter = 3

// notifyXray alerts when the deployed Xray stops answering, and when it is back.
func notifyXray(st *State, us *Usage, err error) {
	if err != nil {
		us.Failures++
	} else {
		us.Failures = 0
	}
	down := us.Notified["xray-down"] != 0
	if err == nil && down && st.Notify.On() {
		if sendAlert(st.Notify, st.Addr(), "xray-up", "Xray is answering again") != nil {
			return
		}
	}
	msg := ""
	if err != nil {
		msg = "the deployed Xray is not answering (check: xtune logs): " + tail(err.Error(), 300)
	}
	alert(st, us, "xray-down", err != nil && us.Failures >= xrayDownAfter, msg)
}

// notifyUsers alerts at 80% and 100% of a user's quota and before and at their expiry.
func notifyUsers(st *State, us *Usage, now time.Time) {
	for _, u := range st.Users {
		uu := us.Users[u.Name]
		used := int64(0)
		if uu != nil {
			used = uu.Up + uu.Down
		}
		on := u.Enabled
		over := u.Quota > 0 && used >= u.Quota
		alert(st, us, u.Name+":quota80", on && u.Quota > 0 && used*10 >= u.Quota*8 && !over,
			fmt.Sprintf("%s has used %d%% of their %s quota", u.Name, used*100/max(u.Quota, 1), fmtBytes(u.Quota)))
		alert(st, us, u.Name+":quota", on && over,
			fmt.Sprintf("%s is over their %s quota and was disconnected", u.Name, fmtBytes(u.Quota)))
		exp := time.Unix(u.Expires, 0)
		expired := u.Expires > 0 && !now.Before(exp)
		alert(st, us, u.Name+":expiring", on && u.Expires > 0 && !expired && exp.Sub(now) < expiringSoon,
			fmt.Sprintf("%s expires on %s", u.Name, fmtExpiry(u.Expires, now)))
		alert(st, us, u.Name+":expired", on && expired, u.Name+" has expired and was disconnected")
		if st.Bot.On() && st.Bot.SelfService && u.Telegram != 0 {
			lang := orStr(st.Bot.Lang, "en")
			quota := fmtBytes(u.Quota)
			if lang == "fa" {
				quota = faDigits(quota)
			}
			tell := func(key string, cond bool, text string) { userAlert(st, us, u.Name+":tg-"+key, u.Telegram, cond, text) }
			tell("quota80", on && u.Quota > 0 && used*10 >= u.Quota*8 && !over, tr(lang, "u_quota80", int(used*100/max(u.Quota, 1)), quota))
			tell("quota", on && over, tr(lang, "u_quota", quota))
			tell("expiring", on && u.Expires > 0 && !expired && exp.Sub(now) < expiringSoon, tr(lang, "u_expiring", fmtDate(exp, lang)))
			tell("expired", on && expired, tr(lang, "u_expired"))
		}
	}
	// alerts of deleted users
	for k := range us.Notified {
		if name, _, ok := strings.Cut(k, ":"); ok {
			if _, u := st.User(name); u == nil {
				delete(us.Notified, k)
			}
		}
	}
}

// ---------------------------------------------------------------- settings

// SetNotify replaces the alert settings and sends a test alert when any channel is on.
func SetNotify(r *Server, n Notify) error {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	st.Notify = n
	if err := saveState(r, st); err != nil {
		return err
	}
	if n.On() {
		return sendAlert(n, st.Addr(), "test", "alerts are on")
	}
	return nil
}

func (n Notify) describe() string {
	var on []string
	if n.TelegramToken != "" && n.TelegramChat != "" {
		on = append(on, "Telegram chat "+n.TelegramChat)
	}
	if n.Webhook != "" {
		u, err := url.Parse(n.Webhook)
		host := "?"
		if err == nil {
			host = u.Host
		}
		on = append(on, "webhook on "+host) // the path is usually the secret
	}
	if len(on) == 0 {
		return "off"
	}
	return strings.Join(on, ", ")
}
