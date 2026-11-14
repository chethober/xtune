package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/xtls/xray-core/app/observatory"
	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/features/stats"
	_ "github.com/xtls/xray-core/main/distro/all"
)

type J = protocols.J

// Options are how the app wants to connect.
type Options struct {
	Mode     string `json:"mode"`     // "tun" (the whole device) | "proxy" (local SOCKS/HTTP only)
	Selected string `json:"selected"` // a config ID, or "" for Auto: the fastest working one
	// Pool limits Auto to these configs, in priority order (the first is the fallback while health
	// checks have not answered). Empty: every config the app can run.
	Pool []string `json:"pool,omitempty"`
	// Android hands its VpnService descriptor over; desktops create an interface of this name.
	TunFd   int    `json:"tunFd,omitempty"`
	TunName string `json:"tunName,omitempty"`
	// Routes: the core sets the system's routes around the TUN interface (desktop).
	Routes       bool `json:"routes,omitempty"`
	SystemProxy  bool `json:"systemProxy,omitempty"` // desktop proxy mode: point the system proxy at it
	SocksPort    int  `json:"socksPort,omitempty"`
	HTTPPort     int  `json:"httpPort,omitempty"`
	ShareLAN     bool `json:"shareLan,omitempty"`
	BlockAds     bool `json:"blockAds,omitempty"`
	Domestic     bool `json:"domestic,omitempty"`     // the profile country's sites go direct
	IncludeDNSTT bool `json:"includeDnstt,omitempty"` // let Auto use the DNS tunnel too
	// AutoMode is how Auto picks: "" the lowest ping, checked every minute; latency: the lowest
	// ping, checked every 15 s so it moves sooner (calls, games); stable: the steadiest (lowest
	// spread of ping), for downloads and streams that dislike switching.
	AutoMode string `json:"autoMode,omitempty"`
	LogLevel string `json:"logLevel,omitempty"`

	// DNS: the resolver for everything else (through the tunnel): https://… (DoH), tcp://IP or a
	// plain IP (UDP); DomesticDNS answers for the domestic sites, directly; FakeDNS hands apps
	// placeholder addresses so connections start without waiting for a lookup (TUN mode).
	DNS         string `json:"dns,omitempty"`
	DomesticDNS string `json:"domesticDns,omitempty"`
	FakeDNS     bool   `json:"fakeDns,omitempty"`

	IPv6      bool `json:"ipv6,omitempty"`      // resolve and route IPv6 (else IPv4 only)
	BlockQUIC bool `json:"blockQuic,omitempty"` // apps fall back from QUIC (UDP 443) to TCP
	// ProxyLAN sends local-network addresses through the proxy too (normally they go direct).
	ProxyLAN      bool     `json:"proxyLan,omitempty"`
	BypassPush    bool     `json:"bypassPush,omitempty"`    // Apple and Google push go direct
	BypassDomains []string `json:"bypassDomains,omitempty"` // your own list that goes direct
	Rules         []Rule   `json:"rules,omitempty"`         // your rules, before everything else

	Tweaks       Tweaks            `json:"tweaks"`                 // for every config
	ConfigTweaks map[string]Tweaks `json:"configTweaks,omitempty"` // per config ID, over Tweaks

	// KillSwitch (Linux desktops): no traffic leaves outside the tunnel while it reconnects or if
	// the engine stops unexpectedly; only Disconnect lifts it. Android has its own (always-on VPN).
	KillSwitch bool `json:"killSwitch,omitempty"`
	// Smart reconnect: watch which network the device is on and, when it changes, fetch the
	// subscription's ranking for the new network and reconnect with its best configs.
	SmartURL string `json:"smartUrl,omitempty"`
	ASN      string `json:"asn,omitempty"` // the network the profile was fetched for
}

// Tweaks change how configs connect, to get past filtering by hand.
type Tweaks struct {
	Fragment    string `json:"fragment,omitempty"`    // "" (as the server set it) | "on" | "off"
	FragLength  string `json:"fragLength,omitempty"`  // bytes per fragment, e.g. "100-200"
	FragDelay   string `json:"fragDelay,omitempty"`   // ms between fragments, e.g. "10-20"
	Fingerprint string `json:"fingerprint,omitempty"` // uTLS: chrome, firefox, safari, ios, android, edge, randomized
	MTU         int    `json:"mtu,omitempty"`         // WireGuard and AmneziaWG
	Mux         string `json:"mux,omitempty"`         // "" | "on" | "off"
	MuxConc     int    `json:"muxConcurrency,omitempty"`
}

