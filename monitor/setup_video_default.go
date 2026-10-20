package main

import (
	"context"
	"fmt"
	"log"
	"time"

	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

// videoPolicy is the account default selection policy video apps read; the
// MCP gateway lists oa_video_generate while it names a local model.
const videoPolicy = "oa-video-default"

// setupVideoServed reports whether the router serves the video set here;
// tests replace it.
var setupVideoServed = func(ctx context.Context) (bool, error) { return stepVideoRouted(ctx, stepVideoSet, false) }

// setupFillVideoDefault names the video set in the account's video policy
// when the policy has no candidate and the router serves the set on this
// computer. A policy the person set stays as it is. Setup runs it whenever
// it reads its overview and after the video set becomes ready, and the
// presenter at each sign-in (fillVideoDefaultAtSignIn), which heals installs
// that got video before the policy existed.
func setupFillVideoDefault(ctx context.Context) error {
	_, err := setupVideoDefaultNamed(ctx)
	return err
}

// setupVideoDefaultNamed is setupFillVideoDefault reporting whether the
// policy names a model afterwards.
func setupVideoDefaultNamed(ctx context.Context) (bool, error) {
	store, err := setupPictureDefaultStore(ctx)
	if err != nil {
		return false, err
	}
	read, err := store.ReadAccountDefault(ctx, videoPolicy)
	if err != nil {
		return false, err
	}
	switch read.Outcome {
	case iwire.SelectionReadOutcomeFound:
		if read.Policy != nil && len(read.Policy.Candidates) > 0 {
			return true, nil
		}
	case iwire.SelectionReadOutcomeUnknown:
	default:
		return false, fmt.Errorf("the video default cannot be read: %s", read.Outcome)
	}
	served, err := setupVideoServed(ctx)
	if err != nil || !served {
		return false, err
	}
	change, err := store.PutAccountDefault(ctx, read.Revision, localFirstPolicy(videoPolicy, read, stepVideoSet))
	if err != nil {
		return false, err
	}
	if change.Outcome != iwire.EditOutcomeApplied {
		return false, fmt.Errorf("the video default was not written: %s %s", change.Outcome, change.Reason)
	}
	return true, nil
}

// The sign-in fill tries while the runtime and its model hosts start.
var (
	videoDefaultAttempts = 5
	videoDefaultWait     = time.Minute
)

// fillVideoDefaultAtSignIn runs setupFillVideoDefault as the presenter
// starts, until the policy names a model or the tries run out; a computer
// without the video set then keeps no policy, and one log line says why.
func fillVideoDefaultAtSignIn(lifetime context.Context) {
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(lifetime, 2*time.Minute)
		named, err := setupVideoDefaultNamed(ctx)
		cancel()
		if named || lifetime.Err() != nil {
			return
		}
		if attempt == videoDefaultAttempts {
			if err != nil {
				log.Printf("video default: not written after %d tries: %v", attempt, err)
			}
			return
		}
		select {
		case <-lifetime.Done():
			return
		case <-time.After(videoDefaultWait):
		}
	}
}
