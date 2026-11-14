package core

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The connection timeline: when the device connected, switched configs, moved networks or lost
// the connection, and why. It lives in the data directory, so it outlasts restarts, and keeps the
// last timelineMax events.

const timelineMax = 300

// Event is one entry.
type Event struct {
	Time int64  `json:"t"`    // unix seconds
	Kind string `json:"kind"` // connect | disconnect | switch | network | pause | error
	Text string `json:"text"`
}

var (
	tlMu     sync.Mutex
	tlEvents []Event
	tlLoaded bool
)

func timelineFile() string {
	dataMu.Lock()
	d := dataDir
	dataMu.Unlock()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "timeline.jsonl")
}

func loadTimeline() {
	if tlLoaded {
		return
	}
	tlLoaded = true
	f, err := os.Open(timelineFile())
	if err != nil {
		return
	}
	defer f.Close()
	var saved []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Time > 0 {
			saved = append(saved, e)
		}
	}
	tlEvents = append(saved, tlEvents...) // events recorded before the data directory was known
	if len(tlEvents) > timelineMax {
		tlEvents = tlEvents[len(tlEvents)-timelineMax:]
	}
}

// record adds an event (and rewrites the file when it has grown well past the limit).
func record(kind, text string) {
	tlMu.Lock()
	defer tlMu.Unlock()
	loadTimeline()
	e := Event{Time: time.Now().Unix(), Kind: kind, Text: text}
	tlEvents = append(tlEvents, e)
	path := timelineFile()
	if len(tlEvents) > timelineMax*2 || path == "" {
		tlEvents = tlEvents[max(0, len(tlEvents)-timelineMax):]
		if path != "" {
			var b []byte
			for _, x := range tlEvents {
				line, _ := json.Marshal(x)
				b = append(append(b, line...), '\n')
			}
			os.WriteFile(path, b, 0o600)
		}
		return
	}
	line, _ := json.Marshal(e)
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		f.Write(append(line, '\n'))
		f.Close()
	}
}

// Timeline is the events, newest first, at most n (all when n <= 0).
func Timeline(n int) []Event {
	tlMu.Lock()
	defer tlMu.Unlock()
	loadTimeline()
	out := make([]Event, 0, len(tlEvents))
	for i := len(tlEvents) - 1; i >= 0; i-- {
		out = append(out, tlEvents[i])
	}
	if len(out) > timelineMax {
		out = out[:timelineMax]
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// ClearTimeline forgets every event.
func ClearTimeline() {
	tlMu.Lock()
	defer tlMu.Unlock()
	tlEvents, tlLoaded = nil, true
	if p := timelineFile(); p != "" {
		os.Remove(p)
	}
}

// connectedText describes a connection that just came up.
func connectedText(s Status, reason string) string {
	t := "Connected"
	if s.Mode == "tun" {
		t += " (VPN)"
	} else {
		t += " (proxy)"
	}
	if s.Selected == "" {
		t += " · Auto"
		if s.ActiveName != "" {
			t += ", starting with " + s.ActiveName
		}
	} else if s.ActiveName != "" {
		t += " · " + s.ActiveName
	}
	if why := map[string]string{"boot": "the phone started", "wifi": "an untrusted Wi-Fi", "tile": "the Quick Settings tile",
		"widget": "the widget", "always-on": "Android's always-on VPN", "resume": "the pause ended", "launch": "the app opened",
		"auto": "the app opened"}[reason]; why != "" {
		t += " — " + why
	}
	return t
}
