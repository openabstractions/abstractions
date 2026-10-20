package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	storageclient "github.com/openabstractions/abstraction-storage/go/client"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

func TestRuntimeContentConfiguresScopedReadAndWrite(t *testing.T) {
	state := t.TempDir()
	rights := testRights(t, state)
	content, err := composeContent(runtimeFlags{endpoint: "content-test"}, state, rights)
	if err != nil {
		t.Fatal(err)
	}
	var options host.Options
	content.configure(&options)
	if options.Storage != content.store || options.StorageEndpoint != content.endpoint || options.StoragePolicy == nil || options.StorageWritePolicy == nil {
		t.Fatalf("content was not wired: %+v", options)
	}
	if options.StorageWriteLimit != runtimeContentLimit || options.StorageWriteRecordPath != content.records {
		t.Fatalf("writer configuration: limit=%d records=%q", options.StorageWriteLimit, options.StorageWriteRecordPath)
	}
	if !contains(options.RightsActions, contentReadAction) || !contains(options.RightsActions, contentWriteAction) {
		t.Fatalf("rights actions: %v", options.RightsActions)
	}

	body := []byte("image bytes")
	sum := sha256.Sum256(body)
	digest := fmt.Sprintf("sha256:%x", sum)
	ref, err := content.store.Place(digest, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(content.store.Path(ref), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := content.store.Commit(ref); err != nil {
		t.Fatal(err)
	}
	subject := inference.Subject{Account: rights.owner, Program: rights.operators[0]}
	if err := rights.policy.RegisterAction(contentReadAction); err != nil {
		t.Fatal(err)
	}
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentReadAction, digest, true); err != nil {
		t.Fatal(err)
	}
	got, outcome := content.read(context.Background(), subject, digest, int64(len(body)))
	if outcome != inference.ContentResolved || string(got) != string(body) {
		t.Fatalf("read outcome=%v bytes=%q", outcome, got)
	}
	if _, outcome = content.read(context.Background(), inference.Subject{Account: rights.owner, Program: filepath.Join(rights.operators[0], "other")}, digest, int64(len(body))); outcome != inference.ContentForbidden {
		t.Fatalf("different program outcome=%v", outcome)
	}
	if _, outcome = content.read(context.Background(), inference.Subject{Account: "other-account", Program: subject.Program}, digest, int64(len(body))); outcome != inference.ContentForbidden {
		t.Fatalf("different account outcome=%v", outcome)
	}
	unknown := "sha256:" + fmt.Sprintf("%064x", 1)
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentReadAction, unknown, true); err != nil {
		t.Fatal(err)
	}
	if _, outcome = content.read(context.Background(), subject, unknown, int64(len(body))); outcome != inference.ContentUnknown {
		t.Fatalf("unknown digest outcome=%v", outcome)
	}
	if _, outcome = content.read(context.Background(), subject, digest, int64(len(body)-1)); outcome != inference.ContentTooLarge {
		t.Fatalf("size bound outcome=%v", outcome)
	}

	// Gateway upload authorization is write-only. Storing a digest does not
	// grant the subsequent transcription read.
	upload := []byte("gateway audio")
	uploadDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(upload))
	if err := rights.policy.RegisterAction(contentWriteAction); err != nil {
		t.Fatal(err)
	}
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentWriteAction, uploadDigest, true); err != nil {
		t.Fatal(err)
	}
	if outcome = content.write(context.Background(), subject, uploadDigest, upload); outcome != inference.ContentResolved {
		t.Fatalf("write outcome=%v", outcome)
	}
	if _, outcome = content.read(context.Background(), subject, uploadDigest, int64(len(upload))); outcome != inference.ContentForbidden {
		t.Fatalf("write implicitly granted read: %v", outcome)
	}

	generated := []byte("RIFF speech output")
	generatedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(generated))
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentWriteAction, speechOutputResource, true); err != nil {
		t.Fatal(err)
	}
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentReadAction, speechOutputResource, true); err != nil {
		t.Fatal(err)
	}
	commit, outcome := content.prepareWrite(context.Background(), subject, "speech", "audio/wav", int64(len(generated)))
	if outcome != inference.ContentResolved || commit == nil {
		t.Fatalf("prepare outcome=%v", outcome)
	}
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentWriteAction, speechOutputResource, false); err != nil {
		t.Fatal(err)
	}
	if outcome = commit(context.Background(), generatedDigest, generated); outcome != inference.ContentForbidden {
		t.Fatalf("revoked output commit=%v", outcome)
	}
	if _, ok := content.store.Find(generatedDigest); ok {
		t.Fatal("revoked output was committed")
	}
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentWriteAction, speechOutputResource, true); err != nil {
		t.Fatal(err)
	}
	commit, outcome = content.prepareWrite(context.Background(), subject, "speech", "audio/wav", int64(len(generated)))
	if outcome != inference.ContentResolved {
		t.Fatalf("second prepare=%v", outcome)
	}
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentReadAction, speechOutputResource, false); err != nil {
		t.Fatal(err)
	}
	if outcome = commit(context.Background(), generatedDigest, generated); outcome != inference.ContentForbidden {
		t.Fatalf("revoked output read commit=%v", outcome)
	}
	if _, ok := content.store.Find(generatedDigest); ok {
		t.Fatal("read-revoked output was committed")
	}
}

