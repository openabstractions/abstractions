//go:build !windows && !darwin && !linux

package main

import "errors"

func messageStartNativeSession() (string, error) {
	return "", errors.New("message start: native session unavailable")
}
