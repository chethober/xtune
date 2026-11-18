package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/chethober/xtune/pkg/remote"
	"github.com/chethober/xtune/pkg/state"
)

// `xtune change ID` gives one deployed config new settings in place: a REALITY config another
// target (SNI), a path-based one a new path, any other a new port. When a config is blocked on
// users' networks while the server's IP still works, that is cheaper than moving the IP or a new
// tune: the filter usually blocks the combination it saw (the SNI, the port, the path), not the
// server. Users' apps get the change on their next subscription update; links sent by hand need
// sending again.

// changeWhat is what to change; none set means the usual for the config's kind (changeAuto).
type changeWhat struct{ SNI, Port, Path bool }

// ChangeResult is what `xtune change --json` prints.
type ChangeResult struct {
	ID      string   `json:"id"`
	Changed []string `json:"changed"` // "sni a.com → b.com", …
	Key     string   `json:"key"`     // the config's new key (the port is in it)
	Auto    bool     `json:"auto"`    // users' apps get it by themselves (subscriptions)
}

// changeAuto is the usual change: what the filter most often keys on for that kind.
func changeAuto(e state.Entry) changeWhat {
	switch {
	case e.Reality():
		return changeWhat{SNI: true}
	case e.Path != "" || e.Svc != "":
		return changeWhat{Path: true}
	}
	return changeWhat{Port: true}
}

// changeable reports whether e can be changed this way, and why not.
func changeable(e state.Entry, w changeWhat) error {
	switch {
	case e.Worker():
		return fmt.Errorf("%s runs through the Cloudflare Worker: change it with xtune cloudflare worker sync", e.ID)
	case e.Kind == "ikev2" || e.Kind == "dnstt":
		return fmt.Errorf("%s (%s) uses fixed ports and nothing else to change: tune again for another protocol", e.ID, e.Kind)
	case e.HandshakeOnly() && e.Kind != "mtproto" && w.Port:
		return fmt.Errorf("%s (%s): its users have profile files with the port in them; send new ones after a tune", e.ID, e.Kind)
	case w.SNI && !e.Reality():
		return fmt.Errorf("%s is not a REALITY config: it has no target (SNI) to change", e.ID)
	case w.Path && e.Path == "" && e.Svc == "":
		return fmt.Errorf("%s has no path", e.ID)
	}
	return nil
}

// newPort picks a free port for e: for a CDN config one of the few the CDN passes through.
func newPort(e state.Entry, st *state.State, used map[string]map[int]bool) (int, error) {
	taken := map[int]bool{}
	for _, o := range st.Entries {
		if o.Proto() == e.Proto() {
			taken[o.Port] = true
		}
	}
	if st.Sub.Mode != "" {
		taken[st.Sub.Port] = true
	}
	free := func(p int) bool { return p != e.Port && !taken[p] && !used[e.Proto()][p] }
	if e.CDN() {
		for _, p := range cdnPorts {
			if free(p) {
				return p, nil
			}
		}
		return 0, errors.New("every port the CDN passes through is taken")
	}
	for range 200 {
		if p := 20000 + rand.IntN(40000); free(p) && (e.Hop == "" || !inHop(e.Hop, p)) {
			return p, nil
		}
	}
	return 0, errors.New("no free port found")
}

func inHop(hop string, p int) bool {
	var a, b int
	if n, _ := fmt.Sscanf(hop, "%d-%d", &a, &b); n == 2 {
		return p >= a && p <= b
	}
	return false
}

