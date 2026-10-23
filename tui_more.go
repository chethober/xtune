package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
)

// ---------------------------------------------------------------- plans (server)

// pickPlan asks for one of the plans; "" is the other choice. With no plans it returns "".
func (t *tui) pickPlan(st *State, title, other string) (string, error) {
	if len(st.Plans) == 0 {
		return "", nil
	}
	var ns []string
	for n := range st.Plans {
		ns = append(ns, n)
	}
	slices.Sort(ns)
	opts := []huh.Option[string]{huh.NewOption(other, "")}
	for _, n := range ns {
		opts = append(opts, huh.NewOption(n+": "+st.Plans[n].String(), n))
	}
	plan := ""
	err := huh.NewSelect[string]().Title(title).Options(opts...).Value(&plan).WithTheme(t.theme).Run()
	return plan, err
}

func (t *tui) plans() error {
	for {
		st, err := requireState(t.r)
		if err != nil {
			return err
		}
		fmt.Println(titleStyle.Render("\nPlans"))
		printPlans(st)
		fmt.Println(dimStyle.Render("A plan is a named set of limits. Add users with one; renewing extends their expiry by its days."))
		opts := []huh.Option[string]{huh.NewOption("Add or change a plan", "set")}
		if len(st.Plans) > 0 {
			opts = append(opts, huh.NewOption("Delete a plan", "del"))
		}
		opts = append(opts, huh.NewOption("Back", "back"))
		action := "set"
		if err := huh.NewSelect[string]().Title("Plans").Options(opts...).Value(&action).WithTheme(t.theme).Run(); err != nil || action == "back" {
			return err
		}
		if action == "del" {
			name, err := t.pickPlan(st, "Delete which plan?", "Cancel")
			if err != nil || name == "" {
				return err
			}
			showErr(DeletePlan(t.r, name))
			continue
		}
		var name string
		if err := huh.NewInput().Title("Plan name").Value(&name).Validate(func(s string) error {
			return validUserName(strings.TrimSpace(s))
		}).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		name = strings.TrimSpace(name)
		var cur User
		if p, ok := st.Plans[name]; ok {
			cur = User{Quota: int64(p.QuotaGB * (1 << 30)), Devices: p.Devices, Reset: p.Reset}
		}
		l, err := t.limitsForm(cur, nil)
		if err != nil {
			return err
		}
		p := Plan{QuotaGB: *l.QuotaGB, Days: *l.Days, Devices: *l.Devices, Reset: cur.Reset}
		if l.Reset != nil {
			p.Reset = *l.Reset
		}
		showErr(SetPlan(t.r, name, p))
	}
}

// ---------------------------------------------------------------- several servers (server)

func (t *tui) peers() error {
	st, err := requireState(t.r)
	if err != nil {
		return err
	}
	fmt.Println(titleStyle.Render("\nSeveral servers under one subscription"))
	fmt.Println(dimStyle.Render("One server (the primary) owns the users. Others follow it: they take its users and report\n" +
		"their traffic to it, and its subscriptions list every server's configs, so users fail over\n" +
		"to another server when one is blocked."))
	printPeers(st)
	var opts []huh.Option[string]
	switch {
	case st.Follow != nil:
		opts = append(opts, huh.NewOption("Stop following (keep the users, manage them here)", "unfollow"))
	case st.Peers != nil:
		opts = append(opts, huh.NewOption("Remove a follower", "remove"), huh.NewOption("Turn off (drops the followers)", "off"))
	default:
		opts = append(opts, huh.NewOption("Make this the primary: let other servers follow it", "on"),
			huh.NewOption("Follow a primary: take its users", "follow"))
	}
	opts = append(opts, huh.NewOption("Back", "back"))
	action := ""
	if err := huh.NewSelect[string]().Title("Servers").Options(opts...).Value(&action).WithTheme(t.theme).Run(); err != nil || action == "back" {
		return err
	}
	switch action {
	case "on":
		if st, err = EnablePeers(t.r); err != nil {
			return err
		}
		printPeers(st)
	case "off":
		if err := DisablePeers(t.r); err != nil {
			return err
		}
	case "remove":
		var names []string
		for n := range st.Peers.Servers {
			names = append(names, n)
		}
		if len(names) == 0 {
			return errors.New("no followers")
		}
		slices.Sort(names)
		name := names[0]
		if err := huh.NewSelect[string]().Title("Remove").Options(huh.NewOptions(names...)...).Value(&name).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		if err := RemovePeer(t.r, name); err != nil {
			return err
		}
	case "follow":
		var u, name string
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Peer URL").Description("On the primary: Several servers, or `xtune peers`.").Value(&u),
			huh.NewInput().Title("Name for this server (optional)").Value(&name),
		)).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		ok := false
		if err := huh.NewConfirm().Title("Replace this server's users with the primary's?").Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
			return err
		}
		st, err := SetFollow(t.r, strings.TrimSpace(u), strings.TrimSpace(name))
		if err != nil {
			return err
		}
		fmt.Println(okStyle.Render(fmt.Sprintf("following as %q: %d users", st.Follow.Name, len(st.Users))))
	case "unfollow":
		if _, err := SetFollow(t.r, "", ""); err != nil {
			return err
		}
		fmt.Println(okStyle.Render("stopped following"))
	}
	pause()
	return nil
}

