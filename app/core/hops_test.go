package core

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestHostRTT(t *testing.T) {
	// something listening on 443 is not possible without root; a refused port counts as an answer
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	old := rttPorts
	defer func() { rttPorts = old }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	rttPorts = []string{port}
	if ms := HostRTT(context.Background(), "127.0.0.1", time.Second); ms < 1 {
		t.Fatalf("listening port: got %d", ms)
	}
	// a closed port refuses at once: still a round trip
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	_, closed, _ := net.SplitHostPort(ln2.Addr().String())
	ln2.Close()
	rttPorts = []string{closed}
	if ms := HostRTT(context.Background(), "127.0.0.1", time.Second); ms < 1 {
		t.Fatalf("refused port: got %d", ms)
	}
	// a name that does not resolve: no answer
	if ms := HostRTT(context.Background(), "nothing.invalid", 300*time.Millisecond); ms != -1 {
		t.Fatalf("unresolvable host: got %d", ms)
	}
}

func TestNetRTTCall(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	old := rttPorts
	defer func() { rttPorts = old }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	rttPorts = []string{port}

	var r struct {
		Ok     bool           `json:"ok"`
		Result map[string]int `json:"result"`
	}
	if err := json.Unmarshal(Call("net.rtt", []byte(`{"hosts":["127.0.0.1",""],"timeoutMs":1000}`)), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Ok || r.Result["127.0.0.1"] < 1 || len(r.Result) != 1 {
		t.Fatalf("got %+v", r)
	}
}
