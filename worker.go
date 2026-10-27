package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
)

// The Cloudflare Worker relay: clients speak VLESS (with VLESS Encryption) over WebSocket + TLS
// to a Worker, on its workers.dev hostname or a hostname on one of the user's zones; the Worker
// opens a TCP connection to a worker-ws inbound on the server (cloudflare:sockets) and pipes the
// bytes. Clients never see the server's IP, users and traffic stay on the server, and the
// Worker holds no secrets beyond the routes. xtune creates and updates it with the Cloudflare
// token (Account: Workers Scripts:Edit, besides the DNS permissions).

//go:embed worker.js
var workerJS string

// Worker is the deployment's Cloudflare Worker.
type Worker struct {
	Account string            `json:"account"`
	Name    string            `json:"name"`
	Host    string            `json:"host"`             // NAME.SUBDOMAIN.workers.dev
	Domain  string            `json:"domain,omitempty"` // a hostname on one of the user's zones
	Routes  map[string]string `json:"routes,omitempty"` // path -> origin IP:port, as last uploaded
}

// workerPath is the WebSocket path clients use: the entry's path, with Xray early data (the
// first bytes go in a header, saving a round trip) that the Worker decodes.
func workerPath(e Entry) string { return e.Path + "?ed=2560" }

// workerRoutes maps each worker-ws entry's path to its inbound on the server at ip.
func workerRoutes(entries []Entry, ip string) map[string]string {
	routes := map[string]string{}
	for _, e := range entries {
		if e.Worker() {
			routes[e.Path] = hostPort(ip, e.Port)
		}
	}
	return routes
}

// randLabel is a DNS label that says nothing about what it is for.
func randLabel() string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	const alnum = letters + "0123456789"
	b := []byte{letters[randInt(0, len(letters)-1)]}
	for range 6 {
		b = append(b, alnum[randInt(0, len(alnum)-1)])
	}
	return string(b)
}

// cfZoneInfo finds the zone a domain belongs to, with its name and account.
func cfZoneInfo(token, domain string) (id, name, account string, err error) {
	labels := strings.Split(strings.TrimSuffix(domain, "."), ".")
	for i := 0; i+2 <= len(labels); i++ {
		var zones []struct {
			ID, Name string
			Account  struct{ ID string }
		}
		n := strings.Join(labels[i:], ".")
		if err := cfCall(token, "GET", "/zones?name="+url.QueryEscape(n), nil, &zones); err != nil {
			return "", "", "", err
		}
		if len(zones) > 0 {
			return zones[0].ID, zones[0].Name, zones[0].Account.ID, nil
		}
	}
	return "", "", "", fmt.Errorf("cloudflare: no zone for %s that this token can see", domain)
}

// cfAccount is the account to put the Worker in: the zone's, when there is a domain, or the
// token's only account.
func cfAccount(token, domain string) (string, error) {
	if domain != "" {
		_, _, acc, err := cfZoneInfo(token, domain)
		if err == nil && acc != "" {
			return acc, nil
		}
	}
	var accs []struct{ ID, Name string }
	if err := cfCall(token, "GET", "/accounts", nil, &accs); err != nil {
		return "", err
	}
	switch len(accs) {
	case 0:
		return "", errors.New("cloudflare: the token can see no account (it needs Account: Workers Scripts:Edit)")
	case 1:
		return accs[0].ID, nil
	}
	return "", errors.New("cloudflare: the token can see several accounts: give a domain (--zone) to pick one")
}

// cfWorkerUpload uploads the Worker script with these routes.
func cfWorkerUpload(token string, w *Worker, routes map[string]string) error {
	rj, _ := json.Marshal(routes)
	meta, _ := json.Marshal(J{"main_module": "worker.js", "compatibility_date": "2024-09-23",
		"bindings": []J{{"type": "plain_text", "name": "ROUTES", "text": string(rj)}}})
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="metadata"`)
	h.Set("Content-Type", "application/json")
	p, _ := mw.CreatePart(h)
	p.Write(meta)
	h = textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="worker.js"; filename="worker.js"`)
	h.Set("Content-Type", "application/javascript+module")
	p, _ = mw.CreatePart(h)
	p.Write([]byte(workerJS))
	mw.Close()
	if err := cfDo(token, "PUT", "/accounts/"+w.Account+"/workers/scripts/"+w.Name, mw.FormDataContentType(), &body, nil); err != nil {
		return err
	}
	w.Routes = routes
	return nil
}

// cfWorkersSubdomain is the account's workers.dev subdomain, registered first if it has none.
func cfWorkersSubdomain(token, account string) (string, error) {
	var s struct{ Subdomain string }
	if err := cfCall(token, "GET", "/accounts/"+account+"/workers/subdomain", nil, &s); err == nil && s.Subdomain != "" {
		return s.Subdomain, nil
	}
	name := randLabel()
	if err := cfCall(token, "PUT", "/accounts/"+account+"/workers/subdomain", J{"subdomain": name}, &s); err != nil {
		return "", fmt.Errorf("registering a workers.dev subdomain: %w", err)
	}
	return orStr(s.Subdomain, name), nil
}

