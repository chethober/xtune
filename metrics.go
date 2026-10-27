package main

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The subscription server can serve Prometheus metrics at /PATH/metrics, for admins who run
// several servers: users, traffic, each config's last use, the enforce job and the followers.
// It needs a token of its own (xtune sub metrics on), sent as "Authorization: Bearer TOKEN", or
// in the URL (/PATH/metrics/TOKEN) for scrapers that cannot set headers.

// metricsURL is where the metrics are, or "" when they are off.
func (st *State) metricsURL() string {
	if st.Sub.Metrics == "" || subHost(st) == "" {
		return ""
	}
	return "https://" + hostPort(subHost(st), st.Sub.Port) + "/" + st.Sub.Path + "/metrics"
}

// metricsAuth checks the token from the header or the URL.
func metricsAuth(st *State, req *http.Request, parts []string) bool {
	if st.Sub.Metrics == "" {
		return false
	}
	tok := ""
	if len(parts) == 3 {
		tok = parts[2]
	} else if b, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer "); ok {
		tok = strings.TrimSpace(b)
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(st.Sub.Metrics)) == 1
}

// promEscape escapes a label value.
func promEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

type promWriter struct {
	w    io.Writer
	seen map[string]bool
}

// metric writes one sample, with the HELP and TYPE lines before a family's first sample.
func (p *promWriter) metric(name, kind, help string, v float64, labels ...string) {
	if !p.seen[name] {
		p.seen[name] = true
		fmt.Fprintf(p.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	}
	var ls []string
	for i := 0; i+1 < len(labels); i += 2 {
		ls = append(ls, fmt.Sprintf(`%s="%s"`, labels[i], promEscape(labels[i+1])))
	}
	l := ""
	if len(ls) > 0 {
		l = "{" + strings.Join(ls, ",") + "}"
	}
	fmt.Fprintf(p.w, "%s%s %g\n", name, l, v)
}

// writeMetrics is the metrics page in the Prometheus text format.
func writeMetrics(w io.Writer, st *State, us *Usage, now time.Time) {
	p := &promWriter{w: w, seen: map[string]bool{}}
	p.metric("xtune_info", "gauge", "xtune version on this server.", 1, "version", version, "server", st.Addr())
	status := map[string]int{}
	for _, u := range st.Users {
		uu := orUsage(us.Users[u.Name])
		status[userStatus(u, uu, now)]++
	}
	for _, s := range []string{"active", "disabled", "expired", "over quota"} {
		p.metric("xtune_users", "gauge", "Users by status.", float64(status[s]), "status", s)
	}
	for _, u := range st.Users {
		uu := orUsage(us.Users[u.Name])
		p.metric("xtune_user_traffic_bytes", "counter", "Traffic per user since their last usage reset.", float64(uu.Up), "user", u.Name, "direction", "up")
		p.metric("xtune_user_traffic_bytes", "counter", "Traffic per user since their last usage reset.", float64(uu.Down), "user", u.Name, "direction", "down")
	}
	for _, u := range st.Users {
		p.metric("xtune_user_online_devices", "gauge", "Client IPs online per user at the last enforce run.", float64(len(orUsage(us.Users[u.Name]).Online)), "user", u.Name)
	}
	for _, u := range st.Users {
		if u.Quota > 0 {
			p.metric("xtune_user_quota_bytes", "gauge", "Traffic quota per user (users without one are left out).", float64(u.Quota), "user", u.Name)
		}
	}
	for _, u := range st.Users {
		if u.Expires > 0 {
			p.metric("xtune_user_expiry_timestamp_seconds", "gauge", "Expiry per user (users who never expire are left out).", float64(u.Expires), "user", u.Name)
		}
	}
	quiet := quietEntries(st, us, now)
	for _, e := range st.Entries {
		eu := us.Entries[entryKey(e)]
		if eu == nil {
			eu = &EntryUsage{}
		}
		p.metric("xtune_config_traffic_bytes", "counter", "Traffic per deployed config since it was deployed.", float64(eu.Bytes), "config", e.ID, "kind", e.Kind)
	}
	for _, e := range st.Entries {
		if eu := us.Entries[entryKey(e)]; eu != nil && eu.Active > 0 {
			p.metric("xtune_config_last_active_timestamp_seconds", "gauge", "When each config last carried traffic.", float64(eu.Active), "config", e.ID)
		}
	}
	for _, e := range st.Entries {
		q := 0.0
		if quiet[e.ID] > 0 {
			q = 1
		}
		p.metric("xtune_config_quiet", "gauge", "1 when a config carried nothing for 2 hours while others were in use: probably blocked.", q, "config", e.ID)
	}
	p.metric("xtune_enforce_last_run_timestamp_seconds", "gauge", "When the usage and limits job last finished.", float64(us.Updated))
	p.metric("xtune_xray_api_failures", "gauge", "Enforce runs in a row the Xray API did not answer.", float64(us.Failures))
	up := 1.0
	if us.Failures >= xrayDownAfter || now.Unix()-us.Updated > 5*60 {
		up = 0
	}
	p.metric("xtune_xray_up", "gauge", "1 when the deployed Xray answered the last enforce runs.", up)
	p.metric("xtune_device_blocks", "gauge", "Client IPs blocked for being over a device limit.", float64(len(us.Blocks)))
	if st.Peers != nil {
		names := make([]string, 0, len(st.Peers.Servers))
		for n := range st.Peers.Servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p.metric("xtune_peer_last_report_timestamp_seconds", "gauge", "When each follower last reported.", float64(st.Peers.Servers[n].Seen), "peer", n)
		}
	}
	if us.LastBackup > 0 {
		p.metric("xtune_backup_last_success_timestamp_seconds", "gauge", "When the last scheduled backup was delivered.", float64(us.LastBackup))
	}
	for _, wu := range us.WorkerUse {
		p.metric("xtune_worker_requests_today", "gauge", "Requests to each Cloudflare Worker today (UTC); the free plan allows 100000.", float64(wu.Requests), "worker", wu.Host)
	}
}

