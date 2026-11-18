package state

// NetRank is how the deployed configs did from one network.
type NetRank struct {
	Name    string             `json:"name,omitempty"` // the ISP, as the ASN registry names it
	Updated int64              `json:"updated"`
	Scores  map[string]float64 `json:"scores"`          // by EntryKey; 0 = blocked there
	Addrs   map[string]string  `json:"addrs,omitempty"` // by EntryKey: CDN and Worker configs' clean IP there
	// Hours are the same scores by time of day (HourBlock of the measurer's local hour): filtering
	// often changes in the evening, so a config can work in the morning and not at night.
	Hours map[string]*HourRank `json:"hours,omitempty"`
}

// HourRank is one block of the day's scores from a network.
type HourRank struct {
	Updated int64              `json:"updated"`
	Scores  map[string]float64 `json:"scores"` // by EntryKey; 0 = blocked then
}

// HourBlocks is how many blocks the day is split into (four hours each).
const HourBlocks = 6

// HourBlock is the block a local hour (0-23) falls in, as the key of NetRank.Hours; "" for an
// hour out of range (not known).
func HourBlock(hour int) string {
	if hour < 0 || hour > 23 {
		return ""
	}
	return string(rune('0' + hour/(24/HourBlocks)))
}

// SetHour records scores for the block of the day hour falls in.
func (nr *NetRank) SetHour(hour int, scores map[string]float64, updated int64, blend bool) {
	b := HourBlock(hour)
	if b == "" || len(scores) == 0 {
		return
	}
	if nr.Hours == nil {
		nr.Hours = map[string]*HourRank{}
	}
	hr := nr.Hours[b]
	if hr == nil {
		hr = &HourRank{Scores: map[string]float64{}}
		nr.Hours[b] = hr
	}
	for k, s := range scores {
		if old, ok := hr.Scores[k]; ok && blend {
			s = float64(int((old+s)/2*100+0.5)) / 100
		}
		hr.Scores[k] = s
	}
	hr.Updated = updated
}
