package state

import "strconv"

// Relay is a server, usually in the users' own country, that forwards ports to this one: client →
// relay → this server (multi-hop). Domestic routes are often filtered less than international
// ones, and datacenter-to-datacenter links less than home and mobile ones. A forwarding relay holds
// no keys and no users: configs through it are this server's configs, dialled at the relay's
// address. A bridge (RelayBridge) ends REALITY itself and holds the users' IDs.
type Relay struct {
	Name     string       `json:"name"`
	Addr     string       `json:"addr"`           // the address clients dial: the relay's IP or a domain pointing at it
	Flag     string       `json:"flag,omitempty"` // the relay's country (ISO 3166-1 alpha-2), "" = unknown
	SSH      RelaySSH     `json:"ssh"`            // how xtune reaches the relay again (sync, removal); never a secret
	Target   string       `json:"target"`         // the address of this server the relay forwards to
	Forwards []Forward    `json:"forwards"`
	Added    int64        `json:"added"`
	Bridge   *RelayBridge `json:"bridge,omitempty"` // the relay decrypts and re-encrypts its REALITY configs
}

// RelayBridge is a relay that ends the client's connection itself (REALITY, with keys of its own)
// and opens a second one to this server with a config of its own choosing (Leg2), tuned from the
// relay, as the same user, so quotas, limits and usage still count here. The price: the relay
// holds the users' IDs (`xtune users rotate` makes a leaked one useless).
type RelayBridge struct {
	RealityPriv string             `json:"realityPrivateKey"`
	RealityPub  string             `json:"realityPublicKey"`
	ShortID     string             `json:"shortId"`
	Entries     []Entry            `json:"entries"`        // the configs clients reach on the relay (relay ports)
	Leg2        string             `json:"leg2"`           // the entry of this server the relay connects to
	Knobs       KnobOverride       `json:"knobs,omitzero"` // its client settings
	Users       map[string]string  `json:"users"`          // user name -> UserStamp, as synced to the relay
	Synced      int64              `json:"synced"`
	Scores      map[string]float64 `json:"scores,omitempty"` // second-leg candidates as measured from the relay
}

// Serves says whether the bridge knows user u as they are now.
func (b *RelayBridge) Serves(u User) bool { return b.Users[u.Name] == UserStamp(u) }

// UserStamp is a short fingerprint of a user's credentials: a bridge synced before a rotation
// does not serve the user until the next sync.
func UserStamp(u User) string {
	h := uint32(2166136261)
	for _, c := range []byte(u.UUID + "|" + u.SSKey) {
		h = (h ^ uint32(c)) * 16777619
	}
	return strconv.FormatUint(uint64(h), 36)
}

// RelaySSH is the relay's SSH address. Passwords and keys stay on the admin's computer.
type RelaySSH struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
	User string `json:"user,omitempty"`
}

// Forward is one port the relay forwards: Port on the relay to To on this server.
type Forward struct {
	ID   string `json:"id"`           // the entry it carries
	Port int    `json:"port"`         // listening on the relay
	To   int    `json:"to"`           // the entry's port on this server
	Net  string `json:"net"`          // tcp | udp
	Ms   int    `json:"ms,omitempty"` // relay → server TCP connect time when last synced, 0 = not measured (UDP)
}
