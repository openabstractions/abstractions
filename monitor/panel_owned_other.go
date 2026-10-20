//go:build !windows

package main

import "errors"

func runOwnedPanel() error { return errors.New("the owned Panel host requires Windows") }
