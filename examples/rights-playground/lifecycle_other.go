//go:build !darwin

package main

import (
	"errors"
	"io"
)

func runLifecycle([]string, io.Writer) error {
	return errors.New("use this Windows pack's Start/Stop/Reset.cmd lifecycle helpers")
}

func registerMenu(connection) error { return nil }
