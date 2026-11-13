// xtune-core is the client app's engine on desktops: the app starts it and talks to it over a
// local HTTP port, guarded by a random token. For the whole-device (TUN) mode the app starts it
// with administrator rights instead (pkexec on Linux, UAC on Windows); it then writes its port to
// a file the app reads.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chethober/xtune-client/core"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "where to listen")
	tokenFile := flag.String("token-file", "", "file holding the token every call must carry (deleted once read)")
	portFile := flag.String("port-file", "", "write the port here once listening")
	parent := flag.Int("parent", 0, "exit when this process exits")
	assets := flag.String("assets", "", "directory with geoip.dat and geosite.dat (default: next to this program)")
	version := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *version {
		fmt.Println(core.Version)
		return
	}
	if *assets == "" {
		if exe, err := os.Executable(); err == nil {
			*assets = filepath.Dir(exe)
		}
	}
	os.Setenv("xray.location.asset", *assets)
	token := os.Getenv("XTUNE_CORE_TOKEN")
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			log.Fatal(err)
		}
		token = string(b)
		os.Remove(*tokenFile)
	}
	if len(token) < 16 {
		log.Fatal("a token of at least 16 characters is required (XTUNE_CORE_TOKEN or --token-file)")
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if *portFile != "" {
		if err := os.WriteFile(*portFile, []byte(fmt.Sprint(port)), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("LISTEN %d\n", port)
	os.Stdout.Sync()

	quit := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/call", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var in struct {
			Method string          `json:"method"`
			Args   json.RawMessage `json:"args"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if in.Method == "quit" {
			w.Write([]byte(`{"ok":true}`))
			close(quit)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(core.Call(in.Method, in.Args))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-sig:
		case <-quit:
		case <-tick.C:
			if *parent == 0 || alive(*parent) {
				continue
			}
		}
		break
	}
	core.Stop() // restore routes and proxy settings
	srv.Close()
}
