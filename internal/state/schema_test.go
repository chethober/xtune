package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// testdata/vX.Y.Z.json is what release X.Y.Z wrote with every field set: state.json under
// "state", usage.json under "usage" (made from each release's own types, see fill). The current
// format is testdata/current.json; `go test ./internal/state -update` rewrites it after a change.
var update = flag.Bool("update", false, "rewrite testdata/current.json from the current types")

// fill sets every exported field to a non-zero value, so the JSON shows each field.
func fill(v reflect.Value, name string, depth int) {
	if depth > 4 {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x-" + name)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(7)
	case reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint8:
		v.SetUint(7)
	case reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Pointer:
		n := reflect.New(v.Type().Elem())
		fill(n.Elem(), name, depth+1)
		v.Set(n)
	case reflect.Slice:
		if v.Type() == reflect.TypeOf(json.RawMessage{}) {
			v.SetBytes([]byte(`{"legacy":true}`))
			return
		}
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0), name, depth+1)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fill(k, "k", depth+1)
		e := reflect.New(v.Type().Elem()).Elem()
		fill(e, name, depth+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Type().Field(i); f.IsExported() {
				fill(v.Field(i), f.Name, depth+1)
			}
		}
	}
}

func generic(t *testing.T, b []byte) any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// lost lists what in old is missing or different in new: every field an old release wrote must
// survive reading and writing the file again.
func lost(old, new any, path string) []string {
	switch o := old.(type) {
	case map[string]any:
		n, ok := new.(map[string]any)
		if !ok {
			return []string{path + ": no longer an object"}
		}
		var out []string
		for k, v := range o {
			nv, ok := n[k]
			if !ok {
				out = append(out, path+"."+k+": dropped")
				continue
			}
			out = append(out, lost(v, nv, path+"."+k)...)
		}
		return out
	case []any:
		n, ok := new.([]any)
		if !ok || len(n) != len(o) {
			return []string{path + ": list changed"}
		}
		var out []string
		for i := range o {
			out = append(out, lost(o[i], n[i], fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	}
	if fmt.Sprint(old) != fmt.Sprint(new) {
		return []string{fmt.Sprintf("%s: %v became %v", path, old, new)}
	}
	return nil
}

func fixtures(t *testing.T) []string {
	t.Helper()
	fs, _ := filepath.Glob("testdata/v*.json")
	fs = append(fs, "testdata/current.json")
	sort.Strings(fs)
	return fs
}

func TestCurrentFixture(t *testing.T) {
	var st State
	fill(reflect.ValueOf(&st).Elem(), "State", 0)
	var us Usage
	fill(reflect.ValueOf(&us).Elem(), "Usage", 0)
	st.Schema, us.Schema = Schema, Schema
	b, _ := json.MarshalIndent(map[string]any{"state": st, "usage": us}, "", " ")
	if *update {
		if err := os.WriteFile("testdata/current.json", b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.ReadFile("testdata/current.json")
	if err != nil {
		t.Fatal(err)
	}
	// a field renamed or removed since the fixture was written breaks files on servers
	if l := lost(generic(t, old), generic(t, b), ""); len(l) > 0 {
		t.Errorf("the current types lost fields of testdata/current.json (a rename needs a migration and a Schema bump; "+
			"after adding fields run go test ./internal/state -update):\n%s", strings.Join(l, "\n"))
	}
}

// Every release's files read, and write back, without losing anything.
func TestMigrateEveryRelease(t *testing.T) {
	fs := fixtures(t)
	if len(fs) < 15 {
		t.Fatalf("fixtures missing: %v", fs)
	}
	for _, f := range fs {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var in struct{ State, Usage json.RawMessage }
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		st, err := Decode(in.State)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if st.Schema != Schema || st.Host == "" || len(st.Users) != 1 || st.Users[0].Name == "" || len(st.Entries) != 1 {
			t.Errorf("%s: decoded %+v", f, st)
		}
		// only a migration may drop a field: decoding the migrated file must keep everything
		migrated, err := upgrade("state.json", in.State)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(st)
		if l := lost(generic(t, migrated), generic(t, out), "state"); len(l) > 0 {
			t.Errorf("%s:\n%s", f, strings.Join(l, "\n"))
		}
		if len(in.Usage) == 0 { // v0.1.0 had no usage.json
			continue
		}
		us, err := DecodeUsage(in.Usage)
		if err != nil {
			t.Errorf("%s usage: %v", f, err)
			continue
		}
		if migrated, err = upgrade("usage.json", in.Usage); err != nil {
			t.Fatal(err)
		}
		out, _ = json.Marshal(us)
		if l := lost(generic(t, migrated), generic(t, out), "usage"); len(l) > 0 {
			t.Errorf("%s:\n%s", f, strings.Join(l, "\n"))
		}
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	_, err := Decode([]byte(`{"schema": 99, "host": "h"}`))
	var ne *NewerError
	if !errors.As(err, &ne) || ne.Schema != 99 || !strings.Contains(err.Error(), "update xtune") {
		t.Errorf("newer state: %v", err)
	}
	if _, err := DecodeUsage([]byte(`{"schema": 2}`)); !errors.As(err, &ne) {
		t.Errorf("newer usage: %v", err)
	}
	if _, err := Decode([]byte(`{"schema": -1}`)); err == nil {
		t.Error("a negative schema was accepted")
	}
	if _, err := Decode([]byte(`not json`)); err == nil {
		t.Error("garbage accepted")
	}
}

// Numbers survive a migration exactly (a float64 round trip would round large byte counts).
func TestMigrationKeepsBigNumbers(t *testing.T) {
	us, err := DecodeUsage([]byte(`{"users": {"a": {"up": 9007199254740993, "down": 1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if us.Users["a"].Up != 9007199254740993 {
		t.Errorf("up = %d", us.Users["a"].Up)
	}
}

// The migration from 0.x drops exactly the bot's user links, and nothing else.
func TestStateFrom0(t *testing.T) {
	raw, _ := os.ReadFile("testdata/v0.6.0.json")
	var in struct{ State json.RawMessage }
	json.Unmarshal(raw, &in)
	migrated, err := upgrade("state.json", in.State)
	if err != nil {
		t.Fatal(err)
	}
	l := lost(generic(t, in.State), generic(t, migrated), "state")
	sort.Strings(l)
	want := []string{"state.check.owner: dropped", "state.check.telegram: dropped", "state.users[0].owner: dropped", "state.users[0].telegram: dropped"}
	if strings.Join(l, "\n") != strings.Join(want, "\n") {
		t.Errorf("0.x migration changed:\n%s", strings.Join(l, "\n"))
	}
	if m := generic(t, migrated).(map[string]any); fmt.Sprint(m["schema"]) != fmt.Sprint(Schema) || m["bot"] == nil {
		t.Errorf("schema %v, bot %v", m["schema"], m["bot"])
	}
}
