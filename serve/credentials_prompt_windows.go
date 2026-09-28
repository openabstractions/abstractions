//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// promptSecret reads one line from the console with echo off.
func promptSecret(diagnostics io.Writer) ([]byte, error) {
	handle := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return nil, errors.New("stdin is not a console")
	}
	if err := windows.SetConsoleMode(handle, (mode&^windows.ENABLE_ECHO_INPUT)|windows.ENABLE_LINE_INPUT|windows.ENABLE_PROCESSED_INPUT); err != nil {
		return nil, err
	}
	defer windows.SetConsoleMode(handle, mode)
	//unchecked: a failed prompt write does not stop the read that follows, and there is nothing else to do about it here
	fmt.Fprint(diagnostics, "secret (not echoed): ")
	defer fmt.Fprintln(diagnostics)
	return readSecretLine(os.Stdin)
}
