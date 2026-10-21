package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The Telegram bot (systemd unit xray-autotune-bot) manages users from Telegram. It long-polls the
// Bot API, so the server opens no port. It answers:
//   - admins: everything, plus scheduled encrypted backups;
//   - resellers: only the users they added, within a user count and a total quota;
//   - users, when self-service is on: their own usage and links, once they link their Telegram
//     with their subscription link or an invite link, and warnings before their quota or time
//     runs out.
// Every change goes through the same functions as `xtune users`, under the server lock.

type Bot struct {
	Token       string     `json:"token,omitempty"`
	Admins      []int64    `json:"admins,omitempty"`      // Telegram user IDs with full access
	Lang        string     `json:"lang,omitempty"`        // en (default) | fa
	SelfService bool       `json:"selfService,omitempty"` // users can link their Telegram and see their usage
	Resellers   []Reseller `json:"resellers,omitempty"`
	Backup      BotBackup  `json:"backup,omitzero"`
}

// A Reseller manages their own users only, within limits. 0 = no limit.
type Reseller struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name,omitempty"`
	MaxUsers int     `json:"maxUsers,omitempty"`
	QuotaGB  float64 `json:"quotaGB,omitempty"` // total quota of their users; each then needs a quota
}

// BotBackup sends admins an encrypted backup every Hours.
type BotBackup struct {
	Hours      int    `json:"hours,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

func (b Bot) On() bool { return b.Token != "" && len(b.Admins) > 0 }

func (b Bot) reseller(id int64) *Reseller {
	for i := range b.Resellers {
		if b.Resellers[i].ID == id {
			return &b.Resellers[i]
		}
	}
	return nil
}

func (b Bot) describe() string {
	if !b.On() {
		return "off"
	}
	s := "on, admins " + fmtIDs(b.Admins)
	if len(b.Resellers) > 0 {
		s += fmt.Sprintf(", %d resellers", len(b.Resellers))
	}
	if b.SelfService {
		s += ", user self-service"
	}
	if b.Backup.Hours > 0 {
		s += fmt.Sprintf(", backups every %dh", b.Backup.Hours)
	}
	if b.Lang == "fa" {
		s += ", Persian"
	}
	return s
}

func (r Reseller) String() string {
	users, quota := "any number of users", "any quota"
	if r.MaxUsers > 0 {
		users = fmt.Sprintf("up to %d users", r.MaxUsers)
	}
	if r.QuotaGB > 0 {
		quota = fmt.Sprintf("%g GB in total", r.QuotaGB)
	}
	return fmt.Sprintf("%d %s: %s, %s", r.ID, orStr(r.Name, "-"), users, quota)
}

func fmtIDs(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(s, ", ")
}

// parseIDs reads comma- or space-separated Telegram user IDs.
func parseIDs(s string) ([]int64, error) {
	var ids []int64
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a Telegram user ID (a positive number)", f)
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ---------------------------------------------------------------- Bot API

type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

func (u tgUser) String() string {
	s := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.Username != "" {
		s += " (@" + u.Username + ")"
	}
	return s
}

type tgMessage struct {
	MessageID int64   `json:"message_id"`
	From      *tgUser `json:"from"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Date int64  `json:"date"`
	Text string `json:"text"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgUpdate struct {
	UpdateID int64       `json:"update_id"`
	Message  *tgMessage  `json:"message"`
	Callback *tgCallback `json:"callback_query"`
}

type btn struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

type tgClient struct {
	token string
	hc    *http.Client
}

func newTG(token string) *tgClient {
	return &tgClient{token: token, hc: &http.Client{Timeout: 70 * time.Second}}
}

// redact keeps the token, which is part of every API URL, out of error messages.
func (c *tgClient) redact(err error) error {
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "***"))
}

// call posts a Bot API method with JSON parameters and decodes its result into out.
func (c *tgClient) call(method string, params any, out any) error {
	b, _ := json.Marshal(params)
	return c.post(method, "application/json", bytes.NewReader(b), out)
}

func (c *tgClient) post(method, ctype string, body io.Reader, out any) error {
	req, _ := http.NewRequestWithContext(context.Background(), "POST", telegramAPI+"/bot"+c.token+"/"+method, body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "xtune/"+version)
	resp, err := c.hc.Do(req)
	if err != nil {
		return c.redact(err)
	}
	defer resp.Body.Close()
	var res struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&res); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d", method, resp.StatusCode)
	}
	if !res.OK {
		return fmt.Errorf("telegram %s: %s", method, orStr(res.Description, fmt.Sprintf("error %d", res.ErrorCode)))
	}
	if out != nil {
		return json.Unmarshal(res.Result, out)
	}
	return nil
}

// me checks the token and returns the bot's username.
func (c *tgClient) me() (string, error) {
	var u tgUser
	if err := c.call("getMe", J{}, &u); err != nil {
		return "", err
	}
	return u.Username, nil
}

func (c *tgClient) updates(offset int64, wait int) ([]tgUpdate, error) {
	var ups []tgUpdate
	err := c.call("getUpdates", J{"offset": offset, "timeout": wait,
		"allowed_updates": []string{"message", "callback_query"}}, &ups)
	return ups, err
}

func markup(kb [][]btn) J {
	if kb == nil {
		kb = [][]btn{}
	}
	return J{"inline_keyboard": kb}
}

func (c *tgClient) send(chat int64, text string, kb [][]btn) error {
	return c.call("sendMessage", J{"chat_id": chat, "text": text, "parse_mode": "HTML",
		"link_preview_options": J{"is_disabled": true}, "reply_markup": markup(kb)}, nil)
}

func (c *tgClient) edit(chat, msg int64, text string, kb [][]btn) error {
	err := c.call("editMessageText", J{"chat_id": chat, "message_id": msg, "text": text, "parse_mode": "HTML",
		"link_preview_options": J{"is_disabled": true}, "reply_markup": markup(kb)}, nil)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

// upload sends a file as a photo or a document, with an HTML caption.
func (c *tgClient) upload(method, field string, chat int64, name string, data []byte, caption string) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("chat_id", strconv.FormatInt(chat, 10))
	w.WriteField("caption", caption)
	w.WriteField("parse_mode", "HTML")
	f, _ := w.CreateFormFile(field, name)
	f.Write(data)
	w.Close()
	return c.post(method, w.FormDataContentType(), &body, nil)
}

func (c *tgClient) sendPhoto(chat int64, png []byte, caption string) error {
	return c.upload("sendPhoto", "photo", chat, "qr.png", png, caption)
}

func (c *tgClient) sendDocument(chat int64, name string, data []byte, caption string) error {
	return c.upload("sendDocument", "document", chat, name, data, caption)
}
