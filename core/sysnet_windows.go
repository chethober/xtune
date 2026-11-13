//go:build windows

package core

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	return string(out), err
}

// defaultRoute is the current IPv4 default gateway and its interface index.
func defaultRoute() (string, int, error) {
	out, err := output("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-NetRoute -AddressFamily IPv4 -DestinationPrefix 0.0.0.0/0 | Sort-Object { $_.RouteMetric + $_.InterfaceMetric } | "+
			"Select-Object -First 1 | ForEach-Object { \"$($_.NextHop) $($_.InterfaceIndex)\" }")
	if err != nil {
		return "", 0, fmt.Errorf("default route: %w", err)
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return "", 0, errors.New("no default route: is the network up?")
	}
	idx, _ := strconv.Atoi(f[1])
	return f[0], idx, nil
}

// setRoutes sends everything through the Wintun adapter, except the servers' own addresses.
func setRoutes(tun string, bypass []netip.Addr) (func(), error) {
	var ifc *net.Interface
	for i := 0; ; i++ { // Xray creates the adapter as it starts
		if x, err := net.InterfaceByName(tun); err == nil {
			ifc = x
			break
		}
		if i == 100 {
			return nil, fmt.Errorf("adapter %s did not appear", tun)
		}
		time.Sleep(100 * time.Millisecond)
	}
	gw, gwIdx, err := defaultRoute()
	if err != nil {
		return nil, err
	}
	var undo []func()
	undoAll := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	if err := run("netsh", "interface", "ipv4", "set", "address", "name="+tun, "source=static", "address=172.19.0.1", "mask=255.255.255.252"); err != nil {
		return nil, err
	}
	run("netsh", "interface", "ipv4", "set", "dnsservers", "name="+tun, "source=static", "address=1.1.1.1", "register=none", "validate=no")
	run("netsh", "interface", "ipv4", "set", "interface", tun, "metric=1")
	for _, a := range bypass {
		if !a.Is4() || a.IsLoopback() || a.IsPrivate() {
			continue
		}
		ip := a.String()
		if err := run("route", "add", ip, "mask", "255.255.255.255", gw, "if", strconv.Itoa(gwIdx), "metric", "1"); err != nil {
			undoAll()
			return nil, err
		}
		undo = append(undo, func() { run("route", "delete", ip, "mask", "255.255.255.255", gw) })
	}
	for _, half := range []string{"0.0.0.0", "128.0.0.0"} {
		h := half
		if err := run("route", "add", h, "mask", "128.0.0.0", "172.19.0.2", "if", strconv.Itoa(ifc.Index), "metric", "1"); err != nil {
			undoAll()
			return nil, err
		}
		undo = append(undo, func() { run("route", "delete", h, "mask", "128.0.0.0", "172.19.0.2") })
	}
	return undoAll, nil
}

const internetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

var (
	wininet           = windows.NewLazySystemDLL("wininet.dll")
	internetSetOption = wininet.NewProc("InternetSetOptionW")
)

// refreshProxy tells running programs the proxy settings changed.
func refreshProxy() {
	const settingsChanged, refresh = 39, 37
	internetSetOption.Call(0, settingsChanged, uintptr(unsafe.Pointer(nil)), 0)
	internetSetOption.Call(0, refresh, uintptr(unsafe.Pointer(nil)), 0)
}

// setSystemProxy points Windows' proxy settings (which browsers and most apps follow) at the
// local HTTP port, and restores the old ones afterwards.
func setSystemProxy(httpPort, socksPort int) (func(), error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	oldEnable, _, _ := k.GetIntegerValue("ProxyEnable")
	oldServer, _, _ := k.GetStringValue("ProxyServer")
	oldOverride, _, _ := k.GetStringValue("ProxyOverride")
	if err := k.SetStringValue("ProxyServer", fmt.Sprintf("127.0.0.1:%d", httpPort)); err != nil {
		return nil, err
	}
	k.SetStringValue("ProxyOverride", "<local>;localhost;127.*;10.*;172.16.*;192.168.*")
	k.SetDWordValue("ProxyEnable", 1)
	refreshProxy()
	return func() {
		k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.SET_VALUE)
		if err != nil {
			return
		}
		defer k.Close()
		k.SetDWordValue("ProxyEnable", uint32(oldEnable))
		k.SetStringValue("ProxyServer", oldServer)
		k.SetStringValue("ProxyOverride", oldOverride)
		refreshProxy()
	}, nil
}

func enableKillSwitch() (func(), error) {
	return nil, errors.New("the kill switch is not available on Windows yet")
}
