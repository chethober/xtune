package state

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chethober/xtune/internal/remote"
)

// Load reads state.json; it returns nil, nil when the server has no deployment yet.
func Load(r *remote.Server) (*State, error) {
	out, err := r.Root("cat " + remote.Dir + "/state.json 2>/dev/null || true")
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

// Save writes state.json and the certificate. Users get a subscription token and a
// WireGuard address if they lack one.
func Save(r *remote.Server, st *State) error {
	for i := range st.Users {
		if st.Users[i].SubToken == "" {
			st.Users[i].SubToken = randHex(16)
		}
	}
	st.AssignWGIPs()
	stJSON, _ := json.MarshalIndent(st, "", " ")
	for _, f := range []struct{ path, body string }{
		{remote.Dir + "/state.json", string(stJSON)},
		{remote.Dir + "/hy.crt", st.CertPEM},
		{remote.Dir + "/hy.key", st.KeyPEM},
	} {
		if err := r.Upload(f.path, []byte(f.body), "600"); err != nil {
			return err
		}
	}
	return nil
}
