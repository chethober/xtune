package main

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/chethober/xtune/pkg/netinfo"
	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/state"
)

// The xtune client app's two endpoints on the subscription server, next to a user's
// subscription:
//
//	GET  /PATH/TOKEN/client   the user's client profile (protocols.ClientProfile), ordered for the
//	                          network the app says it is on (?asn=), else the requester's
//	POST /PATH/TOKEN/report   what the app measured on its network, which joins that network's
//	                          ranking, so the next subscriber there gets the working configs first

// ClientReport is what the app sends after checking its configs.
type ClientReport struct {
	ASN     string               `json:"asn"`  // the network it measured from (looked up before connecting)
	Name    string               `json:"name"` // the network's name
	Results []ClientReportResult `json:"results"`
}

type ClientReportResult struct {
	Key  string  `json:"key"` // protocols.ClientEntry.Key
	Ok   int     `json:"ok"`
	N    int     `json:"n"`
	Warm int     `json:"warmMs"` // -1: never answered
	Down float64 `json:"downMbps"`
	Up   float64 `json:"upMbps"`
}

var asnRe = regexp.MustCompile(`^[0-9]{1,10}$`)

// clientNetwork is the network a client app asks for: its own lookup (it may be asking through the
// VPN, where the requester is the server itself), else the requester's.
func clientNetwork(req *http.Request, st *state.State, now time.Time) string {
	if a := req.URL.Query().Get("asn"); asnRe.MatchString(a) {
		return a
	}
	return netinfo.ASNOf(requesterIP(req, st), now)
}

func serveClientProfile(w http.ResponseWriter, req *http.Request, st *state.State, u state.User, uu *state.UserUsage, now time.Time) {
	view, netName := st, ""
	if len(st.Networks) > 0 {
		view, netName = forNetwork(st, clientNetwork(req, st, now), now)
	}
	p := protocols.ClientProfileFor(view, u)
	p.Network = netName
	p.Usage = protocols.ClientUsage{Up: uu.Up, Down: uu.Down, Quota: u.Quota, Expires: u.Expires, Devices: u.Devices,
		Page: SubURL(st, u), Disabled: !u.Enabled, Resets: u.ResetAt}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

// reportLimit is how often one user's reports for one network count.
const reportLimit = 10 * time.Minute

var lastReports sync.Map // user + "/" + ASN -> time.Time

// serveClientReport folds a client's measurements into its network's ranking. One user's reports
// count at most every reportLimit per network, and each moves a score only halfway to what it
// measured, so a single user can neither flood nor overturn what tunes and checks found.
func (h subHandler) serveClientReport(w http.ResponseWriter, req *http.Request, u state.User, now time.Time) {
	var in ClientReport
	if err := json.NewDecoder(io.LimitReader(req.Body, 64<<10)).Decode(&in); err != nil || !asnRe.MatchString(in.ASN) ||
		len(in.Name) > 80 || len(in.Results) == 0 || len(in.Results) > 200 {
		http.Error(w, "bad report", http.StatusBadRequest)
		return
	}
	key := u.Name + "/" + in.ASN
	if t, ok := lastReports.Load(key); ok && now.Sub(t.(time.Time)) < reportLimit {
		w.WriteHeader(http.StatusAccepted) // fine, but too soon to count again
		return
	}
	if h.r == nil {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := applyClientReport(h.r.Lock, func() (*state.State, error) { return requireState(h.r) },
		func(st *state.State) error { return state.Save(h.r, st) }, in, now); err != nil {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	lastReports.Store(key, now)
	w.WriteHeader(http.StatusNoContent)
}

func applyClientReport(lock func() func(), load func() (*state.State, error), save func(*state.State) error, in ClientReport, now time.Time) error {
	defer lock()()
	st, err := load()
	if err != nil {
		return err
	}
	keys := map[string]bool{}
	for _, e := range st.Entries {
		keys[state.EntryKey(e)] = true
	}
	if st.Networks == nil {
		st.Networks = map[string]*state.NetRank{}
	}
	nr := st.Networks[in.ASN]
	fresh := nr != nil && now.Sub(time.Unix(nr.Updated, 0)) <= netRankFresh
	if nr == nil {
		nr = &state.NetRank{Scores: map[string]float64{}}
	}
	changed := false
	for _, r := range in.Results {
		if !keys[r.Key] || r.N <= 0 || r.N > 100 || r.Ok < 0 || r.Ok > r.N || r.Down < 0 || r.Up < 0 || r.Down > 10000 || r.Up > 10000 {
			continue
		}
		s := score(&Variant{Ok: r.Ok, N: r.N, Warm: r.Warm, Down: r.Down, Up: r.Up})
		if old, ok := nr.Scores[r.Key]; ok && fresh {
			s = round2((old + s) / 2)
		}
		nr.Scores[r.Key] = s
		changed = true
	}
	if !changed {
		return nil
	}
	if nr.Name == "" && in.Name != "" {
		nr.Name = in.Name
	}
	nr.Updated = now.Unix()
	st.Networks[in.ASN] = nr
	forgetStaleRanks(st)
	return save(st)
}
