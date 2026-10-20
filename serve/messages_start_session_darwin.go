package main

import (
	"errors"
	identity "github.com/openabstractions/abstraction-identity"
	"strconv"
)

func messageStartNativeSession() (string, error) {
	self, err := identity.Self()
	if err != nil || self.AuditSessionID == 0 {
		return "", errors.New("message start: native session unavailable")
	}
	return "darwin:" + strconv.FormatUint(uint64(self.AuditSessionID), 10), nil
}
