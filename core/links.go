package core

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/chethober/xtune/pkg/protocols"
)

// Share links from any provider: vless://, vmess://, trojan://, ss://, hysteria2:// (hy2://),
// wireguard:// and WireGuard .conf files, each turned into an Xray outbound.

// ParseLinks reads one link per line (or a base64 block of them, as subscriptions serve) and
// returns a config for each it understands, and how many lines it skipped.
func ParseLinks(text string) ([]protocols.ClientEntry, int) {
	text = strings.TrimSpace(text)
	if !strings.Contains(text, "://") && !strings.Contains(text, "[Interface]") {
		if b, err := decodeB64(text); err == nil {
			text = string(b)
		}
	}
	if strings.Contains(text, "[Interface]") && strings.Contains(text, "[Peer]") {
		c, err := parseWGConf(text, "wireguard")
		if err != nil {
			return nil, 1
		}
		return []protocols.ClientEntry{c}, 0
	}
	var out []protocols.ClientEntry
	skipped := 0
	seen := map[string]int{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		c, err := ParseLink(line)
		if err != nil {
			skipped++
			continue
		}
		// unique IDs: the same name twice gets a suffix
		base := c.ID
		if n := seen[base]; n > 0 {
			c.ID = fmt.Sprintf("%s-%d", base, n+1)
		}
		seen[base]++
		c.Key = c.ID
		out = append(out, c)
	}
	return out, skipped
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", ""))
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not base64")
}

// ParseLink turns one share link into a config.
func ParseLink(link string) (protocols.ClientEntry, error) {
	scheme, _, ok := strings.Cut(link, "://")
	if !ok {
		return protocols.ClientEntry{}, errors.New("not a link")
	}
	var ob J
	var name, kind, label, server string
	var err error
	switch strings.ToLower(scheme) {
	case "vless", "trojan":
		ob, name, server, err = parseVlessTrojan(link, strings.ToLower(scheme))
		kind, label = strings.ToLower(scheme), map[string]string{"vless": "VLESS", "trojan": "Trojan"}[strings.ToLower(scheme)]
	case "vmess":
		ob, name, server, err = parseVmess(link)
		kind, label = "vmess", "VMess"
	case "ss":
		ob, name, server, err = parseSS(link)
		kind, label = "shadowsocks", "Shadowsocks"
	case "hysteria2", "hy2":
		ob, name, server, err = parseHy2(link)
		kind, label = "hysteria2", "Hysteria2"
	case "wireguard", "wg":
		ob, name, server, err = parseWGLink(link)
		kind, label = "wireguard", "WireGuard"
	default:
		return protocols.ClientEntry{}, fmt.Errorf("%s links are not supported", scheme)
	}
	if err != nil {
		return protocols.ClientEntry{}, fmt.Errorf("%s link: %w", scheme, err)
	}
	if ss, ok := ob["streamSettings"].(J); ok {
		if n, _ := ss["network"].(string); n != "" && n != "tcp" && n != "raw" && kind != "hysteria2" {
			label += " · " + strings.ToUpper(n)
		}
		if sec, _ := ss["security"].(string); sec == "reality" && kind == "vless" {
			label = strings.Replace(label, "VLESS", "REALITY", 1)
		}
	}
	if name == "" {
		name = label + " · " + server
	}
	return protocols.ClientEntry{ID: slug(name), Name: name, Kind: kind, Label: label, Engine: "xray", Server: server, Outbound: ob, Link: link}, nil
}

// slug makes a config ID out of a name: letters, digits and dashes.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "config"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func fragmentName(u *url.URL) string {
	n, _ := url.PathUnescape(u.Fragment)
	return strings.TrimSpace(n)
}

func hostPort(u *url.URL) (string, int, error) {
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if host == "" || err != nil || port <= 0 || port > 65535 {
		return "", 0, errors.New("missing server or port")
	}
	return host, port, nil
}

