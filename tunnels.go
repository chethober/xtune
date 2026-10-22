package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"www.bamsoftware.com/git/dnstt.git/dns"

	dnsttclient "github.com/chethober/xtune/internal/dnstt/client"
	dnsttserver "github.com/chethober/xtune/internal/dnstt/server"
)

// The SSH tunnel server: `xtune tunnels run CONF` serves the ssh, ssh-tls, cdn-ssh and dnstt
// entries in CONF (tunnels.json, written next to config.json for the active users). It forwards
// TCP for logged-in users and nothing else: no shell, no remote forwarding, no private addresses.
// It counts traffic per user and per entry into a stats file that the enforce job reads.

// The tunnel server's timings and destination filter; variables so tests can shorten and widen them.
var (
	tunnelStatsEvery = 15 * time.Second
	tunnelWatchEvery = 5 * time.Second
	allowDest        = publicIP
)

// TunnelConf is what the tunnel server runs.
type TunnelConf struct {
	Entries   []Entry      `json:"entries"`
	Users     []TunnelUser `json:"users"`
	HostKey   string       `json:"hostKey"`
	CertPEM   string       `json:"certPem"`
	KeyPEM    string       `json:"keyPem"`
	DNSTTKey  string       `json:"dnsttKey,omitempty"`
	DNSDomain string       `json:"dnsDomain,omitempty"`
	Stats     string       `json:"stats"` // where the server writes its counters
}

type TunnelUser struct {
	Name    string `json:"name"`
	Pass    string `json:"pass"`
	Devices int    `json:"devices,omitempty"`
}

// tunnelStats are the server's counters since it started (Boot). The enforce job adds what
// changed since its last read, so a restart loses nothing it has already read.
type tunnelStats struct {
	Boot    string              `json:"boot"`
	Users   map[string][2]int64 `json:"users"`   // up, down
	Entries map[string]int64    `json:"entries"` // bytes by entry ID
	Online  map[string][]string `json:"online"`  // client IPs connected now, by user
	Written int64               `json:"written"` // unix seconds
}

// TunnelConf builds the tunnel server's config for these users.
func (st *State) TunnelConf(entries []Entry, users []User, statsPath string) TunnelConf {
	c := TunnelConf{Entries: filterEntries(entries, Entry.SSH), HostKey: st.SSHHostKey, CertPEM: st.CertPEM, KeyPEM: st.KeyPEM,
		DNSTTKey: st.DNSTTKey, DNSDomain: st.DNSDomain, Stats: statsPath, Users: []TunnelUser{}}
	for _, u := range users {
		if u.Enabled {
			c.Users = append(c.Users, TunnelUser{Name: u.Name, Pass: tunnelPassword(u), Devices: u.Devices})
		}
	}
	return c
}

// ---------------------------------------------------------------- server

type tunnelServer struct {
	path    string
	conf    TunnelConf
	sshConf *ssh.ServerConfig

	mu       sync.Mutex
	users    map[string]TunnelUser
	sessions map[*tunSession]bool
	boot     string
	traffic  map[string]*[2]atomic.Int64
	entries  map[string]*atomic.Int64
}

type tunSession struct {
	user, pass, ip string
	conn           ssh.Conn
}

func cmdTunnels(args []string) error {
	if len(args) != 2 || args[0] != "run" {
		return fmt.Errorf("usage: xtune tunnels run CONF (started by the xray-autotune-tunnels service)")
	}
	return runTunnels(args[1], nil)
}

