package protocols

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/chethober/xtune/internal/state"
)

var NamePlaceholder = regexp.MustCompile(`\{(\w+)\}`)

// kindLabels are the protocol names shown for {proto}.
var kindLabels = map[string]string{
	"reality-tcp": "REALITY", "reality-xhttp": "REALITY XHTTP", "reality-grpc": "REALITY gRPC",
	"vlessenc-tcp": "VLESS PQ", "vlessenc-xhttp": "VLESS PQ XHTTP", "cdn-xhttp": "CDN XHTTP",
	"hysteria2": "Hysteria2", "ss2022": "Shadowsocks", "wireguard": "WireGuard", "worker-ws": "Worker",
	"trojan-reality": "Trojan", "amneziawg": "AmneziaWG", "ssh": "SSH", "ssh-tls": "SSH TLS", "cdn-ssh": "CDN SSH",
	"dnstt": "DNS tunnel", "mtproto": "MTProto", "openvpn-udp": "OpenVPN UDP", "openvpn-tcp": "OpenVPN TCP", "ikev2": "IKEv2",
}

func KindLabel(kind string) string {
	if l, ok := kindLabels[kind]; ok {
		return l
	}
	return kind
}

// FlagEmoji is the flag of an ISO 3166-1 alpha-2 country code ("DE" → 🇩🇪), "" for anything else.
func FlagEmoji(cc string) string {
	if !ValidCountryCode(cc) {
		return ""
	}
	cc = strings.ToUpper(cc)
	return string([]rune{rune(cc[0]) - 'A' + 0x1F1E6, rune(cc[1]) - 'A' + 0x1F1E6})
}

func ValidCountryCode(cc string) bool {
	if len(cc) != 2 {
		return false
	}
	for _, c := range strings.ToUpper(cc) {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// ServerFlag is the country code behind {flag} and {country}, "" when unknown or off.
func ServerFlag(st *state.State) string {
	if ValidCountryCode(st.Naming.Flag) {
		return strings.ToUpper(st.Naming.Flag)
	}
	return ""
}

// expandName fills a template and tidies it: placeholders left empty take their separators along.
func expandName(tmpl string, vars map[string]string) string {
	s := NamePlaceholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		if v, ok := vars[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
	var out []string
	for _, t := range strings.Fields(s) {
		if isNameSep(t) && (len(out) == 0 || isNameSep(out[len(out)-1])) {
			continue
		}
		out = append(out, t)
	}
	for len(out) > 0 && isNameSep(out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	return strings.Join(out, " ")
}

func isNameSep(t string) bool {
	return strings.Trim(t, "-·|/•,:–—") == ""
}

func nameVars(st *state.State, u state.User) map[string]string {
	cc := ServerFlag(st)
	return map[string]string{"flag": FlagEmoji(cc), "country": cc, "server": st.Naming.Label, "user": u.Name}
}

// ConfigName is what apps show for one config of u.
func ConfigName(st *state.State, e state.Entry, u state.User) string {
	v := nameVars(st, u)
	v["id"], v["proto"], v["port"] = e.ID, KindLabel(e.Kind), fmt.Sprint(ClientPort(e))
	v["via"] = ""
	if st.Naming.Via != "" {
		v["via"] = "via " + st.Naming.Via
	}
	return expandName(orStr(st.Naming.Config, state.DefaultConfigName), v)
}

// SubName is the subscription's name in apps (Profile-Title) and of the failover config.
func SubName(st *state.State, u state.User) string {
	return expandName(orStr(st.Naming.Sub, state.DefaultSubName), nameVars(st, u))
}
