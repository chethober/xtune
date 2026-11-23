package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// Bridge relays (`xtune relay add|sync NAME --bridge`): instead of forwarding bytes, the relay
// ends the client's REALITY connection itself (with REALITY keys of its own) and opens a second
// connection to this server with whichever config gets through best *from the relay*: the first
// leg is tuned for the users' networks, the second for the relay's. Routes where the international
// leg is filtered differently from the domestic one can use different configs on each.
//
// The relay connects as the same user (one outbound per user, chosen by Xray's routing on the
// user's email), so quotas, expiry, device limits and usage still work here, and a disabled user
// is refused here as before. The relay holds the users' IDs: the trade-off for re-encrypting.
// Users added or rotated since the last sync are not given the bridge's configs until
// `xtune relay sync`. Configs a bridge cannot end (Hysteria2, WireGuard, SSH, VLESS Encryption)
// are still forwarded as they are.

// bridgeable are the configs a bridge relay ends itself.
func bridgeable(e state.Entry) bool { return e.Reality() && !e.CDN() && protocols.Relayable(e) }

// leg2able are the configs the relay can use to reach this server.
func leg2able(e state.Entry) bool {
	return e.Xray() && e.Kind != "wireguard" && e.Kind != "worker-ws"
}

// newBridgeKeys makes the relay's own REALITY keys.
func newBridgeKeys(b *state.RelayBridge) error {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	b.RealityPriv = base64.RawURLEncoding.EncodeToString(priv.Bytes())
	b.RealityPub = base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	b.ShortID = randHex(4)
	return nil
}

// planBridge picks the relay's ports for the bridged configs (the config's own port when free
// there, as forwards do) and keeps the bridge's keys.
func planBridge(st *state.State, prev *state.RelayBridge, used map[string]map[int]bool, only []string) (*state.RelayBridge, error) {
	b := &state.RelayBridge{}
	if prev != nil {
		b.RealityPriv, b.RealityPub, b.ShortID, b.Leg2, b.Knobs = prev.RealityPriv, prev.RealityPub, prev.ShortID, prev.Leg2, prev.Knobs
	}
	if b.RealityPriv == "" {
		if err := newBridgeKeys(b); err != nil {
			return nil, err
		}
	}
	for _, e := range st.Entries {
		if !bridgeable(e) || len(only) > 0 && !slices.Contains(only, e.ID) {
			continue
		}
		port := e.Port
		if prev != nil {
			if i := slices.IndexFunc(prev.Entries, func(x state.Entry) bool { return x.ID == e.ID }); i >= 0 {
				port = prev.Entries[i].Port
			}
		}
		for used["tcp"][port] {
			port = 20000 + mrand.IntN(40000)
		}
		used["tcp"][port] = true
		be := e
		be.Port, be.Hop, be.Addr = port, "", ""
		b.Entries = append(b.Entries, be)
	}
	if len(b.Entries) == 0 {
		return nil, errors.New("no REALITY config to bridge (a bridge ends REALITY configs; the rest are forwarded)")
	}
	return b, nil
}

// bridgeState is the deployment as the relay's REALITY inbounds see it: the bridge's keys.
func bridgeState(st *state.State, b *state.RelayBridge) *state.State {
	bs := *st
	bs.RealityPriv, bs.RealityPub, bs.ShortID = b.RealityPriv, b.RealityPub, b.ShortID
	return &bs
}

