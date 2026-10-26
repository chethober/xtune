package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestWorkerConfigs(t *testing.T) {
	st := testState()
	u := User{Name: "bob", UUID: "11111111-2222-4333-8444-555555555555"}
	e := Entry{ID: "worker", Kind: "worker-ws", Port: 31000, Path: "/abc123", SNI: "x7.sub.workers.dev"}
	if e.Proto() != "tcp" || !e.Xray() || e.family() != "worker" || e.CDN() {
		t.Fatalf("kind: %+v", e)
	}
	ib := st.inbound(e, []User{u})
	ss := ib["streamSettings"].(J)
	if ib["settings"].(J)["decryption"] != st.VencDec || ss["network"] != "tcp" || ss["sockopt"].(J)["acceptProxyProtocol"] != true {
		t.Errorf("inbound: VLESS Encryption over plain TCP with the PROXY header: %v", ib)
	}
	ob := st.Outbound(e, u, Knobs{FP: "firefox", Frag: "frag-mild"}, "proxy")
	vn := ob["settings"].(J)["vnext"].([]J)[0]
	oss := ob["streamSettings"].(J)
	if vn["address"] != e.SNI || vn["port"] != 443 || vn["users"].([]J)[0]["encryption"] != st.VencEnc {
		t.Errorf("outbound server: %v", vn)
	}
	if oss["network"] != "ws" || oss["wsSettings"].(J)["path"] != "/abc123?ed=2560" || oss["wsSettings"].(J)["host"] != e.SNI ||
		oss["tlsSettings"].(J)["alpn"].([]string)[0] != "http/1.1" || oss["tlsSettings"].(J)["fingerprint"] != "firefox" ||
		oss["sockopt"].(J)["dialerProxy"] != "frag-mild" {
		t.Errorf("outbound stream: %v", oss)
	}
	l, err := url.Parse(st.Link(e, u, Knobs{FP: "chrome"}))
	if err != nil {
		t.Fatal(err)
	}
	q := l.Query()
	if l.Host != e.SNI+":443" || q.Get("type") != "ws" || q.Get("security") != "tls" || q.Get("sni") != e.SNI ||
		q.Get("host") != e.SNI || q.Get("path") != "/abc123?ed=2560" || q.Get("encryption") != st.VencEnc || q.Get("alpn") != "http/1.1" {
		t.Errorf("link: %s", l)
	}
	if r := workerRoutes([]Entry{e, {ID: "r", Kind: "reality-tcp", Port: 443}}, "203.0.113.7"); len(r) != 1 || r["/abc123"] != "203.0.113.7:31000" {
		t.Errorf("routes: %v", r)
	}
}

// fakeWorkers is the part of the Cloudflare API the Worker relay uses, plus DNS records.
type fakeWorkers struct {
	mu        sync.Mutex
	subdomain string
	scripts   map[string]map[string]string // name -> routes
	enabled   map[string]bool
	domains   map[string]string // hostname -> script
	records   map[string]cfRecord
	ssl       string
	calls     []string
}

func newFakeWorkers() *fakeWorkers {
	return &fakeWorkers{scripts: map[string]map[string]string{}, enabled: map[string]bool{}, domains: map[string]string{},
		records: map[string]cfRecord{}, ssl: "flexible"}
}