func (t Tweaks) over(o Tweaks) Tweaks { // o's set fields win
	if o.Fragment != "" {
		t.Fragment = o.Fragment
	}
	if o.FragLength != "" {
		t.FragLength = o.FragLength
	}
	if o.FragDelay != "" {
		t.FragDelay = o.FragDelay
	}
	if o.Fingerprint != "" {
		t.Fingerprint = o.Fingerprint
	}
	if o.MTU != 0 {
		t.MTU = o.MTU
	}
	if o.Mux != "" {
		t.Mux = o.Mux
	}
	if o.MuxConc != 0 {
		t.MuxConc = o.MuxConc
	}
	return t
}

// Rule sends matching traffic direct, through the proxy, or nowhere.
type Rule struct {
	Match  string `json:"match"`            // domain (and its subdomains) | full | keyword | geosite | ip | geoip | port | app
	Value  string `json:"value"`            // e.g. example.com, category-ir, 10.0.0.0/8, ir, 443 or 1000-2000; app: telegram.exe, firefox
	Action string `json:"action"`           // direct | proxy | block | via (through Config, whatever Auto picks)
	Config string `json:"config,omitempty"` // via: the config's ID
}

// appRules: Xray tells apps apart by their process on Linux and Windows; Android hides other
// apps' connections from it (per-app routing there is the VPN's allow and deny lists).
var appRules = runtime.GOOS == "linux" || runtime.GOOS == "windows"

// viaTag is the outbound a "via" rule uses: the config's own when Auto runs it, else one of its
// own, outside Auto's balancer.
func viaTag(id string, configs []protocols.ClientEntry) string {
	for _, c := range configs {
		if c.ID == id {
			return tagOf(c)
		}
	}
	return "via-" + id
}

// viaExtras are the configs "via" rules need that the connection does not run already.
func viaExtras(p *Profile, o Options, configs []protocols.ClientEntry) []protocols.ClientEntry {
	running := map[string]bool{}
	for _, c := range configs {
		running[c.ID] = true
	}
	var out []protocols.ClientEntry
	for _, r := range o.Rules {
		if r.Action != "via" || running[r.Config] {
			continue
		}
		for _, c := range p.Configs {
			if c.ID == r.Config && usable(c) {
				out = append(out, c)
				running[c.ID] = true
			}
		}
	}
	return out
}

func (o *Options) defaults() {
	if o.Mode == "" {
		o.Mode = "proxy"
	}
	if o.SocksPort == 0 {
		o.SocksPort = 10808
	}
	if o.HTTPPort == 0 {
		o.HTTPPort = 10809
	}
	if o.TunName == "" {
		o.TunName = "xtune0"
	}
	if o.DNS == "" {
		o.DNS = "https://1.1.1.1/dns-query"
	}
	if o.LogLevel == "" {
		o.LogLevel = "warning"
	}
}

// usable reports whether the app itself runs config c (not another app).
func usable(c protocols.ClientEntry) bool {
	switch c.Engine {
	case "xray":
		return c.Outbound != nil
	case "ssh":
		return c.SSH != nil
	case "awg":
		return c.AWG != nil
	}
	return false
}

