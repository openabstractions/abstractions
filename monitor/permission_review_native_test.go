package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rights "github.com/openabstractions/abstraction-rights/go/client"
)

func nativeReviewFixture(t *testing.T, picture bool) (*servicePanel, *reviewPolicyFixture, *permissionReviewSources, permissionReviewContext, string) {
	t.Helper()
	p, f, s, c := reviewFixture(t)
	if picture {
		c = permissionReviewContext{Feature: "picture-workflow", App: "selected-app", Host: "selected-image"}
	}
	oldContext, oldPath := startPermissionReview, startPermissionReviewReceipt
	t.Cleanup(func() { startPermissionReview, startPermissionReviewReceipt = oldContext, oldPath })
	path := filepath.Join(t.TempDir(), "native-review", "decision.json")
	if err := preparePermissionReceiptPath(path); err != nil {
		t.Fatal(err)
	}
	startPermissionReview, startPermissionReviewReceipt = c, path
	return p, f, s, c, path
}

type reviewReceiptFailureOperator struct {
	*reviewPolicyFixture
	path string
}

type reviewBlockingOperator struct {
	*reviewPolicyFixture
	entered, release chan struct{}
}

func (f *reviewBlockingOperator) SetRuleForContext(ctx context.Context, revision string, rule rights.PolicyRule, ttl time.Duration, why string) (rights.PolicyEdit, error) {
	close(f.entered)
	<-f.release
	return f.reviewPolicyFixture.SetRuleForContext(ctx, revision, rule, ttl, why)
}

func TestPersonalReviewNativeWaitsForCommitAndReceiptAfterButton(t *testing.T) {
	p, f, s, _, path := nativeReviewFixture(t, false)
	op := &reviewBlockingOperator{f, make(chan struct{}), make(chan struct{})}
	s.Resolve = func(context.Context) (permissionReviewOperator, rights.Subject, error) { return op, f.setter, nil }
	done := make(chan error, 1)
	go func() {
		done <- p.askPersonalPermissionReview(context.Background(), unlockedSession(), &fakeSurface{option: "allow"})
	}()
	select {
	case <-op.entered:
	case <-time.After(time.Second):
		t.Fatal("commit not entered")
	}
	select {
	case err := <-done:
		t.Fatalf("returned before commit: %v", err)
	default:
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("receipt completed before commit")
	}
	close(op.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("returned before receipt")
	}
}

func (f *reviewReceiptFailureOperator) SetRuleForContext(ctx context.Context, revision string, rule rights.PolicyRule, ttl time.Duration, why string) (rights.PolicyEdit, error) {
	edit, err := f.reviewPolicyFixture.SetRuleForContext(ctx, revision, rule, ttl, why)
	if err == nil {
		err = os.WriteFile(f.path, []byte("occupied after apply"), 0600)
	}
	return edit, err
}

func TestPersonalReviewNativeReceiptFailureReportsLandedAndNoRoutineReport(t *testing.T) {
	p, f, s, _, path := nativeReviewFixture(t, false)
	op := &reviewReceiptFailureOperator{f, path}
	s.Resolve = func(context.Context) (permissionReviewOperator, rights.Subject, error) { return op, f.setter, nil }
	err := p.askPersonalPermissionReview(context.Background(), unlockedSession(), &fakeSurface{option: "allow"})
	if err == nil || f.writes != 1 || !strings.Contains(err.Error(), "receipt") || !strings.Contains(err.Error(), "Show notifications") {
		t.Fatalf("lost landed failure: %v writes%d", err, f.writes)
	}
	bridge, frames := nativeBridgeFixture(t)
	bridge.session(true)
	done := make(chan error, 1)
	go func() { done <- finishPersonalPermissionReview(err, bridge.showReport) }()
	frame := nextNativeFrame(t, frames, "report")
	if !strings.Contains(frame.Content, "Show notifications") {
		t.Fatal("native error report lost saved operation")
	}
	bridge.dismiss(nativeAsksResponse{})
	if <-done == nil {
		t.Fatal("report converted failure to success")
	}
	for _, outcome := range []error{nil, errPersonalReviewDismissed, errPromptNotShown} {
		if e := finishPersonalPermissionReview(outcome, func(presenterReport) bool { t.Fatal("routine confirmation shown"); return false }); e != nil {
			t.Fatal(e)
		}
	}
}

