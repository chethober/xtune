package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/chethober/xtune/pkg/state"
)

// fakeSSH is an SSH server that answers exec requests from a table, recording what ran.
type fakeSSH struct {
	addr   string
	hostPK ssh.PublicKey
	mu     sync.Mutex
	ran    []string
	answer func(cmd, stdin string) (string, int)
}

func newFakeSSH(t *testing.T, user, pass string, answer func(cmd, stdin string) (string, int)) *fakeSSH {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == user && string(p) == pass {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSSH{addr: ln.Addr().String(), hostPK: signer.PublicKey(), answer: answer}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, cfg)
		}
	}()
	return f
}

func (f *fakeSSH) serve(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for r := range creqs {
				if r.Type != "exec" {
					r.Reply(false, nil)
					continue
				}
				var p struct{ Cmd string }
				ssh.Unmarshal(r.Payload, &p)
				r.Reply(true, nil)
				f.mu.Lock()
				f.ran = append(f.ran, p.Cmd)
				f.mu.Unlock()
				out, code := f.answer(p.Cmd, "")
				ch.Write([]byte(out))
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(code)}))
				return
			}
		}()
	}
}

func (f *fakeSSH) commands() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.ran, "\n")
}

func TestManage(t *testing.T) {
	st := &state.State{Host: "203.0.113.10", Country: "ir", Entries: []state.Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443}}}
	u := state.NewUser("carol")
	u.Enabled = true
	st.Users = []state.User{u}
	st.Networks = map[string]*state.NetRank{"12880": {Name: "TCI", Updated: time.Now().Unix(), Scores: map[string]float64{"r-tcp/443": 12}}}
	stJSON, _ := json.Marshal(st)
	today := state.DayKey(time.Now())
	usJSON := fmt.Sprintf(`{"users":{"carol":{"up":1,"down":2,"online":["5.1.2.3"],"daily":{%q:1000}}}}`, today)
	f := newFakeSSH(t, "root", "pw", func(cmd, _ string) (string, int) {
		switch {
		case cmd == "true":
			return "", 0
		case strings.Contains(cmd, "state.json"):
			return string(stJSON), 0
		case strings.Contains(cmd, "usage.json"):
			return usJSON, 0
		case strings.Contains(cmd, "xtune version"):
			return "xtune 1.0.1\n", 0
		case cmd == "xtune 'users' '--json' 'list'":
			return `[{"name":"carol","status":"active"}]`, 0
		case strings.HasPrefix(cmd, "xtune 'users' '--quota' '50' '--days' '30' '--json' 'add' 'dave'"):
			return `{"name":"dave","links":[]}`, 0
		}
		return "unknown command: " + cmd, 1
	})
	host, port, _ := net.SplitHostPort(f.addr)
	var p int
	fmt.Sscan(port, &p)
	c := ServerConn{Host: host, Port: p, User: "root", Password: "pw"}

	if _, err := ServerOverview(c); err == nil {
		t.Error("reads need a pinned host key (connect first)")
	}
	got, err := ConnectServer(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.0.1" || got.HostKey != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(f.hostPK))) {
		t.Errorf("connect: %+v", got)
	}
	c.HostKey = got.HostKey
	o, err := ServerOverview(c)
	if err != nil {
		t.Fatal(err)
	}
	if o.Users != 1 || o.Online != 1 || o.TodayBytes != 1000 || len(o.Entries) != 1 || len(o.Networks) != 1 || o.Networks[0].Best[0] != "r-tcp" {
		t.Errorf("overview: %+v", o)
	}
	us, err := Users(c)
	if err != nil || !strings.Contains(string(us), "carol") {
		t.Errorf("users: %s %v", us, err)
	}
	q, d := 50.0, 30
	out, err := DoUser(c, UserAction{Action: "add", Name: "dave", QuotaGB: &q, Days: &d})
	if err != nil || !strings.Contains(out, "dave") {
		t.Errorf("add: %q %v", out, err)
	}
	if _, err := DoUser(c, UserAction{Action: "add", Name: "x; rm -rf /"}); err == nil {
		t.Error("a bad name should be refused")
	}
	if _, err := DoUser(c, UserAction{Action: "uninstall", Name: "x"}); err == nil {
		t.Error("an unknown action should be refused")
	}

	// a changed host key is refused
	bad := c
	bad.HostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG3pTbG1ZFm9b9b6mOq7gq5m6pJ9YQe0ZK5b1u7cO9m3"
	DisconnectServer(c)
	if _, err := ConnectServer(bad); err == nil || !strings.Contains(err.Error(), "host key changed") {
		t.Errorf("changed host key: %v", err)
	}
}
