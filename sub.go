package main

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"rsc.io/qr"
)

// The subscription server (systemd unit xray-autotune-sub) gives every user one public HTTPS link:
// a page in a browser, the base64 link list for client apps, and /json for the failover config.
// It is reached through the CDN domain (TLS ends at the CDN, which accepts the self-signed origin
// certificate) or on the direct domain with a Let's Encrypt certificate.

func subHost(st *State) string {
	switch st.Sub.Mode {
	case "cdn":
		return st.CDNDomain
	case "direct":
		return st.Domain
	}
	return ""
}

// SubURL is the user's public subscription link, or "" when the server is off.
func (st *State) SubURL(u User) string {
	h := subHost(st)
	if h == "" || u.SubToken == "" {
		return ""
	}
	return "https://" + hostPort(h, st.Sub.Port) + "/" + st.Sub.Path + "/" + u.SubToken
}

// botBase is where the connect and management links point: port 443 when the server listens there.
func (st *State) botBase() string {
	h := subHost(st)
	if h == "" {
		return ""
	}
	if st.Sub.Port == 443 || st.Sub.Port443 {
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		return "https://" + h + "/" + st.Sub.Path
	}
	return "https://" + hostPort(h, st.Sub.Port) + "/" + st.Sub.Path
}

// ensurePort443 makes the subscription server also listen on 443 when nothing else uses it, so
// serverless bots can reach the connect and management links. It reports whether 443 is reachable.
func ensurePort443(r *Server, st *State) (bool, error) {
	if st.Sub.Port == 443 || st.Sub.Port443 {
		return true, nil
	}
	out, err := r.Helper("probe")
	if err != nil {
		return false, err
	}
	if usedPorts(out)["tcp"][443] || st.APIPort == 443 {
		return false, nil
	}
	for _, e := range st.Entries {
		if e.Port == 443 && e.Proto() == "tcp" {
			return false, nil
		}
	}
	st.Sub.Port443 = true
	if err := saveState(r, st); err != nil {
		return false, err
	}
	return true, restartSub(r, st)
}

const port443Note = "note: @chthvpnbot can only reach ports 80 and 443, and port 443 is in use on this server,\n" +
	"so the bot cannot use this link (free port 443, or: xtune sub enable --port 443)"

// EnableSub turns the subscription server on, or moves it to another mode or port. Port 0 picks a
// free CDN-compatible port. Existing links keep working as long as the mode and port stay the same.
func EnableSub(r *Server, mode string, port int) (*State, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	switch mode {
	case "cdn":
		if st.CDNDomain == "" {
			return nil, fmt.Errorf("set a CDN domain first (Addresses / xtune domains --cdn)")
		}
	case "direct":
		if st.Domain == "" {
			return nil, fmt.Errorf("set a direct domain first (Addresses / xtune domains --direct)")
		}
	default:
		return nil, fmt.Errorf("unknown mode %q (cdn or direct)", mode)
	}
	out, err := r.Helper("probe")
	if err != nil {
		return nil, err
	}
	used := usedPorts(out)["tcp"]
	if st.Sub.Mode != "" { // our own server holds these
		delete(used, st.Sub.Port)
		if st.Sub.Port443 {
			delete(used, 443)
		}
		if st.Sub.Mode == "direct" {
			delete(used, 80)
		}
	}
	taken := map[int]bool{st.APIPort: true}
	for _, e := range st.Entries {
		taken[e.Port] = true
	}
	if port == 0 {
		for _, p := range cdnPorts {
			if !used[p] && !taken[p] {
				port = p
				break
			}
		}
		if port == 0 {
			return nil, fmt.Errorf("none of the ports %v is free; pass one with --port", cdnPorts)
		}
	} else if used[port] || taken[port] {
		return nil, fmt.Errorf("port %d is already in use", port)
	}
	if mode == "cdn" && !contains2(cdnPorts, port) {
		return nil, fmt.Errorf("the CDN only forwards HTTPS on ports %v", cdnPorts)
	}
	if mode == "direct" && used[80] {
		return nil, fmt.Errorf("Let's Encrypt needs port 80, which is in use; use the cdn mode instead")
	}
	if st.Sub.Path == "" {
		st.Sub.Path = randHex(8)
	}
	st.Sub.Mode, st.Sub.Port = mode, port
	if err := saveState(r, st); err != nil {
		return nil, err
	}
	return st, restartSub(r, st)
}

