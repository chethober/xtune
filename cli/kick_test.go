package main

import (
	"slices"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/state"
)

// The operator kicks one device: it is blocked at once and stays blocked though the user has no
// device limit (which would otherwise clear blocks), until it expires or is unblocked.
func TestKickDevice(t *testing.T) {
	ann := state.NewUser("ann") // no device limit
	r, fs := deployed(t, ann)
	us := &state.Usage{Users: map[string]*state.UserUsage{"ann": {Online: []string{"198.51.100.1", "198.51.100.2"}}}}
	state.SaveUsage(r, us)
	if err := KickDevice(r, "ann", "not-an-ip", time.Hour); err == nil {
		t.Error("an IP is checked")
	}
	if err := KickDevice(r, "bob", "198.51.100.1", time.Hour); err == nil {
		t.Error("an unknown user is refused")
	}
	if err := KickDevice(r, "ann", "198.51.100.2", time.Hour); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fs.sib, "198.51.100.2") {
		t.Errorf("Xray blocks it at once: sib %v", fs.sib)
	}
	got, _ := state.LoadUsage(r)
	if slices.Contains(got.Users["ann"].Online, "198.51.100.2") {
		t.Error("it is no longer listed online")
	}
	// the enforcement run keeps a manual block though ann has no device limit
	st, _ := state.Load(r)
	if err := applyDeviceLimits(r, st, got, map[string][]string{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if b := blocksFor(got, "ann", time.Now()); len(b) != 1 || b[0] != "198.51.100.2" {
		t.Errorf("kept by the enforcement run: %v", b)
	}
	if ips := blockedIPs(got, "ann", time.Now()); len(ips) != 1 {
		t.Errorf("users --json lists it: %v", ips)
	}
	// and it ends: when it expires, or by hand
	if err := applyDeviceLimits(r, st, got, map[string][]string{}, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if b := blocksFor(got, "ann", time.Now().Add(2*time.Hour)); len(b) != 0 {
		t.Errorf("expired: %v", b)
	}
	state.SaveUsage(r, us)
	KickDevice(r, "ann", "198.51.100.1", time.Hour)
	if err := KickDevice(r, "ann", "198.51.100.1", 0); err != nil {
		t.Fatal(err)
	}
	got, _ = state.LoadUsage(r)
	if len(got.Blocks) != 0 {
		t.Errorf("unblocked: %v", got.Blocks)
	}
}
