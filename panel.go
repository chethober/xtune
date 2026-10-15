package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Panel talks to a 3x-ui panel through the SSH session: requests are made by curl on the server
// against 127.0.0.1, so the panel never has to be exposed. Credentials travel on curl's stdin.
type Panel struct {
	r     *Remote
	base  string // http://127.0.0.1:PORT/BASE/
	token string
	jar   string // cookie jar path on the server when using username/password login
}

func OpenPanel(r *Remote, st ServerStatus, ask Prompter) (*Panel, error) {
	if !st.XUI {
		return nil, fmt.Errorf("3x-ui is not installed on this server")
	}
	base := "/" + strings.Trim(st.XUIBase, "/") + "/"
	if base == "//" {
		base = "/"
	}
	p := &Panel{r: r, base: "http://127.0.0.1:" + st.XUIPort + base, token: st.XUIToken}
	if p.token != "" {
		if _, err := p.call("GET", "server/status", nil); err == nil {
			return p, nil
		}
		p.token = ""
	}
	if ask == nil {
		return nil, fmt.Errorf("no panel API token found; log in interactively")
	}
	user, err := ask("3x-ui panel username", false)
	if err != nil {
		return nil, err
	}
	pass, err := ask("3x-ui panel password", true)
	if err != nil {
		return nil, err
	}
	jar, _, err := r.Exec("umask 077; mktemp", "", false)
	if err != nil {
		return nil, err
	}
	p.jar = strings.TrimSpace(jar)
	form := url.Values{"username": {user}, "password": {pass}}.Encode()
	out, _, err := r.Exec(fmt.Sprintf("curl -s -m 20 -c %s --data-binary @- %s", shq(p.jar), shq(p.base+"login")), form, false)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Success bool
		Msg     string
	}
	if json.Unmarshal([]byte(out), &resp) != nil || !resp.Success {
		return nil, fmt.Errorf("panel login failed: %s", resp.Msg)
	}
	return p, nil
}

func (p *Panel) Close() {
	if p.jar != "" {
		p.r.Exec("rm -f "+shq(p.jar), "", false)
	}
}

func curlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// call performs an API request and returns the "obj" field.
func (p *Panel) call(method, path string, body any) (json.RawMessage, error) {
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "url = %s\nrequest = %s\nsilent\nmax-time = 120\n", curlQuote(p.base+"panel/api/"+path), method)
	if p.token != "" {
		fmt.Fprintf(&cfg, "header = %s\n", curlQuote("Authorization: Bearer "+p.token))
	}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&cfg, "header = \"Content-Type: application/json\"\ndata-binary = %s\n", curlQuote(string(b)))
	}
	cmd := "curl -K -"
	if p.jar != "" {
		cmd += " -b " + shq(p.jar)
	}
	out, _, err := p.r.Exec(cmd, cfg.String(), false)
	if err != nil {
		return nil, fmt.Errorf("panel %s %s: %w", method, path, err)
	}
	var resp struct {
		Success bool            `json:"success"`
		Msg     string          `json:"msg"`
		Obj     json.RawMessage `json:"obj"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("panel %s %s: unexpected response %q", method, path, tail(out, 200))
	}
	if !resp.Success {
		return nil, fmt.Errorf("panel %s %s: %s", method, path, resp.Msg)
	}
	return resp.Obj, nil
}

type PanelUser struct {
	ID         int    `json:"id"`
	Email      string `json:"email"`
	SubID      string `json:"subId"`
	Enable     bool   `json:"enable"`
	TotalGB    int64  `json:"totalGB"` // bytes, despite the name; 0 = unlimited
	ExpiryTime int64  `json:"expiryTime"`
	InboundIDs []int  `json:"inboundIds"`
	Traffic    struct {
		Up         int64 `json:"up"`
		Down       int64 `json:"down"`
		LastOnline int64 `json:"lastOnline"`
	} `json:"traffic"`
}

type PanelInbound struct {
	ID       int    `json:"id"`
	Remark   string `json:"remark"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Enable   bool   `json:"enable"`
}

func (p *Panel) Users() ([]PanelUser, error) {
	obj, err := p.call("GET", "clients/list", nil)
	if err != nil {
		return nil, err
	}
	var us []PanelUser
	err = json.Unmarshal(obj, &us)
	sort.Slice(us, func(i, j int) bool { return us[i].Email < us[j].Email })
	return us, err
}

