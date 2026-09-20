//go:build !windows && !linux && !darwin

package main

import identity "github.com/openabstractions/abstraction-identity"

func applicationSession(*identity.Peer) (string, bool) { return "", false }
