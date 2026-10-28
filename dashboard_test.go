package main

import (
	"strings"
	"testing"
	"time"
)

func TestSnapshotDash(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	st := keyedState(t)
	st.Host = "203.0.113.10"
	st.Entries = []Entry{
		{ID: "r-tcp", Kind: "reality-tcp", Port: 443},
		{ID: "hy2", Kind: "hysteria2", Port: 8443},
		{ID: "ss2022", Kind: "ss2022", Port: 23456},
	}
	ann, bob, carol := NewUser("ann"), NewUser("bob"), NewUser("carol")
	ann.Quota = 100
	bob.Quota = 100
	bob.Devices = 2
	carol.Enabled = false
	st.Users = []User{ann, bob, carol}

	us := &Usage{Users: map[string]*UserUsage{
		"ann":   {Up: 10, Down: 10, Online: []string{"1.2.3.4"}}, // 20% of quota, online
		"bob":   {Up: 60, Down: 40, Online: []string{"5.6.7.8"}}, // 100% of quota, online
		"carol": {Up: 1, Down: 1},
	}, Entries: map[string]*EntryUsage{
		// r-tcp carried traffic just now: busy, so the quiet check is live
		entryKey(st.Entries[0]): {Bytes: 5_000_000, Active: now.Unix() - 60, First: now.Unix() - 7200},
		// hy2 last carried traffic 3 hours ago: quiet (> quietAfter)
		entryKey(st.Entries[1]): {Bytes: 1_000_000, Active: now.Unix() - 3*3600, First: now.Unix() - 7200},
		// ss2022 freshly deployed, no traffic yet: idle (not quiet until quietAfter passes)
		entryKey(st.Entries[2]): {First: now.Unix() - 60},
	}}

	d := snapshotDash(st, us, now)

	if d.Host != "203.0.113.10" {
		t.Errorf("host: %q", d.Host)
	}
	// ann is active; bob is over quota; carol is disabled → 1 active, 2 online
	if d.NUsers != 3 || d.NActive != 1 || d.NOnline != 2 {
		t.Errorf("counts: users=%d active=%d online=%d", d.NUsers, d.NActive, d.NOnline)
	}
	wantState := map[string]string{"r-tcp": "active", "hy2": "quiet", "ss2022": "idle"}
	for _, c := range d.Configs {
		if wantState[c.ID] != c.State {
			t.Errorf("config %s: state %q, want %q", c.ID, c.State, wantState[c.ID])
		}
	}
	// busiest user first
	if d.Users[0].Name != "bob" {
		t.Errorf("users not sorted by traffic: first is %q", d.Users[0].Name)
	}
	for _, u := range d.Users {
		switch u.Name {
		case "ann":
			if u.Percent != 20 {
				t.Errorf("ann percent: %d", u.Percent)
			}
		case "bob":
			if u.Percent != 100 || u.Status != "over quota" {
				t.Errorf("bob: percent=%d status=%q", u.Percent, u.Status)
			}
		case "carol":
			if u.Percent != -1 || u.Status != "disabled" {
				t.Errorf("carol: percent=%d status=%q", u.Percent, u.Status)
			}
		}
	}

	// the rendered view mentions the host, a quiet config and a user, and does not panic.
	out := d.render(now, 100)
	for _, want := range []string{"203.0.113.10", "hy2", "blocked?", "bob", "ann"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q", want)
		}
	}
}

func TestDashHelpers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "-"},
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-3 * time.Hour), "3h"},
		{now.Add(-50 * time.Hour), "2d"},
	}
	for _, c := range cases {
		if got := ago(c.t, now); got != c.want {
			t.Errorf("ago(%v) = %q, want %q", c.t, got, c.want)
		}
	}
	if got := trunc("short", 16); got != "short" {
		t.Errorf("trunc short: %q", got)
	}
	if got := trunc("a-very-long-user-name", 10); len([]rune(got)) != 10 {
		t.Errorf("trunc long: %q (%d runes)", got, len([]rune(got)))
	}
	// unlimited bar is all rule, no fill block
	if strings.Contains(bar(-1, 8), "█") {
		t.Errorf("unlimited bar has fill: %q", bar(-1, 8))
	}
}
