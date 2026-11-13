package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
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
	// Android hands its VpnService descriptor over; desktops create an interface of this name.
	TunFd   int    `json:"tunFd,omitempty"`
	TunName string `json:"tunName,omitempty"`
	// Routes: the core sets the system's routes around the TUN interface (desktop).
	Routes       bool   `json:"routes,omitempty"`
	SystemProxy  bool   `json:"systemProxy,omitempty"` // desktop proxy mode: point the system proxy at it
	SocksPort    int    `json:"socksPort,omitempty"`
	HTTPPort     int    `json:"httpPort,omitempty"`
	ShareLAN     bool   `json:"shareLan,omitempty"`
	BlockAds     bool   `json:"blockAds,omitempty"`
	Domestic     bool   `json:"domestic,omitempty"`     // the profile country's sites go direct
	IncludeDNSTT bool   `json:"includeDnstt,omitempty"` // let Auto use the DNS tunnel too
	DNS          string `json:"dns,omitempty"`          // DoH URL, through the tunnel
	LogLevel     string `json:"logLevel,omitempty"`
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

// chosen are the configs a connection uses: the selected one, or Auto's set.
func chosen(p *Profile, o Options) ([]protocols.ClientEntry, error) {
	var out []protocols.ClientEntry
	for _, c := range p.Configs {
		if !usable(c) {
			continue
		}
		if o.Selected != "" {
			if c.ID == o.Selected {
				return []protocols.ClientEntry{c}, nil
			}
			continue
		}
		if c.SSH != nil && c.SSH.DNSTT != nil && !o.IncludeDNSTT {
			continue // slow: only when asked for
		}
		out = append(out, c)
	}
	if o.Selected != "" {
		return nil, fmt.Errorf("config %q is not in this subscription", o.Selected)
	}
	if len(out) == 0 {
		return nil, errors.New("no config this app can run")
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

// BuildConfig is the Xray config for a connection. ports are the local SOCKS ports of the
// non-Xray configs; hosts are server names resolved beforehand (for TUN mode, where a lookup
// through the system would loop back into the tunnel).
func BuildConfig(p *Profile, o Options, configs []protocols.ClientEntry, ports map[string]int, hosts map[string][]string) J {
	o.defaults()
	listen := "127.0.0.1"
	if o.ShareLAN {
		listen = "0.0.0.0"
	}
	sniff := J{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}
	var inbounds []J
	if o.Mode == "tun" {
		inbounds = append(inbounds, J{"tag": "tun", "port": 0, "protocol": "tun", "settings": J{"name": o.TunName, "MTU": 1500},
			"sniffing": sniff})
	}
	inbounds = append(inbounds,
		J{"tag": "socks", "listen": listen, "port": o.SocksPort, "protocol": "socks", "settings": J{"udp": true}, "sniffing": sniff},
		J{"tag": "http", "listen": listen, "port": o.HTTPPort, "protocol": "http"})

	var obs []J
	for _, c := range configs {
		var ob J
		if c.Engine == "xray" {
			ob = clone(c.Outbound)
			if o.Mode == "tun" && len(hosts) > 0 { // resolve the server through Xray's DNS (its hosts table)
				ss, _ := ob["streamSettings"].(map[string]any)
				if ss == nil {
					ss = map[string]any{}
					ob["streamSettings"] = ss
				}
				so, _ := ss["sockopt"].(map[string]any)
				if so == nil {
					so = map[string]any{}
					ss["sockopt"] = so
				}
				so["domainStrategy"] = "UseIPv4"
			}
		} else {
			ob = J{"protocol": "socks", "settings": J{"servers": []J{{"address": "127.0.0.1", "port": ports[c.ID]}}}}
		}
		ob["tag"] = tagOf(c)
		obs = append(obs, ob)
	}
	for _, f := range p.Frag {
		obs = append(obs, clone(f))
	}
	obs = append(obs, J{"tag": "direct", "protocol": "freedom", "settings": J{"domainStrategy": "UseIPv4"}},
		J{"tag": "block", "protocol": "blackhole"}, J{"tag": "dns-out", "protocol": "dns"})

	rules := []J{}
	if o.Mode == "tun" {
		rules = append(rules, J{"inboundTag": []string{"tun"}, "port": "53", "outboundTag": "dns-out"})
	}
	rules = append(rules, J{"ip": []string{"geoip:private"}, "outboundTag": "direct"})
	if o.BlockAds {
		rules = append(rules, J{"domain": []string{"geosite:category-ads-all"}, "outboundTag": "block"})
	}
	if c, ok := protocols.Countries[p.Country]; ok && o.Domestic {
		rules = append(rules, J{"domain": []string{c[0]}, "outboundTag": "direct"}, J{"ip": []string{c[1]}, "outboundTag": "direct"})
	}
	cfg := J{"log": J{"loglevel": o.LogLevel}, "inbounds": inbounds, "outbounds": obs,
		"stats": J{}, "policy": J{"system": J{"statsInboundUplink": true, "statsInboundDownlink": true}}}
	dns := J{"servers": []any{o.DNS}, "queryStrategy": "UseIPv4", "tag": "dns-internal"}
	if len(hosts) > 0 {
		dns["hosts"] = hosts
	}
	cfg["dns"] = dns
	if len(configs) == 1 {
		rules = append(rules, J{"network": "tcp,udp", "outboundTag": tagOf(configs[0])})
		cfg["routing"] = J{"domainStrategy": "AsIs", "rules": rules}
		return cfg
	}
	rules = append(rules, J{"network": "tcp,udp", "balancerTag": "auto"})
	cfg["routing"] = J{"domainStrategy": "AsIs", "rules": rules,
		"balancers": []J{{"tag": "auto", "selector": []string{"proxy-"}, "strategy": J{"type": "leastPing"}, "fallbackTag": tagOf(configs[0])}}}
	cfg["burstObservatory"] = J{"subjectSelector": []string{"proxy-"},
		"pingConfig": J{"destination": orStr(p.Probe, protocols.ProbeURL), "interval": "1m", "timeout": "6s", "sampling": 2}}
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
	since   time.Time
	undo    func() // system routes or proxy settings to restore
	lastErr string
}

var engine Engine

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
	for _, c := range configs {
		if c.Engine == "xray" {
			continue
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
		hosts, bypass = serverHosts(configs)
		if o.TunFd > 0 {
			os.Setenv("xray.tun.fd", fmt.Sprint(o.TunFd))
		}
	}
	cfg := BuildConfig(p, o, configs, ports, hosts)
	b, _ := json.Marshal(cfg)
	xc, err := xcore.LoadConfig("json", bytes.NewReader(b))
	if err != nil {
		return fail(fmt.Errorf("config: %w", err))
	}
	inst, err := xcore.New(xc)
	if err != nil {
		return fail(fmt.Errorf("start: %w", err))
	}
	if err := inst.Start(); err != nil {
		inst.Close()
		return fail(fmt.Errorf("start: %w", err))
	}
	var undo func()
	if o.Mode == "tun" && o.Routes {
		if undo, err = setRoutes(o.TunName, bypass); err != nil {
			inst.Close()
			return fail(fmt.Errorf("routes: %w", err))
		}
	} else if o.Mode == "proxy" && o.SystemProxy {
		if undo, err = setSystemProxy(o.HTTPPort, o.SocksPort); err != nil {
			inst.Close()
			return fail(fmt.Errorf("system proxy: %w", err))
		}
	}
	engine.inst, engine.closers, engine.opts, engine.profile, engine.configs = inst, closers, o, p, configs
	engine.since, engine.undo, engine.lastErr = time.Now(), undo, ""
	return nil
}

// Stop disconnects and restores what Start changed.
func Stop() {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.undo != nil {
		engine.undo()
		engine.undo = nil
	}
	if engine.inst != nil {
		engine.inst.Close()
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
	Running   bool            `json:"running"`
	Mode      string          `json:"mode,omitempty"`
	Since     int64           `json:"since,omitempty"`
	Selected  string          `json:"selected,omitempty"`
	Active    string          `json:"active,omitempty"` // the config in use (Auto: the fastest alive)
	Outbounds []OutboundState `json:"outbounds,omitempty"`
	Up        int64           `json:"up"`
	Down      int64           `json:"down"`
	Error     string          `json:"error,omitempty"`
}

func CurrentStatus() Status {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	s := Status{Error: engine.lastErr}
	if engine.inst == nil {
		return s
	}
	s.Running, s.Mode, s.Since, s.Selected = true, engine.opts.Mode, engine.since.Unix(), engine.opts.Selected
	if m, ok := engine.inst.GetFeature(stats.ManagerType()).(stats.Manager); ok {
		for _, in := range []string{"tun", "socks", "http"} {
			if c := m.GetCounter("inbound>>>" + in + ">>>traffic>>>uplink"); c != nil {
				s.Up += c.Value()
			}
			if c := m.GetCounter("inbound>>>" + in + ">>>traffic>>>downlink"); c != nil {
				s.Down += c.Value()
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
	return s
}
