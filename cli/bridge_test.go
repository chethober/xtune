package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

func TestBridgeRelay(t *testing.T) {
	r, _ := deployed(t, state.NewUser("ann"), state.NewUser("bob"))
	st, _ := state.Load(r)
	st.Entries = append(st.Entries, state.Entry{ID: "hy2", Kind: "hysteria2", Port: 8443}, state.Entry{ID: "venc", Kind: "vlessenc-tcp", Port: 2083})
	bin := os.Getenv("XRAY_BIN")
	if bin != "" { // real keys, for Xray to check the relay's config
		k, err := NewState(st.Host, bin)
		if err != nil {
			t.Fatal(err)
		}
		st.RealityPriv, st.RealityPub, st.VencDec, st.VencEnc, st.CertPEM, st.KeyPEM = k.RealityPriv, k.RealityPub, k.VencDec, k.VencEnc, k.CertPEM, k.KeyPEM
	}
	state.Save(r, st)
	relay, rfs := relayFake()
	rfs.helper["relay-check"] = "TCP 443 35\nTCP 30001 40\nTCP 2083 30"
	// from the relay, REALITY to the server is blocked and VLESS Encryption gets through
	rfs.helper["relay-probe"] = ""
	probeOut := func() string {
		var cfg struct{ Inbounds []J }
		json.Unmarshal([]byte(rfs.files[remote.Dir+"/relay-probe.json"]), &cfg)
		var b strings.Builder
		var obs struct{ Outbounds []J }
		json.Unmarshal([]byte(rfs.files[remote.Dir+"/relay-probe.json"]), &obs)
		for i, in := range cfg.Inbounds {
			port := int(in["port"].(float64))
			ok := 0
			ob, _ := json.Marshal(obs.Outbounds[i])
			if !strings.Contains(string(ob), "reality") && obs.Outbounds[i]["protocol"] == "vless" {
				ok = 3
			}
			b.WriteString("PROBE " + itoa(port) + " " + itoa(ok) + " 3 " + map[bool]string{true: "80", false: "-1"}[ok > 0] + "\n")
		}
		return b.String()
	}
	rfs.helperFn = map[string]func() string{"relay-probe": probeOut}
	on := true
	st, err := SyncRelay(r, relay, RelayOpts{Name: "teh1", Bridge: &on, Log: func(s string) { t.Log(s) }})
	if err != nil {
		t.Fatal(err)
	}
	rl := st.Relays[0]
	b := rl.Bridge
	if b == nil || b.Leg2 != "venc" || len(b.Users) != 2 || b.RealityPub == st.RealityPub || len(b.Entries) != 1 || b.Entries[0].ID != "r-tcp" {
		t.Fatalf("bridge: %+v", b)
	}
	for _, f := range rl.Forwards {
		if f.ID == "r-tcp" {
			t.Error("a bridged config should not be forwarded too")
		}
	}
	// the relay's config: REALITY with the bridge's key, one outbound per user to venc, routed by user
	var cfg struct {
		Inbounds  []J
		Outbounds []J
		Routing   struct{ Rules []J }
	}
	json.Unmarshal([]byte(rfs.files[remote.Dir+"/relay.json"]), &cfg)
	body := rfs.files[remote.Dir+"/relay.json"]
	if !strings.Contains(body, `"privateKey": "`+b.RealityPriv+`"`) || strings.Contains(body, `"privateKey": "`+st.RealityPriv+`"`) {
		t.Error("the relay must have its own REALITY key, never the server's")
	}
	ups := 0
	for _, ob := range cfg.Outbounds {
		if strings.HasPrefix(ob["tag"].(string), "up-") {
			ups++
		}
	}
	if ups != 2 || len(cfg.Routing.Rules) != 3 {
		t.Errorf("per-user outbounds %d, rules %d", ups, len(cfg.Routing.Rules))
	}
	// subscriptions: ann gets the bridge's REALITY config with the bridge's key
	_, ann := st.User("ann")
	view, _ := userView(st, *ann, "", -1, time.Now())
	var link string
	for _, l := range protocols.NamedLinks(view, *ann) {
		if l.Name == "teh1-r-tcp" {
			link = l.URL
		}
	}
	if !strings.Contains(link, "pbk="+b.RealityPub) || !strings.Contains(link, "198.51.100.20") {
		t.Errorf("bridge link: %q", link)
	}
	// a user added after the sync does not get the bridge's configs until the next one
	if _, _, err := AddUser(r, "carol", Limits{}); err != nil {
		t.Fatal(err)
	}
	st, _ = state.Load(r)
	_, carol := st.User("carol")
	view, _ = userView(st, *carol, "", -1, time.Now())
	for _, l := range protocols.NamedLinks(view, *carol) {
		if l.Name == "teh1-r-tcp" {
			t.Error("carol is not on the bridge yet")
		}
	}
	if p := relayProblem(st, st.Relays[0], "ok"); !strings.Contains(p, "1 users added") {
		t.Errorf("problem: %s", p)
	}
	// Xray accepts the relay's config
	if bin != "" {
		var raw J
		json.Unmarshal([]byte(body), &raw)
		if err := xrayAccepts(bin, raw); err != nil {
			t.Errorf("xray rejects the bridge config: %v", err)
		}
	}
}
