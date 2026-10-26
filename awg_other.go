//go:build !linux

package main

import (
	"errors"

	"github.com/amnezia-vpn/amneziawg-go/device"
)

func awgServerDevice(e Entry, uapi string) (*device.Device, error) {
	return nil, errors.New("the AmneziaWG server runs on Linux only")
}
