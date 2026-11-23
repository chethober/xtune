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

// A user's own tuning, from the Check tab, on an xtune subscription:
//
//	GET  SUB/prefs   their tuning: what their admin set, their own choices, their configs
//	PUT  SUB/prefs   their own choices: pinned and hidden configs, client settings, auto order
//	GET  SUB/tune    a personal tune: variants of their configs, as a profile to measure
//	POST SUB/tune    what this device measured; the server keeps the best settings of each config
//
// The app measures a personal tune with the same check as the Check tab (StartProbe).

// subCall makes one request to the user's subscription server, directly (not through the VPN).
func subCall(method, subURL, path, query string, body any) (json.RawMessage, error) {
	if !IsXtuneSub(subURL) {
		return nil, errors.New("this subscription is not from an xtune server")
	}
	base, err := NormalizeSubURL(subURL)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path+query, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xtune-client/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := directClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the subscription server: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == 404:
		return nil, errors.New("the server does not support personal tuning yet (update xtune on it)")
	case resp.StatusCode >= 300:
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(out, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !json.Valid(out) {
		return nil, errors.New("unexpected answer from the subscription server")
	}
	return out, nil
}

// Prefs is the user's tuning view (xtune's userTuneView), as is.
func Prefs(subURL string) (json.RawMessage, error) {
	return subCall("GET", subURL, "/prefs", "", nil)
}

// PrefsChange is what the user may change.
type PrefsChange struct {
	Pin   []string                  `json:"pin"`
	Hide  []string                  `json:"hide"`
	Knobs map[string]map[string]any `json:"knobs,omitempty"`
	Auto  *bool                     `json:"auto,omitempty"`
}

// SetPrefs saves the user's own choices and returns their tuning view.
func SetPrefs(subURL string, p PrefsChange) (json.RawMessage, error) {
	return subCall("PUT", subURL, "/prefs", "", p)
}

// PersonalTune is a personal tune to measure: its profile's configs are variants of the user's.
type PersonalTune struct {
	ID      string   `json:"id"`
	Expires int64    `json:"expires"`
	Profile Profile  `json:"profile"`
	Skipped []string `json:"skipped,omitempty"`
}

// FetchPersonalTune asks the server for a personal tune for the network asn.
func FetchPersonalTune(subURL, asn string) (*PersonalTune, error) {
	q := fmt.Sprintf("?hour=%d", time.Now().Hour())
	if a := trimAS(strings.TrimSpace(asn)); a != "" {
		q += "&asn=" + url.QueryEscape(a)
	}
	out, err := subCall("GET", subURL, "/tune", q, nil)
	if err != nil {
		return nil, err
	}
	var pt PersonalTune
	if err := json.Unmarshal(out, &pt); err != nil {
		return nil, fmt.Errorf("bad personal tune: %w", err)
	}
	if len(pt.Profile.Configs) == 0 {
		return nil, errors.New("you have no configs to tune")
	}
	return &pt, nil
}

// SendPersonalTune sends a personal tune's results; the server answers with what it chose.
func SendPersonalTune(subURL string, n Network, results []Result) (json.RawMessage, error) {
	type res struct {
		Key  string  `json:"key"`
		Ok   int     `json:"ok"`
		N    int     `json:"n"`
		Warm int     `json:"warmMs"`
		Down float64 `json:"downMbps"`
		Up   float64 `json:"upMbps"`
	}
	in := struct {
		ASN     string `json:"asn"`
		Name    string `json:"name"`
		Hour    int    `json:"hour"`
		Results []res  `json:"results"`
	}{ASN: trimAS(n.ASN), Name: n.Name, Hour: time.Now().Hour()}
	for _, r := range results {
		if r.State == "skipped" || r.State == "testing" || r.Key == "" {
			continue
		}
		in.Results = append(in.Results, res{Key: r.Key, Ok: r.Ok, N: r.N, Warm: r.Warm, Down: r.Down})
	}
	if in.ASN == "" {
		return nil, errors.New("the network is not known yet: try again in a moment")
	}
	if len(in.Results) == 0 {
		return nil, errors.New("nothing was measured")
	}
	return subCall("POST", subURL, "/tune", "", in)
}

// UserTune is `xtune users tune NAME --json` on a server the app manages (the admin's view).
func UserTune(c ServerConn, name string) (json.RawMessage, error) {
	out, err := xtune(c, "users", "tune", name, "--json")
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(out)) {
		return nil, errors.New("unexpected answer from xtune on the server: update it (xtune self-update)")
	}
	return json.RawMessage(out), nil
}

// SetUserTune changes a user's tuning on a server the app manages: change is xtune's TuneChange.
func SetUserTune(c ServerConn, name string, change map[string]any) (json.RawMessage, error) {
	b, _ := json.Marshal(change)
	out, err := xtune(c, "users", "tune", name, "--set", string(b))
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(out)) {
		return nil, errors.New("unexpected answer from xtune on the server: " + strings.TrimSpace(out))
	}
	return json.RawMessage(out), nil
}
