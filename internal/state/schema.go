package state

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Schema is the format of state.json and usage.json this xtune reads and writes. Files from
// every 0.x release have no "schema" field, which reads as 0.
//
// When a release changes the meaning or name of a field, it raises Schema and adds a migration;
// fields that are only added need neither (older files just lack them). A file with a newer
// schema is refused rather than rewritten: an older xtune would drop what it does not know.
const Schema = 1

// migrations[file][i] takes a decoded file from schema i to i+1.
var migrations = map[string][]func(map[string]any) error{
	"state.json": {stateFrom0},
	"usage.json": {migrateNothing},
}

// stateFrom0 is 0.x → 1. Every 0.x field kept its name and meaning (fields added since default to
// their zero values), except the users' links to xtune's own Telegram bot, removed in 0.7.0
// with the bot: they are dropped here. ("bot" itself stays until Enforce has removed the bot's unit.)
func stateFrom0(m map[string]any) error {
	users, _ := m["users"].([]any)
	if c, ok := m["check"].(map[string]any); ok {
		users = append(users, c)
	}
	for _, u := range users {
		if u, ok := u.(map[string]any); ok {
			delete(u, "telegram") // the user's linked Telegram ID (self-service)
			delete(u, "owner")    // the bot's reseller (a Telegram ID) who managed the user
		}
	}
	return nil
}

func migrateNothing(map[string]any) error { return nil }

// NewerError is a file written by a newer xtune.
type NewerError struct {
	File   string
	Schema int
}

func (e *NewerError) Error() string {
	return fmt.Sprintf("%s has schema %d, newer than this xtune knows (%d): update xtune (xtune self-update) before changing anything", e.File, e.Schema, Schema)
}

// upgrade returns the file b at the current schema.
func upgrade(file string, b []byte) ([]byte, error) {
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	switch {
	case head.Schema == Schema:
		return b, nil
	case head.Schema > Schema:
		return nil, &NewerError{File: file, Schema: head.Schema}
	case head.Schema < 0:
		return nil, fmt.Errorf("%s: bad schema %d", file, head.Schema)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber() // byte counts and timestamps stay exact
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	for v := head.Schema; v < Schema; v++ {
		if err := migrations[file][v](m); err != nil {
			return nil, fmt.Errorf("%s: schema %d to %d: %w", file, v, v+1, err)
		}
	}
	m["schema"] = Schema
	return json.Marshal(m)
}

// Decode reads state.json, from any schema up to the current one.
func Decode(b []byte) (*State, error) {
	b, err := upgrade("state.json", b)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("parse state.json: %w", err)
	}
	return &st, nil
}

// DecodeUsage reads usage.json, from any schema up to the current one.
func DecodeUsage(b []byte) (*Usage, error) {
	b, err := upgrade("usage.json", b)
	if err != nil {
		return nil, err
	}
	us := &Usage{}
	if err := json.Unmarshal(b, us); err != nil {
		return nil, fmt.Errorf("parse usage.json: %w", err)
	}
	if us.Users == nil {
		us.Users = map[string]*UserUsage{}
	}
	return us, nil
}