// runTunnels serves conf until a listener fails or the entries in the file change (the service
// manager starts it again with the new ones). ready, if set, is closed once it listens.
func runTunnels(path string, ready chan<- []net.Addr) error {
	ts := &tunnelServer{path: path, users: map[string]TunnelUser{}, sessions: map[*tunSession]bool{},
		boot: randHex(8), traffic: map[string]*[2]atomic.Int64{}, entries: map[string]*atomic.Int64{}}
	if err := readJSONFile(path, &ts.conf); err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey([]byte(ts.conf.HostKey))
	if err != nil {
		return fmt.Errorf("host key: %w", err)
	}
	ts.sshConf = &ssh.ServerConfig{PasswordCallback: ts.auth, MaxAuthTries: 3,
		ServerVersion: "SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.5"}
	ts.sshConf.AddHostKey(signer)
	ts.setUsers(ts.conf.Users)

	errc := make(chan error, len(ts.conf.Entries)+1)
	var addrs []net.Addr
	for _, e := range ts.conf.Entries {
		ts.entries[e.ID] = &atomic.Int64{}
		a, err := ts.listen(e, errc)
		if err != nil {
			return fmt.Errorf("%s: %w", e.ID, err)
		}
		addrs = append(addrs, a)
	}
	if ready != nil {
		ready <- addrs
	}
	go ts.watch(errc)
	tick := time.NewTicker(tunnelStatsEvery)
	defer tick.Stop()
	for {
		select {
		case err := <-errc:
			ts.writeStats()
			return err
		case <-tick.C:
			ts.writeStats()
		}
	}
}

func (ts *tunnelServer) listen(e Entry, errc chan<- error) (net.Addr, error) {
	switch e.Kind {
	case "dnstt":
		priv, err := hex.DecodeString(ts.conf.DNSTTKey)
		if err != nil || len(priv) != 32 {
			return nil, fmt.Errorf("bad DNS tunnel key")
		}
		domain, err := dns.ParseName(ts.conf.DNSDomain)
		if err != nil {
			return nil, fmt.Errorf("DNS tunnel domain %q: %w", ts.conf.DNSDomain, err)
		}
		pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", e.Port))
		if err != nil {
			return nil, err
		}
		go func() {
			errc <- dnsttserver.Serve(priv, domain, pc, func(c net.Conn) { ts.serve(c, e.ID, "") })
		}()
		return pc.LocalAddr(), nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", e.Port))
	if err != nil {
		return nil, err
	}
	if e.Kind != "ssh" { // ssh-tls, cdn-ssh: TLS with the deployment's certificate
		cert, err := tls.X509KeyPair([]byte(ts.conf.CertPEM), []byte(ts.conf.KeyPEM))
		if err != nil {
			ln.Close()
			return nil, err
		}
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				errc <- err
				return
			}
			ip, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			go ts.serve(c, e.ID, ip)
		}
	}()
	return ln.Addr(), nil
}

// watch reloads the users when the config file changes, and stops the server when the entries
// change.
func (ts *tunnelServer) watch(errc chan<- error) {
	var last time.Time
	if fi, err := os.Stat(ts.path); err == nil {
		last = fi.ModTime()
	}
	for range time.Tick(tunnelWatchEvery) {
		fi, err := os.Stat(ts.path)
		if err != nil || fi.ModTime().Equal(last) {
			continue
		}
		last = fi.ModTime()
		var c TunnelConf
		if readJSONFile(ts.path, &c) != nil {
			continue
		}
		if !reflect.DeepEqual(c.Entries, ts.conf.Entries) || c.HostKey != ts.conf.HostKey || c.DNSDomain != ts.conf.DNSDomain {
			errc <- errors.New("entries changed; restarting")
			return
		}
		ts.setUsers(c.Users)
	}
}

// setUsers replaces the users and disconnects sessions whose user is gone or changed.
func (ts *tunnelServer) setUsers(us []TunnelUser) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.users = map[string]TunnelUser{}
	for _, u := range us {
		ts.users[u.Name] = u
	}
	for s := range ts.sessions {
		if u, ok := ts.users[s.user]; !ok || u.Pass != s.pass {
			s.conn.Close()
		}
	}
}

func (ts *tunnelServer) auth(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	u, ok := ts.users[meta.User()]
	if !ok || subtle.ConstantTimeCompare([]byte(u.Pass), pass) != 1 {
		return nil, errors.New("denied")
	}
	ip := ""
	if a := meta.RemoteAddr(); a != nil { // DNS tunnel streams have none
		ip, _, _ = net.SplitHostPort(a.String())
	}
	if u.Devices > 0 && net.ParseIP(ip) != nil {
		ips := map[string]bool{}
		for s := range ts.sessions {
			if s.user == u.Name && s.ip != "" {
				ips[s.ip] = true
			}
		}
		if !ips[ip] && len(ips) >= u.Devices {
			return nil, errors.New("device limit")
		}
	}
	return &ssh.Permissions{Extensions: map[string]string{"user": u.Name, "pass": u.Pass}}, nil
}

