package state

// Autopilot is the provider the server can get a new IP from.
type Autopilot struct {
	Provider string `json:"provider"`          // digitalocean
	Token    string `json:"token"`             // API token (write access to reserved IPs)
	Droplet  int64  `json:"droplet,omitempty"` // this server's droplet ID
	Owned    string `json:"owned,omitempty"`   // the reserved IP xtune assigned, released at the next move
}