// streamFrom builds streamSettings from the query parameters vless://, trojan:// and vmess
// (as a map) share: type, security, sni, fp, alpn, pbk, sid, spx, path, host, serviceName, mode.
func streamFrom(q func(string) string, defaultSecurity string) J {
	network := strings.ToLower(q("type"))
	if network == "" {
		network = "tcp"
	}
	if network == "http" {
		network = "xhttp" // older links name splithttp/xhttp this way
	}
	if network == "splithttp" {
		network = "xhttp"
	}
	security := strings.ToLower(q("security"))
	if security == "" {
		security = defaultSecurity
	}
	ss := J{"network": network, "security": security}
	sni := q("sni")
	if sni == "" {
		sni = q("peer")
	}
	alpn := []string{}
	if a := q("alpn"); a != "" {
		alpn = strings.Split(a, ",")
	}
	switch security {
	case "tls":
		t := J{"serverName": sni, "fingerprint": orStr(q("fp"), "chrome")}
		if len(alpn) > 0 {
			t["alpn"] = alpn
		}
		if q("allowInsecure") == "1" || q("insecure") == "1" {
			t["allowInsecure"] = true
		}
		ss["tlsSettings"] = t
	case "reality":
		ss["realitySettings"] = J{"serverName": sni, "fingerprint": orStr(q("fp"), "chrome"), "publicKey": q("pbk"), "shortId": q("sid"),
			"spiderX": orStr(q("spx"), "/")}
	}
	path, host := q("path"), q("host")
	switch network {
	case "ws":
		ss["wsSettings"] = J{"path": orStr(path, "/"), "host": host}
	case "httpupgrade":
		ss["httpupgradeSettings"] = J{"path": orStr(path, "/"), "host": host}
	case "grpc":
		ss["grpcSettings"] = J{"serviceName": orStr(q("serviceName"), path), "multiMode": q("mode") == "multi"}
	case "xhttp":
		x := J{"path": orStr(path, "/"), "mode": orStr(q("mode"), "auto")}
		if host != "" {
			x["host"] = host
		}
		if extra := q("extra"); extra != "" {
			var e J
			if json.Unmarshal([]byte(extra), &e) == nil {
				x["extra"] = e
			}
		}
		ss["xhttpSettings"] = x
	case "tcp", "raw":
		if q("headerType") == "http" {
			ss["tcpSettings"] = J{"header": J{"type": "http", "request": J{"path": []string{orStr(path, "/")}, "headers": J{"Host": []string{host}}}}}
		}
	case "kcp", "mkcp":
		ss["network"] = "kcp"
		ss["kcpSettings"] = J{"header": J{"type": orStr(q("headerType"), "none")}, "seed": q("seed")}
	}
	return ss
}

func parseVlessTrojan(link, scheme string) (J, string, string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, "", "", err
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, "", "", err
	}
	q := u.Query()
	secret := u.User.Username()
	if secret == "" {
		return nil, "", "", errors.New("missing id or password")
	}
	def := "none"
	if scheme == "trojan" {
		def = "tls"
	}
	ss := streamFrom(q.Get, def)
	if scheme == "trojan" {
		return J{"protocol": "trojan", "settings": J{"servers": []J{{"address": host, "port": port, "password": secret}}}, "streamSettings": ss},
			fragmentName(u), host, nil
	}
	user := J{"id": secret, "encryption": orStr(q.Get("encryption"), "none")}
	if f := q.Get("flow"); f != "" {
		user["flow"] = f
	}
	return J{"protocol": "vless", "settings": J{"vnext": []J{{"address": host, "port": port, "users": []J{user}}}}, "streamSettings": ss},
		fragmentName(u), host, nil
}

func parseVmess(link string) (J, string, string, error) {
	b, err := decodeB64(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		return nil, "", "", errors.New("not base64 JSON")
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "", "", err
	}
	str := func(k string) string {
		switch v := m[k].(type) {
		case string:
			return v
		case float64:
			return strconv.Itoa(int(v))
		}
		return ""
	}
	host := str("add")
	port, _ := strconv.Atoi(str("port"))
	if host == "" || port <= 0 || str("id") == "" {
		return nil, "", "", errors.New("missing server, port or id")
	}
	q := func(k string) string {
		switch k {
		case "type":
			return str("net")
		case "security":
			if s := str("tls"); s != "" && s != "none" {
				return s
			}
			return "none"
		case "headerType":
			return str("type")
		}
		return str(k)
	}
	ss := streamFrom(q, "none")
	aid, _ := strconv.Atoi(str("aid"))
	return J{"protocol": "vmess", "settings": J{"vnext": []J{{"address": host, "port": port,
		"users": []J{{"id": str("id"), "alterId": aid, "security": orStr(str("scy"), "auto")}}}}}, "streamSettings": ss}, str("ps"), host, nil
}

// parseSS reads SIP002 (ss://base64(method:password)@host:port#name) and the older
// ss://base64(method:password@host:port)#name.
func parseSS(link string) (J, string, string, error) {
	rest := strings.TrimPrefix(link, "ss://")
	name := ""
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		name, _ = url.PathUnescape(rest[i+1:])
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest = rest[:i] // plugins are not supported; the plain server still works without them
	}
	var userinfo, hp string
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		userinfo, hp = rest[:at], rest[at+1:]
		if b, err := decodeB64(userinfo); err == nil && strings.Contains(string(b), ":") {
			userinfo = string(b)
		} else if u, err := url.PathUnescape(userinfo); err == nil {
			userinfo = u
		}
	} else {
		b, err := decodeB64(rest)
		if err != nil {
			return nil, "", "", errors.New("unreadable")
		}
		at := strings.LastIndexByte(string(b), '@')
		if at < 0 {
			return nil, "", "", errors.New("unreadable")
		}
		userinfo, hp = string(b[:at]), string(b[at+1:])
	}
	method, password, ok := strings.Cut(userinfo, ":")
	host, portS, err := net.SplitHostPort(strings.TrimSuffix(hp, "/"))
	port, perr := strconv.Atoi(portS)
	if !ok || err != nil || perr != nil {
		return nil, "", "", errors.New("missing method, password, server or port")
	}
	return J{"protocol": "shadowsocks", "settings": J{"servers": []J{{"address": host, "port": port, "method": method, "password": password}}}},
		strings.TrimSpace(name), host, nil
}

