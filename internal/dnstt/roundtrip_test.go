package dnstt_test

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"

	"github.com/chethober/xtune/internal/dnstt/client"
	"github.com/chethober/xtune/internal/dnstt/server"
	"www.bamsoftware.com/git/dnstt.git/dns"
	"www.bamsoftware.com/git/dnstt.git/noise"
)

// TestRoundTrip runs a tunnel over loopback UDP: a stream's bytes come back through the echo handler.
func TestRoundTrip(t *testing.T) {
	priv, err := noise.GeneratePrivkey()
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	name, _ := dns.ParseName("t.example.com")
	go server.Serve(priv, name, pc, func(c net.Conn) { io.Copy(c, c) })
	defer pc.Close()

	s, err := client.Dial("udp:"+pc.LocalAddr().String(), noise.PubkeyFromPrivkey(priv), "t.example.com", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.Open()
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 64<<10)
	rand.Read(want)
	go c.Write(want)
	got := make([]byte, len(want))
	c.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("echo differs")
	}

	// a wrong key never completes the handshake
	other, _ := noise.GeneratePrivkey()
	if s, err := client.Dial("udp:"+pc.LocalAddr().String(), noise.PubkeyFromPrivkey(other), "t.example.com", 3*time.Second); err == nil {
		s.Close()
		t.Fatal("handshake with the wrong key succeeded")
	}
}
