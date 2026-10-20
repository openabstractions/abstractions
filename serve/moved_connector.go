package main

import "github.com/openabstractions/abstractions/mcp-gateway/nativecli"

// movedConnectorCaller reports a caller program that is a connector copy Setup
// moved aside when it published an update. Windows names a running process by
// its moved image, so an agent app that kept running the previous connector
// reconnects under that throwaway path. Every rights decision for it reads
// denied and no person question names it: Allow would save a rule for a file
// the next sweep deletes. The app's restart runs the published connector.
func movedConnectorCaller(program string) bool {
	_, moved := nativecli.MovedAgentImage(program)
	return moved
}