func (f *fakeWorkers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	f.calls = append(f.calls, r.Method+" "+p)
	var result any
	fail := func(msg string) {
		json.NewEncoder(w).Encode(J{"success": false, "errors": []J{{"message": msg}}})
	}
	switch {
	case p == "/user/tokens/verify":
		result = J{"status": "active"}
	case p == "/zones":
		result = []J{}
		if n := r.URL.Query().Get("name"); n == "example.com" {
			result = []J{{"id": "z1", "name": "example.com", "account": J{"id": "acc1"}}}
		}
	case p == "/accounts":
		result = []J{{"id": "acc1", "name": "me"}}
	case p == "/accounts/acc1/workers/subdomain":
		if r.Method == "PUT" {
			var b J
			json.NewDecoder(r.Body).Decode(&b)
			f.subdomain = b["subdomain"].(string)
		}
		if f.subdomain == "" {
			fail("no subdomain")
			return
		}
		result = J{"subdomain": f.subdomain}
	case strings.HasPrefix(p, "/accounts/acc1/workers/scripts/") && strings.HasSuffix(p, "/subdomain"):
		f.enabled[strings.Split(p, "/")[5]] = true
	case strings.HasPrefix(p, "/accounts/acc1/workers/scripts/") && r.Method == "DELETE":
		name := strings.Split(p, "/")[5]
		delete(f.scripts, name)
		for h, s := range f.domains {
			if s == name {
				delete(f.domains, h)
			}
		}
	case strings.HasPrefix(p, "/accounts/acc1/workers/scripts/") && r.Method == "PUT":
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		var routes map[string]string
		js := ""
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(part)
			switch part.FormName() {
			case "metadata":
				var meta struct {
					MainModule string `json:"main_module"`
					Bindings   []struct{ Type, Name, Text string }
				}
				json.Unmarshal(b, &meta)
				if meta.MainModule != "worker.js" || len(meta.Bindings) != 1 || meta.Bindings[0].Name != "ROUTES" {
					fail("bad metadata")
					return
				}
				json.Unmarshal([]byte(meta.Bindings[0].Text), &routes)
			case "worker.js":
				js = string(b)
			}
		}
		if !strings.Contains(js, "cloudflare:sockets") || routes == nil {
			fail("bad upload")
			return
		}
		f.scripts[strings.Split(p, "/")[5]] = routes
	case p == "/accounts/acc1/workers/domains":
		var b J
		json.NewDecoder(r.Body).Decode(&b)
		if b["zone_id"] != "z1" {
			fail("bad zone")
			return
		}
		f.domains[b["hostname"].(string)] = b["service"].(string)
	case p == "/zones/z1/dns_records":
		recs := []cfRecord{}
		if r.Method == "POST" {
			var rec cfRecord
			json.NewDecoder(r.Body).Decode(&rec)
			rec.ID = "r" + rec.Name
			f.records[rec.ID] = rec
		} else {
			for _, rec := range f.records {
				if rec.Name == r.URL.Query().Get("name") {
					recs = append(recs, rec)
				}
			}
		}
		result = recs
	case p == "/zones/z1/settings/ssl":
		if r.Method == "PATCH" {
			f.ssl = "full"
		}
		result = J{"value": f.ssl}
	default:
		fail("unexpected " + r.Method + " " + p)
		return
	}
	json.NewEncoder(w).Encode(J{"success": true, "result": result})
}

func withFakeCF(t *testing.T, h http.Handler) {
	srv := httptest.NewServer(h)
	old := cloudflareAPI
	cloudflareAPI = srv.URL
	t.Cleanup(func() { cloudflareAPI = old; srv.Close() })
}