// serve runs one client connection: an optional HTTP payload (HTTP Injector style, or a
// WebSocket upgrade through a CDN), then SSH.
func (ts *tunnelServer) serve(c net.Conn, entry, ip string) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	conn, err := answerPayload(c)
	if err != nil {
		return
	}
	sc, chans, reqs, err := ssh.NewServerConn(conn, ts.sshConf)
	if err != nil {
		return
	}
	c.SetDeadline(time.Time{})
	s := &tunSession{user: sc.Permissions.Extensions["user"], pass: sc.Permissions.Extensions["pass"], ip: ip, conn: sc}
	ts.mu.Lock()
	if u, ok := ts.users[s.user]; !ok || u.Pass != s.pass { // removed while logging in
		ts.mu.Unlock()
		sc.Close()
		return
	}
	ts.sessions[s] = true
	cnt := ts.traffic[s.user]
	if cnt == nil {
		cnt = &[2]atomic.Int64{}
		ts.traffic[s.user] = cnt
	}
	ecnt := ts.entries[entry]
	ts.mu.Unlock()
	defer func() {
		ts.mu.Lock()
		delete(ts.sessions, s)
		ts.mu.Unlock()
	}()
	go ssh.DiscardRequests(reqs) // keepalives are answered "no", which clients take as alive
	for nc := range chans {
		switch nc.ChannelType() {
		case "direct-tcpip":
			go forward(nc, cnt, ecnt)
		case "session": // some apps open a shell and expect it to stay quiet
			ch, rq, err := nc.Accept()
			if err != nil {
				continue
			}
			go func() {
				for r := range rq {
					ok := r.Type == "pty-req" || r.Type == "shell" || r.Type == "env" || r.Type == "window-change"
					r.Reply(ok, nil)
				}
			}()
			go io.Copy(io.Discard, ch)
		default:
			nc.Reject(ssh.UnknownChannelType, "not allowed")
		}
	}
}

// answerPayload reads HTTP requests the client sends before SSH (as HTTP Injector payloads and
// WebSocket clients do) and answers each, then hands over the connection for SSH.
func answerPayload(c net.Conn) (net.Conn, error) {
	br := bufio.NewReaderSize(c, 8<<10)
	for i := 0; i < 4; i++ {
		head, err := br.Peek(4)
		if err != nil {
			return nil, err
		}
		if string(head) == "SSH-" {
			break
		}
		req, err := http.ReadRequest(br)
		if err != nil {
			return nil, err
		}
		if req.ContentLength > 0 && req.ContentLength < 1<<20 {
			io.CopyN(io.Discard, req.Body, req.ContentLength)
		}
		resp := "HTTP/1.1 200 Connection established\r\n\r\n"
		if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
			resp = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
			if k := req.Header.Get("Sec-WebSocket-Key"); k != "" {
				resp += "Sec-WebSocket-Accept: " + wsAccept(k) + "\r\n"
			}
			resp += "\r\n"
		}
		if _, err := io.WriteString(c, resp); err != nil {
			return nil, err
		}
	}
	return &bufConn{Conn: c, r: br}, nil
}

