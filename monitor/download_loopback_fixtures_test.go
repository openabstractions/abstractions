package main

import downloadserve "github.com/openabstractions/abstraction-download/go/serve"

// The download fixtures these tests serve listen on loopback, which the
// download service refuses outside tests (download DL-G4).
func init() { downloadserve.AllowLoopbackForTesting() }
