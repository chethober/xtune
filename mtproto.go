package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync/atomic"
	"time"
)

// The MTProto proxy serves Telegram's own proxy protocol, for users who only need Telegram.
// Telegram apps connect to it natively (Settings > Data and Storage > Proxy, or a t.me/proxy
// link), so nothing else needs installing. xtune serves it inside the tunnel server
// (`xtune tunnels run`), next to the SSH and DNS tunnels.
//
// This is the plain secret proxy (the "obfuscated2" transport): a client opens with a random
// 64-byte frame from which both sides derive AES-256-CTR keys, mixing in the proxy secret; the
// frame carries which Telegram data centre the client wants. The proxy de-obfuscates the stream,
// dials that data centre, and relays the stream to it re-obfuscated with no secret, as a Telegram
// client would. Every user has a 16-byte secret of their own, derived from their UUID like their
// other credentials, so quotas, expiry, device limits and disabling all apply.

// mtprotoDCs are Telegram's production data centres (as published in tdesktop).
var mtprotoDCs = map[int]string{
	1: "149.154.175.50:443", 2: "149.154.167.51:443", 3: "149.154.175.100:443",
	4: "149.154.167.91:443", 5: "149.154.171.5:443", 203: "91.105.192.100:443",
}

// mtprotoDialDC dials a Telegram data centre; a variable so tests point it at a local stand-in.
var mtprotoDialDC = func(ctx context.Context, dc int) (net.Conn, error) {
	addr, ok := mtprotoDCs[dc]
	if !ok {
		addr = mtprotoDCs[2]
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
}

const (
	mtprotoFrameLen = 64
	mtprotoKeyLen   = 16 // the proxy secret
)

// mtprotoConnType marks the padded-intermediate transport both sides of the proxy use, so the
// de-obfuscated byte stream is identical on each and can be copied straight through.
var mtprotoConnType = [4]byte{0xdd, 0xdd, 0xdd, 0xdd}

func mtprotoKey(u User) []byte { return secret(u, "mtproto")[:mtprotoKeyLen] }

// mtprotoSecret is the secret Telegram apps take: the "dd" prefix (padded-intermediate transport,
// no fake TLS) plus the user's 16-byte key in hex.
func mtprotoSecret(u User) string { return "dd" + hex.EncodeToString(mtprotoKey(u)) }

// mtprotoLink opens the proxy in Telegram and doubles as the page's QR code.
func (st *State) mtprotoLink(e Entry, u User) string {
	q := url.Values{"server": {st.Addr()}, "port": {fmt.Sprint(e.Port)}, "secret": {mtprotoSecret(u)}}
	return "https://t.me/proxy?" + q.Encode()
}

// MTProtoInfo is what to type into Telegram's proxy settings by hand.
func (st *State) MTProtoInfo(e Entry, u User) string {
	return fmt.Sprintf("%s for %s\n\nTelegram > Settings > Data and Storage > Proxy > Add proxy > MTProto\n\n"+
		"Server: %s\nPort: %d\nSecret: %s\n\nOr open: %s\n",
		e.ID, u.Name, st.Addr(), e.Port, mtprotoSecret(u), st.mtprotoLink(e, u))
}

// ---------------------------------------------------------------- obfuscation

// obfConn carries the de-obfuscated MTProto stream over a raw connection: reads are decrypted,
// writes encrypted, with the two AES-CTR streams the handshake set up.
type obfConn struct {
	net.Conn
	recv, send cipher.Stream
}

func (c *obfConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.recv.XORKeyStream(p[:n], p[:n])
	}
	return n, err
}

func (c *obfConn) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	c.send.XORKeyStream(b, p)
	return c.Conn.Write(b)
}