func wsAccept(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

type directTCPIP struct {
	Host     string
	Port     uint32
	OrigHost string
	OrigPort uint32
}

// forward connects a direct-tcpip channel to its destination, which must be public.
func forward(nc ssh.NewChannel, cnt *[2]atomic.Int64, ecnt *atomic.Int64) {
	var d directTCPIP
	if err := ssh.Unmarshal(nc.ExtraData(), &d); err != nil || d.Port == 0 || d.Port > 65535 {
		nc.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	if d.Port == 25 { // no mail from the server's IP
		nc.Reject(ssh.Prohibited, "port 25 is blocked")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, d.Host)
	if err != nil || len(ips) == 0 {
		nc.Reject(ssh.ConnectionFailed, "cannot resolve")
		return
	}
	for _, ip := range ips {
		if !allowDest(ip.IP) {
			nc.Reject(ssh.Prohibited, "private address")
			return
		}
	}
	out, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ips[0].IP.String(), strconv.Itoa(int(d.Port))))
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, "connect failed")
		return
	}
	defer out.Close()
	ch, rq, err := nc.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(rq)
	done := make(chan struct{})
	go func() {
		n, _ := io.Copy(out, ch)
		cnt[0].Add(n)
		ecnt.Add(n)
		if tc, ok := out.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		close(done)
	}()
	n, _ := io.Copy(ch, out)
	cnt[1].Add(n)
	ecnt.Add(n)
	ch.CloseWrite()
	<-done
}

// publicIP is false for the addresses Xray's configs block too (geoip:private).
func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64)) // CGNAT
}

func (ts *tunnelServer) writeStats() {
	ts.mu.Lock()
	s := tunnelStats{Boot: ts.boot, Users: map[string][2]int64{}, Entries: map[string]int64{}, Online: map[string][]string{},
		Written: time.Now().Unix()}
	for name, c := range ts.traffic {
		s.Users[name] = [2]int64{c[0].Load(), c[1].Load()}
	}
	for id, c := range ts.entries {
		s.Entries[id] = c.Load()
	}
	for x := range ts.sessions {
		if x.ip != "" && !contains2s(s.Online[x.user], x.ip) {
			s.Online[x.user] = append(s.Online[x.user], x.ip)
		}
	}
	ts.mu.Unlock()
	if ts.conf.Stats == "" {
		return
	}
	b, _ := json.Marshal(s)
	tmp := ts.conf.Stats + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, ts.conf.Stats)
	}
}

func contains2s(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- client (tuning and checks)

// tunnelDial returns a dialer that reaches addr through a fresh SSH session for every
// connection, like a client app connecting anew, so reliability means what it means for the
// Xray configs.
func (st *State) tunnelDial(e Entry, u User, k Knobs) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, closeExtra, err := st.tunnelRaw(ctx, e, k)
		if err != nil {
			return nil, err
		}
		fail := func(err error) (net.Conn, error) {
			raw.Close()
			closeExtra()
			return nil, err
		}
		if dl, ok := ctx.Deadline(); ok {
			raw.SetDeadline(dl)
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(st.SSHHostPub))
		if err != nil {
			return fail(fmt.Errorf("server host key: %w", err))
		}
		sc, chans, reqs, err := ssh.NewClientConn(raw, addr, &ssh.ClientConfig{User: u.Name,
			Auth: []ssh.AuthMethod{ssh.Password(tunnelPassword(u))}, HostKeyCallback: ssh.FixedHostKey(pub)})
		if err != nil {
			return fail(err)
		}
		cl := ssh.NewClient(sc, chans, reqs)
		c, err := cl.DialContext(ctx, network, addr)
		if err != nil {
			cl.Close()
			return fail(err)
		}
		raw.SetDeadline(time.Time{})
		return &tunnelConn{Conn: c, close: func() { cl.Close(); raw.Close(); closeExtra() }}, nil
	}
}

type tunnelConn struct {
	net.Conn
	close func()
	once  sync.Once
}

func (t *tunnelConn) Close() error {
	err := t.Conn.Close()
	t.once.Do(t.close)
	return err
}