func contains2(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// DisableSub stops the subscription server. Its path and port are kept, so turning it back on
// restores the same links.
func DisableSub(r *Server) error {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	st.Sub.Mode = ""
	if err := saveState(r, st); err != nil {
		return err
	}
	_, err = r.Helper("sub-disable")
	return err
}

// restartSub (re)starts the subscription server so it picks up a new mode, port or domain.
func restartSub(r *Server, st *State) error {
	if st.Sub.Mode == "" {
		return nil
	}
	args := []string{"sub-enable", strconv.Itoa(st.Sub.Port) + "/tcp"}
	if st.Sub.Port443 && st.Sub.Port != 443 {
		args = append(args, "443/tcp")
	}
	if st.Sub.Mode == "direct" {
		args = append(args, "80/tcp")
	}
	out, err := r.Helper(args...)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(out, "active") || strings.HasSuffix(out, "inactive") {
		return fmt.Errorf("the subscription server did not start (see: xtune logs --unit xray-autotune-sub): %s", out)
	}
	return nil
}

// ---------------------------------------------------------------- the server itself

// Serve runs the subscription server; systemd starts it as `xtune serve`.
func Serve() error {
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if st.Sub.Mode == "" {
		return fmt.Errorf("the subscription server is turned off")
	}
	srv := &http.Server{Addr: fmt.Sprintf(":%d", st.Sub.Port), Handler: subHandler{r: r},
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10,
		ErrorLog: log.New(io.Discard, "", 0)} // scanners cause endless TLS handshake noise
	switch st.Sub.Mode {
	case "cdn":
		cert, err := tls.LoadX509KeyPair(remoteDir+"/hy.crt", remoteDir+"/hy.key")
		if err != nil {
			return err
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	case "direct":
		m := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(st.Domain),
			Cache: autocert.DirCache(remoteDir + "/acme")}
		go func() {
			err := http.ListenAndServe(":80", m.HTTPHandler(http.NotFoundHandler()))
			fmt.Fprintln(os.Stderr, "port 80 (Let's Encrypt challenges):", err)
		}()
		srv.TLSConfig = m.TLSConfig()
		srv.TLSConfig.MinVersion = tls.VersionTLS12
	}
	fmt.Printf("subscription server on :%d (%s, %s)\n", st.Sub.Port, st.Sub.Mode, subHost(st))
	if st.Sub.Port443 && st.Sub.Port != 443 {
		alt := &http.Server{Addr: ":443", Handler: srv.Handler, TLSConfig: srv.TLSConfig,
			ReadHeaderTimeout: srv.ReadHeaderTimeout, ReadTimeout: srv.ReadTimeout, WriteTimeout: srv.WriteTimeout,
			IdleTimeout: srv.IdleTimeout, MaxHeaderBytes: srv.MaxHeaderBytes, ErrorLog: srv.ErrorLog}
		go func() {
			err := alt.ListenAndServeTLS("", "")
			fmt.Fprintln(os.Stderr, "port 443 (links for bots):", err)
		}()
	}
	return srv.ListenAndServeTLS("", "")
}

// readLocal reads state and usage straight from disk, so every request sees current users.
func readLocal() (*State, *Usage, error) {
	b, err := os.ReadFile(remoteDir + "/state.json")
	if err != nil {
		return nil, nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, nil, err
	}
	us := &Usage{Users: map[string]*UserUsage{}}
	if b, err := os.ReadFile(remoteDir + "/usage.json"); err == nil {
		json.Unmarshal(b, us)
	}
	if us.Users == nil {
		us.Users = map[string]*UserUsage{}
	}
	return &st, us, nil
}

type subHandler struct {
	load func() (*State, *Usage, error)
	r    *Server // for followers' reports
}

func (h subHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Robots-Tag", "noindex, nofollow")
	load := h.load
	if load == nil {
		load = readLocal
	}
	parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	isPeer := req.Method == http.MethodPost && len(parts) == 3 && parts[1] == "peer"
	isAdm := req.Method == http.MethodPost && len(parts) == 3 && parts[1] == "adm"
	isMetrics := req.Method == http.MethodGet && (len(parts) == 2 || len(parts) == 3) && parts[1] == "metrics"
	isPub := len(parts) >= 3 && parts[1] == "pub" &&
		(len(parts) == 3 && req.Method == http.MethodGet || len(parts) == 4 && req.Method == http.MethodPost)
	if req.Method != http.MethodGet && req.Method != http.MethodHead && !isPeer && !isPub && !isAdm {
		http.NotFound(w, req)
		return
	}
	st, us, err := load()
	if err != nil {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if isPeer {
		if st.Sub.Mode == "" || parts[0] != st.Sub.Path {
			http.NotFound(w, req)
			return
		}
		h.servePeer(w, req, st, parts[2])
		return
	}
	if isAdm {
		if st.Sub.Mode == "" || parts[0] != st.Sub.Path {
			http.NotFound(w, req)
			return
		}
		h.serveManage(w, req, st, parts[2])
		return
	}
	if isMetrics {
		if st.Sub.Mode == "" || parts[0] != st.Sub.Path {
			http.NotFound(w, req)
			return
		}
		serveMetrics(w, req, st, us, parts)
		return
	}
	if isPub {
		if st.Sub.Mode == "" || parts[0] != st.Sub.Path {
			http.NotFound(w, req)
			return
		}
		h.servePublic(w, req, st, parts)
		return
	}
	if st.Sub.Mode == "" || len(parts) < 2 || len(parts) > 4 || parts[0] != st.Sub.Path ||
		len(parts) == 3 && parts[2] != "json" || len(parts) == 4 && parts[2] != "f" {
		http.NotFound(w, req)
		return
	}
	var u *User
	for i := range st.Users {
		if t := st.Users[i].SubToken; t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(parts[1])) == 1 {
			u = &st.Users[i]
		}
	}
	if u == nil {
		http.NotFound(w, req)
		return
	}
	uu := us.Users[u.Name]
	if uu == nil {
		uu = &UserUsage{}
	}
	// the de-facto standard headers clients use to show usage and expiry
	hd.Set("Subscription-Userinfo", fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", uu.Up, uu.Down, u.Quota, u.Expires))
	hd.Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte("xtune - "+u.Name)))
	hd.Set("Profile-Update-Interval", "6")
	hd.Set("Profile-Web-Page-Url", st.SubURL(*u))
	now := time.Now()
	view, netName := st, ""
	if len(st.Networks) > 0 { // the requester's network's best configs first
		view, netName = st.forNetwork(asnOf(requesterIP(req, st), now), now)
	}
	switch {
	case len(parts) == 4: // a profile file
		for _, p := range st.Profiles(*u) {
			if p.File == parts[3] {
				hd.Set("Content-Type", p.Type)
				hd.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", p.File))
				io.WriteString(w, p.Body)
				return
			}
		}
		http.NotFound(w, req)
	case len(parts) == 3:
		hd.Set("Content-Type", "application/json")
		b, _ := json.MarshalIndent(view.FailoverConfig(*u), "", "  ")
		w.Write(b)
	case strings.Contains(req.Header.Get("Accept"), "text/html"):
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; "+
			"img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hd.Set("Content-Type", "text/html; charset=utf-8")
		p := pageData(view, *u, uu, now, pageLang(req, st.Sub.Lang))
		if netName != "" {
			p.Network = fmt.Sprintf(p.T["sorted"], netName)
		}
		subPage.Execute(w, p)
	default:
		hd.Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, view.Subscription(*u))
	}
}

