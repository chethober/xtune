package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func manageCall(t *testing.T, srv *httptest.Server, token string, q J) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(q)
	res, err := http.Post(srv.URL+"/subs/adm/"+token, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestManageAdmin(t *testing.T) {
	r, srv := publicServer(t)
	st, err := changeManage(r, func(*State, *Manage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	tok := st.Manage.Token
	if !strings.HasSuffix(st.manageURL(tok), "/subs/adm/"+tok) {
		t.Fatalf("link: %s", st.manageURL(tok))
	}
	if code, _ := manageCall(t, srv, "wrong", J{"op": "users"}); code != http.StatusNotFound {
		t.Errorf("wrong token: %d", code)
	}
	code, s := manageCall(t, srv, tok, J{"op": "status"})
	if code != 200 || s["role"] != "admin" || s["users"].(float64) != 1 {
		t.Fatalf("status: %d %v", code, s)
	}
	code, u := manageCall(t, srv, tok, J{"op": "add", "name": "carol", "quotaGB": 50, "days": 30, "devices": 2})
	if code != 200 || u["name"] != "carol" || u["quota"].(float64) != 50<<30 || !strings.Contains(u["sub"].(string), "/subs/") {
		t.Fatalf("add: %d %v", code, u)
	}
	if len(u["links"].([]any)) == 0 || len(u["daily"].([]any)) != 30 {
		t.Errorf("links/daily: %v", u)
	}
	if code, e := manageCall(t, srv, tok, J{"op": "add", "name": "carol"}); code != http.StatusUnprocessableEntity ||
		!strings.Contains(e["error"].(string), "exists") {
		t.Errorf("duplicate: %d %v", code, e)
	}
	if _, u = manageCall(t, srv, tok, J{"op": "limit", "name": "carol", "quotaGB": 100}); u["quota"].(float64) != 100<<30 {
		t.Errorf("limit: %v", u)
	}
	if _, u = manageCall(t, srv, tok, J{"op": "disable", "name": "carol"}); u["status"] != "disabled" {
		t.Errorf("disable: %v", u)
	}
	before := u["sub"]
	if _, u = manageCall(t, srv, tok, J{"op": "resetSub", "name": "carol"}); u["sub"] == before {
		t.Error("resetSub kept the link")
	}
	if code, _ := manageCall(t, srv, tok, J{"op": "setPlan", "name": "basic", "quotaGB": 10, "days": 7}); code != 200 {
		t.Errorf("setPlan: %d", code)
	}
	if _, u = manageCall(t, srv, tok, J{"op": "renew", "name": "carol", "plan": "basic"}); u["plan"] != "basic" || u["status"] != "active" {
		t.Errorf("renew: %v", u)
	}
	if code, _ := manageCall(t, srv, tok, J{"op": "delete", "name": "carol"}); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := manageCall(t, srv, tok, J{"op": "user", "name": "carol"}); code != http.StatusUnprocessableEntity {
		t.Errorf("deleted user still there: %d", code)
	}

	// a new link: the old one stops working
	st, _ = changeManage(r, func(_ *State, m *Manage) error { m.Token = randHex(16); return nil })
	if code, _ := manageCall(t, srv, tok, J{"op": "status"}); code != http.StatusNotFound {
		t.Error("the old management link still works")
	}
	if code, _ := manageCall(t, srv, st.Manage.Token, J{"op": "status"}); code != 200 {
		t.Error("the new management link does not work")
	}
}

func TestManageReseller(t *testing.T) {
	r, srv := publicServer(t)
	st, _ := changeManage(r, func(_ *State, m *Manage) error {
		m.Resellers = map[string]*Reseller{"rex": {Token: randHex(16), MaxUsers: 2, QuotaGB: 30}}
		return nil
	})
	tok := st.Manage.Resellers["rex"].Token
	if _, s := manageCall(t, srv, tok, J{"op": "status"}); s["role"] != "reseller" || s["users"].(float64) != 0 || s["addr"] != nil {
		t.Errorf("reseller status: %v", s)
	}
	// they don't see the admin's users, or change them
	if _, l := manageCall(t, srv, tok, J{"op": "users"}); len(l["users"].([]any)) != 0 {
		t.Errorf("reseller sees: %v", l)
	}
	if code, _ := manageCall(t, srv, tok, J{"op": "disable", "name": "ann"}); code != http.StatusUnprocessableEntity {
		t.Errorf("reseller disabled ann: %d", code)
	}
	if code, e := manageCall(t, srv, tok, J{"op": "add", "name": "r1"}); code != http.StatusUnprocessableEntity ||
		!strings.Contains(e["error"].(string), "quota") {
		t.Errorf("unlimited user for a reseller with a quota: %d %v", code, e)
	}
	if code, _ := manageCall(t, srv, tok, J{"op": "add", "name": "r1", "quotaGB": 20}); code != 200 {
		t.Fatalf("add r1: %d", code)
	}
	if code, e := manageCall(t, srv, tok, J{"op": "add", "name": "r2", "quotaGB": 20}); code != http.StatusUnprocessableEntity ||
		!strings.Contains(e["error"].(string), "left") {
		t.Errorf("over the total quota: %d %v", code, e)
	}
	manageCall(t, srv, tok, J{"op": "add", "name": "r2", "quotaGB": 10})
	if code, e := manageCall(t, srv, tok, J{"op": "add", "name": "r3", "quotaGB": 0.5}); code != http.StatusUnprocessableEntity ||
		!strings.Contains(e["error"].(string), "at most 2") {
		t.Errorf("over the user count: %d %v", code, e)
	}
	if code, _ := manageCall(t, srv, tok, J{"op": "setPlan", "name": "x"}); code != http.StatusNotFound {
		t.Errorf("a reseller changed plans: %d", code)
	}
	st, _ = LoadState(r)
	if _, u := st.User("r1"); u == nil || u.Reseller != "rex" {
		t.Errorf("r1: %+v", u)
	}
}

func TestLegacyBotRemoved(t *testing.T) {
	r, fs := deployed(t, NewUser("ann"))
	st, _ := LoadState(r)
	raw, _ := json.Marshal(st)
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["bot"] = map[string]any{"token": "1:x", "admins": []int{42}}
	m["users"].([]any)[0].(map[string]any)["telegram"] = 42
	raw, _ = json.Marshal(m)
	fs.files[remoteDir+"/state.json"] = string(raw)
	if err := Enforce(r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fs.calls, "\n"), "bot-remove") {
		t.Error("the old bot's unit was not removed")
	}
	if s := fs.files[remoteDir+"/state.json"]; strings.Contains(s, `"bot"`) || strings.Contains(s, `"telegram"`) {
		t.Error("the old bot's settings are still in state.json")
	}
	if err := cmdBot(nil); err == nil || !strings.Contains(err.Error(), "@chthvpnbot") {
		t.Errorf("xtune bot: %v", err)
	}
}

func TestRedeployKeepsPoolsAndManage(t *testing.T) {
	r, _ := publicServer(t)
	SetPublic(r, "community", PublicPool{Members: 5})
	changeManage(r, func(*State, *Manage) error { return nil })
	st, _ := LoadState(r)
	res := &TuneResult{State: testState(), Best: []*Variant{{Entry: Entry{ID: "hy2", Kind: "hysteria2", Port: 8443}}}}
	if _, err := Deploy(r, res, 1); err != nil {
		t.Fatal(err)
	}
	after, _ := LoadState(r)
	if after.Public["community"] == nil || after.Public["community"].Token != st.Public["community"].Token ||
		after.Manage == nil || after.Manage.Token != st.Manage.Token {
		t.Error("a re-tune dropped the public profiles or the management link")
	}
}
