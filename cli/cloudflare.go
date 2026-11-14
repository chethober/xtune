package main

import (
	"bufio"
	"errors"
	"fmt"
	"golang.org/x/term"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chethober/xtune/pkg/cloudflare"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// With a Cloudflare API token, xtune keeps the domains' DNS records pointed at the server itself
// (after a migrate, or when the domains change), makes the records of new domains when tuning,
// can read and fix the SSL/TLS mode the CDN entry needs, and runs the Worker relay (worker.go).
// The token needs Zone:Read, DNS:Edit and Zone Settings:Edit on the zones, and for the Worker
// Account: Workers Scripts:Edit.

// CloudflarePoint points the direct domain (DNS only) and the CDN domain (proxied) at ip, and
// delegates the DNS tunnel's zone to the server.
func CloudflarePoint(st *state.State, ip string, log func(string)) error {
	if st.Cloudflare.Token == "" {
		return errors.New("no Cloudflare token: xtune cloudflare token")
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%q is not an IP address", ip)
	}
	if st.Domain == "" && st.CDNDomain == "" && st.DNSDomain == "" {
		return errors.New("no domains to point (xtune domains --direct D --cdn C --dns Z)")
	}
	if st.DNSDomain != "" {
		ns := nsHost(st.DNSDomain)
		msg, err := cloudflare.Point(st.Cloudflare.Token, ns, ip, false)
		if err != nil {
			return err
		}
		log(msg)
		msg, err = cloudflare.NS(st.Cloudflare.Token, st.DNSDomain, ns)
		if err != nil {
			return err
		}
		log(msg)
	}
	for _, d := range []struct {
		name    string
		proxied bool
	}{{st.Domain, false}, {st.CDNDomain, true}} {
		if d.name == "" {
			continue
		}
		msg, err := cloudflare.Point(st.Cloudflare.Token, d.name, ip, d.proxied)
		if err != nil {
			return err
		}
		log(msg)
	}
	return nil
}

// nsHost is the name server name xtune uses for a DNS tunnel zone: t.example.com is served by
// ns-t.example.com.
func nsHost(zone string) string { return "ns-" + zone }

// serverIP is the address records should point at: the server's address, resolved if a name.
func serverIP(host string) (string, error) {
	if net.ParseIP(host) != nil {
		return host, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("cannot tell this server's IP from %q: pass --ip", host)
	}
	return ips[0].String(), nil
}

func SetCloudflare(r *remote.Server, cf state.Cloudflare) error {
	if cf.Token != "" {
		if err := cloudflare.Verify(cf.Token); err != nil {
			return err
		}
	}
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	st.Cloudflare = cf
	return state.Save(r, st)
}

func cmdCloudflare(args []string) error {
	if err := serverOnly("cloudflare", args); err != nil {
		return err
	}
	pos, ip := []string{}, ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--ip" && i+1 < len(args) {
			ip, i = args[i+1], i+1
		} else if v, ok := strings.CutPrefix(args[i], "--ip="); ok {
			ip = v
		} else {
			pos = append(pos, args[i])
		}
	}
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	action := "status"
	if len(pos) > 0 {
		action = pos[0]
	}
	log := func(s string) { fmt.Println(s) }
	if action != "token" && action != "off" && action != "status" && st.Cloudflare.Token == "" {
		return errors.New("no Cloudflare token yet: xtune cloudflare token")
	}
	switch action {
	case "status":
		if st.Cloudflare.Token == "" {
			fmt.Println("Cloudflare: off (xtune cloudflare token)")
			return nil
		}
		fmt.Println("Cloudflare: token set")
		for _, d := range []string{st.Domain, st.CDNDomain} {
			if d == "" {
				continue
			}
			var recs []cloudflare.Record
			zone, err := cloudflare.Zone(st.Cloudflare.Token, d)
			if err == nil {
				err = cloudflare.Call(st.Cloudflare.Token, "GET", "/zones/"+zone+"/dns_records?name="+url.QueryEscape(d), nil, &recs)
			}
			if err != nil {
				fmt.Printf("  %s: %v\n", d, err)
				continue
			}
			for _, rec := range recs {
				fmt.Printf("  %s %s %s proxied=%v\n", rec.Name, rec.Type, rec.Content, rec.Proxied)
			}
		}
		if st.CDNDomain != "" {
			mode, err := cloudflare.SSL(st.Cloudflare.Token, st.CDNDomain, false)
			if err != nil {
				return err
			}
			fmt.Printf("  SSL/TLS mode of %s: %s (the CDN entry needs full)\n", st.CDNDomain, mode)
		}
		if w := st.Worker; w != nil {
			fmt.Printf("  Worker %s: %s %s, %d routes\n", w.Name, w.Host, w.Domain, len(w.Routes))
		}
		return nil
	case "token":
		tok := os.Getenv("XTUNE_CLOUDFLARE_TOKEN")
		if tok == "" {
			if tok, err = cfTokenAsk(); err != nil {
				return err
			}
		}
		if err := SetCloudflare(r, state.Cloudflare{Token: strings.TrimSpace(tok)}); err != nil {
			return err
		}
		fmt.Println("Cloudflare: token saved. `xtune migrate` and `xtune domains` now update the DNS records")
		return nil
	case "off":
		return SetCloudflare(r, state.Cloudflare{})
	case "dns":
		if ip == "" {
			if ip, err = serverIP(st.Host); err != nil {
				return err
			}
		}
		return CloudflarePoint(st, ip, log)
	case "worker":
		return cfWorkerCmd(r, st, pos[1:])
	case "ssl":
		if st.CDNDomain == "" {
			return errors.New("no CDN domain set")
		}
		mode, err := cloudflare.SSL(st.Cloudflare.Token, st.CDNDomain, true)
		if err != nil {
			return err
		}
		fmt.Printf("SSL/TLS mode of %s: %s\n", st.CDNDomain, mode)
		return nil
	}
	return fmt.Errorf("unknown cloudflare action %q", action)
}

