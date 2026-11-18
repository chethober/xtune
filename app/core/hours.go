package core

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
)

// Time-of-day ranking on the device. An xtune subscription server ranks configs per network and
// per time of day itself (the app sends its hour). Other subscriptions have nobody to rank them,
// so the app keeps what its own checks found: per network, per block of the day (four hours),
// per config. Auto then starts from the configs that worked at this time of day here.

const (
	hourBlocks = 6
	hourFresh  = 30 * 24 * time.Hour
	hourKeep   = 64 // networks kept
)

type hourScore struct {
	Score float64 `json:"s"` // 0 = blocked then
	At    int64   `json:"t"`
}

// hourFile: ASN ("" when not known) -> block ("0".."5") -> config key -> score
type hourFile map[string]map[string]map[string]hourScore

var hoursMu sync.Mutex

func hourBlock(t time.Time) string { return string(rune('0' + t.Hour()/(24/hourBlocks))) }

func hoursPath() string {
	dataMu.Lock()
	defer dataMu.Unlock()
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "hours.json")
}

func loadHours(path string) hourFile {
	h := hourFile{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &h)
	}
	return h
}

// configKey names a config across subscription refreshes.
func configKey(c protocols.ClientEntry) string {
	if c.Key != "" {
		return c.Key
	}
	return c.ID
}

// resultScore: reliability first, then latency and speed, like the server's scores (0 = blocked).
func resultScore(r Result) float64 {
	if r.N <= 0 || r.Ok <= 0 {
		return 0
	}
	s := 100 * float64(r.Ok) / float64(r.N)
	if r.Warm > 0 {
		s += 20 * math.Max(0, 1-float64(r.Warm)/3000)
	}
	s += math.Min(r.Down, 50) / 5
	return math.Round(s*100) / 100
}

// recordHours keeps a check's results for this time of day. Results are by config ID, which is
// the key for subscriptions without one.
func recordHours(asn string, results []Result, now time.Time) {
	path := hoursPath()
	if path == "" {
		return
	}
	hoursMu.Lock()
	defer hoursMu.Unlock()
	h := loadHours(path)
	net := h[asn]
	if net == nil {
		net = map[string]map[string]hourScore{}
		h[asn] = net
	}
	b := hourBlock(now)
	blk := net[b]
	if blk == nil {
		blk = map[string]hourScore{}
		net[b] = blk
	}
	changed := false
	for _, r := range results {
		if r.State == "skipped" || r.State == "testing" || r.N <= 0 {
			continue
		}
		k := r.Key
		if k == "" {
			k = r.ID
		}
		s := resultScore(r)
		if old, ok := blk[k]; ok && now.Sub(time.Unix(old.At, 0)) < hourFresh {
			s = math.Round((old.Score+s)/2*100) / 100 // one check moves it halfway
		}
		blk[k] = hourScore{Score: s, At: now.Unix()}
		changed = true
	}
	if !changed {
		return
	}
	pruneHours(h, now)
	if b, err := json.Marshal(h); err == nil {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			os.Rename(tmp, path)
		}
	}
}

func pruneHours(h hourFile, now time.Time) {
	newest := map[string]int64{}
	for asn, net := range h {
		for b, blk := range net {
			for k, s := range blk {
				if now.Sub(time.Unix(s.At, 0)) > hourFresh {
					delete(blk, k)
				} else if s.At > newest[asn] {
					newest[asn] = s.At
				}
			}
			if len(blk) == 0 {
				delete(net, b)
			}
		}
		if len(net) == 0 {
			delete(h, asn)
		}
	}
	if len(h) > hourKeep {
		asns := make([]string, 0, len(h))
		for a := range h {
			asns = append(asns, a)
		}
		sort.Slice(asns, func(i, j int) bool { return newest[asns[i]] > newest[asns[j]] })
		for _, a := range asns[hourKeep:] {
			delete(h, a)
		}
	}
}

// byHour orders configs for Auto by what worked at this time of day on this network: those that
// worked first (best first), then those not checked at this time, then those blocked at this time.
// It keeps the order when the device has checked none of them at this time.
func byHour(configs []protocols.ClientEntry, asn string, now time.Time) []protocols.ClientEntry {
	path := hoursPath()
	if path == "" || len(configs) < 2 {
		return configs
	}
	hoursMu.Lock()
	blk := loadHours(path)[trimAS(asn)][hourBlock(now)]
	hoursMu.Unlock()
	rank := func(c protocols.ClientEntry) float64 {
		s, ok := blk[configKey(c)]
		if !ok {
			s, ok = blk[c.ID]
		}
		switch {
		case !ok || now.Sub(time.Unix(s.At, 0)) > hourFresh:
			return -1
		case s.Score == 0:
			return -2
		}
		return s.Score
	}
	known := false
	for _, c := range configs {
		if rank(c) != -1 {
			known = true
			break
		}
	}
	if !known {
		return configs
	}
	out := append([]protocols.ClientEntry{}, configs...)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) > rank(out[j]) })
	return out
}
