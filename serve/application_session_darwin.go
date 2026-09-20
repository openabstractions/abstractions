//go:build darwin

package main

import identity "github.com/openabstractions/abstraction-identity"

// The unix listener cannot bind a macOS peer process, and the applications
// profile already refuses such callers. Activation also refuses rather than
// guessing whether the caller shares this runtime's audit session.
func applicationSession(*identity.Peer) (string, bool) { return "", false }
