package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/chethober/xtune/internal/netinfo"
)

type networkRoundTrip func(*http.Request) (*http.Response, error)

func (f networkRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientNetworkRateLimitFallback(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		c := &http.Client{Transport: networkRoundTrip(func(r *http.Request) (*http.Response, error) {
			code, body := 200, "ip=8.8.8.8\n"
			switch r.URL.Host {
			case "speed.cloudflare.com":
				code, body = 403, "forbidden"
			case "ipinfo.io":
				code, body = 429, "rate limited"
			}
			if invalid && code == 200 {
				body = "ip=127.0.0.1\n"
			}
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		calls := 0
		n, err := netinfo.LookupClient(c, func(ip net.IP) (string, error) {
			calls++
			if !ip.Equal(net.ParseIP("8.8.8.8")) {
				return "", errors.New("wrong IP")
			}
			return "AS15169", nil
		})
		if invalid {
			if err == nil || calls != 0 {
				t.Fatalf("invalid public IP accepted: %v, %d", err, calls)
			}
		} else if err != nil || n.ASN != "AS15169" || calls != 1 {
			t.Fatalf("fallback: %+v, %v, calls=%d", n, err, calls)
		}
	}
}
