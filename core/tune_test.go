package core

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
)

// A tune from the app: the server prepares, this device measures, the server deploys the report.
func TestTuneFromTheApp(t *testing.T) {
	open, err := net.Listen("tcp", "127.0.0.1:0") // an MTProto port that answers
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	go func() {
		for {
			c, err := open.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	closed, _ := net.Listen("tcp", "127.0.0.1:0") // and one that does not
	closedAddr := closed.Addr().(*net.TCPAddr)
	closed.Close()
	link := func(port int) string {
		return fmt.Sprintf("tg://proxy?server=127.0.0.1&port=%d&secret=ee00", port)
	}
	tp := testProfile{ID: "abc123", Expires: time.Now().Add(time.Hour).Unix(), Skipped: []string{"ovpn:1194"},
		Profile: Profile{V: 1, Name: "xtune test", Configs: []protocols.ClientEntry{
			{ID: "mtproto#0", Key: "mtproto:443", Name: "mtproto:443", Kind: "mtproto", Engine: "handoff", Link: link(open.Addr().(*net.TCPAddr).Port)},
			{ID: "mtproto-hiport#1", Key: "mtproto-hiport:9443", Name: "mtproto-hiport:9443", Kind: "mtproto", Engine: "handoff", Link: link(closedAddr.Port)},
		}}}
	tpJSON, _ := json.Marshal(tp)
	var mu sync.Mutex
	var report string
	f := newFakeSSH(t, "root", "pw", func(cmd, stdin string) (string, int) {
		switch {
		case cmd == "true":
			return "", 0
		case cmd == "xtune 'tune' '--prepare' '--depth' 'smart' '--protocols' 'mtproto,ssh'":
			return string(tpJSON) + "\n", 0
		case strings.Contains(cmd, "cat > /opt/xray-autotune/app-report.json"):
			mu.Lock()
			report = stdin
			mu.Unlock()
			return "", 0
		case cmd == "xtune 'deploy' '-n' '2' '/opt/xray-autotune/app-report.json'":
			return "Chosen for 1 network(s):\n  mtproto:443  works on: here\n", 0
		case strings.Contains(cmd, "rm -f /opt/xray-autotune/app-report.json"):
			return "", 0
		}
		return "unexpected " + cmd, 1
	})
	c := ServerConn{Host: "127.0.0.1", Port: f.port(), User: "root", Password: "pw"}
	got, err := ConnectServer(c)
	if err != nil {
		t.Fatal(err)
	}
	c.HostKey = got.HostKey
	if _, err := StartTune(c, TuneOptions{Protocols: []string{"bogus"}}); err == nil {
		t.Error("unknown protocols are refused before anything runs on the server")
	}
	v, err := StartTune(c, TuneOptions{Protocols: []string{"mtproto", "ssh"}, Network: "here"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StartTune(c, TuneOptions{}); err == nil {
		t.Error("one tune of a server at a time")
	}
	deadline := time.Now().Add(30 * time.Second)
	for v.Phase != "measured" && v.Phase != "failed" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		v = TuneStatus(v.ID, "")
	}
	if v.Phase != "measured" {
		t.Fatalf("tune: %+v", v)
	}
	if TuneStatus("", "127.0.0.1").ID != v.ID {
		t.Error("the server's latest tune is found by its host")
	}
	if len(v.Results) != 2 || v.Results[0].Key != "mtproto:443" || v.Scores[0] <= 0 || v.Results[1].State != "blocked" || v.Scores[1] != 0 {
		t.Fatalf("best first, the closed port blocked: %+v %v", v.Results, v.Scores)
	}
	if len(v.Skipped) != 1 {
		t.Error("variants another app runs are listed as skipped")
	}
	if v, err = DeployTune(v.ID, 2); err != nil {
		t.Fatal(err)
	}
	for v.Phase == "deploying" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		v = TuneStatus(v.ID, "")
	}
	if v.Phase != "done" || !strings.Contains(v.Output, "Chosen") {
		t.Fatalf("deploy: %+v", v)
	}
	mu.Lock()
	defer mu.Unlock()
	var rep netReport
	if err := json.Unmarshal([]byte(report), &rep); err != nil {
		t.Fatalf("report %q: %v", report, err)
	}
	if rep.ID != "abc123" || rep.Network != "here" || len(rep.Results) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	if r := rep.Results[0]; r.ID != "mtproto" || r.Label != "mtproto:443" || r.Score <= 0 || r.Ok != 3 {
		t.Errorf("a result names its entry and its variant: %+v", r)
	}
	if err := CancelTune(v.ID); err != nil {
		t.Error(err)
	}
}

func TestPrepareArgs(t *testing.T) {
	a, err := prepareArgs(TuneOptions{Depth: "deep", Protocols: []string{"dnstt", "awg"}, DNSDomain: "t.example.com"})
	if err != nil || strings.Join(a, " ") != "tune --prepare --depth deep --protocols dnstt,awg --dns-domain t.example.com" {
		t.Errorf("%v %v", a, err)
	}
	for _, o := range []TuneOptions{{Depth: "fast"}, {DNSDomain: "-x"}, {DNSDomain: "a b"}, {Protocols: []string{"openvpn"}}} {
		if _, err := prepareArgs(o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
}

func (f *fakeSSH) port() int {
	_, p, _ := net.SplitHostPort(f.addr)
	var n int
	fmt.Sscan(p, &n)
	return n
}
