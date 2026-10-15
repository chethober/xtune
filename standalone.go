package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Standalone manages xtune's own Xray deployment (systemd unit "xray-autotune").

func LoadState(r *Remote) (*State, error) {
	out, err := r.Root("cat " + remoteDir + "/state.json 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var st State
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, fmt.Errorf("parse state.json: %w", err)
	}
	return &st, nil
}

// Apply writes state, certificate and config to the server and (re)starts the service.
func Apply(r *Remote, st *State) error {
	stJSON, _ := json.MarshalIndent(st, "", " ")
	cfgJSON, _ := json.MarshalIndent(st.ServerConfig(st.Entries, st.Users), "", " ")
	files := []struct{ path, body string }{
		{remoteDir + "/state.json", string(stJSON)},
		{remoteDir + "/hy.crt", st.CertPEM},
		{remoteDir + "/hy.key", st.KeyPEM},
		{remoteDir + "/config.json", string(cfgJSON)},
	}
	for _, f := range files {
		if err := r.Upload(f.path, []byte(f.body), "600"); err != nil {
			return err
		}
	}
	specs := []string{"apply"}
	for _, e := range st.Entries {
		specs = append(specs, fmt.Sprintf("%d/%s", e.Port, e.Proto()))
	}
	out, err := r.Helper(specs...)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(out, "active") || strings.HasSuffix(out, "inactive") {
		return fmt.Errorf("xray-autotune did not start: %s", out)
	}
	return nil
}

// Deploy keeps the top n tuned configs running. Existing users keep their identities.
func Deploy(r *Remote, res *TuneResult, n int, names []string) (*State, error) {
	if n > len(res.Best) {
		n = len(res.Best)
	}
	if n <= 0 {
		return nil, fmt.Errorf("nothing to deploy")
	}
	st := res.State
	st.Entries = nil
	for _, v := range res.Best[:n] {
		e := v.Entry
		e.FP, e.Mode, e.Frag = v.K.FP, v.K.Mode, v.K.Frag
		st.Entries = append(st.Entries, e)
	}
	if old, err := LoadState(r); err == nil && old != nil {
		st.Users = old.Users
	}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			if _, u := st.User(name); u == nil {
				st.Users = append(st.Users, NewUser(name))
			}
		}
	}
	if len(st.Users) == 0 {
		st.Users = append(st.Users, NewUser("user1"))
	}
	return st, Apply(r, st)
}

func requireState(r *Remote) (*State, error) {
	st, err := LoadState(r)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, fmt.Errorf("no standalone deployment on this server yet - run a tune and deploy first")
	}
	return st, nil
}

func (st *State) Links(u User) []string {
	var out []string
	for _, e := range st.Entries {
		out = append(out, st.Link(e, u, Knobs{FP: e.FP, Frag: e.Frag, Mode: e.Mode}))
	}
	return out
}

// Subscription is the base64 body most clients accept as an imported subscription.
func (st *State) Subscription(u User) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(st.Links(u), "\n")))
}

func StandaloneAdd(r *Remote, name string) (*State, User, error) {
	st, err := requireState(r)
	if err != nil {
		return nil, User{}, err
	}
	if _, u := st.User(name); u != nil {
		return nil, User{}, fmt.Errorf("user %q already exists", name)
	}
	u := NewUser(name)
	st.Users = append(st.Users, u)
	return st, u, Apply(r, st)
}

func StandaloneDelete(r *Remote, name string) error {
	st, err := requireState(r)
	if err != nil {
		return err
	}
	i, _ := st.User(name)
	if i < 0 {
		return fmt.Errorf("no user %q", name)
	}
	st.Users = append(st.Users[:i], st.Users[i+1:]...)
	return Apply(r, st)
}

func StandaloneSetEnabled(r *Remote, name string, on bool) error {
	st, err := requireState(r)
	if err != nil {
		return err
	}
	_, u := st.User(name)
	if u == nil {
		return fmt.Errorf("no user %q", name)
	}
	u.Enabled = on
	return Apply(r, st)
}

// StandaloneRotate issues new credentials, invalidating the user's old links.
func StandaloneRotate(r *Remote, name string) (*State, User, error) {
	st, err := requireState(r)
	if err != nil {
		return nil, User{}, err
	}
	_, u := st.User(name)
	if u == nil {
		return nil, User{}, fmt.Errorf("no user %q", name)
	}
	u.UUID, u.SSKey = newUUID(), ssKey()
	return st, *u, Apply(r, st)
}
