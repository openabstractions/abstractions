//go:build ignore

// Writes a managed HTTP runtime store with the current job and download sources.
// run.py copies this file into a generated module under .build and builds it
// there; it is never compiled as part of the workspace.
//
// usage: writer STATE_DIR plain|retry|lost
//
// The store root is STATE_DIR/jobs, the layout `openabstractions storage check`
// inspects. Each mode prints one JSON line describing what it left.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	job "github.com/openabstractions/abstraction-job/go"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "writer: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) != 3 {
		fail("usage: writer STATE_DIR plain|retry|lost")
	}
	root, mode := filepath.Join(os.Args[1], "jobs"), os.Args[2]
	body := bytes.Repeat([]byte("downgrade"), 4096)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body)
	}))
	defer source.Close()

	p, err := acceptanceprovider.OpenManaged(root, downloadserve.HTTPExecution{})
	if err != nil {
		fail("open: %v", err)
	}
	accept := p.Bind("downgrade-writer")
	window, err := accept.GetHistoryWindow()
	if err != nil {
		fail("history window: %v", err)
	}
	spec := request.Encode(&request.Request{
		Artifact: request.Artifact{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Size: int64(len(body))},
		Sources:  []request.Source{{Scheme: "http", Locator: source.URL + "/artifact"}},
	})
	s := api.Submission{Identity: api.RequestIdentity{Key: "downgrade-key", HistoryEpoch: window.HistoryEpoch}, Kind: "download", Spec: spec}
	v, err := accept.Submit(s)
	if err != nil || v.Outcome != "accepted" {
		fail("submit: %+v %v", v, err)
	}
	report := map[string]any{"mode": mode, "operation": v.Receipt.OperationID}

	switch mode {
	case "plain":
	case "retry":
		store, err := job.NewFileStore(root)
		if err != nil {
			fail("store: %v", err)
		}
		claimed, err := store.Claim(v.Receipt.OperationID, "downgrade-writer", time.Minute)
		if err != nil {
			fail("claim: %v", err)
		}
		if _, err := store.Update(claimed.ID, claimed.Lease.Epoch, func(r *job.Record) error { r.State = job.StateFailed; return nil }); err != nil {
			fail("fail operation: %v", err)
		}
		s.Identity.Attempt = 1
		retry, err := accept.Submit(s)
		if err != nil || retry.Outcome != "accepted" {
			fail("retry: %+v %v", retry, err)
		}
		report["retry"] = retry.Receipt.OperationID
	case "lost":
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.Execute(ctx) }()
		operations := p.BindOperations("downgrade-writer")
		deadline := time.Now().Add(60 * time.Second)
		for {
			o, err := operations.ObserveWork(s.Identity)
			if err != nil {
				fail("observe: %v", err)
			}
			if o.Snapshot != nil && o.Snapshot.State == "complete" {
				break
			}
			if time.Now().After(deadline) {
				fail("download did not complete: %+v", o)
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		<-done
		if err := os.Remove(filepath.Join(root, "results", v.Receipt.OperationID)); err != nil {
			fail("remove result: %v", err)
		}
		read, err := operations.ReadResult(s.Identity, 0, 16)
		if err != nil {
			fail("read: %v", err)
		}
		o, err := operations.ObserveWork(s.Identity)
		if err != nil || o.Snapshot == nil || o.Snapshot.Failure == nil {
			fail("observe lost result: %+v %v", o, err)
		}
		report["read"], report["state"], report["cause"] = read.Outcome, o.Snapshot.State, o.Snapshot.Failure.Cause
	default:
		fail("unknown mode %q", mode)
	}
	header, err := os.ReadFile(filepath.Join(root, "acceptance", "owner.json"))
	if err != nil {
		fail("owner header: %v", err)
	}
	report["header"] = json.RawMessage(header)
	json.NewEncoder(os.Stdout).Encode(report)
}
