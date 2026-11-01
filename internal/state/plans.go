package state

import "fmt"

type Plan struct {
	QuotaGB float64 `json:"quotaGB,omitempty"`
	Days    int     `json:"days,omitempty"`
	Devices int     `json:"devices,omitempty"`
	Reset   string  `json:"reset,omitempty"`
}

func (p Plan) String() string {
	q, d, dv := "unlimited", "never expires", "any devices"
	if p.QuotaGB > 0 {
		q = fmt.Sprintf("%g GB", p.QuotaGB)
	}
	if p.Days > 0 {
		d = fmt.Sprintf("%d days", p.Days)
	}
	if p.Devices > 0 {
		dv = fmt.Sprintf("%d devices", p.Devices)
	}
	s := q + ", " + d + ", " + dv
	if p.Reset != "" {
		s += ", resets " + p.Reset
	}
	return s
}