// bridgeConfig is the relay's Xray config: the forwards, the bridged inbounds with the users, and
// one outbound per user to this server's Leg2 config.
func bridgeConfig(st *state.State, target string, fs []state.Forward, b *state.RelayBridge, users []state.User) (J, error) {
	cfg := relayConfig(target, fs)
	leg2 := slices.IndexFunc(st.Entries, func(e state.Entry) bool { return e.ID == b.Leg2 })
	if leg2 < 0 {
		return nil, fmt.Errorf("the bridge's second leg %q is not deployed: xtune relay sync", b.Leg2)
	}
	e2 := st.Entries[leg2]
	b.Knobs.Apply(&e2)
	up := *st // the relay dials this server as RelayTarget, like forwards do
	up.Domain, up.ClientIP = "", target
	bs := bridgeState(st, b)
	ins, _ := cfg["inbounds"].([]J)
	for _, e := range b.Entries {
		ib := protocols.Inbound(bs, e, users)
		ib["tag"] = "bridge-" + e.ID
		ins = append(ins, ib)
	}
	obs := []J{}
	var rules []J
	for _, u := range users {
		tag := "up-" + u.Name
		obs = append(obs, protocols.Outbound(&up, e2, u, e2.Knobs(), tag))
		rules = append(rules, J{"user": []string{u.Name}, "outboundTag": tag})
	}
	obs = append(obs, protocols.FragOutbounds(protocols.FragsOf(obs...)...)...)
	obs = append(obs, J{"tag": "out", "protocol": "freedom"}, J{"tag": "block", "protocol": "blackhole"})
	bridged := []string{}
	for _, e := range b.Entries {
		bridged = append(bridged, "bridge-"+e.ID)
	}
	// anything the bridge's inbounds take that is not a known user goes nowhere
	rules = append(rules, J{"inboundTag": bridged, "outboundTag": "block"})
	cfg["inbounds"], cfg["outbounds"] = ins, obs
	cfg["routing"] = J{"rules": rules}
	return cfg, nil
}

// ---------------------------------------------------------------- the second leg, from the relay

// leg2Candidate is a config of this server, with client settings, the relay may use.
type leg2Candidate struct {
	Entry state.Entry
	K     state.Knobs
	Label string
}

func leg2Candidates(st *state.State) []leg2Candidate {
	var out []leg2Candidate
	for _, e := range st.Entries {
		if !leg2able(e) {
			continue
		}
		ks := []state.Knobs{e.Knobs()}
		if e.Reality() || e.CDN() || e.Trojan() { // TLS: the relay's network may need fragmenting too
			for _, f := range []string{"frag-mild", "frag-aggr"} {
				k := e.Knobs()
				if k.Frag != f {
					k.Frag = f
					ks = append(ks, k)
				}
			}
		}
		for _, k := range ks {
			out = append(out, leg2Candidate{Entry: e, K: k, Label: e.ID + " " + orStr(knobLabel(k), "as deployed")})
		}
	}
	return out
}

// measureLeg2 measures every candidate from the relay (helper relay-probe, as the check user) and
// returns their scores by label, best first.
func measureLeg2(relay *remote.Server, st *state.State, target string, cs []leg2Candidate, probes int) (map[string]float64, []string, error) {
	if st.Check == nil {
		return nil, nil, errors.New("no check user")
	}
	up := *st
	up.Domain, up.ClientIP = "", target
	cfg := J{"log": J{"loglevel": "error"}}
	var ins, obs, rules []J
	var ports []string
	base := 21000 + mrand.IntN(20000)
	for i, c := range cs {
		port := base + i
		ports = append(ports, strconv.Itoa(port))
		in, out := fmt.Sprintf("in%d", i), fmt.Sprintf("out%d", i)
		ins = append(ins, J{"tag": in, "listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": J{"udp": true}})
		obs = append(obs, protocols.Outbound(&up, c.Entry, *st.Check, c.K, out))
		rules = append(rules, J{"inboundTag": []string{in}, "outboundTag": out})
	}
	obs = append(obs, protocols.FragOutbounds(protocols.FragsOf(obs...)...)...)
	cfg["inbounds"], cfg["outbounds"], cfg["routing"] = ins, obs, J{"rules": rules}
	b, _ := json.Marshal(cfg)
	if err := relay.Upload(remote.Dir+"/relay-probe.json", b, "600"); err != nil {
		return nil, nil, err
	}
	out, err := relay.Helper(append([]string{"relay-probe", strconv.Itoa(probes)}, ports...)...)
	if err != nil {
		return nil, nil, fmt.Errorf("relay-probe: %w", err)
	}
	res := parseRelayProbe(out)
	scores := map[string]float64{}
	var order []string
	for i, c := range cs {
		p := res[ports[i]]
		v := &Variant{Ok: p[0], N: max(p[1], 1), Warm: p[2]}
		if p[0] == 0 {
			v.Warm = -1
		}
		scores[c.Label] = score(v)
		order = append(order, c.Label)
	}
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
	return scores, order, nil
}