// cfWorkerEnsure creates the Worker if w is nil, or brings it back if it was deleted, keeping
// the routes it had; puts it on workers.dev; and serves it on domain too, when set (Cloudflare
// makes the DNS record). accountHint is a domain whose zone's account to use.
func cfWorkerEnsure(token string, w *Worker, accountHint, domain string) (*Worker, error) {
	if w == nil {
		acc, err := cfAccount(token, accountHint)
		if err != nil {
			return nil, err
		}
		w = &Worker{Account: acc, Name: randLabel()}
	} else {
		cp := *w
		w = &cp
	}
	if err := cfWorkerUpload(token, w, orMap(w.Routes)); err != nil {
		return nil, err
	}
	sub, err := cfWorkersSubdomain(token, w.Account)
	if err != nil {
		return nil, err
	}
	if err := cfCall(token, "POST", "/accounts/"+w.Account+"/workers/scripts/"+w.Name+"/subdomain",
		J{"enabled": true, "previews_enabled": false}, nil); err != nil {
		return nil, fmt.Errorf("enabling workers.dev: %w", err)
	}
	w.Host = w.Name + "." + sub + ".workers.dev"
	if domain != "" {
		zone, _, _, err := cfZoneInfo(token, domain)
		if err != nil {
			return nil, err
		}
		if err := cfCall(token, "PUT", "/accounts/"+w.Account+"/workers/domains",
			J{"hostname": domain, "service": w.Name, "environment": "production", "zone_id": zone}, nil); err != nil {
			return nil, fmt.Errorf("serving the Worker on %s: %w", domain, err)
		}
		w.Domain = domain
	}
	return w, nil
}

// cfWorkerDelete removes the Worker, with its workers.dev route and custom domain.
func cfWorkerDelete(token string, w *Worker) error {
	return cfCall(token, "DELETE", "/accounts/"+w.Account+"/workers/scripts/"+w.Name+"?force=true", nil, nil)
}

// syncWorker points the Worker's routes at the deployed worker-ws inbounds, when they changed
// (new ports or paths after a deploy, a new server IP after a migrate).
func syncWorker(st *State) error {
	if st.Worker == nil || st.Cloudflare.Token == "" {
		return nil
	}
	routes := map[string]string{}
	if slices.ContainsFunc(st.Entries, Entry.Worker) {
		ip, err := serverIP(st.Host)
		if err != nil {
			return err
		}
		routes = workerRoutes(st.Entries, ip)
	}
	if maps.Equal(routes, st.Worker.Routes) {
		return nil
	}
	if err := cfWorkerUpload(st.Cloudflare.Token, st.Worker, routes); err != nil {
		return fmt.Errorf("updating the Cloudflare Worker: %w", err)
	}
	return nil
}

func orMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// deployedRoutes are the Worker's routes for the deployment, without the test's.
func (o *TuneOpts) deployedRoutes() map[string]string {
	if o.deployed == nil || o.deployed.Worker == nil {
		return map[string]string{}
	}
	return maps.Clone(o.deployed.Worker.Routes)
}

// prepareWorker creates the Worker, or updates the deployment's, and routes the test's
// worker-ws entries through it: "worker" on workers.dev, "worker-dom" on a hostname of the zone
// (--zone, or the CDN or direct domain's), when there is one. Without a Worker, they are dropped.
func (o *TuneOpts) prepareWorker(host string, entries []Entry) []Entry {
	if !slices.ContainsFunc(entries, Entry.Worker) {
		return entries
	}
	drop := func(err error) []Entry {
		o.Log("[!] Cloudflare Worker: " + err.Error() + "; skipping the Worker relay")
		return filterEntries(entries, func(e Entry) bool { return !e.Worker() })
	}
	var w *Worker
	if o.deployed != nil {
		w = o.deployed.Worker
	}
	zone, hint := o.Zone, orStr(o.CDNDomain, o.Domain)
	if zone == "" && hint != "" {
		if _, name, _, err := cfZoneInfo(o.Cloudflare, hint); err == nil {
			zone = name
		}
	}
	domain := ""
	if w != nil && w.Domain != "" {
		domain = w.Domain
	} else if zone != "" {
		domain = randLabel() + "." + zone
	}
	w, err := cfWorkerEnsure(o.Cloudflare, w, orStr(zone, hint), domain)
	if err != nil {
		return drop(err)
	}
	ip, err := serverIP(host)
	if err != nil {
		return drop(err)
	}
	routes := o.deployedRoutes()
	var keep []Entry
	for _, e := range entries {
		if e.Worker() {
			e.SNI = w.Host
			if e.ID == "worker-dom" {
				if w.Domain == "" {
					continue
				}
				e.SNI = w.Domain
			}
			routes[e.Path] = hostPort(ip, e.Port)
		}
		keep = append(keep, e)
	}
	if err := cfWorkerUpload(o.Cloudflare, w, routes); err != nil {
		return drop(err)
	}
	o.Log("[*] Cloudflare Worker: " + strings.Join(slices.DeleteFunc([]string{w.Host, w.Domain}, func(s string) bool { return s == "" }), ", "))
	o.worker = w
	return keep
}

// WorkerUse is one Worker's request count today, as Cloudflare's analytics report it.
type WorkerUse struct {
	Host     string `json:"host"`
	Requests int64  `json:"requests"`
}
