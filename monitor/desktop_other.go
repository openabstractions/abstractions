//go:build !windows

package main

import (
	"errors"
	"log"
)

func windowed() bool { return false }

func fail(_ bool, err error) { log.Fatal(err) }

func (w *window) desktop() error {
	return errors.New("the desktop window is Windows only; run without -native and open the page")
}

func (p *servicePanel) desktop(_ string) error {
	return errors.New("native window unavailable on this platform; use -native=false")
}