// chosen are the configs a connection uses: the selected one, or Auto's set (the pool, in its
// order, when one is set).
func chosen(p *Profile, o Options) ([]protocols.ClientEntry, error) {
	byID := map[string]protocols.ClientEntry{}
	for _, c := range p.Configs {
		if usable(c) {
			byID[c.ID] = c
		}
	}
	if o.Selected != "" {
		if c, ok := byID[o.Selected]; ok {
			return []protocols.ClientEntry{c}, nil
		}
		for _, c := range p.Configs {
			if c.ID == o.Selected {
				return nil, fmt.Errorf("%s runs in another app", c.Name)
			}
		}
		return nil, fmt.Errorf("config %q is not in this subscription", o.Selected)
	}
	var out []protocols.ClientEntry
	if len(o.Pool) > 0 {
		for _, id := range o.Pool {
			if c, ok := byID[id]; ok {
				out = append(out, c)
			}
		}
	} else {
		for _, c := range p.Configs {
			if _, ok := byID[c.ID]; !ok || c.SSH != nil && c.SSH.DNSTT != nil && !o.IncludeDNSTT {
				continue // the DNS tunnel is slow: only when asked for
			}
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no config this app can run (check the Auto pool)")
	}
	return out, nil
}

func tagOf(c protocols.ClientEntry) string { return "proxy-" + c.ID }

// clone deep-copies a JSON object, so building a config never changes the profile.
func clone(j J) J {
	b, _ := json.Marshal(j)
	var out J
	json.Unmarshal(b, &out)
	return out
}

func sub(m J, key string) J {
	v, _ := m[key].(map[string]any)
	if v == nil {
		v = J{}
		m[key] = v
	}
	return v
}

// fragTagFor names the outbound that fragments TLS hellos the way t says; configs with the same
// fragment settings share one.
func fragTagFor(t Tweaks) string {
	return "frag-tweak-" + slug(orStr(t.FragLength, "100-200")+"-"+orStr(t.FragDelay, "10-20"))
}

// applyTweaks changes one Xray outbound as t says.
func applyTweaks(ob J, t Tweaks) {
	proto, _ := ob["protocol"].(string)
	ss, _ := ob["streamSettings"].(map[string]any)
	network := ""
	if ss != nil {
		network, _ = ss["network"].(string)
	}
	if t.Fingerprint != "" && ss != nil {
		for _, k := range []string{"realitySettings", "tlsSettings"} {
			if m, ok := ss[k].(map[string]any); ok {
				if _, h3 := m["alpn"].([]any); h3 && network == "hysteria" {
					continue // QUIC: uTLS fingerprints are for TCP
				}
				m["fingerprint"] = t.Fingerprint
			}
		}
	}
	tcpish := network != "hysteria" && network != "kcp" && proto != "wireguard" && proto != "hysteria"
	switch t.Fragment {
	case "on":
		if tcpish {
			sub(sub(ob, "streamSettings"), "sockopt")["dialerProxy"] = fragTagFor(t)
		}
	case "off":
		if ss != nil {
			if so, ok := ss["sockopt"].(map[string]any); ok {
				delete(so, "dialerProxy")
			}
		}
	}
	if t.MTU > 0 && proto == "wireguard" {
		sub(ob, "settings")["mtu"] = t.MTU
	}
	switch t.Mux {
	case "on":
		flow := ""
		if s, ok := ob["settings"].(map[string]any); ok {
			if vn, ok := s["vnext"].([]any); ok && len(vn) > 0 {
				if us, ok := vn[0].(map[string]any)["users"].([]any); ok && len(us) > 0 {
					flow, _ = us[0].(map[string]any)["flow"].(string)
				}
			}
		}
		// Vision and XHTTP do their own thing; Mux would break or slow them
		if (proto == "vless" || proto == "vmess" || proto == "trojan") && flow == "" && network != "xhttp" {
			c := t.MuxConc
			if c <= 0 {
				c = 8
			}
			ob["mux"] = J{"enabled": true, "concurrency": c, "xudpConcurrency": c}
		}
	case "off":
		delete(ob, "mux")
	}
}

// ruleFor turns a user rule into an Xray routing rule; target is the proxy's outbound or balancer.
func ruleFor(r Rule, target J, configs []protocols.ClientEntry) (J, error) {
	v := strings.TrimSpace(r.Value)
	if v == "" {
		return nil, errors.New("empty rule")
	}
	out := J{}
	switch r.Match {
	case "domain":
		out["domain"] = []string{"domain:" + v}
	case "full":
		out["domain"] = []string{"full:" + v}
	case "keyword":
		out["domain"] = []string{"keyword:" + v}
	case "geosite":
		out["domain"] = []string{"geosite:" + v}
	case "ip":
		out["ip"] = []string{v}
	case "geoip":
		out["ip"] = []string{"geoip:" + v}
	case "port":
		out["port"] = v
	case "app":
		if !appRules {
			return nil, errors.New("app rules work on Linux and Windows only")
		}
		var names []string
		for _, n := range strings.Split(v, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		out["process"] = names
	default:
		return nil, fmt.Errorf("unknown rule type %q", r.Match)
	}
	switch r.Action {
	case "direct":
		out["outboundTag"] = "direct"
	case "block":
		out["outboundTag"] = "block"
	case "via":
		if r.Config == "" {
			return nil, errors.New("which config?")
		}
		out["outboundTag"] = viaTag(r.Config, configs)
	default:
		for k, x := range target {
			out[k] = x
		}
	}
	return out, nil
}

// pushDomains are Apple's and Google's push services: notifications arrive sooner, and keep
// arriving when a config is blocked, when they go direct.
var pushDomains = []string{"domain:push.apple.com", "domain:push-apple.com.akadns.net", "full:mtalk.google.com", "full:mtalk4.google.com",
	"full:alt1-mtalk.google.com", "full:alt2-mtalk.google.com", "full:alt3-mtalk.google.com", "domain:firebaseinstallations.googleapis.com"}

// dnsServer turns a resolver setting into Xray's form: https:// stays, tcp://, udp:// and plain
// IPs become what Xray reads.
func dnsServer(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "udp://"):
		return strings.TrimPrefix(s, "udp://")
	case s == "system":
		return "localhost"
	}
	return s
}

