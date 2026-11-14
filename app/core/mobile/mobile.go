// Package mobile is the Android binding (gomobile bind): the same API as the desktop daemon.
package mobile

import "github.com/chethober/xtune/app/core"

// Call runs one API method; args and the reply are JSON (see core.Call).
func Call(method, args string) string { return string(core.Call(method, []byte(args))) }

// Version is the core's version.
func Version() string { return core.Version }