const cfTokenPrompt = "Cloudflare API token (Zone:Read, DNS:Edit, Zone Settings:Edit; Account: Workers Scripts:Edit for the Worker relay, Account Analytics:Read for its usage alerts)"

// cfTokenURL opens Cloudflare's token form with xtune's permissions filled in: the ones above,
// plus Workers Routes:Edit for the Worker's hostname in the zone and Account Settings:Read to list
// the accounts (Account Analytics:Read is in the prompt: the Worker usage alerts read it).
const cfTokenURL = "https://dash.cloudflare.com/profile/api-tokens?permissionGroupKeys=%5B%7B%22key%22%3A%22zone%22%2C%22type%22%3A%22read%22%7D%2C%7B%22key%22%3A%22dns%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22zone_settings%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22workers_scripts%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22workers_routes%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22account_settings%22%2C%22type%22%3A%22read%22%7D%2C%7B%22key%22%3A%22account_analytics%22%2C%22type%22%3A%22read%22%7D%5D&name=xtune&accountId=%2A&zoneId=all"

// cfTokenAsk prompts for a token, after printing the link that makes one.
func cfTokenAsk() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) { // piped (the xtune app sends it this way): one line
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if tok := strings.TrimSpace(line); tok != "" {
			return tok, nil
		}
		if err == nil {
			err = errors.New("empty")
		}
		return "", fmt.Errorf("no Cloudflare token on stdin: %w", err)
	}
	fmt.Fprintln(os.Stderr, "Create a Cloudflare token with xtune's permissions:\n  "+cfTokenURL)
	return cliPrompt(cfTokenPrompt, true)
}

