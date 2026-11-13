package main

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/chethober/xtune/pkg/netinfo"
	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// Config and subscription names. Apps show a config by the name after "#" in its link, and a
// subscription by its Profile-Title header; both come from the templates in state.Naming.

var configVars = []string{"flag", "country", "server", "user", "id", "proto", "port", "via"}
var subVars = []string{"flag", "country", "server", "user"}

// subImports are one-tap links that add the subscription, under its name, to the apps that take
// one. The subscription URL itself stays plain: apps that do not know these links still use it.
func subImports(sub, name string) []protocols.NamedLink {
	if sub == "" {
		return nil
	}
	q := url.QueryEscape
	frag := "#" + url.PathEscape(name)
	return []protocols.NamedLink{
		{Name: "v2rayNG", URL: "v2rayng://install-sub?url=" + q(sub) + "&name=" + q(name)},
		{Name: "Hiddify", URL: "hiddify://import/" + sub + frag},
		{Name: "Streisand", URL: "streisand://import/" + sub + frag},
		{Name: "sing-box", URL: "sing-box://import-remote-profile?url=" + q(sub) + frag},
	}
}

// checkTemplate rejects placeholders a template cannot use.
func checkTemplate(tmpl string, allowed []string) error {
	for _, m := range protocols.NamePlaceholder.FindAllStringSubmatch(tmpl, -1) {
		ok := false
		for _, a := range allowed {
			ok = ok || a == m[1]
		}
		if !ok {
			return fmt.Errorf("unknown placeholder {%s} (use %s)", m[1], "{"+strings.Join(allowed, "} {")+"}")
		}
	}
	return nil
}

// ---------------------------------------------------------------- the server's country

// detectFlag looks up the server's country when it is not known yet, at most once a day after a
// failure. The country is where the server's outbound address is (Cloudflare's trace, run here on
// the server); when that cannot be reached, the registry's country of the server's IP block stands
// in and is retried daily, as are flags detected before the outbound lookup existed. It reports
// whether the naming changed.
func detectFlag(st *state.State, now time.Time) bool {
	n := &st.Naming
	stale := n.Detected && !n.Outbound // a registry guess: try the outbound lookup again
	if (n.Flag != "" && !stale) || now.Unix()-n.Checked < 24*3600 {
		return false
	}
	if cc, err := netinfo.OutboundCountry(); err == nil {
		changed := n.Flag != cc || !n.Detected || !n.Outbound || n.Checked != 0
		n.Flag, n.Detected, n.Outbound, n.Checked = cc, true, true, 0
		return changed
	}
	n.Checked = now.Unix()
	if stale {
		return true // keep the registry's guess until the outbound lookup works
	}
	if cc := registryCountry(orStr(st.ClientIP, st.Host)); cc != "" {
		n.Flag, n.Detected, n.Outbound = cc, true, false
	}
	return true
}

// registryCountry is the country the IP block of host (an IP or a name) is registered in, "" when
// unknown. Hosting companies often register blocks where they are based, not where servers are.
func registryCountry(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		if ips, err := net.LookupIP(host); err == nil && len(ips) > 0 {
			ip = ips[0]
		}
	}
	if ip == nil {
		return ""
	}
	cc, err := netinfo.CountryLookup(ip)
	if err != nil {
		return ""
	}
	return cc
}

// forgetDetectedFlag drops a looked-up country after the server moves, so it is looked up again.
func forgetDetectedFlag(st *state.State) {
	if st.Naming.Detected {
		st.Naming.Flag, st.Naming.Detected, st.Naming.Outbound, st.Naming.Checked = "", false, false, 0
	}
}

// ---------------------------------------------------------------- changing the names

// NamingChange is what `xtune names` changes; nil fields stay as they are.
type NamingChange struct {
	Label, Country, Config, Sub *string
}