// ---------------------------------------------------------------- Cloudflare (server)

func (t *tui) cloudflare() error {
	st, err := requireState(t.r)
	if err != nil {
		return err
	}
	state := "no token"
	if st.Cloudflare.Token != "" {
		state = "token set"
	}
	fmt.Println(titleStyle.Render("\nCloudflare") + "  " + state)
	fmt.Println(dimStyle.Render("With an API token (Zone:Read, DNS:Edit, Zone Settings:Edit), migrations and domain changes\n" +
		"update the DNS records themselves, and the CDN's SSL/TLS mode can be set to Full from here."))
	opts := []huh.Option[string]{huh.NewOption("Set the API token", "token")}
	if st.Cloudflare.Token != "" {
		opts = append(opts, huh.NewOption("Point the domains at this server now", "dns"))
		if st.CDNDomain != "" {
			opts = append(opts, huh.NewOption("Set the SSL/TLS mode to Full", "ssl"))
		}
		opts = append(opts, huh.NewOption("Forget the token", "off"))
	}
	opts = append(opts, huh.NewOption("Back", "back"))
	action := "token"
	if err := huh.NewSelect[string]().Title("Cloudflare").Options(opts...).Value(&action).WithTheme(t.theme).Run(); err != nil || action == "back" {
		return err
	}
	log := func(s string) { fmt.Println(okStyle.Render(s)) }
	switch action {
	case "token":
		tok := ""
		if err := huh.NewInput().Title("API token").EchoMode(huh.EchoModePassword).Value(&tok).WithTheme(t.theme).Run(); err != nil {
			return err
		}
		if err := SetCloudflare(t.r, Cloudflare{Token: strings.TrimSpace(tok)}); err != nil {
			return err
		}
		log("token saved")
	case "off":
		return SetCloudflare(t.r, Cloudflare{})
	case "dns":
		ip, err := serverIP(st.Host)
		if err != nil {
			return err
		}
		if err := CloudflarePoint(st, ip, log); err != nil {
			return err
		}
	case "ssl":
		mode, err := cfSSL(st.Cloudflare.Token, st.CDNDomain, true)
		if err != nil {
			return err
		}
		log("SSL/TLS mode: " + mode)
	}
	pause()
	return nil
}

// ---------------------------------------------------------------- REALITY targets (server)

func (t *tui) snis() error {
	fmt.Println(titleStyle.Render("\nREALITY targets"))
	fmt.Println(dimStyle.Render("Checked from the server weekly; failing or slow ones are replaced by themselves when users\n" +
		"get configs through subscriptions. Checking now..."))
	defer t.r.lock()()
	st, err := requireState(t.r)
	if err != nil {
		return err
	}
	changes, res, err := planSNIs(t.r, st)
	if err != nil {
		return err
	}
	for _, e := range st.Entries {
		if !e.Reality() {
			continue
		}
		state := "failing"
		if rs := res[e.SNI]; rs != nil && rs.ok > 0 {
			state = fmt.Sprintf("ok, %.0f ms", rs.median()*1000)
		}
		fmt.Printf("  %-16s %-24s %s\n", e.ID, e.SNI, state)
	}
	if len(changes) == 0 {
		fmt.Println(okStyle.Render("all REALITY targets are fine"))
		pause()
		return nil
	}
	for _, c := range changes {
		fmt.Printf("  %s: %s %s; better: %s\n", c.ID, c.From, c.Why, c.To)
	}
	ok := false
	desc := "Users' apps get them on their next subscription update."
	if !autoSNI(st) {
		desc = "This changes users' links: send them the new ones afterwards."
	}
	if err := huh.NewConfirm().Title("Change them?").Description(desc).Value(&ok).WithTheme(t.theme).Run(); err != nil || !ok {
		return err
	}
	if err := applySNIs(t.r, st, changes); err != nil {
		return err
	}
	fmt.Println(okStyle.Render("changed"))
	pause()
	return nil
}
