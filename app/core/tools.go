//go:build tools

package core

// gomobile bind needs this package in the module (scripts/build-android-core.sh).
import _ "golang.org/x/mobile/bind"
