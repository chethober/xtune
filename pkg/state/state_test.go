package state

import (
	"encoding/json"
	"strings"
	"testing"
)

// WGOnly marks a tuning test peer in memory only: it must never reach state.json.
func TestUserJSON(t *testing.T) {
	u := NewUser("carol")
	u.WGOnly = true
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.ToLower(string(b)); strings.Contains(s, "wgonly") {
		t.Errorf("WGOnly serialized: %s", b)
	}
	var back User
	if err := json.Unmarshal(b, &back); err != nil || back.Name != "carol" || back.UUID != u.UUID || back.WGOnly {
		t.Errorf("round trip: %+v %v", back, err)
	}
}

func TestNewUser(t *testing.T) {
	a, b := NewUser("a"), NewUser("b")
	if !a.Enabled || len(a.UUID) != 36 || a.SSKey == "" || len(a.SubToken) != 32 {
		t.Errorf("new user: %+v", a)
	}
	if a.UUID == b.UUID || a.SSKey == b.SSKey || a.SubToken == b.SubToken {
		t.Error("users share secrets")
	}
}

func TestEntryKinds(t *testing.T) {
	for _, c := range []struct {
		kind, engine, net string
		handshake         bool
	}{
		{"reality-xhttp", "xray", "xhttp", false},
		{"trojan-reality", "xray", "tcp", false},
		{"worker-ws", "xray", "ws", false},
		{"dnstt", "tunnel", "", false},
		{"mtproto", "tunnel", "", true},
		{"ikev2", "ikev2", "", true},
	} {
		e := Entry{Kind: c.kind}
		if e.Engine() != c.engine || e.Net() != c.net || e.HandshakeOnly() != c.handshake {
			t.Errorf("%s: engine %q net %q handshake %v", c.kind, e.Engine(), e.Net(), e.HandshakeOnly())
		}
	}
}
