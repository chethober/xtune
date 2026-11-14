package core

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// Dialer is what the non-Xray engines (SSH, the DNS tunnel, AmneziaWG) offer: TCP connections
// through the tunnel.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// socksServer puts a Dialer behind a local SOCKS5 port (TCP CONNECT only), so Xray reaches it as
// an ordinary outbound and its router, balancer and health checks treat every kind alike.
type socksServer struct {
	ln   net.Listener
	d    Dialer
	wg   sync.WaitGroup
	mu   sync.Mutex
	open map[net.Conn]bool
}

func startSocks(d Dialer) (*socksServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &socksServer{ln: ln, d: d, open: map[net.Conn]bool{}}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

func (s *socksServer) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *socksServer) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.track(c, true)
		go func() {
			defer s.track(c, false)
			defer c.Close()
			s.handle(c)
		}()
	}
}

func (s *socksServer) track(c net.Conn, add bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if add {
		s.open[c] = true
	} else {
		delete(s.open, c)
	}
}

func (s *socksServer) Close() {
	s.ln.Close()
	s.mu.Lock()
	for c := range s.open {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

var errSocks = errors.New("socks: bad request")

func (s *socksServer) handle(c net.Conn) {
	c.SetDeadline(time.Now().Add(30 * time.Second))
	addr, err := socksHandshake(c)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	up, err := s.d.DialContext(ctx, "tcp", addr)
	cancel()
	if err != nil {
		c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0}) // host unreachable
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	c.SetDeadline(time.Time{})
	relay(c, up)
}

// socksHandshake reads a no-auth SOCKS5 CONNECT and returns host:port.
func socksHandshake(c net.Conn) (string, error) {
	var h [2]byte
	if _, err := io.ReadFull(c, h[:]); err != nil || h[0] != 5 {
		return "", errSocks
	}
	methods := make([]byte, h[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", errSocks
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 {
		return "", errSocks
	}
	if req[1] != 1 { // only CONNECT
		c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", errSocks
	}
	var host string
	switch req[3] {
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	case 4:
		var ip [16]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return "", err
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		return "", errSocks
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p[:])))), nil
}

// relay copies both ways until either side is done.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
