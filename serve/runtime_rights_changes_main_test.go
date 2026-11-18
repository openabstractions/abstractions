package main

import identity "github.com/openabstractions/abstraction-identity"

// Fixtures written before change requests (rights RIGHTS-O17) edit rights
// directly through the operator CLI; the change gate's own tests clear this
// per runtime through composedRights.
func init() { testOperatorDirect = func(*identity.Peer) bool { return true } }
