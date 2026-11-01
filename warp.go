package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chethober/xtune/internal/protocols"
	"github.com/chethober/xtune/internal/remote"
	"github.com/chethober/xtune/internal/state"
)

// warpAPI is Cloudflare's client API, as used by the WARP apps; a variable so tests can replace it.
var warpAPI = "https://api.cloudflareclient.com/v0a2158"

func describeWarp(w *state.Warp) string {
	if w == nil {
		return "off"
	}
	if w.All {
		return "on, all traffic"
	}
	return "on for " + strings.Join(protocols.WarpDomains(w), ", ")
}

// registerWarp creates a free WARP account for a new WireGuard key.
func registerWarp() (*state.Warp, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(J{"key": base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), "install_id": "",
		"fcm_token": "", "tos": time.Now().UTC().Format(time.RFC3339), "model": "PC", "serial_number": "", "locale": "en_US"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", warpAPI+"/reg", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("User-Agent", "okhttp/3.12.1")
	req.Header.Set("CF-Client-Version", "a-6.30-3596")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("WARP registration: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("WARP registration: HTTP %d %s", resp.StatusCode, tail(string(raw), 200))
	}
	var reg struct {
		Config struct {
			ClientID string `json:"client_id"`
			Peers    []struct {
				PublicKey string `json:"public_key"`
				Endpoint  struct {
					Host string `json:"host"`
				} `json:"endpoint"`
			} `json:"peers"`
			Interface struct {
				Addresses struct{ V4, V6 string } `json:"addresses"`
			} `json:"interface"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		return nil, fmt.Errorf("WARP registration: %w", err)
	}
	c := reg.Config
	if len(c.Peers) == 0 || c.Peers[0].PublicKey == "" || c.Interface.Addresses.V4 == "" {
		return nil, fmt.Errorf("WARP registration: unexpected answer %s", tail(string(raw), 200))
	}
	w := &state.Warp{PrivateKey: base64.StdEncoding.EncodeToString(priv.Bytes()), Address4: c.Interface.Addresses.V4,
		Address6: c.Interface.Addresses.V6, PeerKey: c.Peers[0].PublicKey, Endpoint: c.Peers[0].Endpoint.Host}
	if w.Endpoint == "" {
		w.Endpoint = "engage.cloudflareclient.com:2408"
	}
	if id, err := base64.StdEncoding.DecodeString(c.ClientID); err == nil && len(id) == 3 {
		w.Reserved = []int{int(id[0]), int(id[1]), int(id[2])}
	}
	return w, nil
}

// SetWarp turns WARP on with the given routing (registering an account when there is none), or
// off when on is false. Xray restarts, because outbounds change. Settings Xray rejects (an unknown
// geosite list, say) are rolled back.
func SetWarp(r *remote.Server, on bool, domains []string, all bool) (*state.State, error) {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	prev := st.Warp
	switch {
	case !on:
		st.Warp = nil
	case prev == nil:
		if st.Warp, err = registerWarp(); err != nil {
			return nil, err
		}
	default:
		w := *prev
		st.Warp = &w
	}
	if st.Warp != nil {
		st.Warp.Domains, st.Warp.All = domains, all
	}
	if err := Apply(r, st); err != nil {
		st.Warp = prev
		if rerr := Apply(r, st); rerr != nil {
			return nil, fmt.Errorf("%w (and restoring the previous settings failed: %v)", err, rerr)
		}
		return nil, err
	}
	return st, nil
}
