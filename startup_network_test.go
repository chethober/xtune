package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
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
		n, err := lookupClientNetwork(c, func(ip net.IP) (string, error) {
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

func TestTestUnitReplacesLoadedUnit(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	b, err := os.ReadFile("helper.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "start_test_unit() {")
	end := strings.Index(s[start:], "\ntest_stop_extras() {")
	script := `loaded=1
systemctl() {
 case "$1" in
 stop) stopped=1 ;;
 reset-failed) [ "$stopped" = 1 ] || exit 10; loaded=0 ;;
 esac
}
systemd-run() {
 [ "$loaded" = 0 ] || exit 11
 [[ " $* " == *" --collect "* ]] || exit 12
 [[ " $* " == *" --unit=xray-autotune-test-tunnels "* ]] || exit 13
 loaded=1
}
` + s[start:start+end] + "\nstart_test_unit xray-autotune-test-tunnels /bin/true\nstart_test_unit xray-autotune-test-tunnels /bin/true\n"
	if out, err := exec.Command("bash", "-eu", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("restart: %v %s", err, out)
	}
}
