//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
)

// Security review 2026-11-22, F7: the installed Presenter, matched by its
// image's file identity over a real named-pipe peer, answers questions and
// completes the effect of the person's answer, and makes no other edit.
func TestPresenterOnlyAnswersAndCompletes(t *testing.T) {
	c, r := changeGateUnit(t, true)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	held, err := openPanelImage(context.Background(), fileid.Self, exe, fileid.FollowLocalLinks)
	if err != nil {
		t.Fatal(err)
	}
	r.panels.presenters = append(r.panels.presenters, held)
	r.panels.presenterPaths = append(r.panels.presenterPaths, exe)
	peer, _ := applicationV3NativePeer(t)
	by, err := rightsSubjectOf(peer)
	if err != nil {
		t.Fatal(err)
	}
	if !r.isPresenterPeer(context.Background(), peer) {
		t.Fatal("the presenter image was not matched")
	}
	if err := c.Admit(context.Background(), peer, by); !errors.Is(err, rightsservice.ErrOperatorForbidden) {
		t.Fatalf("the presenter made a direct edit: %v", err)
	}
	if err := c.AdmitCompletion(context.Background(), peer, by); err != nil {
		t.Fatalf("the presenter could not complete an answered effect: %v", err)
	}
	if err := c.authorizeAnswer(context.Background(), peer); err != nil {
		t.Fatalf("the presenter could not answer: %v", err)
	}
}
