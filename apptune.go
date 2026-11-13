package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/chethober/xtune/pkg/protocols"
	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// Tuning from the xtune app: the app holds the server's SSH login but no Xray binary of its own to
// drive, and it is on the network that matters (its user's). So the server starts the test matrix
// itself (`xtune tune --prepare`, no measuring) and prints it as a client profile, one config per
// variant; the app measures every config with its own engine and sends back a report, which
// `xtune deploy REPORT` deploys as it would a tester's.

// TestProfile is a prepared tune as the app measures it. Each config's Key is its variant's label,
// which the report's results carry back.
type TestProfile struct {
	ID      string                  `json:"id"`
	Expires int64                   `json:"expires"`
	Profile protocols.ClientProfile `json:"profile"`
	Skipped []string                `json:"skipped,omitempty"` // variants an app cannot measure
}

// prepareTune runs on the server: it starts the test matrix for the protocols asked for (and the
// deployed ones) and prints its TestProfile on stdout. Progress goes to stderr.
func prepareTune(c connFlags, o TuneOpts, set map[string]bool) error {
	if *c.host != "" || *c.profile != "" || !remote.Installed() {
		return errors.New("tune --prepare runs on the server (the xtune app calls it over SSH); on a computer, run xtune tune")
	}
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	defer r.Close()
	old, err := requireState(r)
	if err != nil {
		return err
	}
	r.Conn.Host = old.Host // the address clients reach, not this machine's host name
	o.inherit(old, set)
	o.Prepare, o.KeepTest = true, true
	o.CleanIPs = 0 // edge IPs are chosen from the measuring network, which is the app's
	o.Network = orStr(o.Network, "app")
	o.Out = filepath.Join(os.TempDir(), "xtune-prepare")
	o.Cloudflare = orStr(os.Getenv("XTUNE_CLOUDFLARE_TOKEN"), old.Cloudflare.Token)
	if o.Cloudflare == "" && slices.Contains(o.Protocols, "worker") {
		o.Protocols = slices.DeleteFunc(o.Protocols, func(p string) bool { return p == "worker" })
		fmt.Fprintln(os.Stderr, "[!] the Worker relay needs a Cloudflare token (xtune cloudflare token); skipping it")
	}
	o.XrayBin = remote.Dir + "/xray"
	if _, err := os.Stat(o.XrayBin); err != nil {
		if o.XrayBin, err = EnsureLocalXray("", r, func(s string) { fmt.Fprintln(os.Stderr, s) }); err != nil {
			return err
		}
	}
	// everything Tune and its helpers print is progress: keep stdout for the profile
	out := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = out }()
	o.Log = func(s string) { fmt.Fprintln(os.Stderr, s) }
	res, err := Tune(r, o)
	if err != nil {
		r.Helper("stop-test")
		return err
	}
	tp, err := PrepareProfile(r, res, o)
	if err != nil {
		r.Helper("stop-test")
		return err
	}
	b, _ := json.Marshal(tp)
	_, err = fmt.Fprintln(out, string(b))
	return err
}

// PrepareProfile keeps the prepared tune on the server for the deploy, like Share, and returns
// what the app measures.
func PrepareProfile(r *remote.Server, res *TuneResult, o TuneOpts) (*TestProfile, error) {
	now := time.Now()
	p := Pending{ID: randHex(8), Created: now.Unix(), Network: o.Network, Prepared: true, State: res.State, Variants: res.All}
	b, _ := json.Marshal(p)
	if err := r.Upload(pendingFile, b, "600"); err != nil {
		return nil, err
	}
	return testProfile(p.ID, now.Add(testLifetime).Unix(), res.State, res.User, res.All), nil
}

// testProfile is the variants as configs for user u (the test user), each keyed by its label.
func testProfile(id string, expires int64, st *state.State, u state.User, vs []*Variant) *TestProfile {
	tp := &TestProfile{ID: id, Expires: expires, Profile: protocols.ClientProfile{V: protocols.ClientProfileVersion,
		Name: "xtune test", User: u.Name, Country: st.Country, Probe: protocols.ProbeURL, Frag: protocols.FragOutbounds()}}
	for i, v := range vs {
		if v.Entry.HandshakeOnly() { // OpenVPN and IKEv2 run in other apps
			tp.Skipped = append(tp.Skipped, v.Label)
			continue
		}
		tag := "proxy-" + strconv.Itoa(i)
		c, ok := protocols.ClientEntryFor(st, v.Entry, u, v.K, tag)
		if !ok {
			tp.Skipped = append(tp.Skipped, v.Label)
			continue
		}
		c.ID, c.Key, c.Name = v.Entry.ID+"#"+strconv.Itoa(i), v.Label, v.Label
		tp.Profile.Configs = append(tp.Profile.Configs, c)
	}
	return tp
}

// discardTest stops a prepared or shared test on this server and forgets it.
func discardTest() error {
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Root("rm -f " + remote.Quote(pendingFile)); err != nil {
		return err
	}
	_, err = r.Helper("stop-test")
	if err == nil {
		fmt.Println("test stopped")
	}
	return err
}

// withFamilies adds, for each protocol family the chosen configs lack, the best of that family
// that worked on some network: a server tuned with SSH or AmneziaWG keeps one, like a single
// network's deploy does (deployChoice).
func withFamilies(chosen, vs []*Variant, nets []netScores) []*Variant {
	have := map[string]bool{}
	for _, v := range chosen {
		have[v.Entry.Family()] = true
	}
	total := func(v *Variant) float64 {
		t := 0.0
		for _, ns := range nets {
			t += ns.Score[v.Label]
		}
		return t
	}
	best := map[string]*Variant{}
	var order []string
	for _, v := range vs {
		f := v.Entry.Family()
		if f == "" || have[f] || total(v) <= 0 {
			continue
		}
		if b, ok := best[f]; !ok || total(v) > total(b) {
			if !ok {
				order = append(order, f)
			}
			best[f] = v
		}
	}
	for _, f := range order {
		chosen = append(chosen, best[f])
	}
	return chosen
}
