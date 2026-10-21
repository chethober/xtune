package main

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Traffic per deployed config: Xray counts it per inbound. A config that carries nothing for
// quietAfter while another one is in use is probably blocked on the users' networks; its users'
// apps quietly moved on to the others. The enforce job alerts once, and again when it is back.

const (
	quietAfter  = 2 * time.Hour
	busyWithin  = 15 * time.Minute // another config counts as in use when it carried traffic this recently
	quietPrefix = "quiet/"         // alert keys; no ':' so they are not taken for user alerts
)

// countEntries adds this run's traffic per inbound tag and forgets configs no longer deployed.
func countEntries(st *State, us *Usage, byTag map[string]int64, now time.Time) {
	if us.Entries == nil {
		us.Entries = map[string]*EntryUsage{}
	}
	keep := map[string]bool{}
	for _, e := range st.Entries {
		k := entryKey(e)
		keep[k] = true
		eu := us.Entries[k]
		if eu == nil {
			eu = &EntryUsage{First: now.Unix()}
			us.Entries[k] = eu
		}
		if n := byTag[e.ID]; n > 0 {
			eu.Bytes += n
			eu.Active = now.Unix()
		}
	}
	for k := range us.Entries {
		if !keep[k] {
			delete(us.Entries, k)
		}
	}
}

// quietEntries are the deployed configs that look blocked: nothing for quietAfter, while another
// config carried traffic within busyWithin. Without inbound stats (older deployments) nothing is
// ever active, so nothing is reported.
func quietEntries(st *State, us *Usage, now time.Time) map[string]time.Duration {
	busy := false
	for _, e := range st.Entries {
		if eu := us.Entries[entryKey(e)]; eu != nil && now.Unix()-eu.Active < int64(busyWithin/time.Second) {
			busy = true
		}
	}
	out := map[string]time.Duration{}
	if !busy {
		return out
	}
	for _, e := range st.Entries {
		eu := us.Entries[entryKey(e)]
		if eu == nil {
			continue
		}
		since := max(eu.Active, eu.First)
		if d := now.Sub(time.Unix(since, 0)); d >= quietAfter {
			out[e.ID] = d
		}
	}
	return out
}

// notifyEntries alerts when a config goes quiet and when it carries traffic again.
func notifyEntries(st *State, us *Usage, now time.Time) {
	quiet := quietEntries(st, us, now)
	ids := map[string]bool{}
	for _, e := range st.Entries {
		ids[e.ID] = true
		key := quietPrefix + e.ID
		d, isQuiet := quiet[e.ID]
		if !isQuiet && us.Notified[key] != 0 && st.Notify.On() {
			eu := us.Entries[entryKey(e)]
			if eu != nil && eu.Active > us.Notified[key] {
				if sendAlert(st.Notify, st.Addr(), "config-back", e.ID+" carries traffic again") != nil {
					continue // keep the alert, so the next run retries
				}
			}
		}
		alert(st, us, key, isQuiet, fmt.Sprintf("%s (port %d) has carried no traffic for %s while other configs are in use: "+
			"it is probably blocked on your users' networks. Check from there: xtune check", e.ID, e.Port, fmtSince(d)))
	}
	for k := range us.Notified {
		if id, ok := strings.CutPrefix(k, quietPrefix); ok && !ids[id] {
			delete(us.Notified, k)
		}
	}
}

func fmtSince(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// sparkline draws values as block characters, scaled to the largest.
func sparkline(vals []int64) string {
	bars := []rune("▁▂▃▄▅▆▇█")
	top := int64(0)
	for _, v := range vals {
		top = max(top, v)
	}
	var b strings.Builder
	for _, v := range vals {
		i := 0
		if top > 0 && v > 0 {
			i = int((v*int64(len(bars)-1) + top - 1) / top)
		}
		b.WriteRune(bars[i])
	}
	return b.String()
}

// printUsageHistory shows a user's traffic per day over the last 30 days.
func printUsageHistory(w io.Writer, r *Server, name string) error {
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if _, u := st.User(name); u == nil {
		return fmt.Errorf("no user %q", name)
	}
	us, err := LoadUsage(r)
	if err != nil {
		return err
	}
	now := time.Now()
	days := orUsage(us.Users[name]).lastDays(now, 30)
	total := int64(0)
	for _, v := range days {
		total += v
	}
	fmt.Fprintf(w, "%s, last 30 days: %s  %s\n\n", name, sparkline(days), fmtBytes(total))
	for i := len(days) - 1; i >= 0; i-- {
		if days[i] > 0 {
			fmt.Fprintf(w, "%s  %s\n", dayKey(now.AddDate(0, 0, i-len(days)+1)), fmtBytes(days[i]))
		}
	}
	return nil
}
