package core

import (
	"context"
	"errors"
	"net"
	"sync"
	"syscall"
	"time"
)

// The first hop of a route: how long a TCP handshake to a config's server (or its relay, which is
// what the config connects to) takes from this device. The home screen's map splits a config's
// delay with it: this device → entry, then entry → the internet.

// rttPorts are tried in order; the first that answers (or refuses: a reset comes back in one
// round trip too) gives the time.
var rttPorts = []string{"443", "80", "22"}

// HostRTT is the round trip to host in milliseconds, or -1 when nothing answers in time.
func HostRTT(ctx context.Context, host string, timeout time.Duration) int {
	var d net.Dialer
	for _, port := range rttPorts {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		t0 := time.Now()
		conn, err := d.DialContext(cctx, "tcp", net.JoinHostPort(host, port))
		took := time.Since(t0)
		cancel()
		if err == nil {
			conn.Close()
			return max(1, int(took.Milliseconds()))
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return max(1, int(took.Milliseconds()))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return -1
}

// HostRTTs measures several hosts at once (at most 64).
func HostRTTs(hosts []string, timeout time.Duration) map[string]int {
	if len(hosts) > 64 {
		hosts = hosts[:64]
	}
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 2500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*timeout)
	defer cancel()
	out := make(map[string]int, len(hosts))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, h := range hosts {
		if h == "" {
			continue
		}
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			ms := HostRTT(ctx, h, timeout)
			mu.Lock()
			out[h] = ms
			mu.Unlock()
		}(h)
	}
	wg.Wait()
	return out
}
