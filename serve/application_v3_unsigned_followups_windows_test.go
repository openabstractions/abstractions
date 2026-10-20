//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// changedAndDeclined keeps the program, changes its image, and answers the
// first review Not now. It returns the kept revision.
func (run *unsignedRun) changedAndDeclined(t *testing.T) string {
	t.Helper()
	run.register(t)
	kept := run.keep(t)
	writeUnsignedImage(t, run.program, "ComfyUI", true)
	run.refused(t, kept.Revision)
	run.declineReview(t, pendingReview(t, run.f))
	return kept.Revision
}

// refused revalidates revision and requires the changed-image refusal.
func (run *unsignedRun) refused(t *testing.T, revision string) {
	t.Helper()
	if _, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", revision); !errors.Is(err, errApplicationV3ImageChanged) {
		t.Fatal("a changed unsigned image was not paused", err)
	}
}

// asksAgain revalidates revision and answers the new review Not now.
func (run *unsignedRun) asksAgain(t *testing.T, revision string) {
	t.Helper()
	run.refused(t, revision)
	run.declineReview(t, pendingReview(t, run.f))
}

// asksNothing revalidates revision and requires no new review.
func (run *unsignedRun) asksNothing(t *testing.T, revision string) {
	t.Helper()
	run.refused(t, revision)
	run.waitReviewClosed(t)
	run.noPendingReview(t)
}

// A changed program in a restart loop announces each start. Its own starts
// re-ask about the image the person answered Not now about at most once an
// hour; a start the person makes through OpenAbstractions always re-asks.
func TestUnsignedProgramAnnounceReasksOncePerHour(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	var mu sync.Mutex
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	run.k.unsigned.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	revision := run.changedAndDeclined(t)

	run.d.unsignedLaunch("comfyui", false)
	run.asksAgain(t, revision)
	run.d.unsignedLaunch("comfyui", false)
	run.asksNothing(t, revision)

	run.d.unsignedLaunch("comfyui", true)
	run.asksAgain(t, revision)
	run.d.unsignedLaunch("comfyui", false)
	run.asksNothing(t, revision)

	mu.Lock()
	now = now.Add(unsignedAnnounceReask)
	mu.Unlock()
	run.d.unsignedLaunch("comfyui", false)
	run.asksAgain(t, revision)
}

// A review question belongs to the runtime that asked it. A restart withdraws
// the open one, and the next use asks afresh; the person's Keep on that
// question pins the image.
func TestUnsignedProgramReviewAfterRestartTakesEffect(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	run.register(t)
	kept := run.keep(t)
	writeUnsignedImage(t, run.program, "ComfyUI", true)
	run.refused(t, kept.Revision)
	stale := pendingReview(t, run.f)

	run.restart(t)
	if err := run.f.a.withdrawStale(); err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := run.f.a.book.AnswerApplication(stale.ID, "keep"); err == nil && outcome == "answered" {
		t.Fatal("the previous runtime's review was answerable after the restart")
	}
	run.refused(t, kept.Revision)
	fresh := pendingReview(t, run.f)
	if fresh.ID == stale.ID {
		t.Fatal("the restart reused the previous runtime's review")
	}
	answer(t, run.f, fresh.ID, "keep")
	waitDirectory(t, run.d, func() bool { e, _, _ := run.entry(); return e.Revision != kept.Revision })
	e, f, _ := run.entry()
	image, size := imagePin(t, run.program)
	if e.State != "enabled" || f.Image != image || f.ImageSize != size {
		t.Fatalf("Keep after the restart did not pin the reviewed image: %+v", e)
	}
	held, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", e.Revision)
	if err != nil {
		t.Fatal("the image kept after the restart was refused", err)
	}
	held.Close()
}

// The image the person answered Not now about is refused by its file stamp
// without reading the whole image again. A rewrite is read in full: the
// declined image rewritten stays refused, and the kept image restored is
// admitted.
func TestUnsignedProgramDeclinedImageIsNotHashedAgain(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	var hashed atomic.Int64
	run.d.imagePin = func(ctx context.Context, h *heldApplicationManifest) (string, int64, error) {
		hashed.Add(1)
		return applicationHeldPin(ctx, h)
	}
	revision := run.changedAndDeclined(t)

	hashed.Store(0)
	for range 3 {
		run.refused(t, revision)
	}
	if n := hashed.Load(); n != 0 {
		t.Fatalf("the declined image was hashed %d times", n)
	}
	run.noPendingReview(t)

	// The same declined image written again is read in full, stays declined
	// and is then refused by its new stamp.
	writeUnsignedImage(t, run.program, "ComfyUI", true)
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(run.program, later, later); err != nil {
		t.Fatal(err)
	}
	run.asksNothing(t, revision)
	if hashed.Load() == 0 {
		t.Fatal("a rewritten image was refused without reading it")
	}
	hashed.Store(0)
	run.refused(t, revision)
	if n := hashed.Load(); n != 0 {
		t.Fatalf("the rewritten declined image was hashed %d times", n)
	}

	// The kept image restored continues.
	writeUnsignedImage(t, run.program, "ComfyUI", false)
	restored := later.Add(time.Hour)
	if err := os.Chtimes(run.program, restored, restored); err != nil {
		t.Fatal(err)
	}
	held, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", revision)
	if err != nil {
		t.Fatal("the restored kept image was refused", err)
	}
	held.Close()
}