// BuildConfig is the Xray config for a connection. ports are the local SOCKS ports of the
// non-Xray configs; hosts are server names resolved beforehand (for TUN mode, where a lookup
// through the system would loop back into the tunnel).
func BuildConfig(p *Profile, o Options, configs []protocols.ClientEntry, ports map[string]int, hosts map[string][]string) J {
	o.defaults()
	listen := "127.0.0.1"
	if o.ShareLAN {
		listen = "0.0.0.0"
	}
	query := "UseIPv4"
	if o.IPv6 {
		query = "UseIP"
	}
	sniff := J{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}
	var inbounds []J
	if o.Mode == "tun" {
		tunSniff := sniff
		if o.FakeDNS { // the placeholder address must be swapped back for the name
			tunSniff = J{"enabled": true, "destOverride": []string{"http", "tls", "quic", "fakedns"}, "routeOnly": false}
		}
		inbounds = append(inbounds, J{"tag": "tun", "port": 0, "protocol": "tun", "settings": J{"name": o.TunName, "MTU": 1500}, "sniffing": tunSniff})
	}
	inbounds = append(inbounds,
		J{"tag": "socks", "listen": listen, "port": o.SocksPort, "protocol": "socks", "settings": J{"udp": true}, "sniffing": sniff},
		J{"tag": "http", "listen": listen, "port": o.HTTPPort, "protocol": "http"})

	frags := map[string]Tweaks{} // fragmenting outbounds the tweaks use
	var obs []J
	extras := viaExtras(p, o, configs)
	for _, c := range append(append([]protocols.ClientEntry{}, configs...), extras...) {
		var ob J
		if c.Engine == "xray" {
			ob = clone(c.Outbound)
			t := o.Tweaks.over(o.ConfigTweaks[c.ID])
			applyTweaks(ob, t)
			if t.Fragment == "on" {
				frags[fragTagFor(t)] = t
			}
			if o.Mode == "tun" && len(hosts) > 0 { // resolve the server through Xray's DNS (its hosts table)
				sub(sub(ob, "streamSettings"), "sockopt")["domainStrategy"] = query
			}
		} else {
			ob = J{"protocol": "socks", "settings": J{"servers": []J{{"address": "127.0.0.1", "port": ports[c.ID]}}}}
		}
		ob["tag"] = viaTag(c.ID, configs)
		obs = append(obs, ob)
	}
	for _, f := range p.Frag {
		obs = append(obs, clone(f))
	}
	for _, tag := range sortedKeys(frags) {
		t := frags[tag]
		obs = append(obs, J{"tag": tag, "protocol": "freedom", "settings": J{"fragment": J{"packets": "tlshello",
			"length": orStr(t.FragLength, "100-200"), "interval": orStr(t.FragDelay, "10-20")}}})
	}
	obs = append(obs, J{"tag": "direct", "protocol": "freedom", "settings": J{"domainStrategy": query}},
		J{"tag": "block", "protocol": "blackhole"}, J{"tag": "dns-out", "protocol": "dns"})

	proxy := J{"balancerTag": "auto"}
	if len(configs) == 1 {
		proxy = J{"outboundTag": tagOf(configs[0])}
	}
	rules := []J{}
	if o.Mode == "tun" {
		rules = append(rules, J{"inboundTag": []string{"tun"}, "port": "53", "outboundTag": "dns-out"})
	}
	for _, r := range o.Rules {
		if r.Action == "via" && !hasConfig(configs, r.Config) && !hasConfig(extras, r.Config) {
			continue // the config is gone from the subscription, or another app runs it
		}
		if x, err := ruleFor(r, proxy, configs); err == nil {
			rules = append(rules, x)
		}
	}
	if o.BlockQUIC {
		rules = append(rules, J{"network": "udp", "port": "443", "outboundTag": "block"})
	}
	if !o.IPv6 {
		rules = append(rules, J{"ip": []string{"::/0"}, "outboundTag": "block"}) // nothing leaks out over IPv6
	}
	if !o.ProxyLAN {
		rules = append(rules, J{"ip": []string{"geoip:private"}, "outboundTag": "direct"})
	}
	if o.BypassPush {
		rules = append(rules, J{"domain": pushDomains, "outboundTag": "direct"})
	}
	var bypass []string
	for _, d := range o.BypassDomains {
		if d = strings.TrimSpace(strings.TrimPrefix(d, "*.")); d != "" {
			bypass = append(bypass, "domain:"+d)
		}
	}
	if len(bypass) > 0 {
		rules = append(rules, J{"domain": bypass, "outboundTag": "direct"})
	}
	if o.BlockAds {
		rules = append(rules, J{"domain": []string{"geosite:category-ads-all"}, "outboundTag": "block"})
	}
	country, hasCountry := protocols.Countries[p.Country]
	if hasCountry && o.Domestic {
		rules = append(rules, J{"domain": []string{country[0]}, "outboundTag": "direct"}, J{"ip": []string{country[1]}, "outboundTag": "direct"})
	}
	catchAll := J{"network": "tcp,udp"}
	for k, v := range proxy {
		catchAll[k] = v
	}
	rules = append(rules, catchAll)

	cfg := J{"log": J{"loglevel": o.LogLevel}, "inbounds": inbounds, "outbounds": obs,
		"stats": J{}, "policy": J{"system": J{"statsOutboundUplink": true, "statsOutboundDownlink": true}}}
	if f := logFile(); f != "" {
		cfg["log"].(J)["error"] = f
	}
	var servers []any
	if o.FakeDNS && o.Mode == "tun" {
		servers = append(servers, "fakedns")
		cfg["fakedns"] = []J{{"ipPool": "198.18.0.0/15", "poolSize": 65535}}
	}
	if o.DomesticDNS != "" && hasCountry && o.Domestic {
		servers = append(servers, J{"address": dnsServer(o.DomesticDNS), "domains": []string{country[0]}, "skipFallback": true})
	}
	servers = append(servers, dnsServer(o.DNS))
	dns := J{"servers": servers, "queryStrategy": query, "tag": "dns-internal"}
	if len(hosts) > 0 {
		dns["hosts"] = hosts
	}
	cfg["dns"] = dns
	if len(configs) == 1 {
		cfg["routing"] = J{"domainStrategy": "AsIs", "rules": rules}
		return cfg
	}
	strategy, interval, sampling := J{"type": "leastPing"}, "1m", 2
	switch o.AutoMode {
	case "latency":
		interval, sampling = "15s", 4
	case "stable":
		strategy = J{"type": "leastLoad", "settings": J{"maxRTT": "2s", "tolerance": 0.2}}
		sampling = 6
	}
	cfg["routing"] = J{"domainStrategy": "AsIs", "rules": rules,
		"balancers": []J{{"tag": "auto", "selector": []string{"proxy-"}, "strategy": strategy, "fallbackTag": tagOf(configs[0])}}}
	cfg["burstObservatory"] = J{"subjectSelector": []string{"proxy-"},
		"pingConfig": J{"destination": orStr(p.Probe, protocols.ProbeURL), "interval": interval, "timeout": "6s", "sampling": sampling}}
	return cfg
}

