//go:build linux && !android

package core

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

// tunAddr is the TUN interface's own address; its peer is where the default routes point.
const tunAddr = "172.19.0.1/30"

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// defaultRoute is the current default gateway and interface.
func defaultRoute() (via, dev string, err error) {
	out, err := exec.Command("ip", "-4", "route", "show", "default").Output()
	if err != nil {
		return "", "", fmt.Errorf("ip route: %w", err)
	}
	f := strings.Fields(strings.SplitN(string(out), "\n", 2)[0])
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "via":
			via = f[i+1]
		case "dev":
			dev = f[i+1]
		}
	}
	if dev == "" {
		return "", "", errors.New("no default route: is the network up?")
	}
	return via, dev, nil
}

// setRoutes sends everything through the TUN interface, except the servers' own addresses, which
// keep going out the way they did. It needs root (or CAP_NET_ADMIN).
func setRoutes(tun string, bypass []netip.Addr) (func(), error) {
	for i := 0; ; i++ { // Xray creates the interface as it starts
		if _, err := net.InterfaceByName(tun); err == nil {
			break
		}
		if i == 50 {
			return nil, fmt.Errorf("interface %s did not appear", tun)
		}
		time.Sleep(100 * time.Millisecond)
	}
	via, dev, err := defaultRoute()
	if err != nil {
		return nil, err
	}
	var undo []func()
	undoAll := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	step := func(do, rollback []string) error {
		if err := run("ip", do...); err != nil {
			return err
		}
		if rollback != nil {
			undo = append(undo, func() { run("ip", rollback...) })
		}
		return nil
	}
	if err := step([]string{"addr", "replace", tunAddr, "dev", tun}, nil); err != nil {
		return nil, err
	}
	if err := step([]string{"link", "set", tun, "up"}, nil); err != nil {
		return nil, err
	}
	for _, a := range bypass {
		if !a.Is4() || a.IsLoopback() || a.IsPrivate() {
			continue
		}
		r := []string{"-4", "route", "replace", a.String() + "/32"}
		if via != "" {
			r = append(r, "via", via)
		}
		r = append(r, "dev", dev)
		if err := step(r, []string{"-4", "route", "del", a.String() + "/32"}); err != nil {
			undoAll()
			return nil, err
		}
	}
	for _, half := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := step([]string{"-4", "route", "replace", half, "dev", tun}, []string{"-4", "route", "del", half, "dev", tun}); err != nil {
			undoAll()
			return nil, err
		}
	}
	// systemd-resolved: send every lookup to the tunnel, where Xray answers it through the proxy
	if _, err := exec.LookPath("resolvectl"); err == nil {
		if run("resolvectl", "dns", tun, "1.1.1.1") == nil && run("resolvectl", "domain", tun, "~.") == nil {
			undo = append(undo, func() { run("resolvectl", "revert", tun) })
		}
	}
	return undoAll, nil
}

// setSystemProxy points GNOME's (and apps that follow it) proxy settings at the local ports.
func setSystemProxy(httpPort, socksPort int) (func(), error) {
	if _, err := exec.LookPath("gsettings"); err != nil {
		return nil, errors.New("no gsettings: set your apps' proxy to 127.0.0.1 yourself")
	}
	get := func(schema, key string) string {
		out, _ := exec.Command("gsettings", "get", schema, key).Output()
		return strings.TrimSpace(string(out))
	}
	oldMode := strings.Trim(get("org.gnome.system.proxy", "mode"), "'")
	sets := [][]string{
		{"org.gnome.system.proxy.http", "host", "127.0.0.1"}, {"org.gnome.system.proxy.http", "port", fmt.Sprint(httpPort)},
		{"org.gnome.system.proxy.https", "host", "127.0.0.1"}, {"org.gnome.system.proxy.https", "port", fmt.Sprint(httpPort)},
		{"org.gnome.system.proxy.socks", "host", "127.0.0.1"}, {"org.gnome.system.proxy.socks", "port", fmt.Sprint(socksPort)},
		{"org.gnome.system.proxy", "mode", "manual"},
	}
	for _, s := range sets {
		if err := run("gsettings", append([]string{"set"}, s...)...); err != nil {
			return nil, err
		}
	}
	return func() { run("gsettings", "set", "org.gnome.system.proxy", "mode", orStr(oldMode, "none")) }, nil
}

// enableKillSwitch adds blackhole routes under the tunnel's own (a higher metric loses to them
// while the tunnel is up): when the TUN interface goes away, its routes go with it and traffic
// meets the blackhole instead of the normal default route. The servers' and the local network's
// more specific routes still win. Only Disconnect removes it.
func enableKillSwitch() (func(), error) {
	var undo []func()
	for _, r := range [][]string{{"-4", "0.0.0.0/1"}, {"-4", "128.0.0.0/1"}, {"-6", "::/1"}, {"-6", "8000::/1"}} {
		args := []string{r[0], "route", "replace", "blackhole", r[1], "metric", "4242"}
		if err := run("ip", args...); err != nil {
			if r[0] == "-6" {
				continue // no IPv6 here
			}
			for _, u := range undo {
				u()
			}
			return nil, err
		}
		fam, dst := r[0], r[1]
		undo = append(undo, func() { run("ip", fam, "route", "del", "blackhole", dst, "metric", "4242") })
	}
	return func() {
		for _, u := range undo {
			u()
		}
	}, nil
}
