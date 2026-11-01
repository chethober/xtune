package main

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chethober/xtune/internal/state"

	"github.com/chethober/xtune/internal/remote"
)

var fragNames = []string{"none", "frag-mild", "frag-aggr"}
var fragments = map[string]map[string]string{
	"frag-mild": {"packets": "tlshello", "length": "100-200", "interval": "10-20"},
	"frag-aggr": {"packets": "tlshello", "length": "10-30", "interval": "5-10"},
}

func randHex(n int) string { b := make([]byte, n); rand.Read(b); return hex.EncodeToString(b) }

// NewState generates every secret. REALITY keys and the Hysteria2 certificate are made here;
// the VLESS Encryption pair comes from the local xray binary so its format always matches.
func NewState(host, xrayBin string) (*state.State, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	st := &state.State{Host: host, ShortID: randHex(4), SSKey: state.NewSSKey(), CertSNI: "www.bing.com",
		RealityPriv: base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		RealityPub:  base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())}
	out, err := exec.Command(xrayBin, "vlessenc").Output()
	if err != nil {
		return nil, fmt.Errorf("xray vlessenc: %w (local xray too old?)", err)
	}
	// the X25519-authenticated pair is printed first
	if m := regexp.MustCompile(`"decryption":\s*"([^"]+)"`).FindSubmatch(out); m != nil {
		st.VencDec = string(m[1])
	}
	if m := regexp.MustCompile(`"encryption":\s*"([^"]+)"`).FindSubmatch(out); m != nil {
		st.VencEnc = string(m[1])
	}
	if st.VencDec == "" || st.VencEnc == "" {
		return nil, fmt.Errorf("could not parse `xray vlessenc` output")
	}
	if err := makeCert(st); err != nil {
		return nil, err
	}
	if _, err := ensureKeys(st); err != nil {
		return nil, err
	}
	return st, nil
}

func makeCert(st *state.State) error {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: st.CertSNI},
		DNSNames: []string{st.CertSNI}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		return err
	}
	kd, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(der)
	st.CertHash = hex.EncodeToString(sum[:])
	st.CertPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	st.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
	return nil
}

func addrFor(st *state.State, e state.Entry) string {
	switch {
	case e.CDN():
		return st.CDNDomain
	case e.Worker():
		return e.SNI
	}
	return st.Addr()
}

// dialAddr is the address the client connects to: a clean IP for CDN and Worker entries when
// tuning chose one, the entry's usual address otherwise.
func dialAddr(st *state.State, e state.Entry, k state.Knobs) string {
	if k.Addr != "" && (e.CDN() || e.Worker()) {
		return k.Addr
	}
	return addrFor(st, e)
}

// clientPort is the port clients dial: a Worker answers on 443 whatever port it relays to.
func clientPort(e state.Entry) int {
	if e.Worker() {
		return 443
	}
	return e.Port
}

