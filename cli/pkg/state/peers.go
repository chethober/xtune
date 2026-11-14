package state

// PeerHub is the primary's side: the secret followers use, and what they last reported.
type PeerHub struct {
	Secret  string                 `json:"secret"`
	Servers map[string]*PeerServer `json:"servers,omitempty"`
}

type PeerServer struct {
	State  *State              `json:"state"` // public settings only: addresses, public keys, entries
	Seen   int64               `json:"seen"`
	Online map[string][]string `json:"online,omitempty"` // client IPs per user at its last report
}

// Follow is a follower's side: the primary's peer URL and the name it is listed under.
type Follow struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}
