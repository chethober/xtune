//go:build android || (!linux && !windows)

package core

import (
	"errors"
	"net/netip"
)

func setRoutes(tun string, bypass []netip.Addr) (func(), error) {
	return nil, errors.New("system routes are not supported on this platform")
}

func setSystemProxy(httpPort, socksPort int) (func(), error) {
	return nil, errors.New("setting the system proxy is not supported on this platform")
}
