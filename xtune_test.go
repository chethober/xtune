package main

import (
	"encoding/base64"
	"flag"
	"net/url"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v26.10.10", "26.9.30", true},
		{"v26.9.30", "v26.10.10", false},
		{"v26.9.30", "26.9.30", false},
		{"v1.0.0", "none", true},
		{"", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPlanEntriesAvoidsUsedPorts(t *testing.T) {
	probe := strings.Join([]string{
		"SNI www.a.com 2 0.20", "SNI www.b.com 2 0.10", "SNI www.c.com 1.1 0.05", "SNI www.d.com fail",
		"USED tcp 0.0.0.0:443", "USED tcp [::]:8443", "USED udp 0.0.0.0:443",
	}, "\n")
	entries, snis := PlanEntries(probe)
	if snis[0] != "www.b.com" || len(snis) != 2 {
		t.Fatalf("SNIs not ranked/filtered by h2+latency: %v", snis)
	}
	seen := map[int]bool{}
	for _, e := range entries {
		if e.Port == 443 || (e.Port == 8443 && e.Proto() == "tcp") {
			t.Errorf("%s got a used port %d", e.ID, e.Port)
		}
		if seen[e.Port] {
			t.Errorf("port %d assigned twice", e.Port)
		}
		seen[e.Port] = true
	}
	if !entries[0].Primary || entries[0].SNI != "www.b.com" {
		t.Errorf("primary entry should use the fastest SNI: %+v", entries[0])
	}
}

func testState() *State {
	return &State{Host: "203.0.113.7", RealityPriv: "priv", RealityPub: "pub", ShortID: "abcd1234",
		VencDec: "mlkem768x25519plus.native.600s.dec", VencEnc: "mlkem768x25519plus.native.0rtt.enc",
		SSKey: "c2VydmVya2V5c2VydmVya2V5", CertSNI: "www.bing.com", CertHash: "ff"}
}

func TestLinks(t *testing.T) {
	st := testState()
	u := User{Name: "bob", UUID: "11111111-2222-4333-8444-555555555555", SSKey: "dXNlcmtleXVzZXJrZXk="}
	l := st.Link(Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 2053, SNI: "www.b.com"}, u, Knobs{FP: "firefox"})
	pu, err := url.Parse(l)
	if err != nil {
		t.Fatal(err)
	}
	q := pu.Query()
	if pu.Scheme != "vless" || pu.Port() != "2053" || q.Get("security") != "reality" || q.Get("fp") != "firefox" ||
		q.Get("flow") != "xtls-rprx-vision" || q.Get("pbk") != "pub" || q.Get("sni") != "www.b.com" {
		t.Errorf("bad reality link: %s", l)
	}
	l = st.Link(Entry{ID: "venc", Kind: "vlessenc-xhttp", Port: 3000, Path: "/p"}, u, Knobs{Mode: "packet-up"})
	q = mustQuery(t, l)
	if q.Get("encryption") != st.VencEnc || q.Get("mode") != "packet-up" || q.Get("path") != "/p" || q.Get("security") != "none" {
		t.Errorf("bad vless-encryption link: %s", l)
	}
	l = st.Link(Entry{ID: "ss", Kind: "ss2022", Port: 4000}, u, Knobs{})
	if !strings.HasPrefix(l, "ss://2022-blake3-aes-128-gcm%3A") || !strings.Contains(l, "@203.0.113.7:4000") {
		t.Errorf("bad ss link: %s", l)
	}
	l = st.Link(Entry{ID: "hy", Kind: "hysteria2", Port: 5000}, u, Knobs{})
	if !strings.HasPrefix(l, "hysteria2://"+u.UUID+"@203.0.113.7:5000?") || !strings.Contains(l, "pinSHA256=ff") {
		t.Errorf("bad hysteria2 link: %s", l)
	}
	st.Host = "2001:db8::1"
	if l = st.Link(Entry{ID: "x", Kind: "reality-tcp", Port: 1}, u, Knobs{}); !strings.Contains(l, "@[2001:db8::1]:1?") {
		t.Errorf("IPv6 host not bracketed: %s", l)
	}
}

func mustQuery(t *testing.T, l string) url.Values {
	pu, err := url.Parse(l)
	if err != nil {
		t.Fatal(err)
	}
	return pu.Query()
}

func TestServerConfigOnlyEnabledUsers(t *testing.T) {
	st := testState()
	on, off := NewUser("on"), NewUser("off")
	off.Enabled = false
	entries := []Entry{{ID: "a", Kind: "reality-tcp", Port: 1, SNI: "x.com"}, {ID: "s", Kind: "ss2022", Port: 2}}
	cfg := st.ServerConfig(entries, []User{on, off})
	ibs := cfg["inbounds"].([]J)
	if len(ibs) != 2 {
		t.Fatalf("want 2 inbounds, got %d", len(ibs))
	}
	clients := ibs[0]["settings"].(J)["clients"].([]J)
	if len(clients) != 1 || clients[0]["email"] != "on" || clients[0]["flow"] != "xtls-rprx-vision" {
		t.Errorf("disabled user leaked or flow missing: %v", clients)
	}
	if got := st.ServerConfig(entries, []User{off})["inbounds"].([]J); len(got) != 0 {
		t.Errorf("no enabled users should mean no inbounds, got %d", len(got))
	}
}

func TestPickPrefersSimplestWithinTie(t *testing.T) {
	plain := &Variant{K: Knobs{FP: "chrome", Frag: "none"}, Score: 95}
	frag := &Variant{K: Knobs{FP: "chrome", Frag: "frag-aggr"}, Score: 100}
	if pick([]*Variant{frag, plain}) != plain {
		t.Error("a 5% gain should not justify fragmentation")
	}
	frag.Score = 130
	if pick([]*Variant{frag, plain}) != frag {
		t.Error("a clear win should be picked")
	}
	if pick([]*Variant{{Score: 0}}) != nil {
		t.Error("all-failed group should return nil")
	}
}

func TestCurlQuote(t *testing.T) {
	if got := curlQuote(`{"a":"b\c"}`); got != `"{\"a\":\"b\\c\"}"` {
		t.Errorf("curlQuote: %s", got)
	}
}

func TestParseAllowsFlagsAfterArgs(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	q := fs.Float64("quota", 0, "")
	f := fs.Bool("f", false, "")
	pos := parse(fs, []string{"add", "bob", "--quota", "5", "-f"})
	if strings.Join(pos, " ") != "add bob" || *q != 5 || !*f {
		t.Errorf("pos=%v quota=%v f=%v", pos, *q, *f)
	}
}

func TestSubscriptionIsBase64Links(t *testing.T) {
	st := testState()
	st.Entries = []Entry{{ID: "a", Kind: "reality-tcp", Port: 1, SNI: "x.com"}}
	u := NewUser("z")
	raw, err := base64.StdEncoding.DecodeString(st.Subscription(u))
	if err != nil || !strings.HasPrefix(string(raw), "vless://"+u.UUID) {
		t.Errorf("subscription body: %q %v", raw, err)
	}
}