func TestWorkerLifecycle(t *testing.T) {
	f := newFakeWorkers()
	withFakeCF(t, f)
	w, err := cfWorkerEnsure("t", nil, "example.com", "w1.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if w.Account != "acc1" || f.subdomain == "" || w.Host != w.Name+"."+f.subdomain+".workers.dev" || !f.enabled[w.Name] ||
		f.domains["w1.example.com"] != w.Name || w.Domain != "w1.example.com" {
		t.Fatalf("worker %+v, fake %+v", w, f)
	}
	sub := f.subdomain
	if strings.Contains(w.Name+sub, "xtune") {
		t.Error("names should not say what they are for")
	}
	// the deployment's routes follow its entries and its address
	st := testState()
	st.Host = "198.51.100.9"
	st.Cloudflare.Token = "t"
	st.Worker = w
	st.Entries = []Entry{{ID: "worker", Kind: "worker-ws", Port: 31000, Path: "/p1", SNI: w.Host}}
	if err := syncWorker(st); err != nil {
		t.Fatal(err)
	}
	if got := f.scripts[w.Name]; len(got) != 1 || got["/p1"] != "198.51.100.9:31000" || st.Worker.Routes["/p1"] != got["/p1"] {
		t.Errorf("routes uploaded: %v", got)
	}
	n := len(f.calls)
	if err := syncWorker(st); err != nil || len(f.calls) != n {
		t.Errorf("an unchanged deployment uploads nothing: %v %v", err, f.calls[n:])
	}
	st.Host = "198.51.100.10" // migrated
	if err := syncWorker(st); err != nil || f.scripts[w.Name]["/p1"] != "198.51.100.10:31000" {
		t.Errorf("after a move: %v %v", err, f.scripts[w.Name])
	}
	// an existing Worker keeps its name, subdomain and routes
	again, err := cfWorkerEnsure("t", st.Worker, "", "")
	if err != nil || again.Name != w.Name || f.subdomain != sub || f.scripts[w.Name]["/p1"] == "" {
		t.Errorf("re-ensure: %+v %v", again, err)
	}
	if err := cfWorkerDelete("t", w); err != nil || len(f.scripts) != 0 || len(f.domains) != 0 {
		t.Errorf("delete: %v %+v", err, f)
	}
}

func TestTuneCloudflare(t *testing.T) {
	f := newFakeWorkers()
	withFakeCF(t, f)
	o := TuneOpts{Cloudflare: "t", Zone: "example.com", Protocols: []string{"dnstt", "worker"}, Log: func(string) {}}
	made, err := o.tuneCloudflare("203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{o.Domain, o.CDNDomain, o.DNSDomain} {
		if !strings.HasSuffix(d, ".example.com") || strings.Count(d, ".") != 2 {
			t.Errorf("zone name %q", d)
		}
	}
	direct, cdn, ns := f.records["r"+o.Domain], f.records["r"+o.CDNDomain], f.records["r"+o.DNSDomain]
	if direct.Content != "203.0.113.7" || direct.Proxied || cdn.Content != "203.0.113.7" || !cdn.Proxied ||
		ns.Type != "NS" || ns.Content != nsHost(o.DNSDomain) || f.records["r"+nsHost(o.DNSDomain)].Content != "203.0.113.7" {
		t.Errorf("records: %+v", f.records)
	}
	if f.ssl != "full" || len(made) != 2 {
		t.Errorf("ssl %s, made %v", f.ssl, made)
	}

	// a re-tune of a deployment with these domains changes nothing
	n := len(f.calls)
	o2 := TuneOpts{Domain: o.Domain, CDNDomain: o.CDNDomain, DNSDomain: o.DNSDomain, Log: func(string) {},
		deployed: &State{Domain: o.Domain, CDNDomain: o.CDNDomain, DNSDomain: o.DNSDomain, Cloudflare: Cloudflare{Token: "t"}}}
	if made, err := o2.tuneCloudflare("203.0.113.7"); err != nil || made != nil || len(f.calls) != n || o2.Cloudflare != "t" {
		t.Errorf("re-tune: %v %v %v", made, err, f.calls[n:])
	}
	if _, err := (&TuneOpts{Zone: "example.com", Log: func(string) {}}).tuneCloudflare("203.0.113.7"); err == nil {
		t.Error("--zone without a token")
	}

	// the Worker: workers.dev and a hostname in the zone, with routes for the test entries
	es, _ := planEntries("SNI www.a.com 2 0.20", PlanOpts{Protocols: []string{"worker"}, Worker: true})
	o.deployed = &State{Worker: &Worker{Account: "acc1", Name: "old1", Routes: map[string]string{"/live": "203.0.113.7:9"}}}
	es = o.prepareWorker("203.0.113.7", es)
	var hosts []string
	for _, e := range es {
		if e.Worker() {
			hosts = append(hosts, e.SNI)
		}
	}
	routes := f.scripts["old1"]
	if len(hosts) != 2 || !strings.HasSuffix(hosts[0], ".workers.dev") || !strings.HasSuffix(hosts[1], ".example.com") ||
		len(routes) != 3 || routes["/live"] != "203.0.113.7:9" || o.worker == nil || o.worker.Name != "old1" {
		t.Errorf("hosts %v, routes %v", hosts, routes)
	}
	if r := o.deployedRoutes(); len(r) != 1 || r["/live"] == "" {
		t.Errorf("deployed routes: %v", r)
	}
	// without a token, or when Cloudflare refuses, the Worker entries are dropped
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(J{"success": false, "errors": []J{{"message": "no"}}})
	})
	withFakeCF(t, failing)
	o3 := TuneOpts{Cloudflare: "t", Log: func(string) {}}
	if es := o3.prepareWorker("203.0.113.7", es); hasWorker(es) {
		t.Error("Worker entries kept although Cloudflare refused")
	}
	if es, _ := planEntries("SNI www.a.com 2 0.20", PlanOpts{Protocols: []string{"worker"}}); hasWorker(es) {
		t.Error("Worker entries without a token")
	}
}