// cfWorkerCmd is `xtune cloudflare worker [status | sync | off]`.
func cfWorkerCmd(r *remote.Server, st *state.State, args []string) error {
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}
	w := st.Worker
	if w == nil {
		fmt.Println("No Cloudflare Worker: tune with --protocols worker to make one")
		return nil
	}
	switch action {
	case "status":
		fmt.Printf("Worker %s (account %s)\n  %s\n", w.Name, w.Account, w.Host)
		if w.Domain != "" {
			fmt.Println("  " + w.Domain)
		}
		for p, o := range w.Routes {
			fmt.Printf("  %s -> %s\n", p, o)
		}
		for _, x := range w.Extra {
			fmt.Printf("  also Worker %s (account %s)\n", x.Host, x.Account)
		}
		if l := workerDailyLimit(w); l > 0 {
			fmt.Printf("usage alerts at 80%% and 100%% of %s requests a day (xtune cloudflare worker limit N | none)\n", fmtCount(l))
		} else {
			fmt.Println("usage alerts: off")
		}
		if us, err := state.LoadUsage(r); err == nil && us.WorkerAt > 0 {
			for _, u := range us.WorkerUse {
				fmt.Printf("  %s: %s requests today (UTC), as of %s\n", u.Host, fmtCount(u.Requests), time.Unix(us.WorkerAt, 0).Format("15:04"))
			}
		}
		return nil
	case "add": // another account's Worker on the same routes: another 100,000 requests a day
		tok := os.Getenv("XTUNE_CLOUDFLARE_TOKEN")
		if tok == "" || tok == st.Cloudflare.Token {
			var err error
			if tok, err = cliPrompt("API token of ANOTHER Cloudflare account (Account: Workers Scripts:Edit, Account Analytics:Read)", true); err != nil {
				return err
			}
		}
		_, x, err := addExtraWorker(r, strings.TrimSpace(tok), func(s string) { fmt.Println(s) })
		if err != nil {
			return err
		}
		fmt.Printf("users' subscriptions now offer %s too (they get it on their next update)\n", x.Host)
		return nil
	case "remove", "rm":
		if len(args) < 2 {
			return errors.New("worker remove HOST")
		}
		return removeExtraWorker(r, args[1])
	case "limit":
		if len(args) < 2 {
			return errors.New("worker limit N (requests a day; 0 = the free plan's 100,000) or none")
		}
		n := int64(-1)
		if args[1] != "none" && args[1] != "off" {
			var err error
			if n, err = strconv.ParseInt(args[1], 10, 64); err != nil || n < 0 {
				return fmt.Errorf("worker limit: %q is not a number", args[1])
			}
		}
		return setWorkerLimit(r, n)
	case "sync": // upload it again, routes and script, even when nothing seems to have changed
		defer r.Lock()()
		w.Routes = nil
		if err := syncWorker(st); err != nil {
			return err
		}
		fmt.Printf("Worker %s: %d routes\n", w.Name, len(w.Routes))
		return state.Save(r, st)
	case "off": // the Worker configs go with it, so subscriptions stop offering them
		defer r.Lock()()
		rest := state.FilterEntries(st.Entries, func(e state.Entry) bool { return !e.Worker() })
		if len(rest) == 0 {
			return errors.New("the Worker configs are the only ones deployed: tune and deploy others first")
		}
		for _, x := range w.Extra {
			if err := cfWorkerDelete(x.Token, &state.Worker{Account: x.Account, Name: x.Name}); err != nil {
				return err
			}
		}
		if err := cfWorkerDelete(st.Cloudflare.Token, w); err != nil {
			return err
		}
		st.Worker = nil
		if len(rest) == len(st.Entries) {
			return state.Save(r, st)
		}
		st.Entries = rest
		return Apply(r, st)
	}
	return fmt.Errorf("unknown worker action %q (status, sync, add, remove HOST, limit N|none, off)", action)
}

