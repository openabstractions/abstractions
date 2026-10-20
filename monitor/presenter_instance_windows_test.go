//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
)

func TestQuestionPresenterExcludesRepeatedStartupAndReleasesOnClose(t *testing.T) {
	endpoint := fmt.Sprintf("%s/%d/%d", t.Name(), os.Getpid(), time.Now().UnixNano())
	release, acquired, err := claimQuestionPresenter(endpoint)
	if err != nil || !acquired {
		t.Fatalf("first presenter: acquired=%t err=%v", acquired, err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	duplicateRelease, duplicate, err := claimQuestionPresenter(endpoint)
	if duplicateRelease != nil {
		defer duplicateRelease()
	}
	if err != nil || duplicate {
		t.Fatalf("repeated startup: acquired=%t err=%v", duplicate, err)
	}
	otherRelease, other, err := claimQuestionPresenter(endpoint + "/other-runtime")
	if err != nil || !other {
		t.Fatalf("different runtime: acquired=%t err=%v", other, err)
	}
	otherRelease()
	release()
	release = nil
	restartedRelease, restarted, err := claimQuestionPresenter(endpoint)
	if err != nil || !restarted {
		t.Fatalf("restart after close: acquired=%t err=%v", restarted, err)
	}
	restartedRelease()
}

func TestQuestionPopupDefersSecondPresenterUntilFirstCloses(t *testing.T) {
	// Use a real, isolated kernel slot while another presenter attempts to show.
	path := filepath.Join(t.TempDir(), "question")
	claim := func(ctx context.Context) (func(), error) { return cas.LockContext(ctx, path, false) }
	first := withQuestionPopup(t.Context(), claim, false, func(ctx context.Context) presentResult {
		second := withQuestionPopup(ctx, claim, false, func(context.Context) presentResult {
			t.Fatal("second popup was presented while the first held the slot")
			return presentResult{}
		})
		if second.Outcome != presentDeferred {
			t.Fatal(second)
		}
		return presentResult{Outcome: presentStale, ClosedElsewhere: true}
	})
	if first.Outcome != presentStale {
		t.Fatal(first)
	}
	next := withQuestionPopup(t.Context(), claim, false, func(context.Context) presentResult {
		return presentResult{Outcome: presentAnswered}
	})
	if next.Outcome != presentAnswered {
		t.Fatal(next)
	}
}

func TestQuestionPopupCrossProcessCancellationAndRelease(t *testing.T) {
	const pathEnv = "OA_TEST_QUESTION_POPUP_PATH"
	if path := os.Getenv(pathEnv); path != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		release, err := cas.LockContext(ctx, path, false)
		if release != nil {
			release()
		}
		if os.Getenv(pathEnv+"_BUSY") == "1" {
			if err != context.DeadlineExceeded {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "question")
	release, err := cas.LockContext(t.Context(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	child := func(busy string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQuestionPopupCrossProcessCancellationAndRelease$")
		cmd.Env = append(os.Environ(), pathEnv+"="+path, pathEnv+"_BUSY="+busy)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child: %v: %s", err, output)
		}
	}
	child("1")
	release()
	release = nil
	child("0")
}

func TestQuestionPopupPathUsesKnownFolder(t *testing.T) {
	var first string
	for _, name := range []string{"one", "two"} {
		env := filepath.Join(t.TempDir(), name)
		t.Setenv("LOCALAPPDATA", env)
		path, err := questionPopupPath()
		if err != nil {
			t.Fatal(err)
		}
		if path == filepath.Join(env, "OpenAbstractions", "presenter", filepath.Base(path)) {
			t.Fatal("popup slot followed inherited LOCALAPPDATA")
		}
		if name == "one" {
			first = path
		} else if path != first {
			t.Fatal("popup slot changed across inherited environments")
		}
	}
}
