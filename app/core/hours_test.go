package core

import (
	"strings"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
)

func TestByHour(t *testing.T) {
	SetDataDir(t.TempDir())
	defer SetDataDir("")
	cs := []protocols.ClientEntry{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	ids := func(l []protocols.ClientEntry) string {
		var out []string
		for _, c := range l {
			out = append(out, c.ID)
		}
		return strings.Join(out, ",")
	}
	evening := time.Date(2026, 11, 18, 21, 0, 0, 0, time.Local)
	if got := ids(byHour(cs, "AS1", evening)); got != "a,b,c,d" {
		t.Fatalf("nothing checked yet keeps the order: %s", got)
	}
	recordHours("1", []Result{
		{ID: "a", Ok: 0, N: 3, State: "blocked"},
		{ID: "c", Ok: 3, N: 3, Warm: 200, Down: 20, State: "ok"},
		{ID: "d", Ok: 2, N: 3, Warm: 900, State: "ok"},
		{ID: "b", State: "skipped"},
	}, evening)
	if got := ids(byHour(cs, "AS1", evening.Add(time.Hour))); got != "c,d,b,a" {
		t.Errorf("evening: %s", got)
	}
	if got := ids(byHour(cs, "AS1", evening.Add(-12*time.Hour))); got != "a,b,c,d" {
		t.Errorf("morning has no checks: %s", got)
	}
	if got := ids(byHour(cs, "AS2", evening)); got != "a,b,c,d" {
		t.Errorf("another network: %s", got)
	}
	if got := ids(byHour(cs, "AS1", evening.Add(31*24*time.Hour))); got != "a,b,c,d" {
		t.Errorf("stale: %s", got)
	}
	// through chosen: only for a profile no server ranked
	p := &Profile{Configs: cs}
	for i := range p.Configs {
		p.Configs[i].Engine = "xray"
		p.Configs[i].Outbound = J{"protocol": "freedom"}
	}
	if out, _ := chosen(p, Options{ASN: "AS1"}); len(out) != 4 {
		t.Fatal(out)
	}
	p.Network = "TCI"
	if out, _ := chosen(p, Options{ASN: "AS1"}); ids(out) != "a,b,c,d" {
		t.Errorf("a ranked profile keeps the server's order: %s", ids(out))
	}
}
