//go:build !windows

package main

import (
	"errors"
	"log"
)

func windowed() bool { return false }

func fail(_ bool, err error) { log.Fatal(err) }

func (p *servicePanel) desktop(_ string) error {
	return errors.New("native window unavailable on this platform; use -native=false")
}
