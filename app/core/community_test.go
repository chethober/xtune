package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommunity(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		switch r.URL.Path {
		case "/v1/vpns":
			w.Write([]byte(`{"vpns":[{"id":"abc","name":"Frankfurt"}]}`))
		case "/v1/vpns/abc/join":
			w.WriteHeader(409)
			w.Write([]byte(`{"error":"full","message":"full"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	base := strings.Replace(srv.URL, "http://127.0.0.1", "http://localhost", 1)

	out, err := Community(CommunityRequest{Base: base, Method: "GET", Path: "/v1/vpns"})
	if err != nil || !json.Valid(out) || !strings.Contains(string(out), "Frankfurt") {
		t.Fatalf("list: %s %v", out, err)
	}
	_, err = Community(CommunityRequest{Base: base, Method: "POST", Path: "/v1/vpns/abc/join", Body: json.RawMessage(`{"member":"00"}`), Auth: "Owner k"})
	var ce *CommunityError
	if !errors.As(err, &ce) || ce.Code != "full" || ce.Status != 409 {
		t.Fatalf("join a full pool: %v", err)
	}
	if got[1] != `POST /v1/vpns/abc/join Owner k {"member":"00"}` {
		t.Errorf("request: %q", got[1])
	}
	for _, bad := range []CommunityRequest{
		{Base: base, Path: "/admin"},
		{Base: base, Path: "/v1/../x"},
		{Base: base, Method: "PUT", Path: "/v1/vpns"},
		{Base: "http://example.com", Path: "/v1/vpns"},
		{Path: "/v1/vpns"}, // no directory in this build
	} {
		if _, err := Community(bad); err == nil {
			t.Errorf("%+v: want an error", bad)
		}
	}
}
