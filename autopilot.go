package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// The autopilot watches for the server's IP being blocked: every config that is reached at the
// server's address goes quiet while the relayed ones (CDN, Worker, DNS tunnel) still carry
// traffic, or a recent check from some network (xtune check --every) found every direct config
// blocked while a relayed one worked. It then alerts with the command that moves the server. With
// a DigitalOcean token, and when a check confirms it (traffic alone can mislead), it moves the
// server itself: a new reserved IP is assigned to the droplet,
// the direct domain's DNS record follows (with a Cloudflare token), and subscriptions hand users
// configs with the new address. At most once a day.

// Autopilot is the provider the server can get a new IP from.
type Autopilot struct {
	Provider string `json:"provider"`          // digitalocean
	Token    string `json:"token"`             // API token (write access to reserved IPs)
	Droplet  int64  `json:"droplet,omitempty"` // this server's droplet ID
	Owned    string `json:"owned,omitempty"`   // the reserved IP xtune assigned, released at the next move
}

const (
	rotateEvery   = 24 * time.Hour
	checkSignalIn = 6 * time.Hour // how recent a check must be to count
)

// relayed entries are not reached at the server's address, so they keep working when it is blocked.
func (e Entry) relayed() bool { return e.CDN() || e.Worker() || e.Kind == "dnstt" }

// ipBlocked reports whether the server's IP looks blocked, by either signal, and why.
func ipBlocked(st *State, us *Usage, now time.Time) (bool, string) {
	if b, why := blockConfirmed(st, us, now); b {
		return b, why
	}
	return blockedByTraffic(st, us, now)
}

// splitRelayed sorts the entries into those reached at the server's address and the relayed ones.
func splitRelayed(st *State) (direct, relayed []Entry) {
	for _, e := range st.Entries {
		if e.relayed() {
			relayed = append(relayed, e)
		} else {
			direct = append(direct, e)
		}
	}
	return direct, relayed
}

// blockedByTraffic is the traffic signal: every direct config quiet for 2 hours while a relayed
// one is in use. It is a hint, not proof (a few users whose apps prefer the CDN config look the
// same), so it only alerts.
func blockedByTraffic(st *State, us *Usage, now time.Time) (bool, string) {
	direct, relayed := splitRelayed(st)
	if len(direct) == 0 || len(relayed) == 0 {
		return false, "" // nothing to tell a blocked IP from an idle server
	}
	quiet := quietEntries(st, us, now)
	allQuiet := !slices.ContainsFunc(direct, func(e Entry) bool { return quiet[e.ID] == 0 })
	relayBusy := slices.ContainsFunc(relayed, func(e Entry) bool {
		eu := us.Entries[entryKey(e)]
		return eu != nil && now.Unix()-eu.Active < int64(busyWithin/time.Second)
	})
	if allQuiet && relayBusy {
		return true, "every config at the server's address carried nothing for 2 hours while the CDN or Worker configs were in use"
	}
	return false, ""
}

// blockConfirmed is the check signal: a recent check from some network (since the last move)
// found every direct config blocked while a relayed one worked. Only this one moves the server.
func blockConfirmed(st *State, us *Usage, now time.Time) (bool, string) {
	direct, relayed := splitRelayed(st)
	if len(direct) == 0 || len(relayed) == 0 {
		return false, ""
	}
	for asn, nr := range st.Networks {
		if now.Sub(time.Unix(nr.Updated, 0)) > checkSignalIn || nr.Updated <= us.RotatedAt {
			continue
		}
		blocked, measured := true, 0
		for _, e := range direct {
			if s, ok := nr.Scores[entryKey(e)]; ok {
				measured++
				blocked = blocked && s == 0
			}
		}
		works := slices.ContainsFunc(relayed, func(e Entry) bool { return nr.Scores[entryKey(e)] > 0 })
		if measured > 0 && blocked && works {
			return true, fmt.Sprintf("a check from %s found every config at the server's address blocked while the CDN or Worker configs worked", orStr(nr.Name, asn))
		}
	}
	return false, ""
}

