package main

import (
	"strings"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/state"
)

func TestHourBlock(t *testing.T) {
	for h, want := range map[int]string{0: "0", 3: "0", 4: "1", 19: "4", 20: "5", 23: "5", -1: "", 24: ""} {
		if got := state.HourBlock(h); got != want {
			t.Errorf("HourBlock(%d) = %q, want %q", h, got, want)
		}
	}
}

func TestForNetworkByHour(t *testing.T) {
	st := rankedState()
	now := time.Now()
	nr := &state.NetRank{Name: "TCI", Updated: now.Unix(),
		Scores: map[string]float64{"r-tcp/443": 30, "hy2/8443": 20, "cdn-xhttp/2096": 10}}
	// in the evening the REALITY config is blocked and the CDN one is the best
	nr.SetHour(21, map[string]float64{"r-tcp/443": 0, "cdn-xhttp/2096": 50}, now.Unix(), false)
	st.Networks = map[string]*state.NetRank{"AS12880": nr}
	order := func(hour int) string {
		v, _ := forNetworkAt(st, "AS12880", now, hour)
		var ids []string
		for _, e := range v.Entries {
			ids = append(ids, e.ID)
		}
		return strings.Join(ids, ",")
	}
	if got := order(-1); got != "r-tcp,hy2,cdn-xhttp,ss2022" {
		t.Errorf("all day: %s", got)
	}
	if got := order(9); got != "r-tcp,hy2,cdn-xhttp,ss2022" {
		t.Errorf("morning, no scores for that time: %s", got)
	}
	// 0.6*50 + 0.4*10 = 34 for the CDN config; REALITY blocked at this hour goes last
	if got := order(22); got != "cdn-xhttp,hy2,ss2022,r-tcp" {
		t.Errorf("evening: %s", got)
	}
	nr.Hours["5"].Updated = now.Add(-31 * 24 * time.Hour).Unix()
	if got := order(22); got != "r-tcp,hy2,cdn-xhttp,ss2022" {
		t.Errorf("a stale evening ranking is not used: %s", got)
	}
}

func TestClientReportHour(t *testing.T) {
	st, _ := clientTestState()
	lock := func() func() { return func() {} }
	load := func() (*state.State, error) { return st, nil }
	save := func(*state.State) error { return nil }
	h := 21
	in := ClientReport{ASN: "12880", Name: "TCI", Hour: &h, Results: []ClientReportResult{
		{Key: "r-tcp/443", Ok: 0, N: 3, Warm: -1},
		{Key: "ssh-tls/8443", Ok: 3, N: 3, Warm: 300, Down: 8},
	}}
	if err := applyClientReport(lock, load, save, in, time.Now()); err != nil {
		t.Fatal(err)
	}
	hr := st.Networks["12880"].Hours["5"]
	if hr == nil || hr.Scores["r-tcp/443"] != 0 || hr.Scores["ssh-tls/8443"] <= 0 {
		t.Fatalf("evening ranking: %+v", hr)
	}
	// a report without an hour leaves the blocks alone
	in.Hour = nil
	in.Results = []ClientReportResult{{Key: "r-tcp/443", Ok: 3, N: 3, Warm: 100, Down: 5}}
	applyClientReport(lock, load, save, in, time.Now())
	if st.Networks["12880"].Hours["5"].Scores["r-tcp/443"] != 0 {
		t.Error("a report without an hour changed the evening")
	}
}
