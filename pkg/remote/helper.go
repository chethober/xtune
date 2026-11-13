package remote

import (
	_ "embed"
	"fmt"
	"strings"
)

// helperScript is the shell script every server-side change goes through. It only touches Dir,
// the xray-autotune* units and the /usr/local/bin/xtune link.
//
//go:embed helper.sh
var helperScript string

// Helper uploads helper.sh and runs it as root with args, each shell-quoted; it returns trimmed stdout.
func (r *Server) Helper(args ...string) (string, error) {
	if err := r.Upload(Dir+"/helper.sh", []byte(helperScript), "700"); err != nil {
		return "", fmt.Errorf("upload helper: %w", err)
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	out, err := r.Root("bash " + Dir + "/helper.sh " + strings.Join(q, " "))
	return strings.TrimSpace(out), err
}

// Status is what `helper.sh status` reports about the server.
type Status struct {
	Arch, Xray, Unit string // isolated xtune Xray
	HasState         bool   // a deployment exists
	Xtune, XtuneSHA  string // server copy of xtune ("none" when missing)
	Sub, Timer, Bot  string // subscription server, enforce timer and Telegram bot (systemctl is-active)
	Tunnels          string // SSH and DNS tunnel server
	AWG              string // AmneziaWG server
	Relay            string // multi-hop relay to another server (xray-autotune-relay)
	FW               string // active host firewall: ufw | firewalld | none
}

// Status reports what is installed and running on the server.
func (r *Server) Status() (Status, error) {
	out, err := r.Helper("status")
	var s Status
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch k {
		case "ARCH":
			s.Arch = v
		case "XRAY":
			s.Xray = v
		case "UNIT":
			s.Unit = v
		case "STATE":
			s.HasState = v == "yes"
		case "XTUNE":
			s.Xtune = v
		case "XTUNE_SHA":
			s.XtuneSHA = v
		case "SUB":
			s.Sub = v
		case "TIMER":
			s.Timer = v
		case "BOT":
			s.Bot = v
		case "TUNNELS":
			s.Tunnels = v
		case "AWG":
			s.AWG = v
		case "RELAY":
			s.Relay = v
		case "FW":
			s.FW = v
		}
	}
	return s, err
}