// runAutopilot alerts when the IP looks blocked and, with a provider, moves the server to a new
// IP (the enforce job runs it every minute, under the server lock).
func runAutopilot(r *Server, st *State, us *Usage, now time.Time) {
	blocked, why := ipBlocked(st, us, now)
	confirmed, _ := blockConfirmed(st, us, now)
	ap := st.Autopilot
	canMove := ap != nil && ap.Provider != "" && now.Sub(time.Unix(us.RotatedAt, 0)) >= rotateEvery
	if blocked && canMove && !confirmed {
		why += fmt.Sprintf(". The autopilot moves the server only when a check from your users' network confirms it: "+
			"xtune check --host %s (or keep one running there: --every 6h)", st.Host)
	}
	if confirmed && canMove {
		old := st.Addr()
		ip, err := rotateIP(r, st, us, now)
		if err == nil {
			msg := fmt.Sprintf("the server's IP %s looks blocked (%s): it moved to %s. Users' apps get the new address on their next subscription update", old, why, ip)
			if st.Sub.Mode == "" && st.Domain == "" {
				msg += "; send users their new links (sudo xtune users links NAME)"
			}
			if st.Notify.On() {
				if err := sendAlert(st.Notify, st.Addr(), "ip-rotated", msg); err != nil {
					fmt.Println(err)
				}
			}
			return
		}
		fmt.Println("autopilot:", err)
		why += "; moving to a new IP failed: " + tail(err.Error(), 200)
	}
	alert(st, us, "ip-blocked", blocked, fmt.Sprintf("the server's IP %s looks blocked: %s. Move it to a new server from your computer "+
		"(users keep their links): xtune migrate --host %s --to NEWHOST", st.Host, why, st.Host))
}

// rotateIP gets the server a new IP from the provider and points clients at it.
func rotateIP(r *Server, st *State, us *Usage, now time.Time) (string, error) {
	ap := st.Autopilot
	if ap == nil || ap.Provider != "digitalocean" {
		return "", errors.New("no provider to get a new IP from (xtune autopilot digitalocean)")
	}
	if ap.Droplet == 0 {
		id, err := doDropletID(ap.Token, st.Host)
		if err != nil {
			return "", err
		}
		ap.Droplet = id
	}
	ip, err := doRotate(ap.Token, ap.Droplet, ap.Owned)
	if err != nil {
		return "", err
	}
	ap.Owned, st.ClientIP, us.RotatedAt = ip, ip, now.Unix()
	for _, e := range st.Entries { // the configs start over: quiet now means nothing yet
		if eu := us.Entries[entryKey(e)]; eu != nil {
			eu.First, eu.Active = now.Unix(), 0
		}
	}
	delete(us.Notified, "ip-blocked")
	if st.Domain != "" && st.Cloudflare.Token != "" {
		if msg, err := cfPoint(st.Cloudflare.Token, st.Domain, ip, false); err != nil {
			fmt.Println("autopilot: the direct domain's DNS record:", err)
		} else {
			fmt.Println("autopilot: DNS", msg)
		}
	}
	if hasEngine(st.Entries, "openvpn") || hasEngine(st.Entries, "ikev2") {
		return ip, Apply(r, st) // the VPN certificate names the address
	}
	return ip, saveState(r, st)
}

// ---------------------------------------------------------------- DigitalOcean

// doAPI and doMetadata are variables so tests can point them at a local server.
var (
	doAPI      = "https://api.digitalocean.com/v2"
	doMetadata = "http://169.254.169.254/metadata/v1"
	doWait     = 3 * time.Second // between polls of an assignment
)

func doCall(token, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, doAPI+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		json.Unmarshal(b, &e)
		return fmt.Errorf("digitalocean: HTTP %d %s", resp.StatusCode, e.Message)
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// doDropletID finds this droplet: from the metadata service, else by its public IP.
func doDropletID(token, host string) (int64, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	if resp, err := c.Get(doMetadata + "/id"); err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		var id int64
		if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &id); err == nil && resp.StatusCode == 200 && id > 0 {
			return id, nil
		}
	}
	ip, err := serverIP(host)
	if err != nil {
		return 0, err
	}
	var res struct {
		Droplets []struct {
			ID       int64 `json:"id"`
			Networks struct {
				V4 []struct {
					IP string `json:"ip_address"`
				} `json:"v4"`
			} `json:"networks"`
		} `json:"droplets"`
	}
	if err := doCall(token, "GET", "/droplets?per_page=200", nil, &res); err != nil {
		return 0, err
	}
	for _, d := range res.Droplets {
		for _, n := range d.Networks.V4 {
			if n.IP == ip {
				return d.ID, nil
			}
		}
	}
	return 0, fmt.Errorf("digitalocean: no droplet with the address %s in this account", ip)
}

type doReservedIP struct {
	IP      string `json:"ip"`
	Droplet *struct {
		ID int64 `json:"id"`
	} `json:"droplet"`
}

