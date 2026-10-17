//go:build !linux

package main

// flock is a no-op where server mode cannot run.
func flock(string) func() { return func() {} }
