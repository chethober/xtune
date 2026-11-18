package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The crash log: what closed the app, and errors the app caught and carried on after. Entries are
// files in DATADIR/crash, one each, named UNIXMS-SOURCE-KIND.txt, with "key: value" lines, a blank
// line, then the text. The Android app writes its own (Kotlin) entries there too; the Go runtime
// writes a fatal crash to go-crash.txt, which the next start turns into an entry.

// CrashEntry is one entry, newest first in CrashList.
type CrashEntry struct {
	At     int64  `json:"at"`     // unix ms
	Source string `json:"source"` // go | android | dart
	Kind   string `json:"kind"`   // crash (the app closed) | error (caught: the app carried on)
	Where  string `json:"where"`
	Title  string `json:"title"` // the first line that says what happened
	Text   string `json:"text"`
}

const (
	crashKeep    = 30
	crashMaxText = 48 << 10
)

func crashDir() string {
	dataMu.Lock()
	defer dataMu.Unlock()
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "crash")
}

// setupCrashOutput keeps the last run's fatal Go crash as an entry, then has the runtime write the
// next one to a fresh go-crash.txt (a panic in a goroutine, or a fatal error such as a concurrent
// map write, ends the whole app, and no recover sees it).
func setupCrashOutput(dir string) {
	d := filepath.Join(dir, "crash")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return
	}
	cur := filepath.Join(d, "go-crash.txt")
	if st, err := os.Stat(cur); err == nil && st.Size() > 0 {
		if b, err := os.ReadFile(cur); err == nil {
			writeCrash(d, st.ModTime(), "go", "crash", "the core", string(b))
		}
	}
	f, err := os.Create(cur)
	if err != nil {
		return
	}
	debug.SetCrashOutput(f, debug.CrashOptions{}) // the runtime keeps its own copy of the descriptor
	f.Close()
}

func writeCrash(dir string, at time.Time, source, kind, where, text string) {
	if dir == "" {
		return
	}
	if len(text) > crashMaxText {
		text = text[:crashMaxText] + "\n…"
	}
	clean := func(s string) string { return strings.NewReplacer("\n", " ", "\r", " ", "/", "-").Replace(s) }
	body := fmt.Sprintf("kind: %s\nsource: %s\nwhere: %s\ncore: %s\n\n%s", kind, source, clean(where), Version, text)
	name := fmt.Sprintf("%d-%s-%s.txt", at.UnixMilli(), clean(source), clean(kind))
	if os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600) == nil {
		pruneCrashes(dir)
	}
}

func pruneCrashes(dir string) {
	names := crashFiles(dir)
	for _, n := range names[min(len(names), crashKeep):] {
		os.Remove(filepath.Join(dir, n))
	}
}

// crashFiles are the entries' file names, newest first.
func crashFiles(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".txt") && n != "go-crash.txt" {
			if _, err := strconv.ParseInt(strings.SplitN(n, "-", 2)[0], 10, 64); err == nil {
				out = append(out, n)
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out))) // the same number of digits until the year 2286
	return out
}

// RecordCrash adds an entry: from the core's own recovers, or one the app sends (crash.record).
func RecordCrash(source, kind, where, text string) {
	if kind != "crash" {
		kind = "error"
	}
	writeCrash(crashDir(), time.Now(), source, kind, where, text)
}

// CrashList is the newest n entries (all of them for n <= 0), secrets taken out.
func CrashList(n int) []CrashEntry {
	dir := crashDir()
	out := []CrashEntry{}
	if dir == "" {
		return out
	}
	for _, name := range crashFiles(dir) {
		if n > 0 && len(out) >= n {
			break
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		out = append(out, parseCrash(name, string(b)))
	}
	return out
}

func parseCrash(name, s string) CrashEntry {
	e := CrashEntry{Kind: "error"}
	at, _ := strconv.ParseInt(strings.SplitN(name, "-", 2)[0], 10, 64)
	e.At = at
	head, body, ok := strings.Cut(s, "\n\n")
	if !ok {
		head, body = "", s
	}
	for _, l := range strings.Split(head, "\n") {
		k, v, _ := strings.Cut(l, ": ")
		switch k {
		case "kind":
			e.Kind = v
		case "source":
			e.Source = v
		case "where":
			e.Where = v
		}
	}
	e.Text = Redact(body)
	for _, l := range strings.Split(e.Text, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "goroutine ") {
			e.Title = l
			break
		}
	}
	if len(e.Title) > 200 {
		e.Title = e.Title[:200] + "…"
	}
	return e
}

// ClearCrashes deletes every entry.
func ClearCrashes() {
	dir := crashDir()
	for _, n := range crashFiles(dir) {
		os.Remove(filepath.Join(dir, n))
	}
}

// recovered keeps a panic in a background goroutine as an entry instead of ending the app. Use as
// `defer recovered("what it does")` at the top of the goroutine.
func recovered(where string) {
	if r := recover(); r != nil {
		RecordCrash("go", "error", where, fmt.Sprintf("panic: %v\n\n%s", r, debug.Stack()))
	}
}

// goSafe runs f in a goroutine whose panic is written down, not fatal.
func goSafe(where string, f func()) {
	go func() {
		defer recovered(where)
		f()
	}()
}
