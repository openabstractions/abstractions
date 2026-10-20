package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// --register and --read-secret take no download arguments and exclude each other.
func TestRegisterAndReadSecretTakeNoDownloadArguments(t *testing.T) {
	for _, args := range [][]string{{"--register", "http://h/x", "1", "sha256:00"}, {"--register", "--read-secret", "token"}, {"--read-secret", "token", "extra"}} {
		if err := run(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Usage:") {
			t.Fatalf("%v: want the usage, got %v", args, err)
		}
	}
}

// Each Register outcome reads as what it means for the person.
func TestRegisterSentence(t *testing.T) {
	for _, tc := range []struct {
		change wire.ApplicationChange
		want   string
	}{
		{wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied}, "registered itself as Pretend Lemonade; OpenAbstractions asks whether to keep it"},
		{wire.ApplicationChange{Outcome: wire.ApplicationOutcomeConflict, Reason: "name"}, "Pretend Lemonade is already in Apps"},
		{wire.ApplicationChange{Outcome: wire.ApplicationOutcomeConflict, Reason: "unconfirmed"}, "Pretend Lemonade already added itself and waits for your answer"},
		{wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, "registration refused: forbidden (the Add itself to Apps default is off, or this app is blocked)"},
	} {
		if got := registerSentence(tc.change); got != tc.want {
			t.Errorf("%+v: %q", tc.change, got)
		}
	}
}

// Only a rights refusal, which the person can lift, makes a new run try the
// request again as the next attempt (JOB-A7).
func TestOnlyRightsRefusalsRunAgain(t *testing.T) {
	failed := func(message string) *api.OperationSnapshot {
		return &api.OperationSnapshot{State: api.WorkStateFailed, Failure: &api.WorkFailure{Message: message}}
	}
	for _, tc := range []struct {
		snapshot *api.OperationSnapshot
		want     bool
	}{
		{failed("rights:denied"), true},
		{failed("rights:unavailable"), true},
		{failed("digest_mismatch"), false},
		{&api.OperationSnapshot{State: api.WorkStateFailed}, false},
		{&api.OperationSnapshot{State: api.WorkStateCancelled}, false},
		{&api.OperationSnapshot{State: api.WorkStateComplete}, false},
		{nil, false},
	} {
		if got := rightsRefused(tc.snapshot); got != tc.want {
			t.Errorf("%+v: %v", tc.snapshot, got)
		}
	}
}

// A request that failed for another reason, here a digest the bytes do not
// match, is reused as it is: the next run reports the same failed operation.
func TestOtherFailureIsNotRetried(t *testing.T) {
	serviceRuntime(t)
	payload := bytes.Repeat([]byte("x"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "bad.bin", time.Now(), bytes.NewReader(payload))
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())
	args := []string{srv.URL + "/bad.bin", strconv.Itoa(len(payload)), "sha256:" + strings.Repeat("0", 64)}
	var first bytes.Buffer
	if err := run(args, &first); err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("first run: %v\n%s", err, first.String())
	}
	var second bytes.Buffer
	err := run(args, &second)
	if err == nil || strings.Contains(second.String(), "trying again") || operation(first.String()) == "" || operation(first.String()) != operation(second.String()) {
		t.Fatalf("second run must reuse the failed operation: %v\n%s\n%s", err, first.String(), second.String())
	}
}

func operation(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "submitted "); ok {
			return id
		}
	}
	return ""
}
