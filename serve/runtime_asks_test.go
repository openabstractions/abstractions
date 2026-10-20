package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func firstUseBook(t *testing.T) (*runtimeAsks, *asks.Book) {
	t.Helper()
	book, err := asks.LoadApplicationBook(filepath.Join(t.TempDir(), "application.json"))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return &runtimeAsks{book: book, rights: &runtimeRights{owner: owner.Uid}, scope: "asks-owner-program@1:runtime", display: "runtime.exe",
		report: func(err error) { t.Error(err) }}, book
}

// TestFirstUseAdmissionRecordsRequesterPublisher: the runtime records the
// requester's publisher with the first-use question it admits (ASK-A6). The
// evidence is taken once, when a new question is admitted; a replay of the
// pending question takes none.
func TestFirstUseAdmissionRecordsRequesterPublisher(t *testing.T) {
	a, book := firstUseBook(t)
	calls := 0
	evidence := func() asks.Publisher {
		calls++
		return asks.Publisher{Subject: "Test Signer", Trusted: true}
	}
	subject := rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), "opencode.exe")}
	a.admitFirstUse(subject, evidence, host.ModelLookupAction, "hf")
	a.admitFirstUse(subject, evidence, host.ModelLookupAction, "hf")
	records, _, err := book.ApplicationHistory()
	if err != nil || len(records) != 1 {
		t.Fatalf("one first-use question expected: %+v %v", records, err)
	}
	if r := records[0]; r.About != subject.Program || r.Publisher != "Test Signer" || !r.PublisherTrusted {
		t.Fatalf("the question must carry the requester's publisher: %+v", r)
	}
	if calls != 1 {
		t.Fatalf("publisher evidence taken %d times, want once", calls)
	}
}

// TestExpiredFirstUseQuestionIsAskedAgain: a first-use question nobody
// answered within its lifetime (ASK-A7) is replaced on the next refusal by a
// new question under the same key; expiry consumes no generation.
func TestExpiredFirstUseQuestionIsAskedAgain(t *testing.T) {
	a, book := firstUseBook(t)
	if err := book.SetPendingLifetime(200 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), "opencode.exe")}
	a.admitFirstUse(subject, nil, host.ModelLookupAction, "hf")
	first, _, err := book.ApplicationHistory()
	if err != nil || len(first) != 1 {
		t.Fatalf("first question %+v %v", first, err)
	}
	time.Sleep(300 * time.Millisecond)
	if expired, _, err := book.ApplicationHistory(); err != nil || len(expired) != 0 {
		t.Fatalf("the expired question must no longer be listed: %+v %v", expired, err)
	}
	a.admitFirstUse(subject, nil, host.ModelLookupAction, "hf")
	second, _, err := book.ApplicationHistory()
	if err != nil || len(second) != 1 || second[0].ID == first[0].ID || !second[0].Pending() {
		t.Fatalf("a refusal after expiry must ask again: %+v %v", second, err)
	}
	sum := sha256.Sum256([]byte(subject.Program + "\x00" + host.ModelLookupAction + "\x00" + "hf"))
	if _, outcome, err := book.ObserveApplication(a.scope, "first-use:"+hex.EncodeToString(sum[:20])); err != nil || outcome != "pending" {
		t.Fatalf("the new question must reuse generation 0 of the key: %q %v", outcome, err)
	}
}

// TestFirstUseWithoutPeerRecordsNoPublisher: a not_granted decision that
// reaches the runtime without the caller's peer (the window and remote routes)
// admits its first-use question with no publisher (ASK-A6). Whatever file is
// at the program path then is not evidence about the caller: a program can
// put a signed file there for the moment of the refusal and restore itself.
func TestFirstUseWithoutPeerRecordsNoPublisher(t *testing.T) {
	program := `C:\Program Files\Git\cmd\git.exe`
	if runtime.GOOS != "windows" {
		program = "/usr/bin/git"
	}
	if _, err := os.Stat(program); err != nil {
		t.Skip("a signed program file is needed at a known path to show the file is ignored")
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), firstUseActions)
	if err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	r := &runtimeRights{policy: policy, owner: a.rights.owner, firstUse: a.admitFirstUse}
	a.rights = r
	subject := rwire.Subject{Account: r.owner, Program: program}
	if d := r.decideAsking(context.Background(), subject, host.ModelLookupAction, "hf"); d.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("decision = %+v", d)
	}
	records, _, err := book.ApplicationHistory()
	if err != nil || len(records) != 1 {
		t.Fatalf("one first-use question expected: %+v %v", records, err)
	}
	if r := records[0]; r.Publisher != "" || r.PublisherTrusted {
		t.Fatalf("a question admitted without the peer must record no publisher: %+v", r)
	}
}