func parseHy2(link string) (J, string, string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, "", "", err
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, "", "", err
	}
	q := u.Query()
	auth := u.User.Username()
	if p, ok := u.User.Password(); ok {
		auth += ":" + p
	}
	if q.Get("obfs") != "" && q.Get("obfs") != "none" {
		return nil, "", "", errors.New("obfuscated Hysteria2 is not supported")
	}
	t := J{"serverName": orStr(q.Get("sni"), host), "alpn": []string{"h3"}}
	if q.Get("insecure") == "1" {
		t["allowInsecure"] = true
	}
	if pin := q.Get("pinSHA256"); pin != "" {
		t["pinnedPeerCertSha256"] = pin
	}
	ss := J{"network": "hysteria", "hysteriaSettings": J{"version": 2, "auth": auth}, "security": "tls", "tlsSettings": t}
	if hop := q.Get("mport"); hop != "" {
		ss["finalmask"] = J{"udp": []J{{"type": "udphop", "settings": J{"mode": "intervalLocal,intervalRemote", "remotePorts": hop, "interval": "10-30"}}}}
	}
	return J{"protocol": "hysteria", "settings": J{"version": 2, "address": host, "port": port}, "streamSettings": ss}, fragmentName(u), host, nil
}

// parseWGLink reads wireguard://PRIVATEKEY@host:port?publickey=&address=&mtu=&presharedkey=#name.
func parseWGLink(link string) (J, string, string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, "", "", err
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, "", "", err
	}
	q := u.Query()
	priv, _ := url.PathUnescape(u.User.Username())
	pub := orStr(q.Get("publickey"), q.Get("publicKey"))
	if priv == "" || pub == "" {
		return nil, "", "", errors.New("missing keys")
	}
	addrs := strings.Split(orStr(q.Get("address"), q.Get("ip")), ",")
	mtu, _ := strconv.Atoi(q.Get("mtu"))
	return wgOutbound(priv, pub, q.Get("presharedkey"), net.JoinHostPort(host, strconv.Itoa(port)), addrs, mtu), fragmentName(u), host, nil
}

func wgOutbound(priv, pub, psk, endpoint string, addrs []string, mtu int) J {
	var clean []string
	for _, a := range addrs {
		if a = strings.TrimSpace(a); a != "" {
			clean = append(clean, a)
		}
	}
	if mtu <= 0 {
		mtu = 1280
	}
	peer := J{"publicKey": pub, "endpoint": endpoint, "allowedIPs": []string{"0.0.0.0/0", "::/0"}}
	if psk != "" {
		peer["preSharedKey"] = psk
	}
	return J{"protocol": "wireguard", "settings": J{"secretKey": priv, "address": clean, "peers": []J{peer}, "mtu": mtu}}
}

// parseWGConf reads a WireGuard .conf (one peer).
func parseWGConf(text, name string) (protocols.ClientEntry, error) {
	vals := map[string]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := section + "." + strings.ToLower(strings.TrimSpace(k))
		if _, dup := vals[key]; !dup {
			vals[key] = strings.TrimSpace(v)
		}
	}
	priv, pub, ep := vals["interface.privatekey"], vals["peer.publickey"], vals["peer.endpoint"]
	if priv == "" || pub == "" || ep == "" {
		return protocols.ClientEntry{}, errors.New("a WireGuard config needs PrivateKey, PublicKey and Endpoint")
	}
	if strings.Contains(text, "Jc =") || strings.Contains(text, "Jc=") {
		return protocols.ClientEntry{}, errors.New("AmneziaWG configs come with an xtune subscription")
	}
	host, _, _ := net.SplitHostPort(ep)
	mtu, _ := strconv.Atoi(vals["interface.mtu"])
	ob := wgOutbound(priv, pub, vals["peer.presharedkey"], ep, strings.Split(vals["interface.address"], ","), mtu)
	n := "WireGuard · " + host
	return protocols.ClientEntry{ID: slug(name + "-" + host), Key: slug(name + "-" + host), Name: n, Kind: "wireguard", Label: "WireGuard", Engine: "xray",
		Server: host, Outbound: ob}, nil
}
