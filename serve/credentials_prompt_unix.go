//go:build linux || darwin

package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// promptSecret reads one line from the terminal with echo off.
func promptSecret(diagnostics io.Writer) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	saved, err := unix.IoctlGetTermios(fd, termiosGet)
	if err != nil {
		return nil, errors.New("stdin is not a terminal")
	}
	quiet := *saved
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, termiosSet, &quiet); err != nil {
		return nil, err
	}
	defer unix.IoctlSetTermios(fd, termiosSet, saved)
	fmt.Fprint(diagnostics, "secret (not echoed): ")
	defer fmt.Fprintln(diagnostics)
	return readSecretLine(os.Stdin)
}
