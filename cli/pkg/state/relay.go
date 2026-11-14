package state

// Relay is a server, usually in the users' own country, that forwards ports to this one: client →
// relay → this server (multi-hop). Domestic routes are often filtered less than international
// ones, and datacenter-to-datacenter links less than home and mobile ones. The relay holds no keys
// and no users: configs through it are this server's configs, dialled at the relay's address.
type Relay struct {
	Name     string    `json:"name"`
	Addr     string    `json:"addr"`           // the address clients dial: the relay's IP or a domain pointing at it
	Flag     string    `json:"flag,omitempty"` // the relay's country (ISO 3166-1 alpha-2), "" = unknown
	SSH      RelaySSH  `json:"ssh"`            // how xtune reaches the relay again (sync, removal); never a secret
	Target   string    `json:"target"`         // the address of this server the relay forwards to
	Forwards []Forward `json:"forwards"`
	Added    int64     `json:"added"`
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
