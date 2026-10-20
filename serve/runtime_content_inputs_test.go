package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	storageclient "github.com/openabstractions/abstraction-storage/go/client"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

var editInputPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR request input")

func digestOf(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

// imageRequest and audioRequest are the contexts inference reads request
// input under (inference.WithInputProfile).
func imageRequest() context.Context {
	return inference.WithInputProfile(context.Background(), inference.InputImage)
}

func audioRequest() context.Context {
	return inference.WithInputProfile(context.Background(), inference.InputAudio)
}

func inputContent(t *testing.T) (*runtimeContent, *runtimeRights, inference.Subject) {
	t.Helper()
	state := t.TempDir()
	rights := testRights(t, state)
	for _, action := range []string{contentReadAction, contentWriteAction} {
		if err := rights.policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	content, err := composeContent(runtimeFlags{endpoint: "input-" + filepath.Base(state)}, state, rights)
	if err != nil {
		t.Fatal(err)
	}
	return content, rights, inference.Subject{Account: rights.owner, Program: rights.operators[0]}
}

func grantInput(t *testing.T, rights *runtimeRights, subject inference.Subject, profile string, actions ...string) {
	t.Helper()
	for _, action := range actions {
		if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, action, inference.InputResource(profile), true); err != nil {
			t.Fatal(err)
		}
	}
}

func grantDigest(t *testing.T, rights *runtimeRights, subject inference.Subject, action, digest string) {
	t.Helper()
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, action, digest, true); err != nil {
		t.Fatal(err)
	}
}

// otherProgram is a second application of the same account.
func otherProgram(subject inference.Subject, name string) inference.Subject {
	return inference.Subject{Account: subject.Account, Program: filepath.Join(filepath.Dir(subject.Program), name)}
}

// retentionClock drives a content's clock by hand.
func retentionClock(content *runtimeContent) (time.Time, func(time.Duration)) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var now atomic.Int64
	now.Store(start.UnixNano())
	content.now = func() time.Time { return time.Unix(0, now.Load()).UTC() }
	return start, func(after time.Duration) { now.Store(start.Add(after).UnixNano()) }
}

// pastRetention is a moment after every write of a test made at its start.
const pastRetention = storageservice.RequestRetention + time.Minute

// STO-W12, INF-V4: an application holding only input:image writes an edit
// input, and the runtime reads it back for that application's own request of
// the image profile. Before input profiles the write was
// content:write:forbidden.
func TestInputProfileWriteIsReadBackForItsWriter(t *testing.T) {
	content, rights, subject := inputContent(t)
	digest := digestOf(editInputPNG)
	grantInput(t, rights, subject, inference.InputImage, contentWriteAction, contentReadAction)
	if outcome := content.write(context.Background(), subject, digest, editInputPNG); outcome != inference.ContentResolved {
		t.Fatalf("input write under input:image=%v", outcome)
	}
	got, outcome := content.read(imageRequest(), subject, digest, int64(len(editInputPNG)))
	if outcome != inference.ContentResolved || !bytes.Equal(got, editInputPNG) {
		t.Fatalf("request read of own input=%v %q", outcome, got)
	}
	// The record is durable: a restarted runtime reads it back the same way.
	restarted, err := composeContent(runtimeFlags{endpoint: content.endpoint}, filepath.Dir(filepath.Dir(content.inputs)), rights)
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome := restarted.read(imageRequest(), subject, digest, int64(len(editInputPNG))); outcome != inference.ContentResolved {
		t.Fatalf("read after restart=%v", outcome)
	}
	// A request of another profile, and a read for no request, gain nothing.
	grantInput(t, rights, subject, inference.InputAudio, contentWriteAction, contentReadAction)
	if _, outcome := content.read(audioRequest(), subject, digest, int64(len(editInputPNG))); outcome != inference.ContentForbidden {
		t.Fatalf("a transcription read an image input: %v", outcome)
	}
	if _, outcome := content.read(context.Background(), subject, digest, int64(len(editInputPNG))); outcome != inference.ContentForbidden {
		t.Fatalf("a read for no request: %v", outcome)
	}
	// The read right is rechecked on each read.
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentReadAction, inference.InputResource(inference.InputImage), false); err != nil {
		t.Fatal(err)
	}
	if _, outcome := content.read(imageRequest(), subject, digest, int64(len(editInputPNG))); outcome != inference.ContentForbidden {
		t.Fatalf("read after input:image read revoked=%v", outcome)
	}
}

