//go:build windows

package main

import (
	"fmt"
	"os"
	"testing"
	"time"
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
