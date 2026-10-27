package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// Configs ranked per network. Every tune, check and measured report knows which network it ran
// from: its autonomous system (ASN), which the client looks up for itself. The scores of the
// deployed configs from there are kept on the server, by ASN. The subscription server looks up
// the ASN of whoever asks for a subscription and lists that network's best configs first, with
// the clean IP that worked best there for the CDN and Worker configs. Configs blocked there go
// last, not away: phones move between networks.

// NetRank is how the deployed configs did from one network.
type NetRank struct {
	Name    string             `json:"name,omitempty"` // the ISP, as the ASN registry names it
	Updated int64              `json:"updated"`
	Scores  map[string]float64 `json:"scores"`          // by entryKey; 0 = blocked there
	Addrs   map[string]string  `json:"addrs,omitempty"` // by entryKey: CDN and Worker configs' clean IP there
}

// netRankFresh is how long a network's ranking is used.
const netRankFresh = 30 * 24 * time.Hour

// ---------------------------------------------------------------- the client's network

// Network is the network this machine is on.
type Network struct {
	ASN  string // "AS12880"
	Name string
}

// clientNetwork looks up this machine's network, straight (not through a proxy). A variable so
// tests can choose a network.
var clientNetwork = func() (Network, error) {
	c := &http.Client{Timeout: 8 * time.Second, Transport: uaTransport{&http.Transport{Proxy: nil}}}
	var errs []string
	// Cloudflare's speed test answers with the client's ASN; ipinfo.io is the fallback
	if b, err := getBody(c, "https://speed.cloudflare.com/meta"); err == nil {
		var m struct {
			ASN  json.Number `json:"asn"`
			Org  string      `json:"asOrganization"`
			Host string      `json:"clientIp"`
		}
		if json.Unmarshal(b, &m) == nil && m.ASN.String() != "" && m.ASN.String() != "0" {
			return Network{ASN: "AS" + m.ASN.String(), Name: m.Org}, nil
		}
		errs = append(errs, "speed.cloudflare.com: no ASN")
	} else {
		errs = append(errs, err.Error())
	}
	if b, err := getBody(c, "https://ipinfo.io/json"); err == nil {
		var m struct{ Org string }
		if json.Unmarshal(b, &m) == nil {
			if asn, name, ok := strings.Cut(m.Org, " "); ok && strings.HasPrefix(asn, "AS") {
				return Network{ASN: asn, Name: name}, nil
			}
		}
		errs = append(errs, "ipinfo.io: no ASN")
	} else {
		errs = append(errs, err.Error())
	}
	return Network{}, errors.New(strings.Join(errs, "; "))
}

func getBody(c *http.Client, u string) ([]byte, error) {
	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

// detectNetwork looks up this machine's network for the rankings, and names it when the user did
// not: it logs and returns an empty network when it cannot tell.
func detectNetwork(name *string, log func(string)) Network {
	n, err := clientNetwork()
	if err != nil {
		log("[!] could not tell this network's ASN (" + tail(err.Error(), 200) + "): subscriptions will not be ranked for it")
		return Network{}
	}
	if *name == "" {
		*name = n.Name
	}
	log(fmt.Sprintf("[*] this network: %s (%s)", n.ASN, orStr(n.Name, "?")))
	return n
}

// ---------------------------------------------------------------- recording

// checkScore scores a check like a tune scores a variant: reliability, then speed and latency.
func checkScore(c CheckResult) float64 {
	return score(&Variant{Ok: c.Ok, N: c.N, Warm: c.Warm, Down: c.Down, Up: c.Up})
}

// variantRank scores the deployed entries from a tune's variants: each entry's own (with the
// knobs it was deployed with), and for CDN and Worker entries the clean IP that beat it.
func variantRank(entries []Entry, vs []*Variant) (map[string]float64, map[string]string) {
	scores, addrs := map[string]float64{}, map[string]string{}
	for _, e := range entries {
		best := -1.0
		for _, v := range vs {
			if v.Entry.ID != e.ID {
				continue
			}
			if v.K == e.Knobs() {
				scores[entryKey(e)] = v.Score
			}
			if v.K.Addr != "" && v.Score > best {
				best = v.Score
				addrs[entryKey(e)] = v.K.Addr
			}
		}
		if a, ok := addrs[entryKey(e)]; ok && (best <= scores[entryKey(e)] || a == e.Addr) {
			delete(addrs, entryKey(e))
		}
	}
	return scores, addrs
}

// labelRank does the same from a report's results, which name variants by label: the deployed
// variant of each entry is known by its label in the pending tune.
func labelRank(entries []Entry, deployed map[string]string, rs []CheckResult) (map[string]float64, map[string]string) {
	scores, addrs := map[string]float64{}, map[string]string{}
	by := map[string]float64{}
	for _, r := range rs {
		by[r.Label] = r.Score
	}
	for _, e := range entries {
		k := entryKey(e)
		own, ok := by[deployed[e.ID]]
		if !ok {
			continue
		}
		scores[k] = own
		best := own
		for _, r := range rs {
			if _, ip, found := strings.Cut(r.Label, " ip="); found && r.ID == e.ID && r.Score > best && ip != e.Addr {
				best, addrs[k] = r.Score, ip
			}
		}
	}
	return scores, addrs
}

// RecordNetwork stores one network's scores of the deployed configs on the server, over the
// scores it had. addrs nil keeps the clean IPs it had (a check measures only the deployed knobs).
func RecordNetwork(r *Server, n Network, scores map[string]float64, addrs map[string]string) error {
	if n.ASN == "" || len(scores) == 0 {
		return nil
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if st.Networks == nil {
		st.Networks = map[string]*NetRank{}
	}
	nr := st.Networks[n.ASN]
	if nr == nil {
		nr = &NetRank{Scores: map[string]float64{}}
		st.Networks[n.ASN] = nr
	}
	nr.Name, nr.Updated = orStr(n.Name, nr.Name), time.Now().Unix()
	for k, s := range scores {
		nr.Scores[k] = s
	}
	if addrs != nil {
		nr.Addrs = addrs
	}
	if len(nr.Addrs) == 0 {
		nr.Addrs = nil
	}
	st.forgetStaleRanks()
	return saveState(r, st)
}

// recordCheck stores a check's results as its network's ranking.
func recordCheck(r *Server, n Network, st *State, res []CheckResult) error {
	scores := map[string]float64{}
	for _, c := range res {
		for _, e := range st.Entries {
			if e.ID == c.ID {
				scores[entryKey(e)] = checkScore(c)
			}
		}
	}
	return RecordNetwork(r, n, scores, nil)
}

// forgetStaleRanks drops scores of configs no longer deployed, and networks with none left.
func (st *State) forgetStaleRanks() {
	keys := map[string]bool{}
	for _, e := range st.Entries {
		keys[entryKey(e)] = true
	}
	for asn, nr := range st.Networks {
		for k := range nr.Scores {
			if !keys[k] {
				delete(nr.Scores, k)
				delete(nr.Addrs, k)
			}
		}
		if len(nr.Scores) == 0 {
			delete(st.Networks, asn)
		}
	}
}

// ---------------------------------------------------------------- serving

// forNetwork is the deployment as a client on network asn should see it: its configs best first
// there, with that network's clean IPs. It returns st itself, and "", when there is no fresh
// ranking for asn.
func (st *State) forNetwork(asn string, now time.Time) (*State, string) {
	nr := st.Networks[asn]
	if nr == nil || now.Sub(time.Unix(nr.Updated, 0)) > netRankFresh {
		return st, ""
	}
	cp := *st
	cp.Entries = slices.Clone(st.Entries)
	for i := range cp.Entries {
		e := &cp.Entries[i]
		if a := nr.Addrs[entryKey(*e)]; a != "" && (e.CDN() || e.Worker()) {
			e.Addr = a
		}
	}
	rank := func(e Entry) float64 {
		s, ok := nr.Scores[entryKey(e)]
		switch {
		case !ok:
			return -1 // not measured there: after the ones that work, before the blocked ones
		case s == 0:
			return -2
		}
		return s
	}
	sort.SliceStable(cp.Entries, func(i, j int) bool { return rank(cp.Entries[i]) > rank(cp.Entries[j]) })
	return &cp, orStr(nr.Name, asn)
}

// asnLookup finds the ASN an IP belongs to. A variable so tests can stand in for DNS.
var asnLookup = func(ip net.IP) (string, error) { return cymruASN(net.DefaultResolver, ip) }

// cymruASN asks Team Cymru's IP-to-ASN DNS service: TXT "ASN | prefix | CC | registry | date".
func cymruASN(res *net.Resolver, ip net.IP) (string, error) {
	var q string
	if v4 := ip.To4(); v4 != nil {
		q = fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", v4[3], v4[2], v4[1], v4[0])
	} else {
		hex := fmt.Sprintf("%x", []byte(ip.To16()))
		var b strings.Builder
		for i := len(hex) - 1; i >= 0; i-- {
			b.WriteByte(hex[i])
			b.WriteByte('.')
		}
		q = b.String() + "origin6.asn.cymru.com"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	txts, err := res.LookupTXT(ctx, q)
	if err != nil {
		return "", err
	}
	for _, t := range txts {
		f := strings.Fields(strings.SplitN(t, "|", 2)[0])
		if len(f) > 0 { // several ASNs may announce it; the first will do
			if _, err := strconv.Atoi(f[0]); err == nil {
				return "AS" + f[0], nil
			}
		}
	}
	return "", errors.New("no ASN for " + ip.String())
}

// asnCache keeps lookups by /24 (IPv4) or /48 (IPv6) for a day: apps ask every few hours.
var asnCache = struct {
	sync.Mutex
	m map[string]asnCached
}{m: map[string]asnCached{}}

type asnCached struct {
	asn string
	at  time.Time
}

func asnOf(ip net.IP, now time.Time) string {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() {
		return ""
	}
	key := ip.Mask(net.CIDRMask(48, 128)).String()
	if v4 := ip.To4(); v4 != nil {
		key = v4.Mask(net.CIDRMask(24, 32)).String()
	}
	asnCache.Lock()
	c, ok := asnCache.m[key]
	asnCache.Unlock()
	if ok && now.Sub(c.at) < 24*time.Hour {
		return c.asn
	}
	asn, err := asnLookup(ip)
	if err != nil {
		asn = "" // cached too, briefly: a failing lookup should not slow every request
		now = now.Add(-23 * time.Hour)
	}
	asnCache.Lock()
	if len(asnCache.m) > 50000 {
		asnCache.m = map[string]asnCached{}
	}
	asnCache.m[key] = asnCached{asn, now}
	asnCache.Unlock()
	return asn
}

// requesterIP is who asked: through the CDN, the address Cloudflare saw.
func requesterIP(req *http.Request, st *State) net.IP {
	if st.Sub.Mode == "cdn" {
		if ip := net.ParseIP(strings.TrimSpace(req.Header.Get("CF-Connecting-IP"))); ip != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	return net.ParseIP(host)
}

// ---------------------------------------------------------------- command

func cmdNetworks(args []string) error {
	if err := serverOnly("networks", args); err != nil {
		return err
	}
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	action := "list"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "list", "ls":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		printNetworks(os.Stdout, st, time.Now())
		return nil
	case "del", "rm", "clear":
		defer r.lock()()
		st, err := requireState(r)
		if err != nil {
			return err
		}
		if action == "clear" {
			st.Networks = nil
		} else {
			if len(args) < 2 {
				return errors.New("networks del ASN")
			}
			asn := strings.ToUpper(args[1])
			if !strings.HasPrefix(asn, "AS") {
				asn = "AS" + asn
			}
			if st.Networks[asn] == nil {
				return fmt.Errorf("no ranking for %s", asn)
			}
			delete(st.Networks, asn)
		}
		return saveState(r, st)
	}
	return fmt.Errorf("unknown networks action %q (list, del ASN, clear)", action)
}

func printNetworks(w io.Writer, st *State, now time.Time) {
	if len(st.Networks) == 0 {
		fmt.Fprintln(w, "no networks ranked yet: tune, check (xtune check --every 6h) and measure from your users' networks")
		return
	}
	asns := make([]string, 0, len(st.Networks))
	for a := range st.Networks {
		asns = append(asns, a)
	}
	sort.Strings(asns)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ASN\tNETWORK\tMEASURED\tCONFIGS, BEST FIRST\n")
	for _, a := range asns {
		nr := st.Networks[a]
		ranked, _ := st.forNetwork(a, time.Unix(nr.Updated, 0))
		var ids []string
		for _, e := range ranked.Entries {
			s, ok := nr.Scores[entryKey(e)]
			switch {
			case !ok:
				continue
			case s == 0:
				ids = append(ids, e.ID+" (blocked)")
			case e.Addr != "" && nr.Addrs[entryKey(e)] == e.Addr:
				ids = append(ids, fmt.Sprintf("%s via %s", e.ID, e.Addr))
			default:
				ids = append(ids, e.ID)
			}
		}
		when := time.Unix(nr.Updated, 0).Format("2006-01-02")
		if now.Sub(time.Unix(nr.Updated, 0)) > netRankFresh {
			when += " (stale, unused)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a, orStr(nr.Name, "-"), when, strings.Join(ids, ", "))
	}
	tw.Flush()
}
