package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type tgCall struct {
	method string
	params map[string]any
}

// fakeTelegram is a Bot API that records calls and hands out queued updates once.
type fakeTelegram struct {
	mu      sync.Mutex
	calls   []tgCall
	updates []tgUpdate
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	params := map[string]any{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.ParseMultipartForm(1 << 20)
		for k, v := range r.MultipartForm.Value {
			params[k] = v[0]
		}
	} else {
		json.NewDecoder(r.Body).Decode(&params)
	}
	f.calls = append(f.calls, tgCall{method, params})
	var result any = true
	switch method {
	case "getMe":
		result = tgUser{ID: 1, Username: "xt_bot"}
	case "getUpdates":
		result, f.updates = f.updates, nil
		if result == nil {
			result = []tgUpdate{}
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// take returns and forgets the recorded calls.
func (f *fakeTelegram) take() []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func startTelegram(t *testing.T) *fakeTelegram {
	ft := &fakeTelegram{}
	srv := httptest.NewServer(ft)
	old := telegramAPI
	telegramAPI = srv.URL
	t.Cleanup(func() { telegramAPI = old; srv.Close() })
	return ft
}

func textMsg(from int64, text string, at time.Time) tgUpdate {
	m := &tgMessage{MessageID: 9, From: &tgUser{ID: from, FirstName: "Ann"}, Date: at.Unix(), Text: text}
	m.Chat.ID, m.Chat.Type = from, "private"
	return tgUpdate{Message: m}
}

func press(from int64, data string) tgUpdate {
	u := tgUpdate{Callback: &tgCallback{ID: "cb", From: tgUser{ID: from}, Message: &tgMessage{MessageID: 77}, Data: data}}
	u.Callback.Message.Chat.ID = from
	return u
}

// texts are the texts of the sent and edited messages, by method.
func texts(calls []tgCall, method string) []string {
	var out []string
	for _, c := range calls {
		if c.method == method {
			s, _ := c.params["text"].(string)
			out = append(out, s)
		}
	}
	return out
}

func TestBotManagesUsers(t *testing.T) {
	ft := startTelegram(t)
	r, fs := deployed(t, NewUser("ann"))
	fs.helper = map[string]string{"bot-enable": "active"}
	if _, err := SetBot(r, Bot{Token: "123:abc", Admins: []int64{42}}); err != nil {
		t.Fatal(err)
	}
	ft.take()
	b := &botServer{r: r, tg: newTG("123:abc"), now: time.Now}
	now := time.Now()

	// strangers learn their ID and nothing else
	b.handle(textMsg(7, "/del ann", now))
	if s := texts(ft.take(), "sendMessage"); len(s) != 1 || !strings.Contains(s[0], "<code>7</code>") {
		t.Fatalf("stranger reply: %v", s)
	}
	b.handle(press(7, "del! ann"))
	if st, _ := LoadState(r); len(st.Users) != 1 {
		t.Fatal("a stranger's button deleted a user")
	}
	ft.take()

	b.handle(textMsg(42, "/add@xt_bot bob 10 30 2", now))
	calls := ft.take()
	st, _ := LoadState(r)
	_, bob := st.User("bob")
	if bob == nil || bob.Quota != 10<<30 || bob.Devices != 2 || time.Until(time.Unix(bob.Expires, 0)) < 29*24*time.Hour {
		t.Fatalf("bob: %+v", bob)
	}
	if s := texts(calls, "sendMessage"); len(s) != 1 || !strings.Contains(s[0], "vless://") {
		t.Fatalf("add should send bob's links: %v", s)
	}
	if n := len(texts(calls, "sendPhoto")); n != len(st.Entries) {
		t.Errorf("want a QR code per config without a subscription server, got %d", n)
	}

	b.handle(textMsg(42, "/users", now))
	if s := texts(ft.take(), "sendMessage"); len(s) != 1 || !strings.Contains(s[0], "ann") || !strings.Contains(s[0], "bob") {
		t.Fatalf("users: %v", s)
	}

	b.handle(press(42, "disable bob"))
	calls = ft.take()
	if st, _ = LoadState(r); st.Users[1].Enabled {
		t.Error("bob still enabled")
	}
	if s := texts(calls, "editMessageText"); len(s) != 1 || !strings.Contains(s[0], "disabled") {
		t.Errorf("a button should update its message: %v", calls)
	}

	b.handle(press(42, "del bob"))
	if st, _ = LoadState(r); len(st.Users) != 2 {
		t.Fatal("delete needs a confirmation")
	}
	if s := texts(ft.take(), "editMessageText"); len(s) != 1 || !strings.Contains(s[0], "Delete <b>bob</b>?") {
		t.Fatalf("confirmation: %v", s)
	}
	b.handle(press(42, "del! bob"))
	if st, _ = LoadState(r); len(st.Users) != 1 {
		t.Fatal("bob not deleted")
	}
	ft.take()

	b.handle(textMsg(42, "/limit ann quota=5 reset=monthly", now))
	st, _ = LoadState(r)
	if st.Users[0].Quota != 5<<30 || st.Users[0].Reset != "monthly" {
		t.Errorf("limit: %+v", st.Users[0])
	}
	b.handle(textMsg(42, "/limit ann 5", now))
	if s := texts(ft.take(), "sendMessage"); len(s) != 2 || !strings.Contains(s[1], "key=value") {
		t.Errorf("positional limit should be refused: %v", s)
	}

	b.handle(textMsg(42, "/del ann", now.Add(-time.Hour)))
	if len(ft.take()) != 0 {
		t.Error("a stale command was run")
	}
}

func TestParseLimits(t *testing.T) {
	l, err := parseLimits([]string{"2.5", "0", "reset=none"}, true)
	if err != nil || *l.QuotaGB != 2.5 || *l.Days != 0 || l.Devices != nil || *l.Reset != "" {
		t.Fatalf("%+v %v", l, err)
	}
	for _, bad := range [][]string{{"x"}, {"quota=-1"}, {"days=1.5"}, {"color=red"}, {"1", "2", "3", "4"}} {
		if _, err := parseLimits(bad, true); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if name, args := parseCommand("/Users@xt_bot 2"); name != "users" || len(args) != 1 {
		t.Errorf("parseCommand: %q %v", name, args)
	}
	if ids, err := parseIDs("42, 7 42"); err != nil || fmtIDs(ids) != "42, 7" {
		t.Errorf("parseIDs: %v %v", ids, err)
	}
	if _, err := parseIDs("-5"); err == nil {
		t.Error("a group ID is not a user")
	}
}

func TestBotPairing(t *testing.T) {
	ft := startTelegram(t)
	ft.updates = []tgUpdate{textMsg(42, "/start", time.Now())}
	var asked tgUser
	id, err := pairAdmin("123:abc", time.Minute, func(string) {}, func(u tgUser) (bool, error) { asked = u; return true, nil })
	if err != nil || id != 42 || asked.FirstName != "Ann" {
		t.Fatalf("pair: %d %v %+v", id, err, asked)
	}
	if s := texts(ft.take(), "sendMessage"); len(s) != 1 || !strings.Contains(s[0], "admin") {
		t.Errorf("the new admin should be told: %v", s)
	}
}

func TestBotSettingsSurviveRedeploy(t *testing.T) {
	startTelegram(t)
	r, fs := deployed(t)
	fs.helper = map[string]string{"bot-enable": "active"}
	if _, err := SetBot(r, Bot{Token: "123:abc", Admins: []int64{42}}); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(r)
	fresh := testState()
	if _, err := DeployVariants(r, fresh, []*Variant{{Entry: st.Entries[0]}}); err != nil {
		t.Fatal(err)
	}
	if st, _ = LoadState(r); !st.Bot.On() {
		t.Error("redeploy dropped the bot settings")
	}
}
