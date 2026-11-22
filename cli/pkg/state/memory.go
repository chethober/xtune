package state

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/chethober/xtune/pkg/remote"
)

// Memory is what tunes, checks, personal tunes and the apps' reports learned, kept between them in
// memory.json (next to state.json, so the frequent writes from reports do not rewrite the
// deployment):
//
//   - per network (ASN; "" for all networks together), a weight for every value of every client
//     setting of every kind of config ("reality-tcp|frag|frag-aggr"): Hedge, the full-information
//     form of EXP3, with forgetting. Censors change what they block, so old evidence fades (half
//     of it every MemoryHalfLife), and the next tune samples settings by these weights, with a
//     floor that keeps exploring;
//   - per network, the fragment ranges that worked best, which the next tune tries again;
//   - per user and network, how their own configs did for them (their checks and personal tunes),
//     which orders their subscription before the network's ranking.
type Memory struct {
	Schema int                    `json:"schema"`
	Nets   map[string]*NetMemory  `json:"nets,omitempty"`
	Users  map[string]*UserMemory `json:"users,omitempty"`
}

// NetMemory is one network's.
type NetMemory struct {
	Name    string          `json:"name,omitempty"`
	Updated int64           `json:"updated"`
	Arms    map[string]*Arm `json:"arms,omitempty"`  // "kind|dim|value"
	Frags   []GoodFrag      `json:"frags,omitempty"` // best first, at most MemoryFrags
}

// Arm is one setting value's log-weight (0: no evidence either way), as of T.
type Arm struct {
	L float64 `json:"l"`
	N float64 `json:"n"` // evidence left after forgetting, in observations
	T int64   `json:"t"`
}

// GoodFrag is a fragment range that got through, and how well.
type GoodFrag struct {
	Kind   string  `json:"kind"`
	Spec   string  `json:"spec"`
	Reward float64 `json:"r"`
	T      int64   `json:"t"`
}

// UserMemory is one user's results, per network.
type UserMemory struct {
	Nets map[string]*UserNet `json:"nets"`
}

// UserNet is how a user's configs did for them on one network: scores by ranking key
// (RouteKey), like NetRank's.
type UserNet struct {
	Updated int64              `json:"updated"`
	Scores  map[string]float64 `json:"scores"`
}

const (
	MemoryHalfLife = 3 * 24 * 3600  // seconds: evidence halves every 3 days
	MemoryKeep     = 60 * 24 * 3600 // seconds: networks and users not heard from in 60 days are forgotten
	MemoryFrags    = 8
	MemoryEta      = 1.0 // Hedge's learning rate per observation
	MemoryUserNets = 8   // most networks kept per user
)

func ArmKey(kind, dim, value string) string { return kind + "|" + dim + "|" + value }

// decay is how much evidence from dt seconds ago is left.
func decay(dt int64) float64 {
	if dt <= 0 {
		return 1
	}
	return math.Pow(0.5, float64(dt)/MemoryHalfLife)
}

// Observe adds one observation of a setting value: reward in [0, 1], 0.5 being no better or worse
// than an even chance.
func (nm *NetMemory) Observe(kind, dim, value string, reward float64, now int64) {
	if nm.Arms == nil {
		nm.Arms = map[string]*Arm{}
	}
	k := ArmKey(kind, dim, value)
	a := nm.Arms[k]
	if a == nil {
		a = &Arm{T: now}
		nm.Arms[k] = a
	}
	f := decay(now - a.T)
	a.L = a.L*f + MemoryEta*(min(max(reward, 0), 1)-0.5)
	a.N = a.N*f + 1
	a.T = now
	nm.Updated = max(nm.Updated, now)
}

// Weight is how likely value is to work, 0 to 1 (0.5: nothing known), as of now.
func (nm *NetMemory) Weight(kind, dim, value string, now int64) (float64, bool) {
	if nm == nil {
		return 0.5, false
	}
	a := nm.Arms[ArmKey(kind, dim, value)]
	if a == nil {
		return 0.5, false
	}
	return 1 / (1 + math.Exp(-2*a.L*decay(now-a.T))), true
}

