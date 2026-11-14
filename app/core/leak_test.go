package core

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeLeakService answers like bash.ws and Cloudflare's trace, from behind a test server.
func fakeLeakService(t *testing.T, resolvers string) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/id":
			io.WriteString(w, "4b2f9a\n")
		case strings.HasPrefix(r.URL.Path, "/dnsleak/test/4b2f9a"):
			io.WriteString(w, resolvers)
		case r.URL.Path == "/cdn-cgi/trace":
			io.WriteString(w, "fl=1\nip=203.0.113.50\nloc=DE\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestLeakTest(t *testing.T) {
	geoDir(t)
	if _, err := LeakTest(); err == nil {
		t.Error("needs a connection")
	}
	svc := fakeLeakService(t, `[{"ip":"203.0.113.50","country":"DE","asn":"Example","type":"ip"},
		{"ip":"198.51.100.53","country":"DE","country_name":"Germany","asn":"Example DNS","type":"dns"},
		{"ip":"","type":"conclusion"}]`)
	srv := testServer(t, strings.TrimPrefix(svc.URL, "http://")) // everything through it lands on the fake
	defer func(b, tr string, w time.Duration) { leakBase, traceURL, leakLookupWait = b, tr, w }(leakBase, traceURL, leakLookupWait)
	leakBase, traceURL, leakLookupWait = "http://leak.test", "http://trace.test/cdn-cgi/trace", time.Second
	im, _ := Import(fmt.Sprintf("vless://%s@127.0.0.1:%d?type=tcp&security=none#A", testUUID, srv), "")
	sp, hp := mustPort(t), mustPort(t)
	if err := Start(im.Profile, Options{Mode: "proxy", SocksPort: sp, HTTPPort: hp}); err != nil {
		t.Fatal(err)
	}
	defer Disconnect()
	r, err := LeakTest()
	if err != nil {
		t.Fatal(err)
	}
	if r.ExitIP != "203.0.113.50" || r.ExitCountry != "DE" || !r.Tunneled {
		t.Errorf("exit: %+v", r)
	}
	if len(r.Resolvers) != 1 || r.Resolvers[0].IP != "198.51.100.53" || r.DNSLeak || r.IPv6 != "blocked" {
		t.Errorf("dns: %+v", r)
	}
	if !strings.HasPrefix(r.Conclusion, "No leaks") {
		t.Errorf("conclusion: %q", r.Conclusion)
	}
	// a resolver in the device's own country (and not the exit's) is a leak
	res := &LeakResult{DirectCountry: "IR", ExitCountry: "DE"}
	svc2 := fakeLeakService(t, `[{"ip":"5.200.200.200","country":"IR","asn":"TCI","type":"dns"}]`)
	leakBase = svc2.URL
	if err := dnsLeak(&http.Client{Timeout: 5 * time.Second}, nil, res); err != nil || !res.DNSLeak {
		t.Errorf("a leak is found: %v %+v", err, res)
	}
}
