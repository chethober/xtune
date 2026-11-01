package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chethober/xtune/internal/remote"
	"github.com/chethober/xtune/internal/state"
)

// A plan is a named set of limits ("basic: 50 GB, 30 days, 2 devices, monthly reset"). Users can
// be added with one, and renewing a user extends their expiry by its days, starts their usage
// from zero and enables them again.

// renewDays is how long a renewal extends a user who has no plan.
const renewDays = 30

// planLimits are the plan's limits; set fields of over win.
func planLimits(p state.Plan, name string, over Limits) Limits {
	l := Limits{QuotaGB: &p.QuotaGB, Days: &p.Days, Devices: &p.Devices, Reset: &p.Reset, Plan: &name}
	if over.QuotaGB != nil {
		l.QuotaGB = over.QuotaGB
	}
	if over.Days != nil {
		l.Days = over.Days
	}
	if over.Devices != nil {
		l.Devices = over.Devices
	}
	if over.Reset != nil {
		l.Reset = over.Reset
	}
	return l
}

// withPlan fills l from the plan it names, if any.
func (l Limits) withPlan(st *state.State) (Limits, error) {
	if l.Plan == nil || *l.Plan == "" {
		return l, nil
	}
	p, ok := st.Plans[*l.Plan]
	if !ok {
		return l, fmt.Errorf("no plan %q (%s)", *l.Plan, planNames(st))
	}
	return planLimits(p, *l.Plan, l), nil
}

func planNames(st *state.State) string {
	if len(st.Plans) == 0 {
		return "no plans yet: xtune plans add NAME --quota GB --days N"
	}
	var ns []string
	for n := range st.Plans {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return "plans: " + strings.Join(ns, ", ")
}

func SetPlan(r *remote.Server, name string, p state.Plan) error {
	if err := validUserName(name); err != nil {
		return fmt.Errorf("invalid plan name %q", name)
	}
	if err := validReset(p.Reset); err != nil {
		return err
	}
	if p.QuotaGB < 0 || p.Days < 0 || p.Devices < 0 {
		return errors.New("limits cannot be negative")
	}
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if st.Plans == nil {
		st.Plans = map[string]state.Plan{}
	}
	st.Plans[name] = p
	return state.Save(r, st)
}

func DeletePlan(r *remote.Server, name string) error {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if _, ok := st.Plans[name]; !ok {
		return fmt.Errorf("no plan %q", name)
	}
	delete(st.Plans, name)
	return state.Save(r, st) // users keep their limits and the name, which renewals then ignore
}

// Renew extends the user's expiry by their plan's days (or days, or renewDays), from today or
// from their current expiry if later, starts their usage from zero and enables them. A plan
// given here becomes theirs, with its limits.
func Renew(r *remote.Server, name, plan string, days int) (*state.State, state.User, error) {
	var nu state.User
	st, err := changeUser(r, name, func(st *state.State, u *state.User) error {
		plan = orStr(plan, u.Plan)
		p, hasPlan := st.Plans[plan]
		if plan != "" && !hasPlan && plan != u.Plan {
			return fmt.Errorf("no plan %q (%s)", plan, planNames(st))
		}
		if hasPlan {
			l := planLimits(p, plan, Limits{})
			l.Days = nil // extended below, from the current expiry
			l.apply(u)
		}
		now := time.Now()
		d := days
		if d == 0 && hasPlan {
			d = p.Days
		}
		if d == 0 && (u.Expires > 0 || days > 0) {
			d = renewDays
		}
		if d > 0 {
			from := now
			if exp := time.Unix(u.Expires, 0); u.Expires > 0 && exp.After(now) {
				from = exp
			}
			u.Expires = from.AddDate(0, 0, d).Unix()
		}
		u.Enabled = true
		if u.Reset != "" {
			u.ResetAt = nextReset(u.Reset, now).Unix()
		}
		nu = *u
		return resetUsage(r, name)
	})
	return st, nu, err
}

// ---------------------------------------------------------------- command

func cmdPlans(args []string) error {
	if err := serverOnly("plans", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("plans", flag.ExitOnError)
	quota := fs.Float64("quota", 0, "traffic quota in GB (0 = unlimited)")
	days := fs.Int("days", 0, "days until expiry, and how long a renewal extends (0 = never expires)")
	devices := fs.Int("devices", 0, "simultaneous devices, 0 = unlimited")
	reset := fs.String("reset", "", "start usage from zero every period: monthly, weekly or none")
	pos := parse(fs, args)
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	action := "list"
	if len(pos) > 0 {
		action = pos[0]
	}
	switch action {
	case "list", "ls":
	case "add", "set":
		if len(pos) < 2 {
			return errors.New("plans add NAME --quota GB --days N [--devices N] [--reset monthly|weekly]")
		}
		rs := strings.TrimSpace(*reset)
		if rs == "none" {
			rs = ""
		}
		if err := SetPlan(r, pos[1], state.Plan{QuotaGB: *quota, Days: *days, Devices: *devices, Reset: rs}); err != nil {
			return err
		}
	case "del", "delete", "rm":
		if len(pos) < 2 {
			return errors.New("plans del NAME")
		}
		if err := DeletePlan(r, pos[1]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown plans action %q", action)
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	printPlans(st)
	return nil
}

func printPlans(st *state.State) {
	if len(st.Plans) == 0 {
		fmt.Println("no plans yet: xtune plans add NAME --quota GB --days N [--devices N] [--reset monthly]")
		return
	}
	var ns []string
	for n := range st.Plans {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, n := range ns {
		users := 0
		for _, u := range st.Users {
			if u.Plan == n {
				users++
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%d users\n", n, st.Plans[n], users)
	}
	tw.Flush()
}
