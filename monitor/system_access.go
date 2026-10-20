package main

import (
	"errors"
	"log"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/protected"
)

// launchSystemAccess starts the installed protected-operation helper beside
// the Panel through the platform's administrator prompt (facade FAC-H3). It
// returns once the person answered the prompt and records the outcome, the
// machine's UAC policy and, later, the helper's exit. A layout with no
// trusted helper launches nothing; the ticket then expires unclaimed.
func launchSystemAccess(owner uintptr, operation, ticket string) {
	image, err := protected.OwnImage()
	if err != nil {
		log.Printf("system access %s: %v", ticket, err)
		return
	}
	helper, err := protected.TrustedHelper(image)
	if err != nil {
		log.Printf("system access %s: no administrator prompt raised: %v", ticket, err)
		return
	}
	policy := protected.ReadUACPolicy()
	wait, err := protected.Launch(owner, helper, operation, ticket)
	switch {
	case errors.Is(err, protected.ErrCancelled):
		log.Printf("system access %s: the administrator prompt was refused; %s", ticket, policy)
		return
	case err != nil:
		log.Printf("system access %s: the helper did not start: %v; %s", ticket, err, policy)
		return
	}
	log.Printf("system access %s: the administrator prompt was approved; %s", ticket, policy)
	go func() {
		code, err := wait(protected.HelperDeadline + 15*time.Second)
		log.Printf("system access %s: helper exit %d %v", ticket, code, err)
	}()
}
