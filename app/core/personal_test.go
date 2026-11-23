package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The personal-tune and preference calls, against a stand-in for the subscription server.
func TestPersonalTuneCalls(t *testing.T) {
	var got struct {
		path, method, query string
		body                map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.method, got.query = r.URL.Path, r.Method, r.URL.RawQuery
		got.body = nil
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &got.body)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/tune") && r.Method == "GET":
			io.WriteString(w, `{"id":"personal-ann","expires":1,"profile":{"v":1,"name":"t","configs":[{"id":"r-tcp#0","key":"r-tcp|fp=chrome","name":"r-tcp","engine":"xray"}]}}`)
		case strings.HasSuffix(r.URL.Path, "/tune"):
			io.WriteString(w, `{"measured":1,"chosen":{"r-tcp":"fp=chrome"}}`)
		case strings.HasSuffix(r.URL.Path, "/prefs") && r.Method == "PUT":
			w.WriteHeader(422)
			io.WriteString(w, `{"error":"order: your admin set this; it cannot be changed"}`)
		default:
			io.WriteString(w, `{"name":"ann","configs":[]}`)
		}
	}))
	defer srv.Close()
	sub := srv.URL + "/p/tok"
	pt, err := FetchPersonalTune(sub, "AS44244")
	if err != nil || got.path != "/p/tok/tune" || !strings.Contains(got.query, "asn=44244") || pt.Profile.Configs[0].Key != "r-tcp|fp=chrome" {
		t.Fatalf("fetch: %v %+v %+v", err, got, pt)
	}
	out, err := SendPersonalTune(sub, Network{ASN: "AS44244", Name: "Irancell"},
		[]Result{{ID: "r-tcp#0", Key: "r-tcp|fp=chrome", State: "ok", Ok: 3, N: 3, Warm: 90}, {Key: "x", State: "skipped"}})
	if err != nil || got.method != "POST" || got.body["asn"] != "44244" || len(got.body["results"].([]any)) != 1 || !strings.Contains(string(out), "chosen") {
		t.Fatalf("send: %v %+v %s", err, got.body, out)
	}
	if _, err := Prefs(sub); err != nil || got.path != "/p/tok/prefs" {
		t.Errorf("prefs: %v %+v", err, got)
	}
	if _, err := SetPrefs(sub, PrefsChange{Pin: []string{"r-tcp"}}); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Errorf("a locked change should come back with the server's reason: %v", err)
	}
}