// ---------------------------------------------------------------- running

// Engine is the connection: one at a time.
type Engine struct {
	mu      sync.Mutex
	inst    *xcore.Instance
	closers []func()
	opts    Options
	profile *Profile
	configs []protocols.ClientEntry
	extras  []protocols.ClientEntry // run for "via" rules only
	since   time.Time
	undo    func() // system routes or proxy settings to restore
	lastErr string
	shown   string // the config Auto used at the last status, to record when it moves
}

var engine Engine

// Kept across the restarts smart reconnect and setting changes make; only Disconnect ends them.
var (
	ksMu       sync.Mutex
	ksUndo     func() // the kill switch's routes
	watchGen   int    // the current smart-reconnect watcher; older ones stop
	profileRev int    // bumped when smart reconnect swaps the profile, so the app picks it up
	watchASN   string // the network the running profile is for
)

// lookupHosts are what CurrentNetwork asks; on desktops in TUN mode they go around the tunnel, or
// the device would see the server's network instead of its own.
var lookupHosts = []string{"speed.cloudflare.com", "ipinfo.io"}

// localEngine starts the in-process client for a non-Xray config behind a SOCKS port.
func localEngine(c protocols.ClientEntry) (int, func(), error) {
	var d Dialer
	var stop func()
	switch {
	case c.SSH != nil:
		s := c.SSH.Session(nil)
		d, stop = s, s.Close
	case c.AWG != nil:
		a := c.AWG.Dialer()
		d, stop = a, a.Close
	default:
		return 0, nil, fmt.Errorf("%s: not a local engine", c.ID)
	}
	srv, err := startSocks(d)
	if err != nil {
		stop()
		return 0, nil, err
	}
	return srv.Port(), func() { srv.Close(); stop() }, nil
}