// AddFrag remembers a fragment range that got through.
func (nm *NetMemory) AddFrag(kind, spec string, reward float64, now int64) {
	for i := range nm.Frags {
		if nm.Frags[i].Kind == kind && nm.Frags[i].Spec == spec {
			nm.Frags[i].Reward = (nm.Frags[i].Reward*decay(now-nm.Frags[i].T) + reward) / 2
			nm.Frags[i].T = now
			nm.sortFrags(now)
			return
		}
	}
	nm.Frags = append(nm.Frags, GoodFrag{Kind: kind, Spec: spec, Reward: reward, T: now})
	nm.sortFrags(now)
	if len(nm.Frags) > MemoryFrags {
		nm.Frags = nm.Frags[:MemoryFrags]
	}
}

func (nm *NetMemory) sortFrags(now int64) {
	sort.SliceStable(nm.Frags, func(i, j int) bool {
		return nm.Frags[i].Reward*decay(now-nm.Frags[i].T) > nm.Frags[j].Reward*decay(now-nm.Frags[j].T)
	})
}

// Net is the memory of network asn, made when missing.
func (m *Memory) Net(asn string) *NetMemory {
	if m.Nets == nil {
		m.Nets = map[string]*NetMemory{}
	}
	nm := m.Nets[asn]
	if nm == nil {
		nm = &NetMemory{}
		m.Nets[asn] = nm
	}
	return nm
}

// UserNet is user name's memory of network asn, made when missing.
func (m *Memory) UserNet(name, asn string) *UserNet {
	if m.Users == nil {
		m.Users = map[string]*UserMemory{}
	}
	um := m.Users[name]
	if um == nil {
		um = &UserMemory{Nets: map[string]*UserNet{}}
		m.Users[name] = um
	}
	un := um.Nets[asn]
	if un == nil {
		un = &UserNet{Scores: map[string]float64{}}
		um.Nets[asn] = un
	}
	return un
}

// Prune forgets networks and users not heard from in MemoryKeep, users that are gone, scores of
// configs no longer deployed (keys says which are), and a user's least recent networks past
// MemoryUserNets.
func (m *Memory) Prune(now int64, users map[string]bool, keys map[string]bool) {
	for asn, nm := range m.Nets {
		if now-nm.Updated > MemoryKeep {
			delete(m.Nets, asn)
		}
	}
	for name, um := range m.Users {
		if users != nil && !users[name] {
			delete(m.Users, name)
			continue
		}
		var asns []string
		for asn, un := range um.Nets {
			for k := range un.Scores {
				if keys != nil && !keys[k] {
					delete(un.Scores, k)
				}
			}
			if now-un.Updated > MemoryKeep || len(un.Scores) == 0 {
				delete(um.Nets, asn)
				continue
			}
			asns = append(asns, asn)
		}
		sort.Slice(asns, func(i, j int) bool { return um.Nets[asns[i]].Updated > um.Nets[asns[j]].Updated })
		for _, asn := range asns[min(len(asns), MemoryUserNets):] {
			delete(um.Nets, asn)
		}
		if len(um.Nets) == 0 {
			delete(m.Users, name)
		}
	}
}

const memoryFile = remote.Dir + "/memory.json"

// LoadMemory reads memory.json (empty when there is none).
func LoadMemory(r *remote.Server) (*Memory, error) {
	out, err := r.Root("cat " + memoryFile + " 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	return DecodeMemory([]byte(out)), nil
}

// DecodeMemory reads memory.json's contents; anything unreadable is an empty memory (it is only
// ever a hint for the next tune).
func DecodeMemory(b []byte) *Memory {
	m := &Memory{}
	if strings.TrimSpace(string(b)) != "" {
		if err := json.Unmarshal(b, m); err != nil || m.Schema > Schema {
			m = &Memory{}
		}
	}
	return m
}

// SaveMemory writes memory.json.
func SaveMemory(r *remote.Server, m *Memory) error {
	m.Schema = Schema
	b, _ := json.Marshal(m)
	return r.Upload(memoryFile, b, "600")
}

// MemoryPath is where memory.json is on the server.
const MemoryPath = memoryFile