func TestGeneratedOutputReadProvenanceSurvivesRestartAndRevocation(t *testing.T) {
	state := t.TempDir()
	rights := testRights(t, state)
	for _, action := range []string{contentReadAction, contentWriteAction} {
		if err := rights.policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	runtimeOptions, _ := isolatedRuntime(t)
	content, err := composeContent(runtimeOptions, state, rights)
	if err != nil {
		t.Fatal(err)
	}
	subject := inference.Subject{Account: rights.owner, Program: rights.operators[0]}
	bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
	data := []byte("generated image data")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	if err := rights.policy.Set(bound, contentWriteAction, "output:image", true); err != nil {
		t.Fatal(err)
	}
	if _, outcome := content.prepareWrite(context.Background(), subject, "image", "image/png", int64(len(data))); outcome != inference.ContentForbidden {
		t.Fatalf("read preflight outcome=%v", outcome)
	}
	if err := rights.policy.Set(bound, contentReadAction, "output:image", true); err != nil {
		t.Fatal(err)
	}
	commit, outcome := content.prepareWrite(context.Background(), subject, "image", "image/png", int64(len(data)))
	if outcome != inference.ContentResolved {
		t.Fatalf("prepare=%v", outcome)
	}
	if outcome := commit(context.Background(), digest, data); outcome != inference.ContentResolved {
		t.Fatalf("commit=%v", outcome)
	}
	if outcome := commit(context.Background(), digest, data); outcome != inference.ContentUnavailable {
		t.Fatalf("reused commit=%v", outcome)
	}
	restarted, err := composeContent(runtimeOptions, state, rights)
	if err != nil {
		t.Fatal(err)
	}
	if got, outcome := restarted.read(context.Background(), subject, digest, int64(len(data))); outcome != inference.ContentResolved || !bytes.Equal(got, data) {
		t.Fatalf("restart read outcome=%v data=%q", outcome, got)
	}
	if statusTransportProvesProgram(t) {
		var options host.Options
		restarted.configure(&options)
		server, err := storageservice.Listen(restarted.endpoint, options.Storage, options.StoragePolicy)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = server.Serve(ctx) }()
		reader := storageclient.New(restarted.endpoint)
		opened, err := reader.Open(ctx, digest)
		if err != nil || opened.Outcome != storageclient.OpenOutcomeOpened {
			t.Fatalf("IPC open=%+v %v", opened, err)
		}
		chunk, err := reader.Read(ctx, *opened.Resource, 0, int64(len(data)))
		if err != nil || chunk.Outcome != storageclient.ReadOutcomeData || !bytes.Equal(chunk.Chunk.Data, data) {
			t.Fatalf("IPC read=%+v %v", chunk, err)
		}
		if err := rights.policy.Set(bound, contentReadAction, "output:image", false); err != nil {
			t.Fatal(err)
		}
		chunk, err = reader.Read(ctx, *opened.Resource, 0, int64(len(data)))
		if err != nil || chunk.Outcome != storageclient.ReadOutcomeForbidden {
			t.Fatalf("IPC revoked read=%+v %v", chunk, err)
		}
		cancel()
		server.Close()
		if err := rights.policy.Set(bound, contentReadAction, "output:image", true); err != nil {
			t.Fatal(err)
		}
	}
	other := inference.Subject{Account: rights.owner, Program: filepath.Join(state, "other-app")}
	if err := rights.policy.Set(rwire.Subject{Account: other.Account, Program: other.Program}, contentReadAction, "output:image", true); err != nil {
		t.Fatal(err)
	}
	if _, outcome := restarted.read(context.Background(), other, digest, int64(len(data))); outcome != inference.ContentForbidden {
		t.Fatalf("other program=%v", outcome)
	}
	if _, outcome := restarted.read(context.Background(), inference.Subject{Account: "another", Program: subject.Program}, digest, int64(len(data))); outcome != inference.ContentForbidden {
		t.Fatalf("other account=%v", outcome)
	}
	if err := rights.policy.Set(bound, contentReadAction, digest, false); err != nil {
		t.Fatal(err)
	}
	if _, outcome := restarted.read(context.Background(), subject, digest, int64(len(data))); outcome != inference.ContentForbidden {
		t.Fatalf("exact deny=%v", outcome)
	}
	if err := rights.policy.Revoke(bound, contentReadAction, digest); err != nil {
		t.Fatal(err)
	}
	if err := rights.policy.Set(bound, contentReadAction, "output:image", false); err != nil {
		t.Fatal(err)
	}
	if _, outcome := restarted.read(context.Background(), subject, digest, int64(len(data))); outcome != inference.ContentForbidden {
		t.Fatalf("profile revoked=%v", outcome)
	}
	if err := rights.policy.Set(bound, contentReadAction, "output:image", true); err != nil {
		t.Fatal(err)
	}
	rightsBytes, err := os.ReadFile(rights.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rights.path, []byte("invalid rights state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, outcome := restarted.read(context.Background(), subject, digest, int64(len(data))); outcome != inference.ContentUnavailable {
		t.Fatalf("exact policy outage=%v", outcome)
	}
	if err := os.WriteFile(rights.path, rightsBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restarted.generatedPath(digest), []byte("invalid provenance"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, outcome := restarted.read(context.Background(), subject, digest, int64(len(data))); outcome != inference.ContentUnavailable {
		t.Fatalf("provenance outage=%v", outcome)
	}
}

func TestGeneratedOutputProvenanceFailureDoesNotPublish(t *testing.T) {
	state := t.TempDir()
	rights := testRights(t, state)
	for _, action := range []string{contentReadAction, contentWriteAction} {
		if err := rights.policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	content, err := composeContent(runtimeFlags{endpoint: "generated-failure"}, state, rights)
	if err != nil {
		t.Fatal(err)
	}
	subject := inference.Subject{Account: rights.owner, Program: rights.operators[0]}
	bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
	for _, action := range []string{contentReadAction, contentWriteAction} {
		if err := rights.policy.Set(bound, action, "output:speech", true); err != nil {
			t.Fatal(err)
		}
	}
	data := []byte("generated speech")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	commit, outcome := content.prepareWrite(context.Background(), subject, "speech", "audio/wav", int64(len(data)))
	if outcome != inference.ContentResolved {
		t.Fatalf("prepare=%v", outcome)
	}
	blocked := filepath.Join(state, "blocked-provenance")
	if err := os.WriteFile(blocked, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	content.provenance = blocked // A regular file cannot contain a digest shard.
	if outcome := commit(context.Background(), digest, data); outcome != inference.ContentUnavailable {
		t.Fatalf("commit with failed provenance=%v", outcome)
	}
	content.provenance = filepath.Join(state, "content", "generated-outputs")
	if _, outcome := content.read(context.Background(), subject, digest, int64(len(data))); outcome != inference.ContentForbidden {
		t.Fatalf("unpublished read=%v", outcome)
	}
}

func TestRuntimeContentNativeIPCUploadFeedsComposedReader(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	state := t.TempDir()
	rights := testRights(t, state)
	runtimeOptions, _ := isolatedRuntime(t)
	content, err := composeContent(runtimeOptions, state, rights)
	if err != nil {
		t.Fatal(err)
	}
	var options host.Options
	content.configure(&options)
	for _, action := range []string{contentReadAction, contentWriteAction} {
		if err := rights.policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	subject := inference.Subject{Account: rights.owner, Program: rights.operators[0]}
	body := []byte("uploaded image")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	for _, action := range []string{contentReadAction, contentWriteAction} {
		if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, action, digest, true); err != nil {
			t.Fatal(err)
		}
	}
	server, err := storageservice.Listen(content.endpoint, options.Storage, options.StoragePolicy)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableWriter(options.StorageWritePolicy, options.StorageWriteLimit, casapi.FileStore{}, options.StorageWriteRecordPath); err != nil {
		server.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer server.Close()
	go func() { _ = server.Serve(ctx) }()
	writer := storageclient.NewWriter(content.endpoint)
	request, err := storageclient.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	if stored, err := writer.Write(ctx, request, digest, bytes.NewReader(body), int64(len(body))); err != nil || stored.Digest != digest {
		t.Fatalf("IPC write: %+v %v", stored, err)
	}
	got, outcome := content.read(ctx, subject, digest, int64(len(body)))
	if outcome != inference.ContentResolved || !bytes.Equal(got, body) {
		t.Fatalf("composed read outcome=%v bytes=%q", outcome, got)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