// serverHosts resolves the configs' server names, for the TUN mode's hosts table and routes.
func serverHosts(configs []protocols.ClientEntry) (map[string][]string, []netip.Addr) {
	hosts := map[string][]string{}
	var ips []netip.Addr
	seen := map[netip.Addr]bool{}
	add := func(a netip.Addr) {
		if a.IsValid() && !seen[a] {
			seen[a] = true
			ips = append(ips, a)
		}
	}
	for _, c := range configs {
		for _, h := range endpoints(c) {
			if a, err := netip.ParseAddr(h); err == nil {
				add(a)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			rs, err := net.DefaultResolver.LookupHost(ctx, h)
			cancel()
			if err != nil {
				continue
			}
			for _, r := range rs {
				if a, err := netip.ParseAddr(r); err == nil && a.Is4() {
					hosts[h] = append(hosts[h], r)
					add(a)
				}
			}
		}
	}
	return hosts, ips
}

// endpoints are the hosts config c connects to directly.
func endpoints(c protocols.ClientEntry) []string {
	var out []string
	add := func(h string) {
		if h != "" {
			out = append(out, h)
		}
	}
	switch {
	case c.SSH != nil && c.SSH.DNSTT != nil:
		kind, rest, _ := strings.Cut(c.SSH.DNSTT.Resolver, ":")
		if kind == "doh" {
			if u, err := url.Parse(rest); err == nil {
				add(u.Hostname())
			}
		} else if h, _, err := net.SplitHostPort(rest); err == nil {
			add(h)
		}
	case c.SSH != nil:
		add(c.SSH.Host)
	case c.AWG != nil:
		if h, _, err := net.SplitHostPort(c.AWG.Endpoint); err == nil {
			add(h)
		}
	default:
		add(c.Server)
		collectAddresses(c.Outbound, add)
	}
	return out
}

// collectAddresses finds every "address" in an outbound (vnext, servers, a split download).
func collectAddresses(v any, add func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if s, ok := x.(string); ok && k == "address" {
				add(s)
			} else {
				collectAddresses(x, add)
			}
		}
	case []any:
		for _, x := range t {
			collectAddresses(x, add)
		}
	case []J:
		for _, x := range t {
			collectAddresses(map[string]any(x), add)
		}
	}
}

