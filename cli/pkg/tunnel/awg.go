package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	awgtun "github.com/amnezia-vpn/amneziawg-go/tun"
	wgtun "golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// AWG is one user's AmneziaWG config.
type AWG struct {
	// UAPI is the device config in WireGuard's UAPI form, without the endpoint line.
	UAPI     string `json:"uapi"`
	Endpoint string `json:"endpoint"` // host:port; a name is resolved when the tunnel comes up
	Address  string `json:"address"`  // the user's tunnel address
	MTU      int    `json:"mtu"`
	DNS      string `json:"dns,omitempty"` // resolver inside the tunnel; 1.1.1.1 when empty
}

// AWGDialer connects through an in-process AmneziaWG client on a userspace network stack. The
// tunnel comes up on the first dial.
type AWGDialer struct {
	c    AWG
	once sync.Once
	dev  *device.Device
	tnet *netstack.Net
	err  error
}

func (c AWG) Dialer() *AWGDialer { return &AWGDialer{c: c} }

func (a *AWGDialer) start() {
	if a.c.UAPI == "" {
		a.err = errors.New("no AmneziaWG settings")
		return
	}
	host, port, err := net.SplitHostPort(a.c.Endpoint)
	if err != nil {
		a.err = err
		return
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		ips, err := net.LookupHost(host)
		if err != nil || len(ips) == 0 {
			a.err = fmt.Errorf("resolve %s: %v", host, err)
			return
		}
		ip, _ = netip.ParseAddr(ips[0])
	}
	addr, err := netip.ParseAddr(a.c.Address)
	if err != nil {
		a.err = fmt.Errorf("tunnel address: %w", err)
		return
	}
	dns := netip.MustParseAddr("1.1.1.1")
	if d, err := netip.ParseAddr(a.c.DNS); err == nil {
		dns = d
	}
	mtu := a.c.MTU
	if mtu <= 0 {
		mtu = 1280
	}
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{addr}, []netip.Addr{dns}, mtu)
	if err != nil {
		a.err = err
		return
	}
	d := device.NewDevice(NetTUN(tdev), conn.NewStdNetBind(), device.NewLogger(device.LogLevelSilent, ""))
	if err := d.IpcSet(a.c.UAPI + "endpoint=" + net.JoinHostPort(ip.String(), port) + "\n"); err != nil {
		d.Close()
		a.err = err
		return
	}
	if err := d.Up(); err != nil {
		d.Close()
		a.err = err
		return
	}
	a.dev, a.tnet = d, tnet
}

func (a *AWGDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	a.once.Do(a.start)
	if a.err != nil {
		return nil, a.err
	}
	return a.tnet.DialContext(ctx, network, addr)
}

// ListenUDP opens a UDP socket inside the tunnel, for relaying a client's datagrams.
func (a *AWGDialer) ListenUDP() (net.PacketConn, error) {
	a.once.Do(a.start)
	if a.err != nil {
		return nil, a.err
	}
	return a.tnet.ListenUDP(nil)
}

func (a *AWGDialer) Close() {
	a.once.Do(func() { a.err = errors.New("closed") })
	if a.dev != nil {
		a.dev.Close()
	}
}

// NetTUN adapts WireGuard's userspace network stack to amneziawg-go, whose TUN interface is the
// same but for its own Event type. (amneziawg-go's own copy of the stack needs mid-2025 gvisor,
// whose Go module builds carry a test file the go command refuses.)
type netTUN struct {
	wgtun.Device
	events chan awgtun.Event
}

func NetTUN(d wgtun.Device) awgtun.Device {
	t := &netTUN{Device: d, events: make(chan awgtun.Event, 10)}
	go func() {
		for ev := range d.Events() {
			t.events <- awgtun.Event(ev)
		}
		close(t.events)
	}()
	return t
}

func (t *netTUN) Events() <-chan awgtun.Event { return t.events }