// STO-W12: a write under an input profile gives no one else any right, a
// holder of the same profile included.
func TestInputProfileGivesAnotherSubjectNothing(t *testing.T) {
	content, rights, subject := inputContent(t)
	digest := digestOf(editInputPNG)
	grantInput(t, rights, subject, inference.InputImage, contentWriteAction, contentReadAction)
	if outcome := content.write(context.Background(), subject, digest, editInputPNG); outcome != inference.ContentResolved {
		t.Fatalf("write=%v", outcome)
	}
	other := otherProgram(subject, "other-app.exe")
	grantInput(t, rights, other, inference.InputImage, contentWriteAction, contentReadAction)
	if _, outcome := content.read(imageRequest(), other, digest, int64(len(editInputPNG))); outcome != inference.ContentForbidden {
		t.Fatalf("another program read=%v", outcome)
	}
	if _, outcome := content.read(imageRequest(), inference.Subject{Account: "another-account", Program: subject.Program}, digest, int64(len(editInputPNG))); outcome != inference.ContentForbidden {
		t.Fatalf("another account read=%v", outcome)
	}
}

// STO-W12, INF-V1: without a profile that fits the bytes the write is refused,
// which the gateway reports as content:write:forbidden; an explicit digest
// denial never falls back to a profile.
func TestInputWriteWithoutTheProfileIsForbidden(t *testing.T) {
	content, rights, subject := inputContent(t)
	digest := digestOf(editInputPNG)
	write := func() inference.ContentOutcome {
		return content.write(context.Background(), subject, digest, editInputPNG)
	}
	if outcome := write(); outcome != inference.ContentForbidden {
		t.Fatalf("no grant=%v", outcome)
	}
	grantInput(t, rights, subject, inference.InputImage, contentWriteAction)
	if outcome := write(); outcome != inference.ContentForbidden {
		t.Fatalf("input:image write without read=%v", outcome)
	}
	grantInput(t, rights, subject, inference.InputAudio, contentWriteAction, contentReadAction)
	if outcome := write(); outcome != inference.ContentForbidden {
		t.Fatalf("PNG under input:audio=%v", outcome)
	}
	text := []byte("plain text is no request input")
	grantInput(t, rights, subject, inference.InputImage, contentReadAction)
	if outcome := content.write(context.Background(), subject, digestOf(text), text); outcome != inference.ContentForbidden {
		t.Fatalf("text under both profiles=%v", outcome)
	}
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, contentWriteAction, digest, false); err != nil {
		t.Fatal(err)
	}
	if outcome := write(); outcome != inference.ContentForbidden {
		t.Fatalf("explicit digest denial fell back to the profile: %v", outcome)
	}
	if _, ok := content.store.Find(digest); ok {
		t.Fatal("a refused input was stored")
	}
}