// tunnelRaw opens the transport SSH runs over, payload included. closeExtra releases a DNS
// tunnel session.
func (st *State) tunnelRaw(ctx context.Context, e Entry, k Knobs) (net.Conn, func(), error) {
	nop := func() {}
	if e.Kind == "dnstt" {
		pub, err := hex.DecodeString(st.DNSTTPub)
		if err != nil {
			return nil, nop, err
		}
		timeout := 30 * time.Second
		if dl, ok := ctx.Deadline(); ok {
			timeout = time.Until(dl)
		}
		s, err := dnsttclient.Dial(orStr(k.Resolver, dnsResolvers[0]), pub, st.DNSDomain, timeout)
		if err != nil {
			return nil, nop, err
		}
		c, err := s.Open()
		if err != nil {
			s.Close()
			return nil, nop, err
		}
		return c, func() { s.Close() }, nil
	}
	host := st.Addr()
	if e.CDN() {
		host = st.CDNDomain
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", hostPort(host, e.Port))
	if err != nil {
		return nil, nop, err
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	switch e.Kind {
	case "ssh-tls": // self-signed: pin it
		tc := tls.Client(c, &tls.Config{ServerName: orStr(e.SNI, st.CertSNI), InsecureSkipVerify: true,
			VerifyPeerCertificate: pinCert(st.CertHash)})
		if err := tc.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, nop, err
		}
		c = tc
	case "cdn-ssh": // the CDN's own certificate
		tc := tls.Client(c, &tls.Config{ServerName: st.CDNDomain})
		if err := tc.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, nop, err
		}
		c = tc
	}
	if k.Mode == "ws" || e.CDN() {
		wsHost := orStr(e.SNI, "www.bing.com")
		if e.CDN() {
			wsHost = st.CDNDomain
		}
		if c, err = wsUpgrade(c, wsHost); err != nil {
			return nil, nop, err
		}
	}
	return c, nop, nil
}

func pinCert(hash string) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("no certificate")
		}
		sum := sha256.Sum256(raw[0])
		if hex.EncodeToString(sum[:]) != hash {
			return errors.New("certificate does not match the deployment's")
		}
		return nil
	}
}

// wsUpgrade sends a WebSocket upgrade request and waits for the answer; SSH follows unframed,
// as HTTP Injector style clients do.
func wsUpgrade(c net.Conn, host string) (net.Conn, error) {
	key := base64.StdEncoding.EncodeToString([]byte(randHex(8)))
	req := "GET / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\nUser-Agent: Mozilla/5.0\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	if resp.StatusCode != 101 && resp.StatusCode != 200 {
		c.Close()
		return nil, fmt.Errorf("upgrade refused: HTTP %d", resp.StatusCode)
	}
	return &bufConn{Conn: c, r: br}, nil
}

// dnsResolvers are the resolvers DNS tunnel variants are tried through: plain UDP to public
// resolvers (often the only thing a censor lets out), and DNS over HTTPS and TLS, which look
// like web traffic.
var dnsResolvers = []string{"udp:8.8.8.8:53", "udp:1.1.1.1:53", "udp:9.9.9.9:53",
	"doh:https://cloudflare-dns.com/dns-query", "doh:https://dns.google/dns-query", "dot:dns.quad9.net:853"}