// pointAfterMove updates the DNS records, from the new server, after a migrate. It reports
// whether it did, so the admin is not told to do it by hand.
func pointAfterMove(r *remote.Server, st *state.State, log func(string)) bool {
	if st.Cloudflare.Token == "" || st.Domain == "" && st.CDNDomain == "" && st.DNSDomain == "" {
		return false
	}
	ip, err := serverIP(st.Host)
	if err != nil {
		log("[!] " + err.Error())
		return false
	}
	out, err := r.Root(remote.Dir + "/xtune cloudflare dns --ip " + remote.Quote(ip))
	if err != nil {
		log("[!] the Cloudflare DNS update failed, update the records by hand: " + err.Error())
		return false
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		log("[+] DNS " + l)
	}
	return true
}

// tuneCloudflare gets the domains ready before a tune, when there is a Cloudflare token (the
// option's, or else the deployment's). With a zone, the domains not given get random names in
// it. Domains the deployment does not have yet get their DNS records, and a new CDN domain's zone
// the SSL/TLS mode Full. It returns the names it pointed, which may take a moment to resolve.
func (o *TuneOpts) tuneCloudflare(host string) ([]string, error) {
	dep := o.deployed
	if dep == nil {
		dep = &state.State{}
	}
	if o.Cloudflare == "" {
		o.Cloudflare = dep.Cloudflare.Token
	}
	if o.Cloudflare == "" {
		if o.Zone != "" {
			return nil, errors.New("--zone needs a Cloudflare token: set XTUNE_CLOUDFLARE_TOKEN, or run xtune cloudflare token on the server")
		}
		return nil, nil
	}
	if o.Cloudflare != dep.Cloudflare.Token {
		if err := cloudflare.Verify(o.Cloudflare); err != nil {
			return nil, err
		}
	}
	if o.Zone != "" {
		o.Domain = orStr(o.Domain, randLabel()+"."+o.Zone)
		o.CDNDomain = orStr(o.CDNDomain, randLabel()+"."+o.Zone)
		if slices.Contains(o.Protocols, "dnstt") {
			o.DNSDomain = orStr(o.DNSDomain, randLabel()+"."+o.Zone)
		}
	}
	if o.Domain == dep.Domain && o.CDNDomain == dep.CDNDomain && o.DNSDomain == dep.DNSDomain {
		return nil, nil
	}
	ip, err := serverIP(host)
	if err != nil {
		return nil, err
	}
	var made []string
	logged := func(msg string, err error) error {
		if err == nil {
			o.Log("[+] Cloudflare DNS: " + msg)
		}
		return err
	}
	if o.Domain != "" && o.Domain != dep.Domain {
		if err := logged(cloudflare.Point(o.Cloudflare, o.Domain, ip, false)); err != nil {
			return nil, err
		}
		made = append(made, o.Domain)
	}
	if o.CDNDomain != "" && o.CDNDomain != dep.CDNDomain {
		if err := logged(cloudflare.Point(o.Cloudflare, o.CDNDomain, ip, true)); err != nil {
			return nil, err
		}
		mode, err := cloudflare.SSL(o.Cloudflare, o.CDNDomain, true)
		if err != nil {
			return nil, err
		}
		o.Log("[+] Cloudflare: SSL/TLS mode of " + o.CDNDomain + "'s zone is " + mode)
		made = append(made, o.CDNDomain)
	}
	if o.DNSDomain != "" && o.DNSDomain != dep.DNSDomain {
		ns := nsHost(o.DNSDomain)
		if err := logged(cloudflare.Point(o.Cloudflare, ns, ip, false)); err != nil {
			return nil, err
		}
		if err := logged(cloudflare.NS(o.Cloudflare, o.DNSDomain, ns)); err != nil {
			return nil, err
		}
	}
	return made, nil
}

// waitResolve waits, up to max, until every name resolves from here: records just made take a
// few seconds to appear.
func waitResolve(names []string, max time.Duration, log func(string)) {
	deadline := time.Now().Add(max)
	for _, n := range names {
		for {
			if _, err := net.LookupHost(n); err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(3 * time.Second)
		}
	}
	if len(names) > 0 {
		log("[*] " + strings.Join(names, ", ") + " resolve from here")
	}
}