// SetNaming changes how configs and subscriptions are named. Country is a country code, "auto" to
// look it up, or "none"/"off" for no flag. Empty templates go back to the defaults.
func SetNaming(r *remote.Server, c NamingChange, now time.Time) (*state.State, error) {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	n := st.Naming
	if c.Label != nil {
		n.Label = strings.TrimSpace(*c.Label)
	}
	if c.Config != nil {
		if err := checkTemplate(*c.Config, configVars); err != nil {
			return nil, fmt.Errorf("--config: %w", err)
		}
		n.Config = strings.TrimSpace(*c.Config)
	}
	if c.Sub != nil {
		if err := checkTemplate(*c.Sub, subVars); err != nil {
			return nil, fmt.Errorf("--sub: %w", err)
		}
		n.Sub = strings.TrimSpace(*c.Sub)
	}
	if c.Country != nil {
		switch v := strings.ToLower(strings.TrimSpace(*c.Country)); {
		case v == "auto":
			n.Flag, n.Detected, n.Outbound, n.Checked = "", false, false, 0
		case v == "none" || v == "off" || v == "":
			n.Flag, n.Detected, n.Outbound = "off", false, false
		case protocols.ValidCountryCode(v):
			n.Flag, n.Detected, n.Outbound = strings.ToUpper(v), false, false
		default:
			return nil, fmt.Errorf("--country %q: use a two-letter country code (de, nl, us, …), auto or none", *c.Country)
		}
	}
	st.Naming = n
	if c.Country != nil {
		detectFlag(st, now)
	}
	return st, state.Save(r, st)
}

func describeNaming(st *state.State) string {
	n := st.Naming
	flag := "none yet (looked up automatically)"
	switch {
	case n.Flag == "off":
		flag = "off"
	case protocols.ServerFlag(st) != "":
		flag = protocols.FlagEmoji(protocols.ServerFlag(st)) + " " + protocols.ServerFlag(st)
		if n.Detected && n.Outbound {
			flag += " (looked up from the outbound address)"
		} else if n.Detected {
			flag += " (registry's country of the IP; retried daily)"
		}
	}
	u := state.User{Name: "USER"}
	e := state.Entry{ID: "r-tcp", Kind: "reality-tcp", Port: 443}
	if len(st.Entries) > 0 {
		e = st.Entries[0]
	}
	return fmt.Sprintf("server label:   %s\ncountry flag:   %s\nconfig names:   %s\n                e.g. %s\nsubscription:   %s\n                e.g. %s",
		orStr(n.Label, "(none)"), flag, orStr(n.Config, state.DefaultConfigName+"  (default)"), protocols.ConfigName(st, e, u),
		orStr(n.Sub, state.DefaultSubName+"  (default)"), protocols.SubName(st, u))
}

func cmdNames(args []string) error {
	if err := serverOnly("names", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("names", flag.ExitOnError)
	label := fs.String("label", "", "this server's name, {server} in the templates")
	country := fs.String("country", "", "country flag: a code (de, nl, …), auto (look it up) or none")
	config := fs.String("config", "", "config name template; --config= restores the default ("+state.DefaultConfigName+")")
	sub := fs.String("sub", "", "subscription name template; --sub= restores the default ("+state.DefaultSubName+")")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: xtune names [--label NAME] [--country CC|auto|none] [--config TEMPLATE] [--sub TEMPLATE]")
		fmt.Fprintln(fs.Output(), "placeholders: config {"+strings.Join(configVars, "} {")+"}; subscription {"+strings.Join(subVars, "} {")+"}")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	var c NamingChange
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "label":
			c.Label = label
		case "country":
			c.Country = country
		case "config":
			c.Config = config
		case "sub":
			c.Sub = sub
		}
	})
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	var st *state.State
	if c == (NamingChange{}) {
		st, err = requireState(r)
	} else {
		st, err = SetNaming(r, c, time.Now())
	}
	if err != nil {
		return err
	}
	fmt.Println(describeNaming(st))
	if t := orStr(st.Naming.Config, state.DefaultConfigName); !strings.Contains(t, "{id}") && !strings.Contains(t, "{port}") {
		fmt.Println("note: without {id} or {port} several configs can share a name")
	}
	return nil
}
