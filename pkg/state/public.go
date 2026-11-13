package state

import "fmt"

type PublicPool struct {
	Token   string  `json:"token"`             // secret part of the connect link
	QuotaGB float64 `json:"quotaGB,omitempty"` // per member
	Days    int     `json:"days,omitempty"`
	Devices int     `json:"devices,omitempty"`
	Reset   string  `json:"reset,omitempty"`
	Plan    string  `json:"plan,omitempty"`    // limits come from this plan instead, when set
	Members int     `json:"members,omitempty"` // most active members at once, 0 = unlimited
	Paused  bool    `json:"paused,omitempty"`  // no new members; existing ones keep working
	Created int64   `json:"created"`
}

func (p PublicPool) String() string {
	s := "plan " + p.Plan
	if p.Plan == "" {
		s = Plan{QuotaGB: p.QuotaGB, Days: p.Days, Devices: p.Devices, Reset: p.Reset}.String()
	}
	if p.Members > 0 {
		s += fmt.Sprintf(", at most %d members", p.Members)
	}
	if p.Paused {
		s += ", paused"
	}
	return s
}