// serveMetrics answers GET /PATH/metrics.
func serveMetrics(w http.ResponseWriter, req *http.Request, st *State, us *Usage, parts []string) {
	if !metricsAuth(st, req, parts) {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writeMetrics(w, st, us, time.Now())
}

// SetMetrics turns the metrics on (with a new token when rotate is set, or none yet) or off.
func SetMetrics(r *Server, on, rotate bool) (*State, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	switch {
	case !on:
		st.Sub.Metrics = ""
	case st.Sub.Mode == "":
		return nil, fmt.Errorf("metrics are served by the subscription server: turn it on first (xtune sub enable)")
	case st.Sub.Metrics == "" || rotate:
		st.Sub.Metrics = randHex(16)
	}
	return st, saveState(r, st) // the server reads state.json on every request
}

func printMetrics(w io.Writer, st *State) {
	if st.Sub.Metrics == "" {
		fmt.Fprintln(w, "metrics: off (xtune sub metrics on)")
		return
	}
	fmt.Fprintf(w, "metrics: %s\ntoken:   %s\n\nPrometheus scrape config:\n", orStr(st.metricsURL(), "(the subscription server is off)"), st.Sub.Metrics)
	fmt.Fprintf(w, "  - job_name: xtune\n    scheme: https\n    metrics_path: /%s/metrics\n    authorization: {credentials: %s}\n", st.Sub.Path, st.Sub.Metrics)
	if st.Sub.Mode == "cdn" {
		fmt.Fprintf(w, "    tls_config: {server_name: %s}\n", st.CDNDomain)
	}
	fmt.Fprintf(w, "    static_configs: [{targets: ['%s']}]\n", hostPort(subHost(st), st.Sub.Port))
}
