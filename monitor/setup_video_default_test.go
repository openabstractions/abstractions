package main

import (
	"context"
	"testing"
	"time"

	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
)

// The served video set gets the video policy that lists the gateway's
// oa_video_generate; a policy the person set stays; with no video served
// nothing is written.
func TestSetupFillVideoDefault(t *testing.T) {
	ctx := context.Background()
	f := &fakePictureDefaults{read: iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeUnknown}}
	useFakePictureDefaults(t, f)
	served := false
	previous := setupVideoServed
	setupVideoServed = func(context.Context) (bool, error) { return served, nil }
	t.Cleanup(func() { setupVideoServed = previous })

	if err := setupFillVideoDefault(ctx); err != nil || len(f.puts) != 0 {
		t.Fatalf("no video served wrote %v: %v", f.puts, err)
	}
	served = true
	if err := setupFillVideoDefault(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 || f.puts[0].Name != videoPolicy || len(f.puts[0].Candidates) != 1 ||
		f.puts[0].Candidates[0].Model != stepVideoSet || f.puts[0].Candidates[0].Placement != iwire.CandidatePlacementLocal {
		t.Fatalf("missing policy not filled: %+v", f.puts)
	}
	f.puts = nil
	f.read = iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeFound, Policy: &iclient.SelectionPolicyV2{Name: videoPolicy,
		Candidates: []iclient.SelectionCandidateV2{{Model: "chosen-by-person", Placement: iwire.CandidatePlacementLocal}}}}
	if err := setupFillVideoDefault(ctx); err != nil || len(f.puts) != 0 {
		t.Fatalf("the person's policy was overwritten: %+v %v", f.puts, err)
	}
}

// At sign-in the presenter fills the video policy once the router serves the
// set, trying again while the model host starts, and stops once it is named.
func TestFillVideoDefaultAtSignInWaitsForTheServedSet(t *testing.T) {
	f := &fakePictureDefaults{read: iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeUnknown}}
	useFakePictureDefaults(t, f)
	reads := 0
	previous, wait := setupVideoServed, videoDefaultWait
	setupVideoServed = func(context.Context) (bool, error) { reads++; return reads >= 3, nil }
	videoDefaultWait = time.Millisecond
	t.Cleanup(func() { setupVideoServed, videoDefaultWait = previous, wait })

	fillVideoDefaultAtSignIn(context.Background())
	if reads != 3 || len(f.puts) != 1 || f.puts[0].Name != videoPolicy || f.puts[0].Candidates[0].Model != stepVideoSet {
		t.Fatalf("served on the third read: %d reads, puts %+v", reads, f.puts)
	}
}
