// Package tunnel connects as a client through xtune's non-Xray configs: SSH (plain, over TLS,
// over a CDN's WebSocket, or over the DNS tunnel) and AmneziaWG. It takes only what one user
// needs for one config, so the same code serves tuning on the operator's computer and the client
// app, which gets these parameters from its subscription.
package tunnel

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	dnsttclient "github.com/chethober/xtune/pkg/dnstt/client"
)

// DialFunc opens the underlying connections, so a client can bind them to its physical network
// interface while a VPN routes everything else.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// SSH is one user's SSH config: where to connect, how the transport is wrapped, and the login.
type SSH struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// TLS is "" (none), "pinned" (the server's self-signed certificate, checked against
	// CertHash) or "public" (a CDN's certificate, verified normally).
	TLS      string `json:"tls,omitempty"`
	SNI      string `json:"sni,omitempty"`
	CertHash string `json:"certHash,omitempty"`
	// WSHost, when set, sends a WebSocket upgrade with this Host first; SSH follows unframed.
	WSHost   string `json:"wsHost,omitempty"`
	User     string `json:"user"`
	Password string `json:"password"`
	HostKey  string `json:"hostKey"` // authorized_keys format; pinned
	DNSTT    *DNSTT `json:"dnstt,omitempty"`
}

// DNSTT carries SSH through a DNS tunnel instead of a TCP connection.
type DNSTT struct {
	Resolver string `json:"resolver"` // udp:IP:53, doh:URL or dot:HOST:853
	Domain   string `json:"domain"`
	PubKey   string `json:"pubKey"` // hex
}

// Raw opens the transport SSH runs over, wrapping included. closeExtra releases a DNS tunnel
// session.
func (c SSH) Raw(ctx context.Context, dial DialFunc) (conn net.Conn, closeExtra func(), err error) {
	nop := func() {}
	if c.DNSTT != nil {
		pub, err := hex.DecodeString(c.DNSTT.PubKey)
		if err != nil {
			return nil, nop, err
		}
		timeout := 30 * time.Second
		if dl, ok := ctx.Deadline(); ok {
			timeout = time.Until(dl)
		}
		s, err := dnsttclient.Dial(c.DNSTT.Resolver, pub, c.DNSTT.Domain, timeout)
		if err != nil {
			return nil, nop, err
		}
		sc, err := s.Open()
		if err != nil {
			s.Close()
			return nil, nop, err
		}
		return sc, func() { s.Close() }, nil
	}
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	nc, err := dial(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return nil, nop, err
	}
	if dl, ok := ctx.Deadline(); ok {
		nc.SetDeadline(dl)
	}
	switch c.TLS {
	case "pinned":
		tc := tls.Client(nc, &tls.Config{ServerName: c.SNI, InsecureSkipVerify: true, VerifyPeerCertificate: PinCert(c.CertHash)})
		if err := tc.HandshakeContext(ctx); err != nil {
			nc.Close()
			return nil, nop, err
		}
		nc = tc
	case "public":
		tc := tls.Client(nc, &tls.Config{ServerName: c.SNI})
		if err := tc.HandshakeContext(ctx); err != nil {
			nc.Close()
			return nil, nop, err
		}
		nc = tc
	}
	if c.WSHost != "" {
		if nc, err = WSUpgrade(nc, c.WSHost); err != nil {
			return nil, nop, err
		}
	}
	return nc, nop, nil
}

// Client logs in over a fresh transport. close releases the client and the transport.
func (c SSH) Client(ctx context.Context, dial DialFunc) (*ssh.Client, func(), error) {
	raw, closeExtra, err := c.Raw(ctx, dial)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*ssh.Client, func(), error) {
		raw.Close()
		closeExtra()
		return nil, nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		raw.SetDeadline(dl)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.HostKey))
	if err != nil {
		return fail(fmt.Errorf("server host key: %w", err))
	}
	sc, chans, reqs, err := ssh.NewClientConn(raw, net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), &ssh.ClientConfig{User: c.User,
		Auth: []ssh.AuthMethod{ssh.Password(c.Password)}, HostKeyCallback: ssh.FixedHostKey(pub), Timeout: 30 * time.Second})
	if err != nil {
		return fail(err)
	}
	raw.SetDeadline(time.Time{})
	cl := ssh.NewClient(sc, chans, reqs)
	return cl, func() { cl.Close(); raw.Close(); closeExtra() }, nil
}

// DialFresh reaches addr through a new SSH session for every connection, like an app connecting
// anew: what tuning measures.
func (c SSH) DialFresh(dial DialFunc) DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		cl, closeAll, err := c.Client(ctx, dial)
		if err != nil {
			return nil, err
		}
		conn, err := cl.DialContext(ctx, network, addr)
		if err != nil {
			closeAll()
			return nil, err
		}
		return &closeConn{Conn: conn, close: closeAll}, nil
	}
}

// Session keeps one SSH session and opens every connection as a channel on it, reconnecting when
// it drops: what a client app uses.
type Session struct {
	c    SSH
	dial DialFunc
	mu   sync.Mutex
	cl   *ssh.Client
	done func()
}

func (c SSH) Session(dial DialFunc) *Session { return &Session{c: c, dial: dial} }

func (s *Session) client(ctx context.Context) (*ssh.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cl != nil {
		return s.cl, nil
	}
	cl, done, err := s.c.Client(ctx, s.dial)
	if err != nil {
		return nil, err
	}
	s.cl, s.done = cl, done
	go func() { // forget it when it dies, so the next dial reconnects
		cl.Wait()
		s.mu.Lock()
		if s.cl == cl {
			s.cl = nil
			s.done()
		}
		s.mu.Unlock()
	}()
	go func() { // keepalive, which also notices a dead transport
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			if _, _, err := cl.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				cl.Close()
				return
			}
		}
	}()
	return cl, nil
}

func (s *Session) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	cl, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := cl.DialContext(ctx, network, addr)
	if err != nil {
		cl.Close() // probably dead: the next dial reconnects
		return nil, err
	}
	return conn, nil
}

func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cl != nil {
		s.cl.Close()
		s.done()
		s.cl = nil
	}
}

type closeConn struct {
	net.Conn
	close func()
	once  sync.Once
}

func (t *closeConn) Close() error {
	err := t.Conn.Close()
	t.once.Do(t.close)
	return err
}

// PinCert accepts only the certificate whose SHA-256 is hash (hex).
func PinCert(hash string) func([][]byte, [][]*x509.Certificate) error {
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

// WSUpgrade sends a WebSocket upgrade request and waits for the answer; SSH follows unframed,
// as HTTP Injector style clients do.
func WSUpgrade(c net.Conn, host string) (net.Conn, error) {
	var k [16]byte
	rand.Read(k[:])
	req := "GET / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(k[:]) + "\r\nSec-WebSocket-Version: 13\r\nUser-Agent: Mozilla/5.0\r\n\r\n"
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

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }
