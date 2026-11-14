package main

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/netinfo"
	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/state"
)

func clientTestState() (*state.State, state.User) {
	st := testState()
	st.CDNDomain = "cdn.example.com"
	st.Sub = state.SubConfig{Mode: "cdn", Port: 2096, Path: "p4th"}
	st.SSHHostPub, st.CertSNI, st.CertHash = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOv2", "www.example.com", "ab12"
	st.DNSDomain, st.DNSTTPub = "t.example.com", "00ff"
	st.Entries = []state.Entry{
		{ID: "r-tcp", Kind: "reality-tcp", Port: 443, SNI: "a.com"},
		{ID: "ssh-tls", Kind: "ssh-tls", Port: 8443},
		{ID: "cdn-ssh", Kind: "cdn-ssh", Port: 2053},
		{ID: "dnstt", Kind: "dnstt", Port: 53},
		{ID: "mt", Kind: "mtproto", Port: 8888},
	}
	u := state.NewUser("ann")
	u.Quota, u.Expires = 50<<30, 2000000000
	st.Users = []state.User{u}
	return st, u
}

func TestClientProfile(t *testing.T) {
	st, u := clientTestState()
	us := &state.Usage{Users: map[string]*state.UserUsage{"ann": {Up: 1 << 30, Down: 2 << 30}}}
	h := subHandler{load: func() (*state.State, *state.Usage, error) { return st, us, nil }}
	req := httptest.NewRequest("GET", "/p4th/"+u.SubToken+"/client?asn=12880", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var p protocols.ClientProfile
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 200 {
		t.Fatalf("profile: %d %v %s", w.Code, err, w.Body.String())
	}
	if p.V != protocols.ClientProfileVersion || p.User != "ann" || p.Usage.Quota != 50<<30 || p.Usage.Down != 2<<30 || !strings.Contains(p.Usage.Page, u.SubToken) {
		t.Errorf("profile header: %+v", p)
	}
	byID := map[string]protocols.ClientEntry{}
	for _, c := range p.Configs {
		byID[c.ID] = c
	}
	if c := byID["r-tcp"]; c.Engine != "xray" || c.Outbound["protocol"] != "vless" || c.Key != "r-tcp/443" {
		t.Errorf("xray config: %+v", c)
	}
	if c := byID["ssh-tls"]; c.Engine != "ssh" || c.SSH.TLS != "pinned" || c.SSH.CertHash != "ab12" || c.SSH.Password != protocols.TunnelPassword(u) || c.SSH.HostKey != st.SSHHostPub {
		t.Errorf("ssh-tls config: %+v", c.SSH)
	}
	if c := byID["cdn-ssh"]; c.SSH.Host != "cdn.example.com" || c.SSH.TLS != "public" || c.SSH.WSHost != "cdn.example.com" {
		t.Errorf("cdn-ssh config: %+v", c.SSH)
	}
	if c := byID["dnstt"]; c.SSH.DNSTT == nil || c.SSH.DNSTT.Domain != "t.example.com" || c.Server != "" {
		t.Errorf("dnstt config: %+v", c)
	}
	if c := byID["mt"]; c.Engine != "handoff" || !strings.HasPrefix(c.Link, "https://t.me/proxy") && !strings.HasPrefix(c.Link, "tg://proxy") {
		t.Errorf("mtproto config: %+v", c)
	}
	if strings.Contains(w.Body.String(), st.SSKey) && st.SSKey != "" || strings.Contains(w.Body.String(), st.RealityPriv) && st.RealityPriv != "" {
		t.Error("the profile carries the server's keys")
	}
}

func TestClientProfileOrderedForAppNetwork(t *testing.T) {
	netinfo.ResetASNCache()
	old := netinfo.ASNLookup
	defer func() { netinfo.ASNLookup = old }()
	netinfo.ASNLookup = func(ip net.IP) (string, error) { return "AS3320", nil }
	st, u := clientTestState()
	st.Networks = map[string]*state.NetRank{"12880": {Name: "TCI", Updated: time.Now().Unix(),
		Scores: map[string]float64{"r-tcp/443": 0, "ssh-tls/8443": 40}}}
	h := subHandler{load: func() (*state.State, *state.Usage, error) { return st, &state.Usage{}, nil }}
	get := func(q string) protocols.ClientProfile {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/p4th/"+u.SubToken+"/client"+q, nil))
		var p protocols.ClientProfile
		json.Unmarshal(w.Body.Bytes(), &p)
		return p
	}
	if p := get("?asn=12880"); p.Network != "TCI" || p.Configs[0].ID != "ssh-tls" || p.Configs[len(p.Configs)-1].ID != "r-tcp" {
		t.Errorf("ordered for the app's network: %s %v", p.Network, p.Configs)
	}
	if p := get(""); p.Network != "" || p.Configs[0].ID != "r-tcp" {
		t.Errorf("unranked network: %s %s", p.Network, p.Configs[0].ID)
	}
}

func TestClientReport(t *testing.T) {
	st, _ := clientTestState()
	saved := 0
	lock := func() func() { return func() {} }
	load := func() (*state.State, error) { return st, nil }
	save := func(*state.State) error { saved++; return nil }
	now := time.Now()
	in := ClientReport{ASN: "12880", Name: "TCI", Results: []ClientReportResult{
		{Key: "r-tcp/443", Ok: 0, N: 3, Warm: -1},
		{Key: "ssh-tls/8443", Ok: 3, N: 3, Warm: 300, Down: 8},
		{Key: "nope/1", Ok: 3, N: 3, Warm: 100, Down: 99},
		{Key: "cdn-ssh/2053", Ok: 9, N: 3, Warm: 100, Down: 99}, // impossible: ignored
	}}
	if err := applyClientReport(lock, load, save, in, now); err != nil || saved != 1 {
		t.Fatal(err, saved)
	}
	nr := st.Networks["12880"]
	if nr == nil || nr.Name != "TCI" || nr.Scores["r-tcp/443"] != 0 || nr.Scores["ssh-tls/8443"] <= 0 || len(nr.Scores) != 2 {
		t.Fatalf("ranking: %+v", nr)
	}
	first := nr.Scores["ssh-tls/8443"]
	in.Results = []ClientReportResult{{Key: "ssh-tls/8443", Ok: 0, N: 3, Warm: -1}}
	applyClientReport(lock, load, save, in, now)
	if got := nr.Scores["ssh-tls/8443"]; got != round2(first/2) {
		t.Errorf("a report moves a score halfway: %v -> %v", first, got)
	}
}

func TestClientReportEndpoint(t *testing.T) {
	st, u := clientTestState()
	h := subHandler{load: func() (*state.State, *state.Usage, error) { return st, &state.Usage{}, nil }}
	post := func(body string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/p4th/"+u.SubToken+"/report", strings.NewReader(body)))
		return w.Code
	}
	if c := post(`{"asn":"AS1","results":[]}`); c != 400 {
		t.Errorf("bad report: %d", c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/p4th/nope/report", strings.NewReader(`{}`)))
	if w.Code != 404 {
		t.Errorf("unknown token: %d", w.Code)
	}
}