// STO-W12: an exact-digest decision the runtime cannot obtain is unavailable,
// whatever profile the writer holds, and stores nothing.
func TestUnavailableDigestDecisionNeverFallsBackToAProfile(t *testing.T) {
	content, _, subject := inputContent(t)
	digest := digestOf(editInputPNG)
	content.decide = func(_ context.Context, _ rwire.Subject, action, resource string) rwire.DecisionOutcome {
		if resource == digest {
			return rwire.DecisionOutcomeUnavailable
		}
		return rwire.DecisionOutcomePermitted // every profile right
	}
	if outcome := content.write(context.Background(), subject, digest, editInputPNG); outcome != inference.ContentUnavailable {
		t.Fatalf("write with the digest decision unavailable=%v", outcome)
	}
	bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
	if err := content.authorizeWrite(context.Background(), bound, digest, storageservice.WriteCall{Stage: storageservice.WriteBegin}); !errors.Is(err, storageservice.ErrPolicyUnavailable) {
		t.Fatalf("IPC Begin with the digest decision unavailable: %v", err)
	}
	if _, ok := content.store.Find(digest); ok {
		t.Fatal("content was stored without a decision")
	}
	if _, err := os.Stat(content.inputPath(digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a record was written without a decision: %v", err)
	}
}

// STO-W6, STO-W11, STO-W12: an IPC upload a profile admits must prove its
// bytes, and its publication is decided by the verified bytes and records the
// writer.
func TestInputProfileDecidesIPCWriterStages(t *testing.T) {
	content, rights, subject := inputContent(t)
	bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
	digest := digestOf(editInputPNG)
	call := func(stage storageservice.WriteStage, head []byte) error {
		return content.authorizeWrite(context.Background(), bound, digest, storageservice.WriteCall{Stage: stage, Size: int64(len(editInputPNG)), Head: head})
	}
	if err := call(storageservice.WriteBegin, nil); err == nil || errors.Is(err, storageservice.ErrRequireBytes) {
		t.Fatalf("Begin without a profile: %v", err)
	}
	grantInput(t, rights, subject, inference.InputAudio, contentWriteAction, contentReadAction)
	if err := call(storageservice.WriteBegin, nil); !errors.Is(err, storageservice.ErrRequireBytes) {
		t.Fatalf("Begin under a profile must require the bytes: %v", err)
	}
	for _, stage := range []storageservice.WriteStage{storageservice.WriteAppend, storageservice.WriteCommit} {
		if err := call(stage, nil); err != nil {
			t.Fatalf("stage %d under input:audio: %v", stage, err)
		}
	}
	if err := call(storageservice.WritePublish, editInputPNG); err == nil {
		t.Fatal("a PNG published under input:audio")
	}
	if err := content.authorizeWrite(context.Background(), bound, digest, storageservice.WriteCall{}); err == nil {
		t.Fatal("a call naming no stage was decided by profile")
	}
	grantInput(t, rights, subject, inference.InputImage, contentWriteAction, contentReadAction)
	if err := call(storageservice.WritePublish, editInputPNG); err != nil {
		t.Fatalf("publish under input:image: %v", err)
	}
	if err := content.authorizeRead(context.Background(), bound, digest, inference.InputImage); err != nil {
		t.Fatalf("recorded writer read: %v", err)
	}
}

// ipcWriter serves the composed content's storage endpoint for one test.
func ipcWriter(t *testing.T) (*runtimeContent, *runtimeRights, inference.Subject, *storageclient.Writer, context.Context) {
	t.Helper()
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
	server, err := storageservice.Listen(content.endpoint, options.Storage, options.StoragePolicy)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableWriter(options.StorageWritePolicy, options.StorageWriteLimit, casapi.FileStore{}, options.StorageWriteRecordPath); err != nil {
		server.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); server.Close() })
	go func() { _ = server.Serve(ctx) }()
	return content, rights, inference.Subject{Account: rights.owner, Program: rights.operators[0]}, storageclient.NewWriter(content.endpoint), ctx
}