// parseRelayProbe reads "PROBE PORT OK N MS" lines: port -> ok, n, median ms.
func parseRelayProbe(out string) map[string][3]int {
	res := map[string][3]int{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 5 || f[0] != "PROBE" {
			continue
		}
		ok, _ := strconv.Atoi(f[2])
		n, _ := strconv.Atoi(f[3])
		ms, _ := strconv.Atoi(f[4])
		res[f[1]] = [3]int{ok, n, ms}
	}
	return res
}

// chooseLeg2 is the simplest of the candidates within 10% of the best (as tunes choose).
func chooseLeg2(cs []leg2Candidate, scores map[string]float64) (leg2Candidate, bool) {
	var vs []*Variant
	by := map[*Variant]leg2Candidate{}
	for _, c := range cs {
		v := &Variant{Entry: c.Entry, K: c.K, Score: scores[c.Label]}
		vs = append(vs, v)
		by[v] = c
	}
	best := pick(vs)
	if best == nil {
		return leg2Candidate{}, false
	}
	return by[best], true
}

// bridgeUsers are the users a bridge is synced with: the enabled ones, and their stamps.
func bridgeUsers(st *state.State) ([]state.User, map[string]string) {
	var us []state.User
	stamps := map[string]string{}
	for _, u := range st.Users {
		if u.Enabled && !u.WGOnly {
			us = append(us, u)
			stamps[u.Name] = state.UserStamp(u)
		}
	}
	return us, stamps
}

// bridgeProblem says what is stale about a bridge relay ("" when nothing).
func bridgeProblem(st *state.State, rl state.Relay) string {
	b := rl.Bridge
	if b == nil {
		return ""
	}
	if !slices.ContainsFunc(st.Entries, func(e state.Entry) bool { return e.ID == b.Leg2 }) {
		return "its second leg " + b.Leg2 + " is no longer deployed: xtune relay sync " + rl.Name
	}
	stale := 0
	for _, u := range st.Users {
		if u.Enabled && !b.Serves(u) {
			stale++
		}
	}
	if stale > 0 {
		return fmt.Sprintf("%d users added or rotated since its last sync do not get its configs: xtune relay sync %s", stale, rl.Name)
	}
	return ""
}

// bridgeFilter drops, for user u, the bridged configs of bridges that do not know them yet.
func bridgeFilter(st *state.State, u state.User, es []state.Entry, via string) []state.Entry {
	i := slices.IndexFunc(st.Relays, func(rl state.Relay) bool { return rl.Name == via && rl.Bridge != nil })
	if i < 0 || st.Relays[i].Bridge.Serves(u) {
		return es
	}
	b := st.Relays[i].Bridge
	return slices.DeleteFunc(slices.Clone(es), func(e state.Entry) bool {
		return slices.ContainsFunc(b.Entries, func(x state.Entry) bool { return via+"-"+x.ID == e.ID })
	})
}

// syncBridge measures the second leg from the relay, picks it, and returns the bridge with its
// users, ready for bridgeConfig.
func syncBridge(relay *remote.Server, st *state.State, target string, b *state.RelayBridge, log func(string)) error {
	cs := leg2Candidates(st)
	if len(cs) == 0 {
		return errors.New("no config of this server can be the bridge's second leg")
	}
	log(fmt.Sprintf("measuring %d second-leg candidates from the relay", len(cs)))
	scores, order, err := measureLeg2(relay, st, target, cs, 3)
	if err != nil {
		return err
	}
	for _, l := range order {
		log(fmt.Sprintf("  %-40s %s", l, ff(scores[l])))
	}
	c, ok := chooseLeg2(cs, scores)
	if !ok {
		return errors.New("no config of this server gets through from the relay")
	}
	b.Leg2, b.Knobs, b.Scores = c.Entry.ID, state.OverrideOf(c.K), scores
	_, b.Users = bridgeUsers(st)
	b.Synced = time.Now().Unix()
	log(fmt.Sprintf("second leg: %s (%d users)", c.Label, len(b.Users)))
	return nil
}