// doRotate gives the droplet a new reserved IP. A droplet holds one, so the old one (owned) is
// unassigned first and released once the new one is on.
func doRotate(token string, droplet int64, owned string) (string, error) {
	poll := func(ip string, want func(doReservedIP) bool) error {
		for range 40 {
			var res struct {
				ReservedIP doReservedIP `json:"reserved_ip"`
			}
			if err := doCall(token, "GET", "/reserved_ips/"+ip, nil, &res); err != nil {
				return err
			}
			if want(res.ReservedIP) {
				return nil
			}
			time.Sleep(doWait)
		}
		return fmt.Errorf("digitalocean: the reserved IP %s did not settle", ip)
	}
	if owned != "" {
		if err := doCall(token, "POST", "/reserved_ips/"+owned+"/actions", J{"type": "unassign"}, nil); err != nil {
			return "", fmt.Errorf("unassigning %s: %w", owned, err)
		}
		if err := poll(owned, func(r doReservedIP) bool { return r.Droplet == nil }); err != nil {
			return "", err
		}
	}
	var res struct {
		ReservedIP doReservedIP `json:"reserved_ip"`
	}
	if err := doCall(token, "POST", "/reserved_ips", J{"droplet_id": droplet}, &res); err != nil {
		if owned != "" { // put the old one back rather than leave the droplet without one
			doCall(token, "POST", "/reserved_ips/"+owned+"/actions", J{"type": "assign", "droplet_id": droplet}, nil)
		}
		return "", fmt.Errorf("a new reserved IP: %w", err)
	}
	ip := res.ReservedIP.IP
	if err := poll(ip, func(r doReservedIP) bool { return r.Droplet != nil && r.Droplet.ID == droplet }); err != nil {
		return "", err
	}
	if owned != "" {
		if err := doCall(token, "DELETE", "/reserved_ips/"+owned, nil, nil); err != nil {
			fmt.Println("autopilot: releasing", owned, err) // costs a little until released by hand
		}
	}
	return ip, nil
}

// ---------------------------------------------------------------- command

func cmdAutopilot(args []string) error {
	if err := serverOnly("autopilot", args); err != nil {
		return err
	}
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	us, err := LoadUsage(r)
	if err != nil {
		return err
	}
	switch action {
	case "status":
		blocked, why := ipBlocked(st, us, time.Now())
		fmt.Printf("server address for clients: %s\n", st.Addr())
		if blocked {
			fmt.Println("the IP looks blocked:", why)
		} else {
			fmt.Println("the IP does not look blocked")
		}
		if st.Autopilot == nil {
			fmt.Println("autopilot: alerts only (with a DigitalOcean droplet it can move to a new IP: xtune autopilot digitalocean)")
		} else {
			fmt.Printf("autopilot: moves to a new %s reserved IP when the IP looks blocked, at most once a day\n", st.Autopilot.Provider)
		}
		if us.RotatedAt > 0 {
			fmt.Println("last move:", time.Unix(us.RotatedAt, 0).Format("2006-01-02 15:04"))
		}
		return nil
	case "digitalocean", "do":
		tok := os.Getenv("XTUNE_DO_TOKEN")
		if tok == "" {
			if tok, err = cliPrompt("DigitalOcean API token (read and write)", true); err != nil {
				return err
			}
		}
		ap := &Autopilot{Provider: "digitalocean", Token: strings.TrimSpace(tok)}
		if ap.Droplet, err = doDropletID(ap.Token, st.Host); err != nil {
			return err
		}
		if st.Autopilot != nil && st.Autopilot.Provider == "digitalocean" {
			ap.Owned = st.Autopilot.Owned
		}
		st.Autopilot = ap
		fmt.Printf("autopilot: on for droplet %d. When the IP looks blocked, the droplet gets a new reserved IP (at most once a day)\n", ap.Droplet)
		if st.Domain != "" && st.Cloudflare.Token == "" {
			fmt.Println("note: links use the direct domain; set a Cloudflare token (xtune cloudflare token) so its DNS record follows")
		}
		return saveState(r, st)
	case "rotate", "move":
		if st.Autopilot == nil {
			return errors.New("no provider: xtune autopilot digitalocean")
		}
		ip, err := rotateIP(r, st, us, time.Now())
		if err != nil {
			return err
		}
		fmt.Println("clients now connect to", ip)
		return saveUsage(r, us)
	case "off":
		st.Autopilot = nil
		fmt.Println("autopilot: alerts only")
		return saveState(r, st)
	case "reset": // back to the droplet's own address
		st.ClientIP = ""
		return saveState(r, st)
	}
	return fmt.Errorf("unknown autopilot action %q (status, digitalocean, rotate, off, reset)", action)
}