func (p *Panel) Inbounds() ([]PanelInbound, error) {
	obj, err := p.call("GET", "inbounds/list/slim", nil)
	if err != nil {
		return nil, err
	}
	var ibs []PanelInbound
	return ibs, json.Unmarshal(obj, &ibs)
}

// Add creates a user on the given inbounds (all enabled ones when empty). The panel applies the
// Vision flow only where the inbound supports it.
func (p *Panel) Add(email string, quotaGB float64, days int, inboundIDs []int) error {
	if len(inboundIDs) == 0 {
		ibs, err := p.Inbounds()
		if err != nil {
			return err
		}
		for _, ib := range ibs {
			if ib.Enable {
				inboundIDs = append(inboundIDs, ib.ID)
			}
		}
	}
	if len(inboundIDs) == 0 {
		return fmt.Errorf("the panel has no enabled inbounds")
	}
	client := J{"email": email, "subId": randHex(8), "flow": "xtls-rprx-vision", "enable": true,
		"totalGB": int64(quotaGB * (1 << 30)), "expiryTime": expiryMs(days), "limitIp": 0, "limitHwid": 0, "tgId": 0}
	_, err := p.call("POST", "clients/add", J{"client": client, "inboundIds": inboundIDs})
	return err
}

func expiryMs(days int) int64 {
	if days <= 0 {
		return 0
	}
	return time.Now().AddDate(0, 0, days).UnixMilli()
}

// Update changes a user. The panel replaces the whole client, so the current values are read
// first and sent back in the per-inbound client shape it expects (id = UUID).
func (p *Panel) Update(email string, mutate func(c J)) error {
	obj, err := p.call("GET", "clients/get/"+url.PathEscape(email), nil)
	if err != nil {
		return err
	}
	var got struct {
		Client J `json:"client"`
	}
	if err := json.Unmarshal(obj, &got); err != nil {
		return err
	}
	c := J{"id": got.Client["uuid"]}
	for _, k := range []string{"email", "subId", "flow", "password", "auth", "security", "enable", "totalGB", "expiryTime",
		"limitIp", "limitHwid", "tgId", "comment", "reset", "trafficReset", "trafficResetDay", "group"} {
		if v, ok := got.Client[k]; ok {
			c[k] = v
		}
	}
	mutate(c)
	_, err = p.call("POST", "clients/update/"+url.PathEscape(email), c)
	return err
}

func (p *Panel) Delete(email string) error {
	_, err := p.call("POST", "clients/del/"+url.PathEscape(email), nil)
	return err
}

func (p *Panel) Links(email string) ([]string, error) {
	obj, err := p.call("GET", "clients/links/"+url.PathEscape(email), nil)
	if err != nil {
		return nil, err
	}
	var links []string
	return links, json.Unmarshal(obj, &links)
}

// SubURL returns the user's subscription URL when the panel's subscription service is configured.
func (p *Panel) SubURL(subID string) string {
	obj, err := p.call("POST", "setting/all", nil)
	if err != nil {
		return ""
	}
	var s struct {
		SubEnable bool   `json:"subEnable"`
		SubURI    string `json:"subURI"`
	}
	if json.Unmarshal(obj, &s) != nil || !s.SubEnable || s.SubURI == "" {
		return ""
	}
	return strings.TrimRight(s.SubURI, "/") + "/" + subID
}

func (p *Panel) XrayVersion() string {
	obj, err := p.call("GET", "server/status", nil)
	if err != nil {
		return "unknown"
	}
	var s struct {
		Xray struct{ State, Version string }
	}
	json.Unmarshal(obj, &s)
	return s.Xray.Version + " (" + s.Xray.State + ")"
}

func (p *Panel) AvailableXray() ([]string, error) {
	obj, err := p.call("GET", "server/getXrayVersion", nil)
	if err != nil {
		return nil, err
	}
	var vs []string
	return vs, json.Unmarshal(obj, &vs)
}

func (p *Panel) InstallXray(tag string) error {
	_, err := p.call("POST", "server/installXray/"+url.PathEscape(tag), nil)
	return err
}

func humanBytes(b int64) string {
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

func expiryStr(ms int64) string {
	switch {
	case ms == 0:
		return "never"
	case ms < 0:
		return fmt.Sprintf("%dd after first use", -ms/86400000)
	}
	t := time.UnixMilli(ms)
	if time.Now().After(t) {
		return "EXPIRED " + t.Format("2006-01-02")
	}
	return t.Format("2006-01-02")
}
