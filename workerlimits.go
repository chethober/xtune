package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chethober/xtune/internal/cloudflare"
	"github.com/chethober/xtune/internal/remote"
	"github.com/chethober/xtune/internal/state"
)

// The Workers free plan allows 100,000 requests a day per account, and every connection through
// the relay is a request. Hourly, the enforce job reads each Worker's count from Cloudflare's
// analytics and alerts at 80% and 100% of the daily limit. More Workers, in other Cloudflare
// accounts, relay to the same inbound (xtune cloudflare worker add): subscriptions list a config
// for each, and the failover config moves to another when one runs out.

const (
	workerFreeLimit  = 100000 // requests a day on the Workers free plan
	workerCheckEvery = time.Hour
)

// workerDailyLimit is the requests a day the alerts measure against; 0 means no alerts.
func workerDailyLimit(w *state.Worker) int64 {
	switch {
	case w.Limit < 0:
		return 0
	case w.Limit > 0:
		return w.Limit
	}
	return workerFreeLimit
}

// workerRef is one Worker to count, with the token that can read its account.
type workerRef struct{ token, account, name, host string }

func workerRefs(st *state.State) []workerRef {
	w := st.Worker
	if w == nil {
		return nil
	}
	refs := []workerRef{{st.Cloudflare.Token, w.Account, w.Name, w.Host}}
	for _, x := range w.Extra {
		refs = append(refs, workerRef{x.Token, x.Account, x.Name, x.Host})
	}
	return refs
}

// cfGraphQLAPI is a variable so tests can point it at a local server.
var cfGraphQLAPI = "https://api.cloudflare.com/client/v4/graphql"

// cfWorkerRequests is a Worker's request count since since, from Cloudflare's analytics.
func cfWorkerRequests(token, account, script string, since, until time.Time) (int64, error) {
	q := `query($a: String!, $s: String!, $since: Time!, $until: Time!) { viewer { accounts(filter: {accountTag: $a}) {
  workersInvocationsAdaptive(limit: 1000, filter: {scriptName: $s, datetime_geq: $since, datetime_leq: $until}) { sum { requests } } } } }`
	body, _ := json.Marshal(J{"query": q, "variables": J{"a": account, "s": script,
		"since": since.UTC().Format(time.RFC3339), "until": until.UTC().Format(time.RFC3339)}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", cfGraphQLAPI, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var res struct {
		Data struct {
			Viewer struct {
				Accounts []struct {
					W []struct {
						Sum struct {
							Requests int64 `json:"requests"`
						} `json:"sum"`
					} `json:"workersInvocationsAdaptive"`
				} `json:"accounts"`
			} `json:"viewer"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&res); err != nil {
		return 0, fmt.Errorf("cloudflare analytics: HTTP %d", resp.StatusCode)
	}
	if len(res.Errors) > 0 {
		var msgs []string
		for _, e := range res.Errors {
			msgs = append(msgs, e.Message)
		}
		return 0, fmt.Errorf("cloudflare analytics (the token needs Account Analytics:Read): %s", strings.Join(msgs, "; "))
	}
	var n int64
	for _, a := range res.Data.Viewer.Accounts {
		for _, w := range a.W {
			n += w.Sum.Requests
		}
	}
	return n, nil
}

// workerUsageDue reports whether the hourly count is due.
func workerUsageDue(st *state.State, us *state.Usage, now time.Time) bool {
	return st.Worker != nil && workerDailyLimit(st.Worker) > 0 && st.Cloudflare.Token != "" &&
		now.Unix()-us.WorkerAt >= int64(workerCheckEvery/time.Second)
}

// checkWorkerUsage counts today's (UTC) requests per Worker and alerts near the daily limit.
func checkWorkerUsage(st *state.State, us *state.Usage, now time.Time) error {
	us.WorkerAt = now.Unix()
	day := now.UTC().Truncate(24 * time.Hour)
	limit := workerDailyLimit(st.Worker)
	var use []state.WorkerUse
	var errs []string
	for _, w := range workerRefs(st) {
		n, err := cfWorkerRequests(w.token, w.account, w.name, day, now)
		if err != nil {
			errs = append(errs, w.host+": "+err.Error())
			continue
		}
		use = append(use, state.WorkerUse{Host: w.host, Requests: n})
		pct := n * 100 / limit
		key := "worker/" + w.host + "/" + day.Format("0102")
		alert(st, us, key+"/80", pct >= 80 && pct < 100, fmt.Sprintf("the Cloudflare Worker %s has used %d%% of its %s requests today (%s); "+
			"it stops relaying at the limit until 00:00 UTC. Spread the load: xtune cloudflare worker add (another account)", w.host, pct, fmtCount(limit), fmtCount(n)))
		alert(st, us, key+"/100", pct >= 100, fmt.Sprintf("the Cloudflare Worker %s reached its %s requests today: its configs fail until 00:00 UTC "+
			"(users' failover configs move to others). Add Workers in other accounts: xtune cloudflare worker add", w.host, fmtCount(limit)))
	}
	us.WorkerUse = use
	for k := range us.Notified { // yesterday's alerts
		if strings.HasPrefix(k, "worker/") && !strings.Contains(k, "/"+day.Format("0102")+"/") {
			delete(us.Notified, k)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func fmtCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// ---------------------------------------------------------------- commands

// addExtraWorker creates a Worker in the account token can see and points it at the deployment's
// worker-ws inbounds.
func addExtraWorker(r *remote.Server, token string, log func(string)) (*state.State, *state.WorkerExtra, error) {
	if err := cloudflare.Verify(token); err != nil {
		return nil, nil, err
	}
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, nil, err
	}
	if st.Worker == nil {
		return nil, nil, errors.New("no Worker relay yet: tune with --protocols worker first")
	}
	acc, err := cfAccount(token, "")
	if err != nil {
		return nil, nil, err
	}
	if acc == st.Worker.Account {
		return nil, nil, errors.New("this token's account already has the Worker: the daily limit is per account, so use another account")
	}
	for _, x := range st.Worker.Extra {
		if x.Account == acc {
			return nil, nil, fmt.Errorf("that account already has a Worker for this server (%s)", x.Host)
		}
	}
	w, err := cfWorkerEnsure(token, &state.Worker{Account: acc, Name: randLabel(), Routes: st.Worker.Routes}, "", "")
	if err != nil {
		return nil, nil, err
	}
	x := &state.WorkerExtra{Token: token, Account: acc, Name: w.Name, Host: w.Host}
	st.Worker.Extra = append(st.Worker.Extra, x)
	log("Worker " + x.Host + " relays to this server too")
	return st, x, state.Save(r, st)
}

// removeExtraWorker deletes an extra Worker by its hostname.
func removeExtraWorker(r *remote.Server, host string) error {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if st.Worker == nil {
		return errors.New("no Worker relay")
	}
	for i, x := range st.Worker.Extra {
		if x.Host == host || x.Name == host {
			if err := cfWorkerDelete(x.Token, &state.Worker{Account: x.Account, Name: x.Name}); err != nil {
				return err
			}
			st.Worker.Extra = append(st.Worker.Extra[:i], st.Worker.Extra[i+1:]...)
			return state.Save(r, st)
		}
	}
	return fmt.Errorf("no extra Worker %q (xtune cloudflare worker status)", host)
}

// setWorkerLimit sets the requests a day the alerts measure against (0 = the free plan's, -1 = none).
func setWorkerLimit(r *remote.Server, n int64) error {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if st.Worker == nil {
		return errors.New("no Worker relay")
	}
	st.Worker.Limit = n
	return state.Save(r, st)
}
