// devsub serves a fake xtune subscription for working on the app without a server: a local VLESS
// server (every connection lands on a local test site), a dead config, and handoff configs.
//
//	go run ./cmd/devsub        then add http://127.0.0.1:18080/p/tok in the app
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/chethober/xtune/pkg/protocols"
	xcore "github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
)

type J = map[string]any

const uuid = "b831381d-6324-4d53-ad4f-8cda48b30811"

func freePort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "where to serve the subscription")
	flag.Parse()

	site := http.NewServeMux()
	site.HandleFunc("/204", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	site.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for i := 0; i < 30; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	site.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "through the proxy\n") })
	sitePort := freePort()
	go http.ListenAndServe("127.0.0.1:"+itoa(sitePort), site)

	vp := freePort()
	cfg := J{"log": J{"loglevel": "warning"},
		"inbounds":  []J{{"listen": "127.0.0.1", "port": vp, "protocol": "vless", "settings": J{"clients": []J{{"id": uuid}}, "decryption": "none"}}},
		"outbounds": []J{{"protocol": "freedom", "settings": J{"redirect": "127.0.0.1:" + itoa(sitePort)}}}}
	b, _ := json.Marshal(cfg)
	xc, err := xcore.LoadConfig("json", bytes.NewReader(b))
	if err != nil {
		log.Fatal(err)
	}
	inst, err := xcore.New(xc)
	if err != nil {
		log.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		log.Fatal(err)
	}
	ob := func(port int) J {
		return J{"protocol": "vless", "settings": J{"vnext": []J{{"address": "127.0.0.1", "port": port,
			"users": []J{{"id": uuid, "encryption": "none"}}}}}, "streamSettings": J{"network": "tcp"}}
	}
	p := protocols.ClientProfile{V: 1, Name: "DE · dev", User: "carol", Country: "ir", Network: "Dev network", Probe: "http://probe.test/204",
		Usage: protocols.ClientUsage{Up: 2 << 30, Down: 16 << 30, Quota: 50 << 30, Expires: 2000000000, Devices: 2},
		Configs: []protocols.ClientEntry{
			{ID: "r-xhttp", Key: "r-xhttp/443", Name: "REALITY · XHTTP", Kind: "reality-xhttp", Label: "REALITY XHTTP", Engine: "xray", Outbound: ob(vp)},
			{ID: "hy2", Key: "hy2/443", Name: "Hysteria2", Kind: "hysteria2", Label: "Hysteria2", Engine: "xray", Outbound: ob(vp)},
			{ID: "r-tcp", Key: "r-tcp/8443", Name: "REALITY · TCP", Kind: "reality-tcp", Label: "REALITY TCP", Engine: "xray", Outbound: ob(freePort())},
			{ID: "mt", Key: "mt/8888", Name: "Telegram proxy", Kind: "mtproto", Label: "MTProto", Engine: "handoff", Link: "https://t.me/proxy?server=127.0.0.1&port=8888&secret=dd00"},
			{ID: "ike", Key: "ike/500", Name: "IKEv2", Kind: "ikev2", Label: "IKEv2", Engine: "handoff",
				File: &protocols.Profile{Name: "IKEv2, strongSwan app", File: "carol.sswan", Type: "application/vnd.strongswan.profile", Body: "{}"}},
		}}
	http.HandleFunc("/p/tok/client", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(p) })
	http.HandleFunc("/p/tok/report", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		log.Printf("report: %s", strings.TrimSpace(string(b)))
		w.WriteHeader(204)
	})
	log.Printf("subscription: http://%s/p/tok", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
