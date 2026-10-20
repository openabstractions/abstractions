package main

import (
	"context"
	"log"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// pictureReconcileAttempts and pictureReconcileWait bound how long the
// presenter waits for a runtime that starts after it: an MSI starts the
// runtime first, a sign-in may start the presenter first.
const (
	pictureReconcileAttempts = 5
	pictureReconcileWait     = time.Minute
)

// reconcileSetupPictureEngine runs setup.ReconcilePictureEngine once the
// runtime answers and has read OA's picture store, as the background
// presenter starts. After the last try the earlier registration stays and
// one log line says why.
func reconcileSetupPictureEngine(lifetime context.Context) {
	journal, err := setup.DefaultJournal()
	if err != nil {
		log.Printf("picture engine: %v", err)
		return
	}
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(lifetime, 30*time.Minute)
		err = setup.ReconcilePictureEngine(ctx, journal)
		cancel()
		if err == nil || lifetime.Err() != nil {
			return
		}
		if attempt == pictureReconcileAttempts {
			// Setup's earlier registration stays in place.
			log.Printf("picture engine: the earlier registration stays after %d tries: %v", attempt, err)
			return
		}
		select {
		case <-lifetime.Done():
			return
		case <-time.After(pictureReconcileWait):
		}
	}
}
