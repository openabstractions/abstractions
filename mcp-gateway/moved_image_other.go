//go:build !windows

package main

// ownMovedImageSentence is empty off Windows: a Darwin update publishes a new
// helper path and leaves the running image's name alone.
func ownMovedImageSentence() string { return "" }

// ownImageMoved is false off Windows for the same reason.
func ownImageMoved() bool { return false }

// agentSuccessorLauncher is nil off Windows: a running helper keeps its
// admitted image there, so no handover is needed.
func agentSuccessorLauncher(string, string, []string, []string, agentEnrollmentRead) func() (*agentSuccessor, error) {
	return nil
}
