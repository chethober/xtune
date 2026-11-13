package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/chethober/xtune/pkg/state"
)

func TestParseBackupSchemas(t *testing.T) {
	old := `{"xtuneVersion": "0.9.0", "created": 1, "state": {"host": "h", "realityPrivateKey": "k", "users": [{"name": "ann", "telegram": 5}]},
	 "usage": {"users": {"ann": {"up": 3}}}}`
	b, err := parseBackup("old.json", []byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if b.State.Schema != state.Schema || b.State.Users[0].Name != "ann" || b.Usage.Users["ann"].Up != 3 || b.Version != "0.9.0" {
		t.Errorf("0.9 backup: %+v %+v", b.State, b.Usage)
	}
	noUsage, err := parseBackup("x", []byte(`{"state": {"host": "h", "realityPrivateKey": "k"}}`))
	if err != nil || noUsage.Usage == nil || noUsage.Usage.Users == nil {
		t.Errorf("a backup without usage: %v", err)
	}
	var ne *state.NewerError
	if _, err := parseBackup("new.json", []byte(`{"state": {"schema": 7, "host": "h", "realityPrivateKey": "k"}}`)); !errors.As(err, &ne) {
		t.Errorf("a backup from a newer xtune: %v", err)
	}
	for _, bad := range []string{`{}`, `{"state": null}`, `{"state": {"host": "h"}}`, `[]`} {
		if _, err := parseBackup("bad.json", []byte(bad)); err == nil || !strings.Contains(err.Error(), "bad.json") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