// Start connects with profile p. A running connection is replaced.
func Start(p *Profile, o Options) error {
	o.defaults()
	Stop()
	engine.mu.Lock()
	defer engine.mu.Unlock()
	configs, err := chosen(p, o)
	if err != nil {
		return err
	}
	var closers []func()
	fail := func(err error) error {
		for _, c := range closers {
			c()
		}
		engine.lastErr = err.Error()
		return err
	}
	ports := map[string]int{}
	extras := viaExtras(p, o, configs)
	all := append(append([]protocols.ClientEntry{}, configs...), extras...)
	for i, c := range all {
		if c.Engine == "xray" {
			continue
		}
		if t := o.Tweaks.over(o.ConfigTweaks[c.ID]); t.MTU > 0 && c.AWG != nil {
			a := *c.AWG
			a.MTU = t.MTU
			c.AWG = &a
			if i < len(configs) {
				configs[i] = c
			}
		}
		port, stop, err := localEngine(c)
		if err != nil {
			return fail(err)
		}
		ports[c.ID] = port
		closers = append(closers, stop)
	}
	var hosts map[string][]string
	var bypass []netip.Addr
	if o.Mode == "tun" {
		hosts, bypass = serverHosts(all) // "via" configs' servers too, or their tunnels would loop
		if o.TunFd > 0 {
			os.Setenv("xray.tun.fd", fmt.Sprint(o.TunFd))
		}
		if o.Routes {
			for _, h := range lookupHosts {
				if ips, err := net.LookupHost(h); err == nil {
					for _, ip := range ips {
						if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
							bypass = append(bypass, a)
						}
					}
				}
			}
		}
	}
	cfg := BuildConfig(p, o, configs, ports, hosts)
	b, _ := json.Marshal(cfg)
	inst, err := startXray(b)
	if err != nil {
		return fail(err)
	}
	var undo func()
	if o.Mode == "tun" && o.Routes {
		if undo, err = setRoutes(o.TunName, bypass); err != nil {
			closeXray(inst)
			return fail(fmt.Errorf("routes: %w", err))
		}
	}
	warning := ""
	if o.Mode == "proxy" && o.SystemProxy {
		// the proxy works without it: apps can be pointed at the ports by hand
		if undo, err = setSystemProxy(o.HTTPPort, o.SocksPort); err != nil {
			warning = fmt.Sprintf("connected, but the system proxy was not set (%v): point apps at 127.0.0.1:%d", err, o.HTTPPort)
		}
	}
	if o.KillSwitch && o.Mode == "tun" {
		ksMu.Lock()
		if ksUndo == nil {
			if u, err := enableKillSwitch(); err != nil {
				warning = "kill switch: " + err.Error()
			} else {
				ksUndo = u
			}
		}
		ksMu.Unlock()
	}
	engine.inst, engine.closers, engine.opts, engine.profile, engine.configs = inst, closers, o, p, configs
	engine.shown, engine.extras = "", extras
	engine.since, engine.undo, engine.lastErr = time.Now(), undo, warning
	ksMu.Lock()
	watchGen++
	gen := watchGen
	watchASN = trimAS(o.ASN)
	ksMu.Unlock()
	if o.SmartURL != "" {
		go smartWatch(gen, p, o)
	}
	return nil
}

// smartWatch reconnects when the device moves to another network: it fetches the subscription's
// ranking for the new network and restarts with it (Auto then starts from that network's best).
func smartWatch(gen int, p *Profile, o Options) {
	t := time.NewTicker(time.Duration(smartInterval.Load()))
	defer t.Stop()
	for range t.C {
		ksMu.Lock()
		current, last := gen == watchGen, watchASN
		ksMu.Unlock()
		if !current {
			return
		}
		n, err := CurrentNetwork()
		asn := trimAS(n.ASN)
		if err != nil || asn == "" || asn == last {
			continue
		}
		np, err := FetchProfile(o.SmartURL, asn)
		if err != nil {
			continue // try again next time; the running connection carries on
		}
		ksMu.Lock()
		if gen != watchGen {
			ksMu.Unlock()
			return
		}
		profileRev++
		ksMu.Unlock()
		o.ASN = asn
		if err := Start(np, o); err != nil {
			engine.mu.Lock()
			engine.lastErr = "reconnecting for the new network: " + err.Error()
			engine.mu.Unlock()
			record("error", "Moved to "+orStr(n.Name, "AS"+asn)+", but reconnecting failed: "+err.Error())
		} else {
			record("network", "Moved to "+orStr(n.Name, "AS"+asn)+": reconnected with that network's best configs")
		}
		return // Start runs a new watcher
	}
}

// smartInterval is how often smart reconnect looks at the network (an atomic: tests shorten it).
var smartInterval = func() *atomic.Int64 { var v atomic.Int64; v.Store(int64(30 * time.Second)); return &v }()

// Disconnect is the user's Disconnect: it stops the connection, the smart-reconnect watcher and
// the kill switch.
func Disconnect() {
	ksMu.Lock()
	watchGen++
	ksMu.Unlock()
	Stop()
	ksMu.Lock()
	if ksUndo != nil {
		ksUndo()
		ksUndo = nil
	}
	ksMu.Unlock()
}

// CurrentProfile is the running connection's profile (smart reconnect may have swapped it) and its
// revision.
func CurrentProfile() (*Profile, int) {
	engine.mu.Lock()
	p := engine.profile
	engine.mu.Unlock()
	ksMu.Lock()
	defer ksMu.Unlock()
	return p, profileRev
}

// Stop disconnects and restores what Start changed (the kill switch stays: see Disconnect).
func Stop() {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.undo != nil {
		engine.undo()
		engine.undo = nil
	}
	if engine.inst != nil {
		closeXray(engine.inst)
		engine.inst = nil
	}
	for _, c := range engine.closers {
		c()
	}
	engine.closers, engine.profile, engine.configs = nil, nil, nil
}

// OutboundState is one config's health as the balancer sees it.
type OutboundState struct {
	ID    string `json:"id"`
	Alive bool   `json:"alive"`
	Delay int64  `json:"delayMs"`
}