func hasWorker(es []Entry) bool {
	for _, e := range es {
		if e.Worker() {
			return true
		}
	}
	return false
}

func TestDeployKeepsKnobsAndCloudflare(t *testing.T) {
	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "203.0.113.7"}}
	old := testStateWithCert(t)
	old.Users = []User{NewUser("ann")}
	old.Cloudflare.Token = "old-token"
	old.Worker = &Worker{Account: "acc1", Name: "w", Host: "w.s.workers.dev"}
	if err := saveState(r, old); err != nil {
		t.Fatal(err)
	}
	st := testStateWithCert(t)
	vs := []*Variant{
		{Entry: Entry{ID: "wg", Kind: "wireguard", Port: 51000}, K: Knobs{Frag: "none", MTU: 1280}},
		{Entry: Entry{ID: "awg-std", Kind: "amneziawg", Port: 8443, Subnet: awgSubnet(1, true)}, K: Knobs{Frag: "none", MTU: 1280, Mode: "quic"}},
	}
	got, err := DeployVariants(r, st, vs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries[0].MTU != 1280 || got.Entries[1].MTU != 1280 || got.Entries[1].Mode != "quic" || got.Entries[1].Subnet != "10.80.0.0/16" {
		t.Errorf("entries: %+v", got.Entries)
	}
	if got.Cloudflare.Token != "old-token" || got.Worker == nil || got.Worker.Name != "w" || got.AWG == nil {
		t.Errorf("kept: %+v %+v", got.Cloudflare, got.Worker)
	}
	var ac AWGConf
	if err := json.Unmarshal([]byte(fs.files[remoteDir+"/awg.json"]), &ac); err != nil || len(ac.Peers) != 1 ||
		ac.Params.Priv == "" || ac.Stats != awgStatsFile {
		t.Errorf("awg.json: %v %+v", err, ac)
	}
	if !slices.ContainsFunc(fs.calls, func(c string) bool { return c == "extras awg net:10.80.0.0/16" }) {
		t.Errorf("helper calls: %v", fs.calls)
	}
}

