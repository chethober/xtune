package main

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/chethober/xtune/internal/state"

	"golang.org/x/crypto/ssh"
	"www.bamsoftware.com/git/dnstt.git/noise"
)

// Besides Xray's protocols, xtune serves:
//
//   - wireguard: Xray's WireGuard inbound, tuned and accounted like every other Xray config.
//   - trojan-reality: Trojan over REALITY (opt-in), for apps and networks that do better with
//     Trojan than VLESS; it shares the REALITY keys and targets.
//   - amneziawg: WireGuard with a disguised handshake, served by xtune itself (awg.go).
//   - worker-ws: VLESS over WebSocket through a Cloudflare Worker, relayed to a plain TCP
//     inbound of Xray's (worker.go). It needs a Cloudflare API token.
//   - ssh, ssh-tls, cdn-ssh: an SSH server inside xtune (`xtune tunnels`) that only forwards
//     TCP, for apps such as HTTP Injector, NetMod and NapsternetV. ssh-tls wraps it in TLS
//     ("SSH-SSL"); cdn-ssh is the same TLS port reached through the CDN domain with a WebSocket
//     upgrade. No system accounts are made: users and limits come from the state file.
//   - dnstt: a DNS tunnel (dnstt) to that SSH server, the "SlowDNS" layout. It needs a domain
//     delegated to the server with an NS record, and it is slow: a last resort for networks
//     where only DNS gets through.
//   - openvpn-udp, openvpn-tcp, ikev2: the distribution's OpenVPN and strongSwan. Clients need
//     root to bring a VPN up, so tuning measures only whether and how fast the handshake
//     answers from here.

// ---------------------------------------------------------------- keys

// ensureKeys generates the keys of the newer protocols, for states made before they existed.
// It reports whether anything was added.
func ensureKeys(st *state.State) (bool, error) {
	added := false
	if st.WGPriv == "" {
		k, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return false, err
		}
		st.WGPriv = base64.StdEncoding.EncodeToString(k.Bytes())
		st.WGPub = base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
		added = true
	}
	if st.SSHHostKey == "" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return false, err
		}
		blk, err := ssh.MarshalPrivateKey(priv, "")
		if err != nil {
			return false, err
		}
		signer, err := ssh.NewSignerFromKey(priv)
		if err != nil {
			return false, err
		}
		st.SSHHostKey = string(pem.EncodeToMemory(blk))
		st.SSHHostPub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
		added = true
	}
	if st.DNSTTKey == "" {
		priv, err := noise.GeneratePrivkey()
		if err != nil {
			return false, err
		}
		st.DNSTTKey = hex.EncodeToString(priv)
		st.DNSTTPub = hex.EncodeToString(noise.PubkeyFromPrivkey(priv))
		added = true
	}
	if st.AWG == nil {
		p, err := newAWGParams()
		if err != nil {
			return false, err
		}
		st.AWG = p
		added = true
	}
	return added, nil // the VPN CA comes with the first VPN entry (ensureVPNCert)
}

// keepIdentity takes over another state's keys, so a redeploy keeps every client working.
func keepIdentity(st *state.State, old *state.State) {
	st.RealityPriv, st.RealityPub, st.ShortID = old.RealityPriv, old.RealityPub, old.ShortID
	st.VencDec, st.VencEnc, st.SSKey = old.VencDec, old.VencEnc, old.SSKey
	st.CertSNI, st.CertHash, st.CertPEM, st.KeyPEM = old.CertSNI, old.CertHash, old.CertPEM, old.KeyPEM
	if old.WGPriv != "" {
		st.WGPriv, st.WGPub = old.WGPriv, old.WGPub
	}
	if old.SSHHostKey != "" {
		st.SSHHostKey, st.SSHHostPub = old.SSHHostKey, old.SSHHostPub
	}
	if old.DNSTTKey != "" {
		st.DNSTTKey, st.DNSTTPub = old.DNSTTKey, old.DNSTTPub
	}
	if old.AWG != nil {
		st.AWG = old.AWG
	}
	if old.VPNCA != "" {
		st.VPNCA, st.VPNCAKey, st.VPNCert, st.VPNKey, st.VPNName = old.VPNCA, old.VPNCAKey, old.VPNCert, old.VPNKey, old.VPNName
	}
}

// ---------------------------------------------------------------- WireGuard

// wgVariantUser is who a tuning variant connects as. WireGuard peers roam: two variants with one
// key on the same inbound would keep taking the peer's endpoint from each other. So each MTU
// variant is its own test peer, with a key and address derived from the test user.
func wgVariantUser(u state.User, k state.Knobs) state.User {
	if k.MTU == 0 || k.MTU == 1420 {
		return u
	}
	a := u
	a.Name = fmt.Sprintf("%s-mtu%d", u.Name, k.MTU)
	a.UUID = fmt.Sprintf("%s/mtu%d", u.UUID, k.MTU)
	a.WGOnly = true
	if ip, err := netip.ParseAddr(u.WGIP); err == nil {
		a.WGIP = ip.Next().String()
	}
	return a
}

// wgTestPeers are the test users the server needs for the WireGuard variants: u and its MTU aliases.
func wgTestPeers(u state.User, entries []state.Entry) []state.User {
	if !slices.ContainsFunc(entries, func(e state.Entry) bool { return e.Kind == "wireguard" }) {
		return []state.User{u}
	}
	return []state.User{u, wgVariantUser(u, state.Knobs{MTU: 1280})}
}

// ---------------------------------------------------------------- SSH links

// parseResolvers checks a --resolvers list. A bare IP means plain DNS on port 53.
func parseResolvers(list string) ([]string, error) {
	var out []string
	for _, r := range strings.Split(list, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if net.ParseIP(r) != nil {
			r = "udp:" + net.JoinHostPort(r, "53")
		}
		kind, addr, _ := strings.Cut(r, ":")
		switch {
		case kind == "udp" || kind == "dot":
			if _, _, err := net.SplitHostPort(addr); err != nil {
				return nil, fmt.Errorf("resolver %q: want %s:HOST:PORT", r, kind)
			}
		case kind == "doh" && strings.HasPrefix(addr, "https://"):
		default:
			return nil, fmt.Errorf("resolver %q: want an IP, udp:IP:PORT, doh:https://URL or dot:HOST:PORT", r)
		}
		out = append(out, r)
	}
	return out, nil
}

// inherit fills unset options from the current deployment: its addresses, and the opt-in
// protocols it runs, so a re-tune keeps offering them.
func (o *TuneOpts) inherit(old *state.State, set map[string]bool) {
	if old == nil {
		return
	}
	if !set["domain"] {
		o.Domain = old.Domain
	}
	if !set["cdn"] {
		o.CDNDomain = old.CDNDomain
	}
	if !set["dns-domain"] {
		o.DNSDomain = old.DNSDomain
	}
	if !set["protocols"] {
		for _, e := range old.Entries {
			if f := e.Family(); f != "" && !slices.Contains(o.Protocols, f) {
				o.Protocols = append(o.Protocols, f)
			}
		}
	}
}
