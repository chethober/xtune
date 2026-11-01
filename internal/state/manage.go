package state

import "fmt"

type Manage struct {
	Token     string               `json:"token"`
	Resellers map[string]*Reseller `json:"resellers,omitempty"`
}

// A Reseller manages their own users only, within limits. 0 = no limit.
type Reseller struct {
	Token    string  `json:"token"`
	MaxUsers int     `json:"maxUsers,omitempty"`
	QuotaGB  float64 `json:"quotaGB,omitempty"` // total quota of their users; each then needs a quota
}

func (r Reseller) String() string {
	users, quota := "any number of users", "any quota"
	if r.MaxUsers > 0 {
		users = fmt.Sprintf("up to %d users", r.MaxUsers)
	}
	if r.QuotaGB > 0 {
		quota = fmt.Sprintf("%g GB in total", r.QuotaGB)
	}
	return users + ", " + quota
}