// TestWorkerRelayXray runs real Xray on both ends of the Worker relay, with a relay in between
// that does what worker.js does: WebSocket in, the early data from its header, a PROXY line
// with the client's address, then the bytes both ways. (TLS, which Cloudflare ends, is left out.)
func TestWorkerRelayXray(t *testing.T) {
	bin := localXray(t)
	dir := t.TempDir()
	st, err := NewState("127.0.0.1", bin)
	if err != nil {
		t.Fatal(err)
	}
	ports, _ := freePorts(4)
	inPort, relayPort, socksPort, webPort := ports[0], ports[1], ports[2], ports[3]
	e := Entry{ID: "worker", Kind: "worker-ws", Port: inPort, Path: "/" + randHex(6), SNI: "w.example.workers.dev"}
	u := NewUser("ann")

	srvCfg := st.ServerConfig([]Entry{e}, []User{u})
	delete(srvCfg, "routing") // the target below is on localhost, which freedom refuses by default
	srvCfg["outbounds"].([]J)[0]["settings"] = J{"finalRules": []J{{"action": "allow", "ip": []string{"127.0.0.0/8"}}}}
	srvCfg["log"] = J{"loglevel": "warning", "access": filepath.Join(dir, "access.log")}
	runXray(t, bin, filepath.Join(dir, "server.json"), srvCfg)

	early := make(chan string, 1)
	relay := &http.Server{Addr: hostPort("127.0.0.1", relayPort), Handler: websocket.Server{
		Handshake: func(c *websocket.Config, r *http.Request) error {
			early <- r.Header.Get("Sec-WebSocket-Protocol")
			return nil
		},
		Handler: func(ws *websocket.Conn) {
			ws.PayloadType = websocket.BinaryFrame
			c, err := net.Dial("tcp", hostPort("127.0.0.1", inPort))
			if err != nil {
				return
			}
			defer c.Close()
			fmt.Fprintf(c, "PROXY TCP4 198.51.100.5 127.0.0.1 40000 %d\r\n", inPort)
			if ed := <-early; ed != "" {
				b, err := base64.RawURLEncoding.DecodeString(ed)
				if err != nil {
					t.Errorf("early data: %v", err)
					return
				}
				c.Write(b)
			}
			go io.Copy(c, ws)
			io.Copy(ws, c)
		}}}
	go relay.ListenAndServe()
	defer relay.Close()

	ob := st.Outbound(e, u, Knobs{FP: "chrome", Frag: "none"}, "proxy")
	vn := ob["settings"].(J)["vnext"].([]J)[0]
	vn["address"], vn["port"] = "127.0.0.1", relayPort
	ss := ob["streamSettings"].(J)
	ss["security"] = "none"
	delete(ss, "tlsSettings")
	if ss["wsSettings"].(J)["path"] != e.Path+"?ed=2560" {
		t.Fatalf("path %v", ss["wsSettings"])
	}
	cliCfg := J{"log": J{"loglevel": "warning"}, "outbounds": []J{ob},
		"inbounds": []J{{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks"}}}
	runXray(t, bin, filepath.Join(dir, "client.json"), cliCfg)

	web := &http.Server{Addr: hostPort("127.0.0.1", webPort), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})}
	go web.ListenAndServe()
	defer web.Close()
	time.Sleep(time.Second)
	resp, err := socksClient(socksPort, 10*time.Second).Get("http://" + hostPort("127.0.0.1", webPort) + "/")
	if err != nil || resp.StatusCode != 204 {
		sl, _ := os.ReadFile(filepath.Join(dir, "server.json.log"))
		cl, _ := os.ReadFile(filepath.Join(dir, "client.json.log"))
		t.Fatalf("through the relay: %v %v\nserver:\n%s\nclient:\n%s", resp, err, tail(string(sl), 2000), tail(string(cl), 2000))
	}
	resp.Body.Close()
	time.Sleep(500 * time.Millisecond)
	log, _ := os.ReadFile(filepath.Join(dir, "access.log"))
	if !strings.Contains(string(log), "198.51.100.5") || !strings.Contains(string(log), "ann") {
		t.Errorf("the server should see the client's address from the PROXY line:\n%s", log)
	}
}

func runXray(t *testing.T, bin, path string, cfg J) {
	t.Helper()
	if err := writeJSON(path, cfg); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "run", "-test", "-c", path).CombinedOutput(); err != nil {
		t.Fatalf("xray rejected %s: %v\n%s", filepath.Base(path), err, out)
	}
	cmd := exec.Command(bin, "run", "-c", path)
	lf, _ := os.Create(path + ".log")
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
}

// The bot shows members the guides for the protocols a pool lists.
func TestProtocolNamesNewKinds(t *testing.T) {
	st := keyedState(t)
	st.Entries = []Entry{{ID: "r", Kind: "reality-tcp"}, {ID: "awg", Kind: "amneziawg"}, {ID: "worker", Kind: "worker-ws"}}
	if got := strings.Join(st.protocolNames(), ","); got != "VLESS REALITY,AmneziaWG,CF Worker" {
		t.Errorf("protocol names %s", got)
	}
}
