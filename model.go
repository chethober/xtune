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
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Entry is one server inbound. FP/Mode/Frag are the client-side knobs tuning picked for it.
type Entry struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // reality-tcp reality-xhttp reality-grpc vlessenc-tcp vlessenc-xhttp hysteria2 ss2022
	Port    int    `json:"port"`
	SNI     string `json:"sni,omitempty"`
	Path    string `json:"path,omitempty"`
	Svc     string `json:"svc,omitempty"`
	Primary bool   `json:"primary,omitempty"`
	FP      string `json:"fp,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Frag    string `json:"frag,omitempty"`
}

func (e Entry) Proto() string {
	if e.Kind == "hysteria2" {
		return "udp"
	}
	return "tcp"
}
func (e Entry) Reality() bool  { return strings.HasPrefix(e.Kind, "reality") }
func (e Entry) VlessEnc() bool { return strings.HasPrefix(e.Kind, "vlessenc") }
func (e Entry) Net() string {
	if i := strings.Index(e.Kind, "-"); i > 0 && (e.Reality() || e.VlessEnc()) {
		return e.Kind[i+1:]
	}
	return ""
}

type User struct {
	Name    string `json:"name"`
	UUID    string `json:"uuid"`
	SSKey   string `json:"ssKey"`
	Enabled bool   `json:"enabled"`
	Created int64  `json:"created"`
}

// State is the standalone deployment, stored on the server as state.json (root, 0600).
type State struct {
	Host        string  `json:"host"`
	RealityPriv string  `json:"realityPrivateKey"`
	RealityPub  string  `json:"realityPublicKey"`
	ShortID     string  `json:"shortId"`
	VencDec     string  `json:"vlessDecryption"`
	VencEnc     string  `json:"vlessEncryption"`
	SSKey       string  `json:"ssServerKey"`
	CertSNI     string  `json:"certSni"`
	CertHash    string  `json:"certSha256"`
	CertPEM     string  `json:"certPem"`
	KeyPEM      string  `json:"keyPem"`
	Entries     []Entry `json:"entries"`
	Users       []User  `json:"users"`
}

type Knobs struct{ FP, Frag, Mode string }

var fragNames = []string{"none", "frag-mild", "frag-aggr"}
var fragments = map[string]map[string]string{
	"frag-mild": {"packets": "tlshello", "length": "100-200", "interval": "10-20"},
	"frag-aggr": {"packets": "tlshello", "length": "10-30", "interval": "5-10"},
}

func randHex(n int) string { b := make([]byte, n); rand.Read(b); return hex.EncodeToString(b) }

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func ssKey() string { b := make([]byte, 16); rand.Read(b); return base64.StdEncoding.EncodeToString(b) }

func NewUser(name string) User {
	return User{Name: name, UUID: newUUID(), SSKey: ssKey(), Enabled: true, Created: time.Now().Unix()}
}

// NewState generates every secret. REALITY keys and the Hysteria2 certificate are made here;
// the VLESS Encryption pair comes from the local xray binary so its format always matches.
func NewState(host, xrayBin string) (*State, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	st := &State{Host: host, ShortID: randHex(4), SSKey: ssKey(), CertSNI: "www.bing.com",
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
	if err := st.makeCert(); err != nil {
		return nil, err
	}
	return st, nil
}

func (st *State) makeCert() error {
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

func (st *State) User(name string) (int, *User) {
	for i := range st.Users {
		if st.Users[i].Name == name {
			return i, &st.Users[i]
		}
	}
	return -1, nil
}

// ---------------------------------------------------------------- server config

type J = map[string]any

func (st *State) inbound(e Entry, users []User) J {
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
			"certificates": []J{{"certificateFile": remoteDir + "/hy.crt", "keyFile": remoteDir + "/hy.key"}}}}
		return ib
	case "ss2022":
		var cl []J
		for _, u := range users {
			cl = append(cl, J{"password": u.SSKey, "email": u.Name})
		}
		ib["protocol"] = "shadowsocks"
		ib["settings"] = J{"method": "2022-blake3-aes-128-gcm", "password": st.SSKey, "clients": cl, "network": "tcp,udp"}
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
	if e.VlessEnc() {
		dec = st.VencDec
	}
	ib["protocol"] = "vless"
	ib["settings"] = J{"clients": cl, "decryption": dec}
	ss := J{"network": e.Net(), "security": "none"}
	if e.Reality() {
		ss["security"] = "reality"
		ss["realitySettings"] = J{"target": e.SNI + ":443", "serverNames": []string{e.SNI},
			"privateKey": st.RealityPriv, "shortIds": []string{st.ShortID}}
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
func (st *State) ServerConfig(entries []Entry, users []User) J {
	var enabled []User
	for _, u := range users {
		if u.Enabled {
			enabled = append(enabled, u)
		}
	}
	ibs := []J{}
	if len(enabled) > 0 {
		for _, e := range entries {
			ibs = append(ibs, st.inbound(e, enabled))
		}
	}
	return J{"log": J{"loglevel": "warning"}, "inbounds": ibs,
		"outbounds": []J{{"tag": "direct", "protocol": "freedom"}, {"tag": "block", "protocol": "blackhole"}},
		// keep clients away from the VPS's own localhost services (panels, APIs)
		"routing": J{"rules": []J{{"ip": []string{"geoip:private"}, "outboundTag": "block"},
			{"protocol": []string{"bittorrent"}, "outboundTag": "block"}}}}
}

// ---------------------------------------------------------------- client side

func (st *State) Outbound(e Entry, u User, k Knobs, tag string) J {
	switch e.Kind {
	case "hysteria2":
		return J{"tag": tag, "protocol": "hysteria", "settings": J{"version": 2, "address": st.Host, "port": e.Port},
			"streamSettings": J{"network": "hysteria", "hysteriaSettings": J{"version": 2, "auth": u.UUID},
				"security": "tls", "tlsSettings": J{"serverName": st.CertSNI, "alpn": []string{"h3"}, "pinnedPeerCertSha256": st.CertHash}}}
	case "ss2022":
		return J{"tag": tag, "protocol": "shadowsocks", "settings": J{"servers": []J{{"address": st.Host, "port": e.Port,
			"method": "2022-blake3-aes-128-gcm", "password": st.SSKey + ":" + u.SSKey}}}}
	}
	enc, flow := "none", ""
	if e.VlessEnc() {
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
	switch e.Net() {
	case "xhttp":
		ss["xhttpSettings"] = J{"path": e.Path, "mode": orStr(k.Mode, "auto")}
	case "grpc":
		ss["grpcSettings"] = J{"serviceName": e.Svc}
	}
	if k.Frag != "" && k.Frag != "none" {
		ss["sockopt"] = J{"dialerProxy": k.Frag}
	}
	return J{"tag": tag, "protocol": "vless", "settings": J{"vnext": []J{{"address": st.Host, "port": e.Port,
		"users": []J{{"id": u.UUID, "encryption": enc, "flow": flow}}}}}, "streamSettings": ss}
}

func fragOutbounds() []J {
	var out []J
	for _, n := range fragNames[1:] {
		out = append(out, J{"tag": n, "protocol": "freedom", "settings": J{"fragment": fragments[n]}})
	}
	return out
}

// ClientConfig is a standalone client config (socks 10808, http 10809).
func ClientConfig(out J) J {
	obs := append([]J{out}, fragOutbounds()...)
	obs = append(obs, J{"tag": "direct", "protocol": "freedom"})
	return J{"log": J{"loglevel": "warning"},
		"inbounds": []J{{"tag": "socks", "listen": "127.0.0.1", "port": 10808, "protocol": "socks", "settings": J{"udp": true},
			"sniffing": J{"enabled": true, "destOverride": []string{"http", "tls"}, "routeOnly": true}},
			{"tag": "http", "listen": "127.0.0.1", "port": 10809, "protocol": "http"}},
		"outbounds": obs,
		"routing":   J{"rules": []J{{"ip": []string{"geoip:private"}, "outboundTag": "direct"}}}}
}

func (st *State) Link(e Entry, u User, k Knobs) string {
	name := url.PathEscape(u.Name + "-" + e.ID)
	hostPort := st.Host + ":" + strconv.Itoa(e.Port)
	if strings.Contains(st.Host, ":") { // IPv6
		hostPort = "[" + st.Host + "]:" + strconv.Itoa(e.Port)
	}
	switch e.Kind {
	case "hysteria2":
		return fmt.Sprintf("hysteria2://%s@%s?sni=%s&alpn=h3&pinSHA256=%s#%s", u.UUID, hostPort, st.CertSNI, st.CertHash, name)
	case "ss2022":
		return fmt.Sprintf("ss://%s@%s#%s", url.QueryEscape("2022-blake3-aes-128-gcm:"+st.SSKey+":"+u.SSKey), hostPort, name)
	}
	q := url.Values{"type": {e.Net()}, "encryption": {"none"}, "security": {"none"}}
	if e.VlessEnc() {
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
	if e.Kind == "reality-tcp" {
		q.Set("flow", "xtls-rprx-vision")
	}
	switch e.Net() {
	case "xhttp":
		q.Set("path", e.Path)
		q.Set("mode", orStr(k.Mode, "auto"))
	case "grpc":
		q.Set("serviceName", e.Svc)
		q.Set("mode", "gun")
	}
	return fmt.Sprintf("vless://%s@%s?%s#%s", u.UUID, hostPort, q.Encode(), name)
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
