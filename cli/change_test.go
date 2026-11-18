package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/chethober/xtune/pkg/state"
)

func changeState() *state.State {
	st := rankedState()
	st.Entries = append(st.Entries,
		state.Entry{ID: "w", Kind: "worker-ws", Port: 443, Path: "/w"},
		state.Entry{ID: "x", Kind: "reality-grpc", Port: 8444, SNI: "b.com", Svc: "abc"},
		state.Entry{ID: "v", Kind: "ikev2", Port: 500})
	return st
}

func TestPlanChange(t *testing.T) {
	port := func(e state.Entry) (int, error) { return 31337, nil }
	sni := func() (string, error) { return "new.example.com", nil }
	cases := []struct {
		id   string
		w    changeWhat
		want string // what changed, or the error's start
	}{
		{"r-tcp", changeWhat{}, "sni a.com → new.example.com"},
		{"r-tcp", changeWhat{Port: true}, "port 443 → 31337"},
		{"r-tcp", changeWhat{SNI: true, Port: true}, "sni a.com → new.example.com, port 443 → 31337"},
		{"cdn-xhttp", changeWhat{}, "path /p → /"},
		{"hy2", changeWhat{}, "port 8443 → 31337"},
		{"x", changeWhat{Path: true}, "service abc → "},
		{"hy2", changeWhat{SNI: true}, "error: hy2 is not a REALITY config"},
		{"w", changeWhat{}, "error: w runs through the Cloudflare Worker"},
		{"v", changeWhat{}, "error: v (ikev2) uses fixed ports"},
		{"nope", changeWhat{}, "error: no config \"nope\""},
	}
	for _, c := range cases {
		st := changeState()
		res, err := planChange(st, c.id, c.w, port, sni)
		got := ""
		if err != nil {
			got = "error: " + err.Error()
		} else {
			got = strings.Join(res.Changed, ", ")
		}
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%s %+v: %q, want %q…", c.id, c.w, got, c.want)
		}
	}
	st := changeState()
	res, _ := planChange(st, "hy2", changeWhat{}, port, sni)
	if res.Key != "hy2/31337" || st.Entries[1].Port != 31337 {
		t.Errorf("the entry itself changes, and its key: %s %+v", res.Key, st.Entries[1])
	}
	if _, err := planChange(changeState(), "r-tcp", changeWhat{}, port, func() (string, error) { return "", errors.New("none answers") }); err == nil {
		t.Error("a failed target search should fail the change")
	}
}

func TestNewPort(t *testing.T) {
	st := changeState()
	st.Sub = state.SubConfig{Mode: "cdn", Port: 2053}
	used := map[string]map[int]bool{"tcp": {2083: true}, "udp": {}}
	// a CDN config moves only among the ports the CDN passes through, skipping used and taken ones
	p, err := newPort(st.Entries[3], st, used)
	if err != nil || p != 2087 {
		t.Errorf("CDN port %d %v", p, err)
	}
	for range 50 {
		p, err := newPort(st.Entries[0], st, used)
		if err != nil || p < 20000 || p >= 60000 || p == 443 {
			t.Fatalf("direct port %d %v", p, err)
		}
	}
	e := state.Entry{ID: "h", Kind: "hysteria2", Port: 8443, Hop: "20000-59999"}
	if _, err := newPort(e, st, used); err == nil {
		t.Error("every candidate is in the hop range: want an error")
	}
}