// localResolvers adds the resolvers this machine is configured with: on many censored networks
// the ISP's own resolver is the one that still forwards everything.
func localResolvers() []string {
	var out []string
	for _, h := range resolvConfServers("/etc/resolv.conf") {
		if ip := net.ParseIP(h); ip != nil && !ip.IsLoopback() {
			out = append(out, "udp:"+net.JoinHostPort(h, "53"))
		}
	}
	if p := os.Getenv("PREFIX"); p != "" {
		for _, h := range resolvConfServers(filepath.Join(p, "etc/resolv.conf")) {
			if ip := net.ParseIP(h); ip != nil && !ip.IsLoopback() && !contains2s(out, "udp:"+net.JoinHostPort(h, "53")) {
				out = append(out, "udp:"+net.JoinHostPort(h, "53"))
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- accounting (enforce job)

// collectExtra adds the SSH tunnels' and VPNs' traffic since the last run to the users and to
// byTag, and returns the client IPs online on them and the VPN sessions. Their counters only
// grow while a server or session lives, so the last values seen are kept in us.Extra.
func collectExtra(r *Server, st *State, us *Usage, now time.Time, byTag map[string]int64) (map[string][]string, []vpnSession) {
	online := map[string][]string{}
	seen := map[string][2]int64{}
	add := func(key, user, entry string, up, down int64) {
		prev := us.Extra[key]
		d := [2]int64{up - prev[0], down - prev[1]}
		if d[0] < 0 || d[1] < 0 {
			d = [2]int64{up, down}
		}
		seen[key] = [2]int64{up, down}
		if user != "" && d != [2]int64{} {
			uu := us.of(user)
			uu.Up += d[0]
			uu.Down += d[1]
			uu.addDaily(now, d[0]+d[1])
		}
		if entry != "" {
			byTag[entry] += d[0] + d[1]
		}
	}
	if hasEngine(st.Entries, "tunnel") {
		var ts tunnelStats
		if out, err := r.Root("cat " + tunnelStatsFile + " 2>/dev/null || true"); err == nil && json.Unmarshal([]byte(out), &ts) == nil {
			for name, c := range ts.Users {
				add("tun/"+ts.Boot+"/u/"+name, name, "", c[0], c[1])
			}
			for id, n := range ts.Entries {
				add("tun/"+ts.Boot+"/e/"+id, "", id, n, 0)
			}
			if now.Unix()-ts.Written < 60 { // a stopped server's list is stale
				for name, ips := range ts.Online {
					online[name] = append(online[name], ips...)
				}
			}
		}
	}
	var sessions []vpnSession
	if hasEngine(st.Entries, "openvpn") || hasEngine(st.Entries, "ikev2") {
		out, _ := r.Helper("vpn-sessions")
		sessions = parseVPNSessions(out)
		for _, s := range sessions {
			add(s.Key, s.User, s.Entry(), s.Up, s.Down)
			if s.IP != "" && !contains2s(online[s.User], s.IP) {
				online[s.User] = append(online[s.User], s.IP)
			}
		}
	}
	us.Extra = seen
	return online, sessions
}

// parseVPNSessions reads the helper's vpn-sessions output: "### ovpn ID" and "### ike" sections.
func parseVPNSessions(out string) []vpnSession {
	var all []vpnSession
	for _, sec := range strings.Split("\n"+out, "\n### ")[1:] {
		head, body, _ := strings.Cut(sec, "\n")
		f := strings.Fields(head)
		switch {
		case len(f) == 2 && f[0] == "ovpn":
			all = append(all, parseOpenVPNStatus(f[1], body)...)
		case len(f) == 1 && f[0] == "ike":
			all = append(all, ikeSessions(body)...)
		}
	}
	return all
}

// Entry is the ID of the entry the session is on.
func (s vpnSession) Entry() string {
	p := strings.Split(s.Key, "/")
	if p[0] == "ovpn" && len(p) > 1 {
		return p[1]
	}
	return "ikev2"
}

// kickInactive disconnects VPN sessions of users who may no longer connect (the SSH tunnel server
// does this itself when its user list changes).
func kickInactive(r *Server, active []User, sessions []vpnSession) error {
	ok := map[string]bool{}
	for _, u := range active {
		ok[u.Name] = true
	}
	var kick []string
	for _, s := range sessions {
		if ok[s.User] {
			continue
		}
		k := "ovpn:" + s.Entry() + ":" + s.User
		if p := strings.Split(s.Key, "/"); p[0] == "ike" {
			k = "ike:" + p[1]
		}
		if !contains2s(kick, k) {
			kick = append(kick, k)
		}
	}
	if len(kick) == 0 {
		return nil
	}
	_, err := r.Helper(append([]string{"vpn-kick"}, kick...)...)
	return err
}

// cmdVPNMgmt sends one command to an OpenVPN management socket (used by the helper's vpn-kick).
func cmdVPNMgmt(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: xtune vpn-mgmt SOCKET COMMAND")
	}
	c, err := net.DialTimeout("unix", args[0], 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	if _, err := br.ReadString('\n'); err != nil { // the banner: commands sent before it are lost
		return err
	}
	if _, err := io.WriteString(c, args[1]+"\n"); err != nil {
		return err
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(line, "SUCCESS:"):
			fmt.Print(line)
			io.WriteString(c, "quit\n")
			return nil
		case strings.HasPrefix(line, "ERROR:"):
			io.WriteString(c, "quit\n")
			return errors.New(strings.TrimSpace(line))
		}
	}
}