// ctrStream builds the AES-256-CTR stream for a 64-byte frame: the 32-byte key at [8:40] is
// mixed with the proxy secret (none toward Telegram), the 16-byte IV is at [40:56].
func ctrStream(frame []byte, secret []byte) cipher.Stream {
	key := frame[8:40]
	if secret != nil {
		h := sha256.Sum256(append(append([]byte{}, key...), secret...))
		key = h[:]
	}
	block, _ := aes.NewCipher(key)
	return cipher.NewCTR(block, frame[40:56])
}

// reversed returns the frame with bytes [8:56] reversed, which is how the peer in the other
// direction derives its key and IV from the same handshake.
func reversed(frame []byte) []byte {
	out := append([]byte{}, frame...)
	for i, j := 8, 55; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// mtprotoReadHandshake reads a client's 64-byte frame and, if it authenticates against key,
// returns the data centre it asked for and a de-obfuscated connection. The 64 raw bytes are
// passed in already read, so the caller can try several users' keys against one frame.
func mtprotoReadHandshake(c net.Conn, raw, key []byte) (dc int, _ *obfConn, ok bool) {
	if len(raw) != mtprotoFrameLen {
		return 0, nil, false
	}
	recv := ctrStream(raw, key)
	dec := make([]byte, mtprotoFrameLen)
	recv.XORKeyStream(dec, raw) // advances recv past the frame, where the stream continues
	if subtle.ConstantTimeCompare(dec[56:60], mtprotoConnType[:]) != 1 {
		return 0, nil, false
	}
	send := ctrStream(reversed(raw), key)
	return int(int16(binary.LittleEndian.Uint16(dec[60:62]))), &obfConn{Conn: c, recv: recv, send: send}, true
}

// mtprotoSendHandshake dials data centre dc as a Telegram client would: a random valid frame,
// sent with its key and IV left in the clear so the data centre can read them.
func mtprotoSendHandshake(ctx context.Context, dc int) (*obfConn, error) {
	c, err := mtprotoDialDC(ctx, dc)
	if err != nil {
		return nil, err
	}
	frame := mtprotoFrame(dc)
	send := ctrStream(frame, nil)
	recv := ctrStream(reversed(frame), nil)
	enc := make([]byte, mtprotoFrameLen)
	send.XORKeyStream(enc, frame)
	copy(enc[8:56], frame[8:56]) // the data centre reads key and IV from the clear bytes
	if _, err := c.Write(enc); err != nil {
		c.Close()
		return nil, err
	}
	return &obfConn{Conn: c, recv: recv, send: send}, nil
}

// mtprotoFrame builds a random 64-byte handshake for data centre dc, avoiding the byte patterns
// Telegram reserves for other transports.
func mtprotoFrame(dc int) []byte {
	f := make([]byte, mtprotoFrameLen)
	for {
		rand.Read(f)
		if f[0] == 0xef {
			continue
		}
		switch binary.LittleEndian.Uint32(f[:4]) {
		case 0x44414548, 0x54534f50, 0x20544547, 0x4954504f, 0x02010316, 0xdddddddd, 0xeeeeeeee:
			continue
		}
		if f[4] == 0 && f[5] == 0 && f[6] == 0 && f[7] == 0 {
			continue
		}
		break
	}
	copy(f[56:60], mtprotoConnType[:])
	binary.LittleEndian.PutUint16(f[60:62], uint16(int16(dc)))
	return f
}

// ---------------------------------------------------------------- client (tuning and checks)

// mtprotoHandshake is the tuning probe: it opens an obfuscated session as u and confirms the
// proxy accepts it and opens the onward path to a Telegram data centre. Like the VPNs, only the
// handshake is measured, not throughput: the proxy relays to Telegram alone, so there is no test
// target to download through.
func (st *State) mtprotoHandshake(ctx context.Context, e Entry, u User) (time.Duration, error) {
	t0 := time.Now()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", hostPort(st.Addr(), e.Port))
	if err != nil {
		return 0, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	frame := mtprotoFrame(2) // ask for DC 2, where most users land
	send := ctrStream(frame, mtprotoKey(u))
	enc := make([]byte, mtprotoFrameLen)
	send.XORKeyStream(enc, frame)
	copy(enc[8:56], frame[8:56])
	if _, err := c.Write(enc); err != nil {
		return 0, err
	}
	// The proxy keeps the connection open only once its onward dial to Telegram has succeeded; a
	// wrong secret or an unreachable data centre closes it. A short read settles which happened.
	recv := ctrStream(reversed(frame), mtprotoKey(u))
	buf := make([]byte, 64)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := c.Read(buf)
	if n > 0 {
		recv.XORKeyStream(buf[:n], buf[:n])
		return time.Since(t0), nil
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return time.Since(t0), nil // open and quiet: the path is up, Telegram just has nothing to say yet
	}
	return 0, errors.New("the proxy closed the connection (wrong secret, or Telegram unreachable)")
}

// ---------------------------------------------------------------- server relay

// mtSession is one live MTProto client, tracked so a device limit can count it and a removed or
// changed user can be disconnected.
type mtSession struct {
	user, pass, ip string
	conn           net.Conn
}

// serveMT handles one MTProto client: authenticate it against a user, dial the data centre it
// asked for, and relay the de-obfuscated stream both ways, counting traffic.
func (ts *tunnelServer) serveMT(c net.Conn, entry, ip string) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	raw := make([]byte, mtprotoFrameLen)
	if _, err := io.ReadFull(c, raw); err != nil {
		return
	}
	user, dc, client := ts.mtAuth(c, raw, ip)
	if client == nil {
		return
	}
	c.SetDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	tg, err := mtprotoSendHandshake(ctx, dc)
	cancel()
	if err != nil {
		return
	}
	defer tg.Conn.Close()

	s := &mtSession{user: user.Name, pass: user.Pass, ip: ip, conn: c}
	ts.mu.Lock()
	if u, ok := ts.users[user.Name]; !ok || u.Pass != user.Pass { // removed while connecting
		ts.mu.Unlock()
		return
	}
	ts.mt[s] = true
	cnt := ts.traffic[user.Name]
	if cnt == nil {
		cnt = &[2]atomic.Int64{}
		ts.traffic[user.Name] = cnt
	}
	ecnt := ts.entries[entry]
	ts.mu.Unlock()
	defer func() {
		ts.mu.Lock()
		delete(ts.mt, s)
		ts.mu.Unlock()
	}()

	// Both sides speak the same transport, so the de-obfuscated bytes copy straight through.
	done := make(chan struct{})
	go func() {
		n, _ := io.Copy(tg, client)
		cnt[0].Add(n)
		if ecnt != nil {
			ecnt.Add(n)
		}
		tg.Conn.Close()
		close(done)
	}()
	n, _ := io.Copy(client, tg)
	cnt[1].Add(n)
	if ecnt != nil {
		ecnt.Add(n)
	}
	c.Close()
	<-done
}

// mtAuth finds the user whose secret authenticates raw, enforcing the device limit.
func (ts *tunnelServer) mtAuth(c net.Conn, raw []byte, ip string) (TunnelUser, int, *obfConn) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, u := range ts.users {
		if u.MT == "" {
			continue
		}
		key, err := hex.DecodeString(u.MT)
		if err != nil || len(key) != mtprotoKeyLen {
			continue
		}
		dc, client, ok := mtprotoReadHandshake(c, raw, key)
		if !ok {
			continue
		}
		if u.Devices > 0 && net.ParseIP(ip) != nil {
			ips := map[string]bool{}
			for s := range ts.mt {
				if s.user == u.Name && s.ip != "" {
					ips[s.ip] = true
				}
			}
			if !ips[ip] && len(ips) >= u.Devices {
				return TunnelUser{}, 0, nil
			}
		}
		return u, dc, client
	}
	return TunnelUser{}, 0, nil
}
