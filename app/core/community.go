package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The community directory (community/ in the repository): VPNs people share with everyone. The
// app lists them (Configs, Home), joins them (an xtune pool gives this device its own user), and
// shares the user's own servers from Manage. Its address is set at build time; the app's settings
// can point it elsewhere.

// CommunityURL is the directory the app uses unless its settings say otherwise
// (-ldflags "-X github.com/chethober/xtune/app/core.CommunityURL=https://…").
var CommunityURL = ""

var communityClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}

// CommunityRequest is one call to the directory's API.
type CommunityRequest struct {
	Base   string          `json:"base,omitempty"` // the settings' directory; "" for CommunityURL
	Method string          `json:"method"`
	Path   string          `json:"path"` // /v1/…
	Body   json.RawMessage `json:"body,omitempty"`
	Auth   string          `json:"auth,omitempty"` // "Owner KEY" for an owner's calls
}

// CommunityError is the directory's refusal: code is its error ("full", "gone", "busy", …).
type CommunityError struct {
	Status  int
	Code    string
	Message string
}

func (e *CommunityError) Error() string {
	if e.Message != "" && e.Message != e.Code {
		return fmt.Sprintf("community: %s (%s)", e.Message, e.Code)
	}
	return "community: " + e.Code
}

func communityBase(base string) (string, error) {
	b := strings.TrimRight(strings.TrimSpace(orStr(base, CommunityURL)), "/")
	if b == "" {
		return "", errors.New("community: no directory is set up in this build (Settings → Community directory)")
	}
	u, err := url.Parse(b)
	if err != nil || u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) || u.Host == "" {
		return "", errors.New("community: the directory's address must be https://…")
	}
	return b, nil
}

// Community calls the directory. It goes straight out; when that fails while connected, once more
// through the connection (the directory may be blocked where the device is).
func Community(r CommunityRequest) (json.RawMessage, error) {
	base, err := communityBase(r.Base)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(r.Path, "/v1/") || strings.Contains(r.Path, "..") {
		return nil, errors.New("community: bad path")
	}
	switch r.Method {
	case "GET", "POST", "PATCH", "DELETE":
	case "":
		r.Method = "GET"
	default:
		return nil, errors.New("community: bad method")
	}
	do := func(c *http.Client) (*http.Response, error) {
		var body io.Reader
		if len(r.Body) > 0 {
			body = bytes.NewReader(r.Body)
		}
		req, err := http.NewRequest(r.Method, base+r.Path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "xtune-client/"+Version)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if r.Auth != "" {
			req.Header.Set("Authorization", r.Auth)
		}
		return c.Do(req)
	}
	resp, err := do(communityClient)
	if err != nil {
		if c := throughConnection(); c != nil {
			resp, err = do(c)
		}
		if err != nil {
			return nil, fmt.Errorf("community: could not reach the directory: %w", err)
		}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		json.Unmarshal(b, &e)
		return nil, &CommunityError{Status: resp.StatusCode, Code: orStr(e.Error, fmt.Sprintf("HTTP %d", resp.StatusCode)), Message: e.Message}
	}
	if !json.Valid(b) {
		return nil, errors.New("community: the directory did not answer with JSON")
	}
	return b, nil
}

// throughConnection is a client that goes through the running connection's HTTP proxy, or nil.
func throughConnection() *http.Client {
	engine.mu.Lock()
	running, port := engine.inst != nil, engine.opts.HTTPPort
	engine.mu.Unlock()
	if !running || port == 0 {
		return nil
	}
	p, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(p)}}
}
