package core

import (
	"context"
	"io"
	"reflect"
	"time"
	"unsafe"

	"github.com/xtls/xray-core/app/proxyman/inbound"
	xcore "github.com/xtls/xray-core/core"
	finbound "github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/proxy/tun"
)

// closeTun shuts down the TUN inbound's packet stack and device. Xray's TUN inbound
// (proxy/tun.Handler) has no Close, so instance.Close leaves its gVisor stack running: it keeps
// reading the tunnel's file descriptor and handing packets to the closed instance. On Android
// the app then closes that descriptor and opens a new tunnel, which usually gets the same
// number, so the old stack reads the new tunnel's packets. The first DNS packet reaches the
// closed instance's FakeDNS, whose address pool Close set to nil, and the process panics in
// GetDomainFromFakeDNS. On Linux and Windows the old interface was left behind too.
//
// It reports whether it closed anything. It must run before the instance closes.
func closeTun(inst *xcore.Instance) bool {
	m, ok := inst.GetFeature(finbound.ManagerType()).(finbound.Manager)
	if !ok {
		return false
	}
	h, err := m.GetHandler(context.Background(), "tun")
	if err != nil {
		return false
	}
	ao, ok := h.(*inbound.AlwaysOnInboundHandler)
	if !ok {
		return false
	}
	th, ok := ao.GetInbound().(*tun.Handler)
	if !ok || th == nil {
		return false
	}
	stack, ok := unexported(reflect.ValueOf(th).Elem(), "stack").(io.Closer)
	if !ok || stack == nil || reflect.ValueOf(stack).IsNil() {
		return false
	}
	// the device the stack reads, fetched before the stack goes
	var dev io.Closer
	if sv := reflect.ValueOf(stack); sv.Kind() == reflect.Pointer && sv.Elem().Kind() == reflect.Struct {
		dev, _ = unexported(sv.Elem(), "tun").(io.Closer)
	}
	// stops the fd read loops and waits for them: no new packets after this
	_ = stack.Close()
	if dev != nil && !reflect.ValueOf(dev).IsNil() {
		_ = dev.Close() // Linux and Windows: removes the interface; Android: nothing (the app owns the fd)
	}
	return true
}

// unexported reads a struct field the package does not export, or nil.
func unexported(v reflect.Value, name string) any {
	f := v.FieldByName(name)
	if !f.IsValid() || !f.CanAddr() {
		return nil
	}
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface()
}

// tunDrain is how long a closed TUN stack's connections get to pass the dispatcher's first
// steps (sniffing reads FakeDNS straight away) before the instance closes under them.
const tunDrain = 100 * time.Millisecond
