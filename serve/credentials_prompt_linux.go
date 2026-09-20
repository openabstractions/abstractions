//go:build linux

package main

import "golang.org/x/sys/unix"

const (
	termiosGet = unix.TCGETS
	termiosSet = unix.TCSETS
)