func requestID(t *testing.T) string {
	t.Helper()
	id, err := storageclient.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// STO-W12 over the native transport: the composed writer policy admits an
// upload under input:image and the request read returns it.
func TestInputProfileNativeIPCUploadFeedsRequestRead(t *testing.T) {
	content, rights, subject, writer, ctx := ipcWriter(t)
	grantInput(t, rights, subject, inference.InputImage, contentWriteAction, contentReadAction)
	digest := digestOf(editInputPNG)
	if stored, err := writer.Write(ctx, requestID(t), digest, bytes.NewReader(editInputPNG), int64(len(editInputPNG))); err != nil || stored.Digest != digest {
		t.Fatalf("IPC write: %+v %v", stored, err)
	}
	if got, outcome := content.read(inference.WithInputProfile(ctx, inference.InputImage), subject, digest, int64(len(editInputPNG))); outcome != inference.ContentResolved || !bytes.Equal(got, editInputPNG) {
		t.Fatalf("request read=%v %q", outcome, got)
	}
}

// STO-W6: a writer holding only input:audio cannot learn whether an image is
// stored: Begin answers started for a stored and an absent digest alike, and
// the image bytes are then refused at publication.
func TestInputProfileWriterLearnsNothingFromPresence(t *testing.T) {
	content, rights, subject, writer, ctx := ipcWriter(t)
	owner := otherProgram(subject, "image-owner.exe")
	stored := digestOf(editInputPNG)
	grantDigest(t, rights, owner, contentWriteAction, stored)
	if outcome := content.write(context.Background(), owner, stored, editInputPNG); outcome != inference.ContentResolved {
		t.Fatalf("owner write=%v", outcome)
	}
	grantInput(t, rights, subject, inference.InputAudio, contentWriteAction, contentReadAction)
	absent := digestOf([]byte("\x89PNG\r\n\x1a\nnever stored"))
	for _, digest := range []string{stored, absent} {
		begun, err := writer.Begin(ctx, requestID(t), digest, int64(len(editInputPNG)))
		if err != nil || begun.Outcome != storageclient.BeginOutcomeStarted {
			t.Fatalf("Begin %s under input:audio: %+v %v", digest, begun, err)
		}
		if _, err := writer.Abort(ctx, *begun.Upload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.Write(ctx, requestID(t), stored, bytes.NewReader(editInputPNG), int64(len(editInputPNG))); err == nil {
		t.Fatal("image bytes were accepted under input:audio")
	}
}

// STO-W6, STO-W12, STO-T7: a second application edits a picture another
// application already stored: it proves the bytes over IPC, reads them back
// for its own request, and the sweep keeps the bytes it never owned.
func TestSecondAppEditsAStoredPictureOverIPC(t *testing.T) {
	content, rights, subject, writer, ctx := ipcWriter(t)
	_, advance := retentionClock(content)
	owner := otherProgram(subject, "image-owner.exe")
	digest := digestOf(editInputPNG)
	grantDigest(t, rights, owner, contentWriteAction, digest)
	if outcome := content.write(context.Background(), owner, digest, editInputPNG); outcome != inference.ContentResolved {
		t.Fatalf("owner write=%v", outcome)
	}
	grantInput(t, rights, subject, inference.InputImage, contentWriteAction, contentReadAction)
	if stored, err := writer.Write(ctx, requestID(t), digest, bytes.NewReader(editInputPNG), int64(len(editInputPNG))); err != nil || stored.Digest != digest || stored.Evidence != storageclient.EvidenceHashed {
		t.Fatalf("proof of the stored picture: %+v %v", stored, err)
	}
	if got, outcome := content.read(inference.WithInputProfile(ctx, inference.InputImage), subject, digest, int64(len(editInputPNG))); outcome != inference.ContentResolved || !bytes.Equal(got, editInputPNG) {
		t.Fatalf("second app's request read=%v", outcome)
	}
	advance(pastRetention)
	content.sweepInputs(context.Background())
	if _, ok := content.store.Find(digest); !ok {
		t.Fatal("the sweep removed bytes the input record never owned")
	}
}

// STO-T7 regression: A stores a picture under an exact-digest rule; B, holding
// only input:image, sends the same bytes through the gateway's image edit
// upload. Past retention the sweep keeps A's object. The same holds when A's
// IPC publication was decided before B's gateway write and published after it.
func TestSweepKeepsBytesAnExactDigestWriteStored(t *testing.T) {
	content, rights, a := inputContent(t)
	_, advance := retentionClock(content)
	b := otherProgram(a, "editor.exe")
	grantInput(t, rights, b, inference.InputImage, contentWriteAction, contentReadAction)

	first := []byte("\x89PNG\r\n\x1a\nstored by A first")
	grantDigest(t, rights, a, contentWriteAction, digestOf(first))
	if outcome := content.write(context.Background(), a, digestOf(first), first); outcome != inference.ContentResolved {
		t.Fatalf("A=%v", outcome)
	}
	if outcome := content.write(context.Background(), b, digestOf(first), first); outcome != inference.ContentResolved {
		t.Fatalf("B=%v", outcome)
	}

	// A's IPC upload: its publication is decided, B's gateway write lands, then
	// A's bytes publish.
	raced := []byte("\x89PNG\r\n\x1a\nA publishing while B writes")
	grantDigest(t, rights, a, contentWriteAction, digestOf(raced))
	if err := content.authorizeWrite(context.Background(), rwire.Subject{Account: a.Account, Program: a.Program}, digestOf(raced),
		storageservice.WriteCall{Stage: storageservice.WritePublish, Size: int64(len(raced)), Head: raced}); err != nil {
		t.Fatalf("A's publication: %v", err)
	}
	if outcome := content.write(context.Background(), b, digestOf(raced), raced); outcome != inference.ContentResolved {
		t.Fatalf("B raced=%v", outcome)
	}
	if outcome := content.commitBytes(digestOf(raced), raced); outcome != inference.ContentResolved {
		t.Fatalf("A publishes=%v", outcome)
	}

	// An object stored before any record named it (an earlier runtime's
	// upload) is not B's to own either.
	earlier := []byte("\x89PNG\r\n\x1a\nstored before records")
	if outcome := content.commitBytes(digestOf(earlier), earlier); outcome != inference.ContentResolved {
		t.Fatalf("earlier=%v", outcome)
	}
	if outcome := content.write(context.Background(), b, digestOf(earlier), earlier); outcome != inference.ContentResolved {
		t.Fatalf("B earlier=%v", outcome)
	}

	advance(pastRetention)
	content.sweepInputs(context.Background())
	for _, digest := range []string{digestOf(first), digestOf(raced), digestOf(earlier)} {
		if _, ok := content.store.Find(digest); !ok {
			t.Fatalf("the sweep removed %s, which an exact-digest write stored", digest)
		}
		if _, err := os.Stat(content.inputPath(digest)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("input record of %s left behind: %v", digest, err)
		}
	}
}

// STO-T7: request input is held while its writer's identity retention lasts
// and while a request names it, then removed with its record. An object an
// exact-digest write also named keeps its bytes; its record goes.
func TestRequestInputIsRemovedAfterItsRequestEnds(t *testing.T) {
	content, rights, subject := inputContent(t)
	grantInput(t, rights, subject, inference.InputImage, contentWriteAction, contentReadAction)
	_, advance := retentionClock(content)
	content.report = func(err error) { t.Error(err) }
	var named atomic.Bool
	digest := digestOf(editInputPNG)
	content.addInputHolder(func() (map[string]bool, error) { return map[string]bool{digest: named.Load()}, nil })
	if outcome := content.write(context.Background(), subject, digest, editInputPNG); outcome != inference.ContentResolved {
		t.Fatalf("write=%v", outcome)
	}
	present := func() bool { _, ok := content.store.Find(digest); return ok }

	advance(time.Minute)
	if content.sweepInputs(context.Background()); !present() {
		t.Fatal("input removed within its writer's identity retention")
	}
	named.Store(true) // a request that names the input is running
	advance(pastRetention)
	if content.sweepInputs(context.Background()); !present() {
		t.Fatal("input removed while a request names it")
	}
	named.Store(false) // the request reached its terminal state
	if removed := content.sweepInputs(context.Background()); removed != 1 || present() {
		t.Fatalf("ended request's input: removed=%d present=%v", removed, present())
	}
	if _, err := os.Stat(content.inputPath(digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("input record left behind: %v", err)
	}

	kept := []byte("\x89PNG\r\n\x1a\nkept by a digest rule")
	keptDigest := digestOf(kept)
	if outcome := content.write(context.Background(), subject, keptDigest, kept); outcome != inference.ContentResolved {
		t.Fatalf("kept write=%v", outcome)
	}
	grantDigest(t, rights, subject, contentWriteAction, keptDigest)
	if outcome := content.write(context.Background(), subject, keptDigest, kept); outcome != inference.ContentResolved {
		t.Fatalf("digest write=%v", outcome)
	}
	advance(2*storageservice.RequestRetention + time.Minute)
	content.sweepInputs(context.Background())
	if _, ok := content.store.Find(keptDigest); !ok {
		t.Fatal("content an exact-digest write named was removed")
	}
	if _, err := os.Stat(content.inputPath(keptDigest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kept input record left behind: %v", err)
	}
}

// STO-T7: a holder that cannot answer keeps every object, past retention too.
func TestHolderErrorKeepsRequestInput(t *testing.T) {
	content, rights, subject := inputContent(t)
	grantInput(t, rights, subject, inference.InputImage, contentWriteAction, contentReadAction)
	_, advance := retentionClock(content)
	content.addInputHolder(func() (map[string]bool, error) { return nil, errors.New("job store unreadable") })
	digest := digestOf(editInputPNG)
	if outcome := content.write(context.Background(), subject, digest, editInputPNG); outcome != inference.ContentResolved {
		t.Fatalf("write=%v", outcome)
	}
	advance(pastRetention)
	if removed := content.sweepInputs(context.Background()); removed != 0 {
		t.Fatalf("removed %d with a holder that could not answer", removed)
	}
	if _, ok := content.store.Find(digest); !ok {
		t.Fatal("input removed while a holder could not answer")
	}
}

// STO-T7: an object with both an owning input record and generated-output
// provenance keeps its bytes; only the input record goes.
func TestGeneratedOutputKeepsBytesItsInputRecordOwned(t *testing.T) {
	content, rights, subject := inputContent(t)
	grantInput(t, rights, subject, inference.InputImage, contentWriteAction, contentReadAction)
	for _, action := range []string{contentWriteAction, contentReadAction} {
		grantDigest(t, rights, subject, action, "output:image")
	}
	_, advance := retentionClock(content)
	digest := digestOf(editInputPNG)
	if outcome := content.write(context.Background(), subject, digest, editInputPNG); outcome != inference.ContentResolved {
		t.Fatalf("input write=%v", outcome)
	}
	commit, outcome := content.prepareWrite(context.Background(), subject, "image", "image/png", int64(len(editInputPNG)))
	if outcome != inference.ContentResolved {
		t.Fatalf("prepare=%v", outcome)
	}
	if outcome := commit(context.Background(), digest, editInputPNG); outcome != inference.ContentResolved {
		t.Fatalf("generated commit=%v", outcome)
	}
	advance(pastRetention)
	content.sweepInputs(context.Background())
	if _, ok := content.store.Find(digest); !ok {
		t.Fatal("the sweep removed generated output")
	}
	if _, err := os.Stat(content.inputPath(digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("input record left behind: %v", err)
	}
}
