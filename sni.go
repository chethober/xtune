package main

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// REALITY targets (SNIs) are sites the server imitates; tuning picked the fastest ones from the
// server. Sites change: one can stop offering TLS 1.3 with HTTP/2, or become slow from the server,
// which makes the config slow or easy to spot. The enforce job re-checks them weekly. Changing a
// target changes users' links, so it is only done by itself when users get their configs through
// subscriptions; otherwise the admin is alerted and runs `xtune sni refresh`.

const (
	sniEvery   = 7 * 24 * time.Hour
	sniSlow    = 1.0 // seconds for the TLS handshake from the server
	sniSamples = 3
)

type sniResult struct {
	ok   int       // probes that gave TLS 1.3 + HTTP/2
	secs []float64 // their handshake times
}

func (r sniResult) median() float64 {
	if len(r.secs) == 0 {
		return 0
	}
	s := slices.Clone(r.secs)
	sort.Float64s(s)
	return s[len(s)/2]
}

// parseSNIProbe reads the helper's "SNI host 2 0.123" lines (or "SNI host fail").
func parseSNIProbe(out string) map[string]*sniResult {
	res := map[string]*sniResult{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[0] != "SNI" {
			continue
		}
		r := res[f[1]]
		if r == nil {
			r = &sniResult{}
			res[f[1]] = r
		}
		if len(f) == 4 && f[2] == "2" {
			if t, err := strconv.ParseFloat(f[3], 64); err == nil && t > 0 {
				r.ok++
				r.secs = append(r.secs, t)
			}
		}
	}
	return res
}

// sniChange is a REALITY entry whose target should change.
type sniChange struct {
	ID, From, To, Why string
}

// planSNIs probes the deployed targets (several times) and the candidates, and proposes a
// replacement for each target that failed every probe or is slow next to the best candidate.
func planSNIs(r *Server, st *State) ([]sniChange, map[string]*sniResult, error) {
	var current []string
	for _, e := range st.Entries {
		if e.Reality() && !slices.Contains(current, e.SNI) {
			current = append(current, e.SNI)
		}
	}
	if len(current) == 0 {
		return nil, nil, nil
	}
	args := []string{"probe"}
	for range sniSamples {
		args = append(args, current...)
	}
	for _, c := range sniCandidates {
		if !slices.Contains(current, c) {
			args = append(args, c)
		}
	}
	out, err := r.Helper(args...)
	if err != nil {
		return nil, nil, err
	}
	res := parseSNIProbe(out)
	var spare []string // working candidates, fastest first
	for _, c := range sniCandidates {
		if rs := res[c]; rs != nil && rs.ok > 0 && !slices.Contains(current, c) {
			spare = append(spare, c)
		}
	}
	sort.SliceStable(spare, func(i, j int) bool { return res[spare[i]].median() < res[spare[j]].median() })
	replace := map[string]sniChange{}
	for _, cur := range current {
		rs := res[cur]
		why := ""
		switch {
		case rs == nil || rs.ok == 0:
			why = "no longer answers with TLS 1.3 and HTTP/2 from the server"
		case rs.median() > sniSlow && len(spare) > 0 && rs.median() > 3*res[spare[0]].median():
			why = fmt.Sprintf("is slow from the server (%.0f ms)", rs.median()*1000)
		default:
			continue
		}
		if len(spare) == 0 {
			continue // nothing better to move to
		}
		replace[cur] = sniChange{From: cur, To: spare[0], Why: why}
		spare = spare[1:]
	}
	var changes []sniChange
	for _, e := range st.Entries {
		if c, ok := replace[e.SNI]; ok && e.Reality() {
			c.ID = e.ID
			changes = append(changes, c)
		}
	}
	return changes, res, nil
}

// applySNIs changes the targets and restarts Xray with them.
func applySNIs(r *Server, st *State, changes []sniChange) error {
	for _, c := range changes {
		for i := range st.Entries {
			if st.Entries[i].ID == c.ID {
				st.Entries[i].SNI = c.To
			}
		}
	}
	return Apply(r, st)
}

func describeSNIs(changes []sniChange) string {
	var s []string
	for _, c := range changes {
		s = append(s, fmt.Sprintf("%s: %s %s, use %s", c.ID, c.From, c.Why, c.To))
	}
	return strings.Join(s, "; ")
}

// autoSNI is subscriptions delivering the change to users: the subscription server, or a primary's.
func autoSNI(st *State) bool { return st.Sub.Mode != "" || st.Follow != nil }

// checkSNIs is the weekly check, run by the enforce job (which holds the server lock).
func checkSNIs(r *Server) error {
	st, err := requireState(r)
	if err != nil {
		return err
	}
	changes, _, err := planSNIs(r, st)
	if err != nil || len(changes) == 0 {
		return err
	}
	if !autoSNI(st) {
		if st.Notify.On() {
			return sendAlert(st.Notify, st.Addr(), "sni", "REALITY targets need a change ("+describeSNIs(changes)+
				"). It changes users' links, so run on the server: xtune sni refresh")
		}
		return nil
	}
	if err := applySNIs(r, st, changes); err != nil {
		return err
	}
	if st.Notify.On() {
		return sendAlert(st.Notify, st.Addr(), "sni", "changed REALITY targets ("+describeSNIs(changes)+
			"); users' apps get them on their next subscription update")
	}
	return nil
}

// sniDue reports whether the weekly check is due, and marks it done.
func sniDue(st *State, us *Usage, now time.Time) bool {
	if now.Unix()-us.SNIAt < int64(sniEvery/time.Second) || !slices.ContainsFunc(st.Entries, Entry.Reality) {
		return false
	}
	if us.SNIAt == 0 { // first run after deploy or upgrade: start the week from now
		us.SNIAt = now.Unix()
		return false
	}
	us.SNIAt = now.Unix()
	return true
}

func cmdSNI(args []string) error {
	if err := serverOnly("sni", args); err != nil {
		return err
	}
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	action := "check"
	if len(args) > 0 {
		action = args[0]
	}
	if action != "check" && action != "refresh" {
		return fmt.Errorf("unknown sni action %q (check or refresh)", action)
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	changes, res, err := planSNIs(r, st)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range st.Entries {
		if !e.Reality() || seen[e.SNI] {
			continue
		}
		seen[e.SNI] = true
		rs := res[e.SNI]
		state := "failing"
		if rs != nil && rs.ok > 0 {
			state = fmt.Sprintf("ok, %.0f ms (%d/%d probes)", rs.median()*1000, rs.ok, sniSamples)
		}
		fmt.Printf("%-24s %s\n", e.SNI, state)
	}
	if len(changes) == 0 {
		fmt.Println("all REALITY targets are fine")
		return nil
	}
	for _, c := range changes {
		fmt.Printf("%s: %s %s; better: %s\n", c.ID, c.From, c.Why, c.To)
	}
	if action == "check" {
		fmt.Println("change them with: xtune sni refresh (it changes users' links)")
		return nil
	}
	if err := applySNIs(r, st, changes); err != nil {
		return err
	}
	if !autoSNI(st) {
		fmt.Println("changed. Send users their new links: xtune users links NAME")
		return nil
	}
	fmt.Println("changed. Users' apps get the new targets on their next subscription update")
	return nil
}
