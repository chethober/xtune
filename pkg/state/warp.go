package state

// Warp sends chosen sites (or all traffic) out through Cloudflare WARP instead of the server's
// own IP: many services refuse datacenter IPs. The account is a free WARP registration.
type Warp struct {
	PrivateKey string   `json:"privateKey"`
	Address4   string   `json:"address4"`
	Address6   string   `json:"address6,omitempty"`
	PeerKey    string   `json:"peerPublicKey"`
	Endpoint   string   `json:"endpoint"`
	Reserved   []int    `json:"reserved,omitempty"` // client id, sent in every packet
	Domains    []string `json:"domains,omitempty"`  // routing domains that use WARP
	All        bool     `json:"all,omitempty"`      // all traffic uses WARP
}
