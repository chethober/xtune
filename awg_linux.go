package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun"
)

// awgServerDevice brings up entry e on its own TUN interface with the server's tunnel address.
func awgServerDevice(e Entry, uapi string) (*device.Device, error) {
	name := awgIface(e)
	t, err := tun.CreateTUN(name, 1420)
	if err != nil {
		return nil, fmt.Errorf("TUN %s: %w", name, err)
	}
	d := device.NewDevice(t, conn.NewStdNetBind(), device.NewLogger(device.LogLevelError, "awg "+e.ID+": "))
	if err := d.IpcSet(uapi); err != nil {
		d.Close()
		return nil, err
	}
	if err := d.Up(); err != nil {
		d.Close()
		return nil, err
	}
	for _, args := range [][]string{{"addr", "replace", awgServerAddr(e).String(), "dev", name}, {"link", "set", "dev", name, "up"}} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			d.Close()
			return nil, fmt.Errorf("ip %s: %v %s", strings.Join(args, " "), err, out)
		}
	}
	return d, nil
}
