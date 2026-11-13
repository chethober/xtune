package state

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chethober/xtune/pkg/remote"
)

// Usage is what the enforce job measures, stored in usage.json next to state.json. Traffic
// counters in Xray reset when it restarts, so totals are accumulated here every minute.
type Usage struct {
	Schema    int                    `json:"schema,omitempty"` // the file's format, see Schema
	Users     map[string]*UserUsage  `json:"users"`
	Blocks    []IPBlock              `json:"blocks,omitempty"`    // device-limit blocks
	ActiveKey string                 `json:"activeKey,omitempty"` // which users config.json was last written for
	Updated   int64                  `json:"updated,omitempty"`
	Notified  map[string]int64       `json:"notified,omitempty"` // alerts sent, so each is sent once
	Failures  int                    `json:"failures,omitempty"` // enforce runs in a row the Xray API failed
	Entries   map[string]*EntryUsage `json:"entries,omitempty"`  // traffic per deployed config, by EntryKey
	BackupAt  int64                  `json:"backupAt,omitempty"` // last scheduled backup attempt (before v0.7.0: the bot's)
	SNIAt     int64                  `json:"sniAt,omitempty"`    // last check of the REALITY targets
	Synced    map[string][2]int64    `json:"synced,omitempty"`   // follower: traffic already reported to the primary
	Extra     map[string][2]int64    `json:"extra,omitempty"`    // SSH tunnel and VPN counters at the last run

	LastBackup int64            `json:"lastBackup,omitempty"` // last scheduled backup that was delivered
	BackupErr  string           `json:"backupErr,omitempty"`  // why the last attempt failed
	WorkerAt   int64            `json:"workerAt,omitempty"`   // last check of the Workers' request counts
	WorkerUse  []WorkerUse      `json:"workerUse,omitempty"`  // requests per Worker today, at that check
	Online     map[string]int64 `json:"online,omitempty"`     // primary: "user|ip" -> when first seen online on any server
	RotatedAt  int64            `json:"rotatedAt,omitempty"`  // autopilot: last time it moved the server to a new IP
}

// EntryUsage is the traffic of one deployed config. A config that stops carrying anything while
// the others still do is probably blocked on the users' networks.
type EntryUsage struct {
	Bytes  int64 `json:"bytes"`
	First  int64 `json:"first"`            // when it was first seen deployed
	Active int64 `json:"active,omitempty"` // last run it carried traffic
}

type UserUsage struct {
	Up       int64            `json:"up"`
	Down     int64            `json:"down"`
	LastSeen int64            `json:"lastSeen,omitempty"`
	Online   []string         `json:"online,omitempty"` // client IPs at the last check
	Daily    map[string]int64 `json:"daily,omitempty"`  // bytes per day (server date), the last HistoryDays days
}

// HistoryDays is how many days of per-user traffic are kept.
const HistoryDays = 60

func DayKey(t time.Time) string { return t.Format("2006-01-02") }

// AddDaily counts n bytes on now's day and drops days older than HistoryDays.
func (uu *UserUsage) AddDaily(now time.Time, n int64) {
	if uu.Daily == nil {
		uu.Daily = map[string]int64{}
	}
	uu.Daily[DayKey(now)] += n
	if len(uu.Daily) > HistoryDays {
		cut := DayKey(now.AddDate(0, 0, -HistoryDays))
		for d := range uu.Daily {
			if d < cut {
				delete(uu.Daily, d)
			}
		}
	}
}

// LastDays is the user's traffic on each of the n days up to now, oldest first.
func (uu *UserUsage) LastDays(now time.Time, n int) []int64 {
	out := make([]int64, n)
	for i := range n {
		out[i] = uu.Daily[DayKey(now.AddDate(0, 0, i-n+1))]
	}
	return out
}

// EntryKey identifies a deployed config: a redeploy that moves it to another port starts over.
func EntryKey(e Entry) string { return fmt.Sprintf("%s/%d", e.ID, e.Port) }

type IPBlock struct {
	IP    string `json:"ip"`
	User  string `json:"user"`
	Until int64  `json:"until"`
}

func (us *Usage) Of(name string) *UserUsage {
	if us.Users == nil {
		us.Users = map[string]*UserUsage{}
	}
	if us.Users[name] == nil {
		us.Users[name] = &UserUsage{}
	}
	return us.Users[name]
}

func LoadUsage(r *remote.Server) (*Usage, error) {
	out, err := r.Root("cat " + remote.Dir + "/usage.json 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return &Usage{Users: map[string]*UserUsage{}}, nil
	}
	return DecodeUsage([]byte(out))
}

func SaveUsage(r *remote.Server, us *Usage) error {
	us.Schema = Schema
	b, _ := json.MarshalIndent(us, "", " ")
	return r.Upload(remote.Dir+"/usage.json", b, "600")
}

// UserStatus is why a user can or cannot connect.
func UserStatus(u User, uu *UserUsage, now time.Time) string {
	switch {
	case !u.Enabled:
		return "disabled"
	case u.Expires > 0 && now.Unix() >= u.Expires:
		return "expired"
	case u.Quota > 0 && uu != nil && uu.Up+uu.Down >= u.Quota:
		return "over quota"
	}
	return "active"
}
