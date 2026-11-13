package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/chethober/xtune/pkg/state"
)

// The commands, flags and --json shapes are what 1.x keeps stable (docs/compatibility.md). These
// tests make every change to them deliberate: `go test -run Compat -update .` rewrites the files.
var updateGolden = flag.Bool("update", false, "rewrite testdata/usage.txt")

func TestCompatUsage(t *testing.T) {
	if *updateGolden {
		if err := os.WriteFile("testdata/usage.txt", []byte(usage), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/usage.txt")
	if err != nil {
		t.Fatal(err)
	}
	// lines may be added; a line that disappears is a command or flag 1.x promised to keep
	have := map[string]bool{}
	for _, l := range strings.Split(usage, "\n") {
		have[strings.TrimSpace(l)] = true
	}
	for _, l := range strings.Split(string(want), "\n") {
		if l = strings.TrimSpace(l); l != "" && !have[l] {
			t.Errorf("xtune help lost a line (see docs/compatibility.md; -update after a deliberate change):\n  %s", l)
		}
	}
}

func jsonKeys(t *testing.T, b []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if l, ok := v.([]any); ok {
		v = l[0]
	}
	var keys []string
	for k := range v.(map[string]any) {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestCompatJSON(t *testing.T) {
	st := testState()
	st.CDNDomain, st.Sub = "cdn.example.com", state.SubConfig{Mode: "cdn", Port: 2053, Path: "p"}
	st.Entries = []state.Entry{{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"}}
	u := state.NewUser("carol")
	u.Reset, u.ResetAt = "monthly", 1
	st.Users = []state.User{u}
	var b bytes.Buffer
	if err := printUsersJSON(&b, st, &state.Usage{Users: map[string]*state.UserUsage{}}); err != nil {
		t.Fatal(err)
	}
	requireKeys(t, "users list --json", jsonKeys(t, b.Bytes()),
		"devices down enabled expires lastSeen name online quota reset resetAt status subscription up")
	b.Reset()
	if err := printLinksJSON(&b, st, "carol"); err != nil {
		t.Fatal(err)
	}
	requireKeys(t, "users links --json", jsonKeys(t, b.Bytes()), "enabled failover links name subscription")
}

// requireKeys fails when a promised key is missing; new keys are allowed.
func requireKeys(t *testing.T, what string, got []string, want string) {
	t.Helper()
	for _, k := range strings.Fields(want) {
		if !slices.Contains(got, k) {
			t.Errorf("%s lost the key %q (docs/compatibility.md): %v", what, k, got)
		}
	}
}
