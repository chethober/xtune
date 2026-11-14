//go:build !windows

package main

import "syscall"

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil || syscall.Kill(pid, 0) == syscall.EPERM }
