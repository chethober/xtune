package core

import "errors"

// Control from outside the app (Android's notification): the app adopts what changed here by
// watching Status.SelRev, as it does ProfileRev for smart reconnect, so a switch the app made
// itself is never mistaken for one to adopt.

var selRev int // under ksMu: bumped when NextConfig switches config

// NextConfig moves the running connection to the config after the one in use, in Auto's order
// (the pool's, or the subscription's ranking), and pins it.
func NextConfig() (Status, error) {
	cur := CurrentStatus()
	if !cur.Running {
		return cur, errors.New("not connected")
	}
	engine.mu.Lock()
	p, o := engine.profile, engine.opts
	engine.mu.Unlock()
	o.Selected = ""
	cands, err := chosen(p, o)
	if err != nil {
		return cur, err
	}
	if len(cands) < 2 {
		return cur, errors.New("there is no other config to switch to")
	}
	next := cands[0]
	for i, c := range cands {
		if c.ID == cur.Active {
			next = cands[(i+1)%len(cands)]
			break
		}
	}
	o.Selected = next.ID
	if err := Start(p, o); err != nil {
		return CurrentStatus(), err
	}
	ksMu.Lock()
	selRev++
	ksMu.Unlock()
	record("switch", "Switched to "+next.Name+" (Next config)")
	return CurrentStatus(), nil
}
