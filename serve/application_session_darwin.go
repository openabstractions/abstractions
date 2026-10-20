//go:build darwin

package main

import (
	identity "github.com/openabstractions/abstraction-identity"
	"strconv"
)

// Only a retained native occurrence supplies this session fact. Ordinary XPC
// peers retain their existing unknown generation and audit-session values.
func applicationSession(peer *identity.Peer) (string, bool) {
	if peer == nil || peer.Platform != "darwin" {
		return "", false
	}
	u, userErr := peer.User.AtLeast(identity.ProofKernel)
	p, processErr := peer.Process.AtLeast(identity.ProofBound)
	if userErr != nil || processErr != nil || u.Kind != "posix" || p.Generation == 0 || p.PID <= 0 || p.Recycled || !p.StartTime.IsZero() || u.AuditSessionID == 0 {
		return "", false
	}
	self, err := identity.Self()
	if err != nil || self.AuditSessionID == 0 || self.UID != u.UID || self.AuditSessionID != u.AuditSessionID {
		return "", false
	}
	return "darwin:" + strconv.FormatUint(uint64(self.AuditSessionID), 10), true
}
