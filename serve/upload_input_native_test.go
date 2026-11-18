package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	storageclient "github.com/openabstractions/abstraction-storage/go/client"
)

// UPLOAD-INPUT-QUESTION-NATIVE, architect ruling 2026-11-22 (2), storage
// STO-W13: a fresh native program's upload through content-writer@1 is
// refused at Begin and raises nothing. Through client.UploadInput it asks
// first: permission@1 Request raises the paired input:image question about
// that program, the person's Allow writes the rules, and the same upload
// then publishes and feeds the program's request read.
func TestNativeUploadAsksBeforeWritingInput(t *testing.T) {
	content, r, subject, writer, ctx := ipcWriter(t)
	a, book := firstUseBook(t)
	a.rights = r
	r.firstUse, r.pendingQuestions, r.pairedInput, r.awaitQuestions = a.admitFirstUse, a.pendingQuestions, a.admitPairedInput, a.awaitQuestions
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		t.Fatal(err)
	}
	r.policy.Definitions = catalog
	endpoint := listen.Endpoint(fmt.Sprintf("upload-input-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	h, err := asksservice.Listen(endpoint, book)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePermission(a.preparePermission); err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePermissionObservation(a.observePermission); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- h.Serve(ctx) }()
	t.Cleanup(func() {
		h.Close()
		select {
		case <-served:
		case <-time.After(time.Second):
			t.Error("permission host did not drain")
		}
	})
	pendingAbout := func() []asks.Record {
		t.Helper()
		records, _, err := book.ApplicationHistory()
		if err != nil {
			t.Fatal(err)
		}
		var pending []asks.Record
		for _, q := range records {
			if q.Pending() && samePrograms(q.About, subject.Program) {
				pending = append(pending, q)
			}
		}
		return pending
	}
	digest := digestOf(editInputPNG)
	size := int64(len(editInputPNG))

	// Begin alone: forbidden, and no question is raised.
	var refused *storageclient.OutcomeError
	if _, err := writer.Write(ctx, requestID(t), digest, bytes.NewReader(editInputPNG), size); !errors.As(err, &refused) || refused.Operation != "begin" || refused.Outcome != "forbidden" {
		t.Fatalf("an unasked native upload: %v", err)
	}
	if pending := pendingAbout(); len(pending) != 0 {
		t.Fatalf("Begin raised questions: %+v", pending)
	}

	// UploadInput: the person answers the one question it raises.
	panel := rwire.Subject{Account: r.owner, Program: r.operators[len(r.operators)-1]}
	answered := make(chan string, 1)
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			records, _, err := book.ApplicationHistory()
			if err != nil {
				continue
			}
			for _, q := range records {
				if !q.Pending() || !samePrograms(q.About, subject.Program) {
					continue
				}
				if _, _, err := book.AnswerApplication(q.ID, "allow"); err != nil {
					t.Error(err)
				}
				snapshot, err := r.policy.OperatorSnapshot()
				if err != nil {
					t.Error(err)
				}
				if edit, err := a.applyOperatorAllow(context.Background(), panel, snapshot.Revision, q.ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Errorf("the Allow of %s: %+v %v", q.ID, edit, err)
				}
				answered <- q.ID
				return
			}
		}
		answered <- ""
	}()
	budget, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stored, err := client.UploadInput(budget, askclient.NewPermission(endpoint), writer, client.InputImageProfile, requestID(t), digest, bytes.NewReader(editInputPNG), size)
	if err != nil || stored.Digest != digest {
		t.Fatalf("UploadInput: %+v %v", stored, err)
	}
	id := <-answered
	if id == "" {
		t.Fatal("no question was raised")
	}
	record, found, err := book.ApplicationRecord(id)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	permission, ok := asks.PermissionForRecord(r.owner, record)
	if !ok || len(permission.Targets) != 2 || !samePrograms(permission.Targets[0].Program, subject.Program) || permission.Targets[0].Resource != inference.InputResource(inference.InputImage) {
		t.Fatalf("the question asked %+v, want the paired input:image question about %s", permission, subject.Program)
	}
	if got, outcome := content.read(inference.WithInputProfile(ctx, inference.InputImage), subject, digest, size); outcome != inference.ContentResolved || !bytes.Equal(got, editInputPNG) {
		t.Fatalf("request read=%v", outcome)
	}

	// Asking again with the grant held raises nothing and writes at once.
	if _, err := client.UploadInput(ctx, askclient.NewPermission(endpoint), writer, client.InputImageProfile, requestID(t), digest, bytes.NewReader(editInputPNG), size); err != nil {
		t.Fatalf("UploadInput with the grant held: %v", err)
	}
	if pending := pendingAbout(); len(pending) != 0 {
		t.Fatalf("a held grant raised questions: %+v", pending)
	}
}
