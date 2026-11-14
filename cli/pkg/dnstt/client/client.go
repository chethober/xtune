// Package client is the client end of a dnstt DNS tunnel, adapted from dnstt-client
// (https://www.bamsoftware.com/software/dnstt/, public domain, see ../COPYING) to run inside
// xtune: Dial returns a session whose streams are net.Conns.
package client

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
	"www.bamsoftware.com/git/dnstt.git/dns"
	"www.bamsoftware.com/git/dnstt.git/noise"
	"www.bamsoftware.com/git/dnstt.git/turbotunnel"
)

const idleTimeout = 2 * time.Minute

// Logf receives the tunnel's diagnostics; they are dropped unless it is replaced.
var Logf = func(format string, args ...any) {}

func dnsNameCapacity(domain dns.Name) int {
	// Names must be 255 octets or shorter in total length.
	// https://tools.ietf.org/html/rfc1035#section-2.3.4
	capacity := 255
	// Subtract the length of the null terminator.
	capacity -= 1
	for _, label := range domain {
		// Subtract the length of the label and the length octet.
		capacity -= len(label) + 1
	}
	// Each label may be up to 63 bytes long and requires 64 bytes to
	// encode.
	capacity = capacity * 63 / 64
	// Base32 expands every 5 bytes to 8.
	capacity = capacity * 5 / 8
	return capacity
}

// Session is one tunnel through a resolver. Each stream is a separate TCP-like connection
// to whatever the server hands streams to.
type Session struct {
	pconn net.PacketConn
	conn  *kcp.UDPSession
	sess  *smux.Session
}

// Open starts a new stream.
func (s *Session) Open() (net.Conn, error) { return s.sess.OpenStream() }

func (s *Session) Close() error {
	s.sess.Close()
	s.conn.Close()
	return s.pconn.Close()
}

// Dial opens a tunnel for domain through resolver, which is one of
// "udp:HOST:PORT", "doh:https://URL" or "dot:HOST:PORT". pubkey is the server's Noise public
// key. Dial returns once the encrypted handshake with the server is done, or fails after timeout.
func Dial(resolver string, pubkey []byte, domain string, timeout time.Duration) (*Session, error) {
	name, err := dns.ParseName(domain)
	if err != nil {
		return nil, fmt.Errorf("invalid domain %q: %v", domain, err)
	}
	mtu := dnsNameCapacity(name) - 8 - 1 - numPadding - 1 // clientid + padding length prefix + padding + data length prefix
	if mtu < 80 {
		return nil, fmt.Errorf("domain %s leaves only %d bytes for payload", domain, mtu)
	}
	kind, addr, ok := strings.Cut(resolver, ":")
	if !ok {
		return nil, fmt.Errorf("resolver %q: want udp:, doh: or dot:", resolver)
	}
	var remote net.Addr = turbotunnel.DummyAddr{}
	var transport net.PacketConn
	switch kind {
	case "udp":
		ua, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		if transport, err = net.ListenUDP("udp", nil); err != nil {
			return nil, err
		}
		remote = ua
	case "doh":
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		if transport, err = NewHTTPPacketConn(tr, addr, 32); err != nil {
			return nil, err
		}
	case "dot":
		d := &tls.Dialer{}
		if transport, err = NewTLSPacketConn(addr, d.DialContext); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("resolver %q: want udp:, doh: or dot:", resolver)
	}
	pconn := NewDNSPacketConn(transport, remote, name)

	type result struct {
		s   *Session
		err error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := kcp.NewConn2(remote, nil, 0, 0, pconn)
		if err != nil {
			ch <- result{err: fmt.Errorf("opening KCP conn: %v", err)}
			return
		}
		// Permit coalescing the payloads of consecutive sends.
		conn.SetStreamMode(true)
		// Disable the dynamic congestion window (limit only by the maximum of
		// local and remote static windows).
		conn.SetNoDelay(0, 0, 0, 1)
		conn.SetWindowSize(turbotunnel.QueueSize/2, turbotunnel.QueueSize/2)
		if !conn.SetMtu(mtu) {
			conn.Close()
			ch <- result{err: fmt.Errorf("cannot set MTU %d", mtu)}
			return
		}
		// Put a Noise channel on top of the KCP conn.
		rw, err := noise.NewClient(conn, pubkey)
		if err != nil {
			conn.Close()
			ch <- result{err: err}
			return
		}
		smuxConfig := smux.DefaultConfig()
		smuxConfig.Version = 2
		smuxConfig.KeepAliveTimeout = idleTimeout
		smuxConfig.MaxStreamBuffer = 1 * 1024 * 1024 // default is 65536
		sess, err := smux.Client(rw, smuxConfig)
		if err != nil {
			conn.Close()
			ch <- result{err: fmt.Errorf("opening smux session: %v", err)}
			return
		}
		ch <- result{s: &Session{pconn: pconn, conn: conn, sess: sess}}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			pconn.Close()
		}
		return r.s, r.err
	case <-time.After(timeout):
		pconn.Close() // unblocks the handshake
		go func() {
			if r := <-ch; r.s != nil {
				r.s.Close()
			}
		}()
		return nil, fmt.Errorf("no answer through %s within %v", resolver, timeout)
	}
}