// Status is what the app shows while connected.
type Status struct {
	Running    bool            `json:"running"`
	Mode       string          `json:"mode,omitempty"`
	Since      int64           `json:"since,omitempty"`
	Selected   string          `json:"selected,omitempty"`
	Active     string          `json:"active,omitempty"` // the config in use (Auto: the fastest alive)
	ActiveName string          `json:"activeName,omitempty"`
	Outbounds  []OutboundState `json:"outbounds,omitempty"`
	Up         int64           `json:"up"`
	Down       int64           `json:"down"`
	Error      string          `json:"error,omitempty"`
	KillSwitch bool            `json:"killSwitch,omitempty"` // traffic is blocked outside the tunnel
	ASN        string          `json:"asn,omitempty"`        // the network the profile is ranked for
	ProfileRev int             `json:"profileRev"`           // changes when smart reconnect swaps the profile
	SelRev     int             `json:"selRev"`               // changes when the notification switches config
}

func CurrentStatus() (s Status) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	s = Status{Error: engine.lastErr}
	ksMu.Lock()
	s.KillSwitch, s.ASN, s.ProfileRev, s.SelRev = ksUndo != nil, watchASN, profileRev, selRev
	ksMu.Unlock()
	defer func() { // named result: every return below carries the name
		for _, c := range engine.configs {
			if c.ID == s.Active {
				s.ActiveName = c.Name
			}
		}
	}()
	if engine.inst == nil {
		return s
	}
	s.Running, s.Mode, s.Since, s.Selected = true, engine.opts.Mode, engine.since.Unix(), engine.opts.Selected
	// Traffic is counted on the configs' outbounds: the TUN inbound (VPN mode) hands connections
	// straight to the router, past the listeners where Xray counts inbound traffic, so inbound
	// counters stay at zero there. Sites that go direct are not tunnel traffic and are left out.
	if m, ok := engine.inst.GetFeature(stats.ManagerType()).(stats.Manager); ok {
		for _, c := range append(append([]protocols.ClientEntry{}, engine.configs...), engine.extras...) {
			tag := viaTag(c.ID, engine.configs)
			if n := m.GetCounter("outbound>>>" + tag + ">>>traffic>>>uplink"); n != nil {
				s.Up += n.Value()
			}
			if n := m.GetCounter("outbound>>>" + tag + ">>>traffic>>>downlink"); n != nil {
				s.Down += n.Value()
			}
		}
	}
	if len(engine.configs) == 1 {
		s.Active = engine.configs[0].ID
		s.Outbounds = []OutboundState{{ID: s.Active, Alive: true}}
		return s
	}
	if ob, ok := engine.inst.GetFeature(extension.ObservatoryType()).(extension.Observatory); ok {
		if msg, err := ob.GetObservation(context.Background()); err == nil {
			if r, ok := msg.(*observatory.ObservationResult); ok {
				for _, st := range r.Status {
					s.Outbounds = append(s.Outbounds, OutboundState{ID: strings.TrimPrefix(st.OutboundTag, "proxy-"), Alive: st.Alive, Delay: st.Delay})
				}
			}
		}
	}
	sort.SliceStable(s.Outbounds, func(i, j int) bool {
		a, b := s.Outbounds[i], s.Outbounds[j]
		if a.Alive != b.Alive {
			return a.Alive
		}
		return a.Delay < b.Delay
	})
	if len(s.Outbounds) > 0 && s.Outbounds[0].Alive {
		s.Active = s.Outbounds[0].ID
	} else if len(engine.configs) > 0 {
		s.Active = engine.configs[0].ID // the fallback until health checks answer
	}
	if prev := engine.shown; prev != s.Active && prev != "" && len(s.Outbounds) > 0 {
		why := "it answers faster"
		for _, ob := range s.Outbounds {
			if ob.ID == prev && !ob.Alive {
				why = nameOf(engine.configs, prev) + " stopped answering"
			}
		}
		record("switch", "Auto moved to "+nameOf(engine.configs, s.Active)+": "+why)
	}
	engine.shown = s.Active
	return s
}

func hasConfig(cs []protocols.ClientEntry, id string) bool {
	for _, c := range cs {
		if c.ID == id {
			return true
		}
	}
	return false
}

func nameOf(cs []protocols.ClientEntry, id string) string {
	for _, c := range cs {
		if c.ID == id {
			return c.Name
		}
	}
	return id
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