func TestPersonalReviewNativeBothFeatureViewsAndSharedDecision(t *testing.T) {
	for _, picture := range []bool{false, true} {
		t.Run(map[bool]string{false: "notifications", true: "picture"}[picture], func(t *testing.T) {
			p, f, s, c, path := nativeReviewFixture(t, picture)
			view, _, _, err := buildPermissionReview(context.Background(), *s, c)
			if err != nil {
				t.Fatal(err)
			}
			surface := &fakeSurface{option: "allow"}
			if err = p.askPersonalPermissionReview(context.Background(), unlockedSession(), surface); err != nil {
				t.Fatal(err)
			}
			if len(surface.shown) != 1 || surface.shown[0].Permission == nil {
				t.Fatal("not one native permission prompt")
			}
			prompt := surface.shown[0]
			for _, sentence := range view.Sentences {
				if !strings.Contains(prompt.Permission.Scope, sentence) || !strings.Contains(prompt.Content, sentence) {
					t.Fatalf("missing operation %q", sentence)
				}
			}
			if !strings.Contains(prompt.Permission.Effects, "One hour") || strings.Contains(prompt.Content, "Ongoing") {
				t.Fatal("wrong lifetime")
			}
			if !picture && !strings.Contains(*prompt.Permission.Warning, "all scripts") {
				t.Fatal("shared host breadth hidden")
			}
			if f.writes != len(view.Rules) {
				t.Fatalf("writes %d", f.writes)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var receipt permissionReviewReceipt
			if json.Unmarshal(raw, &receipt) != nil {
				t.Fatal("bad receipt")
			}
			if receipt.Result.Outcome != "applied" || receipt.Review.ID != view.ID || receipt.NativeActor != f.setter || receipt.PID != os.Getpid() {
				t.Fatal("receipt binding differs")
			}
			// HTTP and native share the terminal latch; no second writer exists.
			if _, _, err := p.decidePermissionReview(context.Background(), permissionReviewChoice{Context: view.Context, Shown: view.ID, Allow: true}); err == nil {
				t.Fatal("native decision replayed")
			}
		})
	}
}

func TestPersonalReviewNativeRefusalDismissAndChangedInput(t *testing.T) {
	for _, kind := range []string{"refuse", "dismiss", "invalid", "locked", "changed", "partial", "uncertain"} {
		t.Run(kind, func(t *testing.T) {
			p, f, s, _, path := nativeReviewFixture(t, kind == "partial")
			session := unlockedSession()
			surface := &fakeSurface{option: "allow"}
			switch kind {
			case "refuse":
				surface.option = "refuse"
			case "dismiss":
				surface.option = ""
			case "invalid":
				surface.option = "injected"
			case "locked":
				surface.during = func() { session.revision.Add(1) }
			case "changed":
				surface.during = func() {
					old := s.Program
					s.Program = func(path string) (permissionReviewProgram, error) {
						v, e := old(path)
						v.SHA256 = strings.Repeat("f", 64)
						return v, e
					}
				}
			case "partial":
				f.failAt = 2
			case "uncertain":
				f.uncertain = true
			}
			err := p.askPersonalPermissionReview(context.Background(), session, surface)
			if kind == "refuse" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unexpected success")
			}
			if kind == "partial" {
				if f.writes != 2 || !strings.Contains(err.Error(), "Saved permissions:") {
					t.Fatal("partial not reported")
				}
				return
			}
			if kind == "uncertain" {
				if f.writes != 1 || !strings.Contains(err.Error(), "verified") {
					t.Fatal("uncertainty not reported")
				}
				return
			}
			if f.writes != 0 {
				t.Fatal("unapproved write")
			}
			raw, readErr := os.ReadFile(path)
			if kind == "refuse" {
				var receipt permissionReviewReceipt
				if json.Unmarshal(raw, &receipt) != nil || receipt.Result.Outcome != "refused" {
					t.Fatal("refusal receipt missing")
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal("nondecision wrote receipt")
			}
		})
	}
}

func TestPersonalReviewNativeUsesExistingMacBridgeFrame(t *testing.T) {
	p, f, _, _, _ := nativeReviewFixture(t, false)
	bridge, frames := nativeBridgeFixture(t)
	bridge.session(true)
	done := make(chan error, 1)
	go func() { done <- p.askPersonalPermissionReview(context.Background(), bridge, bridge) }()
	frame := nextNativeFrame(t, frames, "question")
	if frame.Prompt == nil || !frame.Prompt.PermissionPrompt || !strings.Contains(frame.Prompt.Content, "One hour") || !strings.Contains(frame.Prompt.Warning, "all scripts") {
		t.Fatal("native frame lost review meaning")
	}
	bridge.dismiss(nativeAsksResponse{option: "refuse"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.writes != 0 {
		t.Fatal("refusal wrote rights")
	}
}