func hostPort(host string, port int) string {
	if strings.Contains(host, ":") { // IPv6
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}

func certFiles(dir string) []J {
	return []J{{"certificateFile": dir + "/hy.crt", "keyFile": dir + "/hy.key"}}
}

// ---------------------------------------------------------------- server config

type J = map[string]any

func inbound(st *state.State, e state.Entry, users []state.User) J {
	ib := J{"tag": e.ID, "listen": "0.0.0.0", "port": e.Port,
		"sniffing": J{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}}
	switch e.Kind {
	case "hysteria2":
		var cl []J
		for _, u := range users {
			cl = append(cl, J{"auth": u.UUID, "email": u.Name})
		}
		ib["protocol"] = "hysteria"
		ib["settings"] = J{"version": 2, "clients": cl}
		ib["streamSettings"] = J{"network": "hysteria", "security": "tls", "tlsSettings": J{"alpn": []string{"h3"},
			"certificates": certFiles(remote.Dir)}}
		return ib
	case "ss2022":
		var cl []J
		for _, u := range users {
			cl = append(cl, J{"password": u.SSKey, "email": u.Name})
		}
		ib["protocol"] = "shadowsocks"
		ib["settings"] = J{"method": "2022-blake3-aes-128-gcm", "password": st.SSKey, "clients": cl, "network": "tcp,udp"}
		return ib
	case "wireguard":
		wgInbound(st, ib, users)
		return ib
	}
	var cl []J
	for _, u := range users {
		c := J{"id": u.UUID, "email": u.Name}
		if e.Kind == "reality-tcp" {
			c["flow"] = "xtls-rprx-vision"
		}
		cl = append(cl, c)
	}
	dec := "none"
	if e.VlessEnc() || e.Worker() {
		dec = st.VencDec
	}
	ib["protocol"] = "vless"
	ib["settings"] = J{"clients": cl, "decryption": dec}
	if e.Trojan() {
		var tc []J
		for _, u := range users {
			tc = append(tc, J{"password": u.UUID, "email": u.Name})
		}
		ib["protocol"], ib["settings"] = "trojan", J{"clients": tc}
	}
	ss := J{"network": e.Net(), "security": "none"}
	if e.Worker() { // the Worker ends the WebSocket and opens a TCP connection with the client's address first
		ss["network"] = "tcp"
		ss["sockopt"] = J{"acceptProxyProtocol": true}
	}
	if e.Reality() {
		ss["security"] = "reality"
		ss["realitySettings"] = J{"target": e.SNI + ":443", "serverNames": []string{e.SNI},
			"privateKey": st.RealityPriv, "shortIds": []string{st.ShortID}}
	}
	if e.CDN() { // CDNs in "Full" TLS mode accept the self-signed certificate at the origin
		ss["security"] = "tls"
		ss["tlsSettings"] = J{"alpn": []string{"h2", "http/1.1"}, "certificates": certFiles(remote.Dir)}
	}
	switch e.Net() {
	case "xhttp":
		ss["xhttpSettings"] = J{"path": e.Path, "mode": "auto"}
	case "grpc":
		ss["grpcSettings"] = J{"serviceName": e.Svc}
	}
	ib["streamSettings"] = ss
	return ib
}

// ServerConfig builds the full Xray config for the given entries and their enabled users.
func ServerConfig(st *state.State, entries []state.Entry, users []state.User) J {
	var enabled []state.User
	for _, u := range users {
		if u.Enabled {
			enabled = append(enabled, u)
		}
	}
	ibs := []J{}
	if len(enabled) > 0 {
		for _, e := range state.FilterEntries(entries, state.Entry.Xray) {
			us := enabled
			if e.Kind != "wireguard" {
				us = slices.DeleteFunc(slices.Clone(enabled), func(u state.User) bool { return u.WGOnly })
			}
			ibs = append(ibs, inbound(st, e, us))
		}
	}
	obs := []J{{"tag": "direct", "protocol": "freedom"}, {"tag": "block", "protocol": "blackhole"}}
	// keep clients away from the VPS's own localhost services (panels, APIs)
	rules := []J{{"ip": []string{"geoip:private"}, "outboundTag": "block"},
		{"protocol": []string{"bittorrent"}, "outboundTag": "block"}}
	if st.Warp != nil {
		obs = append(obs, warpOutbound(st.Warp))
		rules = append(rules, warpRule(st.Warp))
	}
	cfg := J{"log": J{"loglevel": "warning"}, "inbounds": ibs, "outbounds": obs, "routing": J{"rules": rules}}
	if st.APIPort > 0 {
		// live user changes, traffic and online stats, source-IP blocks; loopback only
		cfg["api"] = J{"tag": "api", "listen": fmt.Sprintf("127.0.0.1:%d", st.APIPort),
			"services": []string{"HandlerService", "StatsService", "RoutingService"}}
		cfg["stats"] = J{}
		cfg["policy"] = J{"levels": J{"0": J{"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true}},
			"system": J{"statsInboundUplink": true, "statsInboundDownlink": true}}
	}
	return cfg
}

// ---------------------------------------------------------------- client side

func Outbound(st *state.State, e state.Entry, u state.User, k state.Knobs, tag string) J {
	switch e.Kind {
	case "hysteria2":
		ss := J{"network": "hysteria", "hysteriaSettings": J{"version": 2, "auth": u.UUID},
			"security": "tls", "tlsSettings": J{"serverName": st.CertSNI, "alpn": []string{"h3"}, "pinnedPeerCertSha256": st.CertHash}}
		if e.Hop != "" { // the server forwards the range to Port; the client changes ports as it goes
			ss["finalmask"] = J{"udp": []J{{"type": "udphop", "settings": J{"mode": "intervalLocal,intervalRemote",
				"remotePorts": e.Hop, "interval": hopInterval}}}}
		}
		return J{"tag": tag, "protocol": "hysteria", "settings": J{"version": 2, "address": st.Addr(), "port": e.Port},
			"streamSettings": ss}
	case "ss2022":
		return J{"tag": tag, "protocol": "shadowsocks", "settings": J{"servers": []J{{"address": st.Addr(), "port": e.Port,
			"method": "2022-blake3-aes-128-gcm", "password": st.SSKey + ":" + u.SSKey}}}}
	case "wireguard":
		return wgOutbound(st, e, u, k, tag)
	}
	enc, flow := "none", ""
	if e.VlessEnc() || e.Worker() {
		enc = st.VencEnc
	}
	if e.Kind == "reality-tcp" {
		flow = "xtls-rprx-vision"
	}
	ss := J{"network": e.Net(), "security": "none"}
	if e.Reality() {
		ss["security"] = "reality"
		ss["realitySettings"] = J{"serverName": e.SNI, "fingerprint": orStr(k.FP, "chrome"), "publicKey": st.RealityPub,
			"shortId": st.ShortID, "spiderX": "/"}
	}
	if e.CDN() {
		ss["security"] = "tls"
		ss["tlsSettings"] = J{"serverName": st.CDNDomain, "fingerprint": orStr(k.FP, "chrome"), "alpn": []string{"h2", "http/1.1"}}
		if k.ALPN == "h3" { // QUIC to the CDN; uTLS fingerprints are for TCP
			ss["tlsSettings"] = J{"serverName": st.CDNDomain, "alpn": []string{"h3"}}
		}
	}
	if e.Worker() { // WebSocket needs HTTP/1.1
		ss["security"] = "tls"
		ss["tlsSettings"] = J{"serverName": e.SNI, "fingerprint": orStr(k.FP, "chrome"), "alpn": []string{"http/1.1"}}
		ss["wsSettings"] = J{"path": workerPath(e), "host": e.SNI}
	}
	switch e.Net() {
	case "xhttp":
		ss["xhttpSettings"] = J{"path": e.Path, "mode": orStr(k.Mode, "auto")}
		if e.CDN() {
			ss["xhttpSettings"].(J)["host"] = st.CDNDomain
		}
		if e.CDN() && k.Split == "cdn-up" {
			ss["xhttpSettings"].(J)["extra"] = J{"downloadSettings": directDownload(st, e, k)}
		}
	case "grpc":
		ss["grpcSettings"] = J{"serviceName": e.Svc}
	}
	if k.Frag != "" && k.Frag != "none" {
		ss["sockopt"] = J{"dialerProxy": k.Frag}
	}
	if e.Trojan() {
		return J{"tag": tag, "protocol": "trojan", "settings": J{"servers": []J{{"address": st.Addr(), "port": e.Port,
			"password": u.UUID}}}, "streamSettings": ss}
	}
	return J{"tag": tag, "protocol": "vless", "settings": J{"vnext": []J{{"address": dialAddr(st, e, k), "port": clientPort(e),
		"users": []J{{"id": u.UUID, "encryption": enc, "flow": flow}}}}}, "streamSettings": ss}
}

// hopInterval is how often (seconds) a port-hopping client moves to another port.
const hopInterval = "10-30"

// directDownload is the download half of a split CDN entry: the same inbound, reached straight at
// the server's address. Its certificate is self-signed, so the client pins it.
func directDownload(st *state.State, e state.Entry, k state.Knobs) J {
	return J{"address": st.Addr(), "port": e.Port, "network": "xhttp", "security": "tls",
		"tlsSettings": J{"serverName": st.CertSNI, "fingerprint": orStr(k.FP, "chrome"), "alpn": []string{"h2", "http/1.1"},
			"pinnedPeerCertSha256": st.CertHash},
		"xhttpSettings": J{"path": e.Path}}
}

func fragOutbounds() []J {
	var out []J
	for _, n := range fragNames[1:] {
		out = append(out, J{"tag": n, "protocol": "freedom", "settings": J{"fragment": fragments[n]}})
	}
	return out
}

func clientInbounds() []J {
	return []J{{"tag": "socks", "listen": "127.0.0.1", "port": 10808, "protocol": "socks", "settings": J{"udp": true},
		"sniffing": J{"enabled": true, "destOverride": []string{"http", "tls"}, "routeOnly": true}},
		{"tag": "http", "listen": "127.0.0.1", "port": 10809, "protocol": "http"}}
}

// ClientConfig is a ready-to-run client config (socks 10808, http 10809).
func ClientConfig(out J) J {
	obs := append([]J{out}, fragOutbounds()...)
	obs = append(obs, J{"tag": "direct", "protocol": "freedom"})
	return J{"log": J{"loglevel": "warning"}, "inbounds": clientInbounds(), "outbounds": obs,
		"routing": J{"rules": []J{{"ip": []string{"geoip:private"}, "outboundTag": "direct"}}}}
}

// countries maps a country code to the client routing lists that send its sites direct.
var countries = map[string][2]string{
	"ir": {"geosite:category-ir", "geoip:ir"},
	"cn": {"geosite:cn", "geoip:cn"},
	"ru": {"geosite:category-ru", "geoip:ru"},
}

// FailoverConfig is a full client config with every deployed config behind one balancer: Xray
// health-checks them and uses the fastest working one, so a blocked config fails over by itself
// (to the CDN entry, for example). Private addresses, and the chosen country's sites, go direct;
// ads are blocked. Domain strategy AsIs keeps DNS lookups for proxied sites off the local network.
func FailoverConfig(st *state.State, u state.User) J {
	var obs []J
	first := ""
	for _, s := range peerServers(st) {
		for _, e := range state.FilterEntries(linkEntries(s), state.Entry.Xray) {
			obs = append(obs, Outbound(s, e, u, e.Knobs(), "proxy-"+e.ID))
			first = orStr(first, "proxy-"+e.ID)
		}
	}
	obs = append(obs, fragOutbounds()...)
	obs = append(obs, J{"tag": "direct", "protocol": "freedom"}, J{"tag": "block", "protocol": "blackhole"})
	rules := []J{{"ip": []string{"geoip:private"}, "outboundTag": "direct"},
		{"domain": []string{"geosite:category-ads-all"}, "outboundTag": "block"}}
	if c, ok := countries[st.Country]; ok {
		rules = append(rules, J{"domain": []string{c[0]}, "outboundTag": "direct"}, J{"ip": []string{c[1]}, "outboundTag": "direct"})
	}
	rules = append(rules, J{"network": "tcp,udp", "balancerTag": "auto"})
	balancer := J{"tag": "auto", "selector": []string{"proxy-"}, "strategy": J{"type": "leastPing"}}
	if first != "" {
		balancer["fallbackTag"] = first
	}
	return J{"remarks": "xtune - " + u.Name, "log": J{"loglevel": "warning"}, "inbounds": clientInbounds(), "outbounds": obs,
		"burstObservatory": J{"subjectSelector": []string{"proxy-"},
			"pingConfig": J{"destination": probeURL, "interval": "1m", "timeout": "5s", "sampling": 3}},
		"routing": J{"domainStrategy": "AsIs", "balancers": []J{balancer}, "rules": rules}}
}

func Link(st *state.State, e state.Entry, u state.User, k state.Knobs) string {
	name := url.PathEscape(u.Name + "-" + e.ID)
	hostPort := hostPort(dialAddr(st, e, k), clientPort(e))
	switch e.Kind {
	case "hysteria2":
		hop := ""
		if e.Hop != "" {
			hop = "&mport=" + e.Hop
		}
		return fmt.Sprintf("hysteria2://%s@%s?sni=%s&alpn=h3&pinSHA256=%s%s#%s", u.UUID, hostPort, st.CertSNI, st.CertHash, hop, name)
	case "ss2022":
		return fmt.Sprintf("ss://%s@%s#%s", url.QueryEscape("2022-blake3-aes-128-gcm:"+st.SSKey+":"+u.SSKey), hostPort, name)
	case "wireguard":
		return wgLink(st, e, u, k, name)
	case "ssh": // ssh:// cannot say TLS or a WebSocket opener: those come as profile files
		if k.Mode == "" {
			return sshLink(st, e, u, name)
		}
	case "mtproto":
		return mtprotoLink(st, e, u)
	}
	if !e.Xray() { // no link scheme: these come as profile files (see Profiles)
		return ""
	}
	q := url.Values{"type": {e.Net()}, "encryption": {"none"}, "security": {"none"}}
	if e.VlessEnc() || e.Worker() {
		q.Set("encryption", st.VencEnc)
	}
	if e.Reality() {
		q.Set("security", "reality")
		q.Set("sni", e.SNI)
		q.Set("fp", orStr(k.FP, "chrome"))
		q.Set("pbk", st.RealityPub)
		q.Set("sid", st.ShortID)
		q.Set("spx", "/")
	}
	if e.CDN() {
		q.Set("security", "tls")
		q.Set("sni", st.CDNDomain)
		q.Set("host", st.CDNDomain)
		q.Set("fp", orStr(k.FP, "chrome"))
		q.Set("alpn", "h2,http/1.1")
		if k.ALPN == "h3" {
			q.Set("alpn", "h3")
			q.Del("fp")
		}
	}
	if e.Worker() {
		q.Set("security", "tls")
		q.Set("sni", e.SNI)
		q.Set("host", e.SNI)
		q.Set("fp", orStr(k.FP, "chrome"))
		q.Set("alpn", "http/1.1")
		q.Set("path", workerPath(e))
	}
	if e.Kind == "reality-tcp" {
		q.Set("flow", "xtls-rprx-vision")
	}
	switch e.Net() {
	case "xhttp":
		q.Set("path", e.Path)
		q.Set("mode", orStr(k.Mode, "auto"))
		if e.CDN() && k.Split == "cdn-up" {
			b, _ := json.Marshal(J{"downloadSettings": directDownload(st, e, k)})
			q.Set("extra", string(b))
		}
	case "grpc":
		q.Set("serviceName", e.Svc)
		q.Set("mode", "gun")
	}
	if e.Trojan() {
		q.Del("encryption")
		return fmt.Sprintf("trojan://%s@%s?%s#%s", url.PathEscape(u.UUID), hostPort, q.Encode(), name)
	}
	return fmt.Sprintf("vless://%s@%s?%s#%s", u.UUID, hostPort, q.Encode(), name)
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
