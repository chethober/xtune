package main

import (
	"context"
	"encoding/hex"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mtprotoClientFrame builds the first 64 bytes a Telegram client sends for user key and DC: a
// random valid frame, encrypted with its key and IV left in the clear, plus the live send/recv
// streams so the test can speak the obfuscated stream afterwards.
func mtprotoClientFrame(key []byte, dc int) (enc []byte, send, recv interface {
	XORKeyStream(dst, src []byte)
}) {
	frame := mtprotoFrame(dc)
	s := ctrStream(frame, key)
	r := ctrStream(reversed(frame), key)
	enc = make([]byte, mtprotoFrameLen)
	s.XORKeyStream(enc, frame)
	copy(enc[8:56], frame[8:56])
	return enc, s, r
}

func TestMTProtoObfuscationRoundTrip(t *testing.T) {
	key := mtprotoKey(NewUser("ann"))
	enc, _, _ := mtprotoClientFrame(key, 3)

	// what the server reads: the same key recovers the DC and a de-obfuscating conn.
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	dc, conn, ok := mtprotoReadHandshake(srv, enc, key)
	if !ok {
		t.Fatal("handshake did not authenticate with the right key")
	}
	if dc != 3 {
		t.Errorf("dc = %d, want 3", dc)
	}
	if conn == nil {
		t.Fatal("nil conn on a good handshake")
	}

	// a wrong key does not authenticate.
	if _, _, ok := mtprotoReadHandshake(srv, enc, mtprotoKey(NewUser("bob"))); ok {
		t.Error("handshake authenticated with the wrong key")
	}
}

func TestMTProtoSecretAndLink(t *testing.T) {
	u := NewUser("ann")
	sec := mtprotoSecret(u)
	if len(sec) != 2+2*mtprotoKeyLen || sec[:2] != "dd" {
		t.Errorf("secret %q: want dd + %d hex chars", sec, 2*mtprotoKeyLen)
	}
	st := &State{Host: "203.0.113.10"}
	link := st.mtprotoLink(Entry{ID: "mtproto", Port: 443}, u)
	for _, want := range []string{"t.me/proxy", "203.0.113.10", "443", sec} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
}

// TestMTProtoRelay runs the proxy end to end against a stand-in Telegram data centre: a client
// authenticates, the proxy dials the "DC", and a byte sent each way arrives de-obfuscated.
func TestMTProtoRelay(t *testing.T) {
	// the stand-in DC echoes the client's first obfuscated frame's DC marker back as plaintext:
	// it reads the 64-byte handshake, then echoes whatever follows.
	dcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dcLn.Close()
	go func() {
		c, err := dcLn.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		hdr := make([]byte, mtprotoFrameLen)
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		// a real data centre derives its keys with no secret; echo the de-obfuscated stream.
		_, conn, ok := mtprotoReadHandshake(c, hdr, nil)
		if !ok {
			return
		}
		io.Copy(conn, conn)
	}()
	defer func(f func(context.Context, int) (net.Conn, error)) { mtprotoDialDC = f }(mtprotoDialDC)
	mtprotoDialDC = func(ctx context.Context, dc int) (net.Conn, error) {
		return net.Dial("tcp", dcLn.Addr().String())
	}

	ts := &tunnelServer{users: map[string]TunnelUser{}, sessions: map[*tunSession]bool{}, mt: map[*mtSession]bool{},
		boot: "test", traffic: map[string]*[2]atomic.Int64{}, entries: map[string]*atomic.Int64{}}
	ts.entries["mt"] = &atomic.Int64{}
	u := NewUser("ann")
	ts.setUsers([]TunnelUser{{Name: "ann", MT: mtHex(u)}})

	cliLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer cliLn.Close()
	served := make(chan struct{})
	go func() {
		defer close(served)
		c, err := cliLn.Accept()
		if err != nil {
			return
		}
		ts.serveMT(c, "mt", "9.9.9.9")
	}()

	conn, err := net.Dial("tcp", cliLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	enc, send, recv := mtprotoClientFrame(mtprotoKey(u), 2)
	if _, err := conn.Write(enc); err != nil {
		t.Fatal(err)
	}
	// send one obfuscated byte; the DC echoes it, the proxy relays it back obfuscated.
	msg := []byte("hi there, telegram")
	ob := make([]byte, len(msg))
	send.XORKeyStream(ob, msg)
	if _, err := conn.Write(ob); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("reading echo: %v", err)
	}
	recv.XORKeyStream(got, got)
	if string(got) != string(msg) {
		t.Errorf("round trip: got %q, want %q", got, msg)
	}
	// counters finalize when the streams end (as the SSH forward does), so close and wait.
	conn.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("serveMT did not finish")
	}
	if n := ts.entries["mt"].Load(); n == 0 {
		t.Error("no traffic counted on the entry")
	}
}

// mtHex is the user's MTProto key in hex, as TunnelConf stores it.
func mtHex(u User) string { return hex.EncodeToString(mtprotoKey(u)) }
