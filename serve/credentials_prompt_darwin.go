//go:build darwin

package main

import "golang.org/x/sys/unix"

const (
	termiosGet = unix.TIOCGETA
	termiosSet = unix.TIOCSETA
)