// newSNI picks the fastest REALITY target that answers from the server, other than the ones in use.
func newSNI(r *remote.Server, st *state.State) (string, error) {
	var inUse []string
	for _, e := range st.Entries {
		if e.Reality() {
			inUse = append(inUse, e.SNI)
		}
	}
	var cands []string
	for _, c := range sniCandidates {
		if !slices.Contains(inUse, c) {
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return "", errors.New("no other REALITY target to move to")
	}
	out, err := r.Helper(append([]string{"probe"}, cands...)...)
	if err != nil {
		return "", err
	}
	res := parseSNIProbe(out)
	var ok []string
	for _, c := range cands {
		if rs := res[c]; rs != nil && rs.ok > 0 {
			ok = append(ok, c)
		}
	}
	if len(ok) == 0 {
		return "", errors.New("none of the other REALITY targets answers from the server")
	}
	sort.SliceStable(ok, func(i, j int) bool { return res[ok[i]].median() < res[ok[j]].median() })
	return ok[0], nil
}

// planChange changes e (in st) as w says, with the free ports and targets the helpers find.
func planChange(st *state.State, id string, w changeWhat, port func(state.Entry) (int, error), sni func() (string, error)) (*ChangeResult, error) {
	i := slices.IndexFunc(st.Entries, func(e state.Entry) bool { return e.ID == id })
	if i < 0 {
		var ids []string
		for _, e := range st.Entries {
			ids = append(ids, e.ID)
		}
		return nil, fmt.Errorf("no config %q (deployed: %s)", id, strings.Join(ids, ", "))
	}
	e := &st.Entries[i]
	if w == (changeWhat{}) {
		w = changeAuto(*e)
	}
	if err := changeable(*e, w); err != nil {
		return nil, err
	}
	res := &ChangeResult{ID: e.ID, Auto: autoSNI(st)}
	if w.SNI {
		s, err := sni()
		if err != nil {
			return nil, err
		}
		res.Changed = append(res.Changed, fmt.Sprintf("sni %s → %s", e.SNI, s))
		e.SNI = s
	}
	if w.Path {
		if e.Path != "" {
			p := "/" + randHex(6)
			res.Changed = append(res.Changed, fmt.Sprintf("path %s → %s", e.Path, p))
			e.Path = p
		}
		if e.Svc != "" {
			s := randHex(6)
			res.Changed = append(res.Changed, fmt.Sprintf("service %s → %s", e.Svc, s))
			e.Svc = s
		}
	}
	if w.Port {
		p, err := port(*e)
		if err != nil {
			return nil, err
		}
		res.Changed = append(res.Changed, fmt.Sprintf("port %d → %d", e.Port, p))
		e.Port = p
	}
	res.Key = state.EntryKey(*e)
	return res, nil
}

// ChangeEntry changes one config on the server and restarts what serves it.
func ChangeEntry(r *remote.Server, id string, w changeWhat) (*ChangeResult, error) {
	defer r.Lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	var used map[string]map[int]bool
	res, err := planChange(st, id, w,
		func(e state.Entry) (int, error) {
			if used == nil {
				out, err := r.Helper("probe")
				if err != nil {
					return 0, err
				}
				used = usedPorts(out)
			}
			return newPort(e, st, used)
		},
		func() (string, error) { return newSNI(r, st) })
	if err != nil {
		return nil, err
	}
	if err := Apply(r, st); err != nil {
		return nil, err
	}
	return res, nil
}

func cmdChange(args []string) error {
	if err := serverOnly("change", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("change", flag.ExitOnError)
	sni := fs.Bool("sni", false, "a new REALITY target")
	port := fs.Bool("port", false, "a new port")
	path := fs.Bool("path", false, "a new path (or gRPC service name)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	pos := parse(fs, args)
	if len(pos) != 1 {
		return errors.New("usage: xtune change ID [--sni] [--port] [--path] [--json] (none: the usual change for that config)")
	}
	r, err := remote.OpenLocal()
	if err != nil {
		return err
	}
	res, err := ChangeEntry(r, pos[0], changeWhat{SNI: *sni, Port: *port, Path: *path})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(res)
	}
	fmt.Printf("%s: %s\n", res.ID, strings.Join(res.Changed, ", "))
	if res.Auto {
		fmt.Println("users' apps get it on their next subscription update")
	} else {
		fmt.Println("send users their new links: xtune users links NAME")
	}
	return nil
}
