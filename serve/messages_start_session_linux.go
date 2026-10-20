package main

import (
	"errors"
	"strconv"
)

func messageStartNativeSession() (string, error) {
	session, ok := linuxAuditSession("/proc/self/sessionid")
	if !ok {
		return "", errors.New("message start: native session unavailable")
	}
	return "linux:" + strconv.FormatUint(session, 10), nil
}
