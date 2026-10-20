package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"strconv"
)

func messageStartNativeSession() (string, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil || session == 0 {
		return "", errors.New("message start: native session unavailable")
	}
	return "windows:" + strconv.FormatUint(uint64(session), 10), nil
}
