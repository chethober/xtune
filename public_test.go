package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func publicServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	r, _ := deployed(t, NewUser("ann"))
	st, _ := LoadState(r)
	st.CDNDomain, st.Sub = "cdn.example.com", SubConfig{Mode: "cdn", Port: 2053, Path: "subs"}
	saveState(r, st)
	load := func() (*State, *Usage, error) {
		st, err := LoadState(r)
		if err != nil {
			return nil, nil, err
		}
		us, err := LoadUsage(r)
		return st, us, err
	}
	srv := httptest.NewServer(subHandler{load: load, r: r})
	t.Cleanup(srv.Close)
	return r, srv
}

func pubCall(t *testing.T, srv *httptest.Server, path, label string, out any) int {
	t.Helper()
	var res *http.Response
	var err error
	if label == "" {
		res, err = http.Get(srv.URL + path)
	} else {
		body, _ := json.Marshal(map[string]string{"label": label})
		res, err = http.Post(srv.URL+path, "application/json", bytes.NewReader(body))
	}
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil && res.StatusCode == 200 {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return res.StatusCode
}

func TestPublicPool(t *testing.T) {
	r, srv := publicServer(t)
	st, err := SetPublic(r, "community", PublicPool{QuotaGB: 20, Days: 30, Devices: 2, Members: 2})
	if err != nil {
		t.Fatal(err)
	}
	link := st.PublicURL("community")
	if !strings.HasPrefix(link, "https://cdn.example.com:2053/subs/pub/") {
		t.Fatalf("connect link: %s", link)
	}
	base := "/subs/pub/" + st.Public["community"].Token

	var j pubJoined
	if code := pubCall(t, srv, base+"/join", "7f3a", &j); code != 200 {
		t.Fatalf("join: %d", code)
	}
	if !strings.HasPrefix(j.Sub, "https://cdn.example.com:2053/subs/") || j.Quota != 20<<30 || j.Status != "active" {
		t.Errorf("joined: %+v", j)
	}
	if days := time.Until(time.Unix(j.Expires, 0)).Hours() / 24; days < 29 || days > 31 {
		t.Errorf("expires in %.1f days", days)
	}
	var again pubJoined
	pubCall(t, srv, base+"/join", "7f3a", &again)
	if again.Sub != j.Sub {
		t.Error("joining again with the same label made another user")
	}
	st, _ = LoadState(r)
	if _, u := st.User("community-7f3a"); u == nil || u.Pool != "community" || u.Devices != 2 {
		t.Fatalf("member user: %+v", u)
	}

	if code := pubCall(t, srv, base+"/join", "b2c4", nil); code != 200 {
		t.Fatalf("second join: %d", code)
	}
	if code := pubCall(t, srv, base+"/join", "d9e1", nil); code != http.StatusConflict {
		t.Errorf("a full pool took a third member: %d", code)
	}
	if code := pubCall(t, srv, base+"/join", "Bad-Label", nil); code != http.StatusBadRequest {
		t.Errorf("bad label: %d", code)
	}
	if code := pubCall(t, srv, "/subs/pub/wrongtoken/join", "aaaa", nil); code != http.StatusNotFound {
		t.Errorf("wrong token: %d", code)
	}

	var info pubInfo
	if code := pubCall(t, srv, base, "", &info); code != 200 {
		t.Fatalf("info: %d", code)
	}
	if info.Active != 2 || info.Max != 2 || len(info.Members) != 2 || info.QuotaGB != 20 ||
		strings.Join(info.Protocols, ",") != "VLESS REALITY,Shadowsocks" {
		t.Errorf("info: %+v", info)
	}

	if code := pubCall(t, srv, base+"/leave", "b2c4", nil); code != 200 {
		t.Fatalf("leave: %d", code)
	}
	if code := pubCall(t, srv, base+"/leave", "b2c4", nil); code != 200 {
		t.Error("leaving twice should be fine")
	}
	// a pool cannot remove users that are not its members, even when the name looks like one
	if _, _, err := AddUser(r, "community-abcd", Limits{}); err != nil {
		t.Fatal(err)
	}
	pubCall(t, srv, base+"/leave", "abcd", nil)
	if _, u := func() (int, *User) { st, _ := LoadState(r); return st.User("community-abcd") }(); u == nil {
		t.Error("leave removed a user outside the pool")
	}
	DeleteUser(r, "community-abcd")
	if st, _ = LoadState(r); len(st.Users) != 2 {
		t.Fatalf("users after leave: %+v", st.Users)
	}

	PausePublic(r, "community", true)
	if code := pubCall(t, srv, base+"/join", "d9e1", nil); code != http.StatusConflict {
		t.Errorf("a paused pool took a member: %d", code)
	}
	PausePublic(r, "community", false)

	st, _ = RotatePublic(r, "community")
	if code := pubCall(t, srv, base, "", nil); code != http.StatusNotFound {
		t.Error("the old connect link still works after rotating")
	}
	if code := pubCall(t, srv, "/subs/pub/"+st.Public["community"].Token, "", nil); code != 200 {
		t.Error("the new connect link does not work")
	}

	if err := DeletePublic(r, "community"); err != nil {
		t.Fatal(err)
	}
	if st, _ = LoadState(r); len(st.Users) != 1 || st.Users[0].Name != "ann" {
		t.Errorf("users after deleting the pool: %+v", st.Users)
	}
}

func TestPublicPrunesExpiredMembers(t *testing.T) {
	r, _ := publicServer(t)
	st, _ := SetPublic(r, "p", PublicPool{Days: 1})
	token := st.Public["p"].Token
	now := time.Now()
	if _, err := JoinPublic(r, token, "aaaa", now); err != nil {
		t.Fatal(err)
	}
	if _, err := PublicInfo(r, token, now.Add(5*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st, _ = LoadState(r); len(st.members("p")) != 1 {
		t.Error("a member expired for 4 days was removed")
	}
	info, _ := PublicInfo(r, token, now.Add(10*24*time.Hour))
	if st, _ = LoadState(r); len(st.members("p")) != 0 || len(info.Members) != 0 {
		t.Error("a member expired for 9 days was kept")
	}
}

func TestPublicNeedsSubServer(t *testing.T) {
	r, _ := deployed(t, NewUser("ann"))
	if _, err := SetPublic(r, "p", PublicPool{}); err == nil || !strings.Contains(err.Error(), "sub enable") {
		t.Errorf("err: %v", err)
	}
}

func TestJoinLimiter(t *testing.T) {
	now := time.Now()
	for i := 0; i < joinsPerMinute; i++ {
		if !allowJoin("lim", now) {
			t.Fatalf("join %d refused", i)
		}
	}
	if allowJoin("lim", now) {
		t.Error("too many joins allowed")
	}
	if !allowJoin("lim", now.Add(61*time.Second)) {
		t.Error("joins not allowed again after a minute")
	}
}
