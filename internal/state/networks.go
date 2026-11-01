package state

// NetRank is how the deployed configs did from one network.
type NetRank struct {
	Name    string             `json:"name,omitempty"` // the ISP, as the ASN registry names it
	Updated int64              `json:"updated"`
	Scores  map[string]float64 `json:"scores"`          // by EntryKey; 0 = blocked there
	Addrs   map[string]string  `json:"addrs,omitempty"` // by EntryKey: CDN and Worker configs' clean IP there
}