// ---------------------------------------------------------------- the user's page

type pageLink struct{ Name, URL string }

type page struct {
	Lang, Dir                     string
	T                             map[string]string
	Name, Status, StatusClass     string
	Used, Quota, Expires, Devices string
	Resets                        string
	Percent                       int
	SubURL, JSONURL, LastSeen     string
	QR                            template.URL // generated here, so safe as a data: URL
	Links                         []pageLink
	Files                         []pageLink // profile files, for apps without share links
	Login                         string     // "name / password" for the SSH and VPN apps
	History                       []histBar  // daily traffic, oldest first; empty without any
	Network                       string     // "sorted for your network: NAME", when ranked for it
}

type histBar struct {
	Pct   int    // height relative to the busiest day
	Title string // date and amount, shown on hover
}

func pageData(st *State, u User, uu *UserUsage, now time.Time, lang string) page {
	T := pageText[lang]
	num := func(s string) string { // Persian digits for the page's numbers
		if lang == "fa" {
			return faDigits(s)
		}
		return s
	}
	status := userStatus(u, uu, now)
	label := T[status]
	if status == "expired" { // T["expired"] is the dated form, "expired %s"
		label = T["expiredState"]
	}
	p := page{Lang: lang, Dir: "ltr", T: T, Name: u.Name, Status: label, Used: num(fmtBytes(uu.Up + uu.Down)),
		Quota: T["unlimited"], Expires: T["never"], Devices: num(fmt.Sprintf(T["online"], len(uu.Online))),
		SubURL: st.SubURL(u), LastSeen: T["never"]}
	if lang == "fa" {
		p.Dir = "rtl"
	}
	p.JSONURL = p.SubURL + "/json"
	p.StatusClass = map[string]string{"active": "ok", "disabled": "off", "expired": "bad", "over quota": "bad"}[status]
	if u.Quota > 0 {
		p.Quota = num(fmtBytes(u.Quota))
		p.Percent = int(min(100, (uu.Up+uu.Down)*100/u.Quota))
	}
	if u.Expires > 0 {
		t := time.Unix(u.Expires, 0)
		if now.After(t) {
			p.Expires = fmt.Sprintf(T["expired"], fmtDate(t, lang))
		} else {
			p.Expires = num(fmt.Sprintf(T["left"], fmtDate(t, lang), int(t.Sub(now).Hours()/24)))
		}
	}
	if u.Devices > 0 {
		p.Devices = num(fmt.Sprintf(T["onlineOf"], len(uu.Online), u.Devices))
	}
	if u.Reset != "" && u.ResetAt > 0 {
		p.Resets = fmt.Sprintf(T[u.Reset], fmtDate(time.Unix(u.ResetAt, 0), lang))
	}
	if uu.LastSeen > 0 {
		t := time.Unix(uu.LastSeen, 0).UTC()
		p.LastSeen = t.Format("2006-01-02 15:04 UTC")
		if lang == "fa" {
			p.LastSeen = fmtDate(t, lang) + " " + faDigits(t.Format("15:04")) + " UTC"
		}
	}
	if c, err := qr.Encode(p.SubURL, qr.M); err == nil {
		c.Scale = 6
		p.QR = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(c.PNG()))
	}
	p.Links = st.NamedLinks(u)
	for _, f := range st.Profiles(u) {
		p.Files = append(p.Files, pageLink{Name: f.Name, URL: p.SubURL + "/f/" + url.PathEscape(f.File)})
	}
	if st.hasLogin() {
		p.Login = u.Name + " / " + tunnelPassword(u)
	}
	days := uu.lastDays(now, 30)
	top := slices.Max(days)
	for i, v := range days {
		if top == 0 {
			break
		}
		d := now.AddDate(0, 0, i-len(days)+1)
		pct := 0
		if v > 0 {
			pct = max(int(v*100/top), 2)
		}
		p.History = append(p.History, histBar{Pct: pct, Title: fmtDate(d, lang) + ": " + num(fmtBytes(v))})
	}
	return p
}

func fmtBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func fmtExpiry(ts int64, now time.Time) string {
	if ts == 0 {
		return "never"
	}
	t := time.Unix(ts, 0)
	if now.After(t) {
		return "expired " + t.Format("2006-01-02")
	}
	return fmt.Sprintf("%s (%d days left)", t.Format("2006-01-02"), int(t.Sub(now).Hours()/24))
}

var subPage = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>{{.Name}} - {{.T.title}}</title>
<style>
:root{--bg:#f6f7f9;--card:#fff;--fg:#14171c;--dim:#5d6673;--line:#e3e6eb;--acc:#2f6fed;--ok:#1a7f45;--bad:#c4332b;--off:#7a828e}
@media (prefers-color-scheme:dark){:root{--bg:#0f1115;--card:#171a20;--fg:#e8eaee;--dim:#98a1ad;--line:#272b33;--acc:#6b9bff;--ok:#4cc38a;--bad:#ff6b61;--off:#8b93a0}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,Vazirmatn,Tahoma,sans-serif}
main{max-width:680px;margin:0 auto;padding:24px 16px 48px}
h1{font-size:22px;margin:0 0 4px}h2{font-size:15px;margin:0 0 10px}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px;margin-top:16px}
.pill{display:inline-block;font-size:13px;font-weight:600;padding:2px 10px;border-radius:99px;border:1px solid currentColor}
.ok{color:var(--ok)}.bad{color:var(--bad)}.off{color:var(--off)}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:12px}
.k{color:var(--dim);font-size:13px}.v{font-weight:600}
.bar{height:8px;background:var(--line);border-radius:99px;overflow:hidden;margin-top:12px}.bar>i{display:block;height:100%;background:var(--acc)}
.row{display:flex;gap:8px;align-items:center;margin-top:8px}
code{direction:ltr;text-align:left;flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font:13px ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--bg);border:1px solid var(--line);border-radius:8px;padding:8px 10px}
button,.btn{flex:none;text-decoration:none;font:inherit;font-size:13px;font-weight:600;color:#fff;background:var(--acc);border:0;border-radius:8px;padding:8px 12px;cursor:pointer}
.qr{display:block;margin:12px auto 0;width:220px;max-width:100%;image-rendering:pixelated;background:#fff;padding:8px;border-radius:8px}
.dim{color:var(--dim);font-size:13px}
.hist{display:flex;align-items:flex-end;gap:2px;height:56px;margin-top:6px;direction:ltr}.hist>i{flex:1;background:var(--acc);border-radius:2px 2px 0 0;opacity:.85}
</style></head><body><main>
<h1>{{.Name}}</h1>
<span class="pill {{.StatusClass}}">{{.Status}}</span>
<div class="card"><div class="grid">
<div><div class="k">{{.T.used}}</div><div class="v">{{.Used}}</div></div>
<div><div class="k">{{.T.quota}}</div><div class="v">{{.Quota}}</div></div>
<div><div class="k">{{.T.expires}}</div><div class="v">{{.Expires}}</div></div>
<div><div class="k">{{.T.devices}}</div><div class="v">{{.Devices}}</div></div>
{{if .Resets}}<div><div class="k">{{.T.resets}}</div><div class="v">{{.Resets}}</div></div>{{end}}
</div>{{if .Percent}}<div class="bar"><i style="width:{{.Percent}}%"></i></div>{{end}}
<p class="dim">{{.T.last}}: {{.LastSeen}}</p>
{{if .History}}<div class="k">{{.T.history}}</div><div class="hist">{{range .History}}<i style="height:{{.Pct}}%" title="{{.Title}}"></i>{{end}}</div>{{end}}</div>
<div class="card"><h2>{{.T.subLink}}</h2>
<div class="row"><code>{{.SubURL}}</code><button data-copy="{{.SubURL}}">{{.T.copy}}</button></div>
{{if .QR}}<img class="qr" src="{{.QR}}" alt="{{.T.qrAlt}}">{{end}}
<p class="dim">{{.T.subHelp}}</p></div>
<div class="card"><h2>{{.T.failover}}</h2>
<div class="row"><code>{{.JSONURL}}</code><button data-copy="{{.JSONURL}}">{{.T.copy}}</button></div>
<p class="dim">{{.T.failHelp}}</p></div>
<div class="card"><h2>{{.T.single}}</h2>{{if .Network}}<p class="dim">{{.Network}}</p>{{end}}
{{range .Links}}<div class="row"><code title="{{.Name}}">{{.URL}}</code><button data-copy="{{.URL}}">{{$.T.copy}}</button></div>{{end}}</div>
{{if or .Files .Login}}<div class="card"><h2>{{.T.files}}</h2>
{{range .Files}}<div class="row"><code>{{.Name}}</code><a class="btn" href="{{.URL}}" download>{{$.T.download}}</a></div>{{end}}
{{if .Login}}<div class="row"><code>{{.Login}}</code><button data-copy="{{.Login}}">{{.T.copy}}</button></div>{{end}}
<p class="dim">{{.T.filesHelp}}</p></div>{{end}}
</main><script>
var copyText={{.T.copy}},copiedText={{.T.copied}};
document.querySelectorAll("button[data-copy]").forEach(function(b){b.addEventListener("click",function(){
var t=b.getAttribute("data-copy"),done=function(){b.textContent=copiedText;setTimeout(function(){b.textContent=copyText},1500)};
if(navigator.clipboard){navigator.clipboard.writeText(t).then(done)}else{var a=document.createElement("textarea");a.value=t;document.body.appendChild(a);a.select();document.execCommand("copy");a.remove();done()}})});
</script></body></html>`))

// SetSubLang chooses the language of users' pages ("" = from the browser).
func SetSubLang(r *Server, lang string) error {
	if err := validLang(lang); err != nil {
		return err
	}
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	st.Sub.Lang = lang
	return saveState(r, st) // the server reads state.json on every request
}
