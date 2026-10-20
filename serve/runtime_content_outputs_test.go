package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	logging "github.com/openabstractions/abstraction-logging/go"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// tmpTenDays is the default tmp_max_retention.
const tmpTenDays = 10 * 24 * time.Hour

// outputWindow is how long an unheld picture lives after it was made: its
// job's result retention, then tmp_max_retention (INF-V5).
const outputWindow = defaultOutputRetention + tmpTenDays

type outputLog struct {
	mu      sync.Mutex
	records []logging.Record
}

func (l *outputLog) Write(r logging.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r)
	return nil
}

func (l *outputLog) find(msg string) []logging.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []logging.Record
	for _, r := range l.records {
		if r.Msg == msg {
			found = append(found, r)
		}
	}
	return found
}

type outputFixture struct {
	content *runtimeContent
	rights  *runtimeRights
	subject inference.Subject
	advance func(time.Duration)
	log     *outputLog
	reads   int
	tmp     func() (time.Duration, error)
}

func newOutputFixture(t *testing.T) *outputFixture {
	t.Helper()
	content, rights, subject := inputContent(t)
	for _, action := range []string{contentWriteAction, contentReadAction} {
		grantDigest(t, rights, subject, action, "output:image")
	}
	_, advance := retentionClock(content)
	f := &outputFixture{content: content, rights: rights, subject: subject, advance: advance, log: &outputLog{}}
	f.tmp = func() (time.Duration, error) { return tmpTenDays, nil }
	content.sink = f.log
	content.readTmp = func(context.Context) (time.Duration, string, error) {
		f.reads++
		d, err := f.tmp()
		return d, "user", err
	}
	return f
}

// publish makes one picture the way an image operation publishes it.
func (f *outputFixture) publish(t *testing.T, data []byte) string {
	t.Helper()
	return f.publishAs(t, f.subject, data)
}

func (f *outputFixture) publishAs(t *testing.T, subject inference.Subject, data []byte) string {
	t.Helper()
	commit, outcome := f.content.prepareWrite(context.Background(), subject, "image", "image/png", int64(len(data)))
	if outcome != inference.ContentResolved {
		t.Fatalf("prepare=%v", outcome)
	}
	digest := digestOf(data)
	if outcome := commit(context.Background(), digest, data); outcome != inference.ContentResolved {
		t.Fatalf("commit=%v", outcome)
	}
	return digest
}

func (f *outputFixture) present(digest string) bool {
	_, ok := f.content.store.Find(digest)
	return ok
}

func (f *outputFixture) pass(t *testing.T) outputPass {
	t.Helper()
	pass := f.content.sweepOutputs(context.Background())
	f.content.logOutputPass(pass)
	return pass
}

func picture(name string) []byte { return []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR " + name) }

// INF-V5: a picture older than its job's retention plus tmp_max_retention,
// with no holder, goes with its provenance and one STO-T6 record. A later
// read of the digest is content:unknown.
func TestGeneratedOutputPassRemovesExpiredUnheldPicture(t *testing.T) {
	f := newOutputFixture(t)
	data := picture("expired")
	digest := f.publish(t, data)
	f.advance(outputWindow + time.Minute)
	pass := f.pass(t)
	if pass.removed != 1 || pass.skipped != "" || f.present(digest) {
		t.Fatalf("pass=%+v present=%v", pass, f.present(digest))
	}
	if _, err := os.Stat(f.content.generatedPath(digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provenance left behind: %v", err)
	}
	removed := f.log.find("abstraction.storage/retention.removed")
	if len(removed) != 1 || removed[0].Attrs["class"] != "tmp" || removed[0].Attrs["kind"] != "generated-output" || removed[0].Attrs["key"] != digest || removed[0].Attrs["level"] != "user" {
		t.Fatalf("STO-T6 records %+v", removed)
	}
	lines := f.log.find("abstraction.storage/retention.pass")
	if len(lines) != 1 || lines[0].Attrs["removed"] != "1" || lines[0].Attrs["examined"] != "1" {
		t.Fatalf("pass lines %+v", lines)
	}
	// The bytes are gone. Without its provenance the profile grant no longer
	// names the digest, and the read is refused before presence is checked.
	if _, outcome := f.content.read(context.Background(), f.subject, digest, int64(len(data))); outcome == inference.ContentResolved {
		t.Fatalf("read after removal=%v", outcome)
	}
}

// INF-V5: a picture inside its window stays, and making the same picture
// again restarts its clock.
func TestGeneratedOutputPassKeepsPictureWithinItsWindow(t *testing.T) {
	f := newOutputFixture(t)
	digest := f.publish(t, picture("recent"))
	f.advance(outputWindow - time.Minute)
	if pass := f.pass(t); pass.kept != 1 || pass.removed != 0 || !f.present(digest) {
		t.Fatalf("within window: %+v present=%v", pass, f.present(digest))
	}
	f.advance(5 * 24 * time.Hour)
	if again := f.publish(t, picture("recent")); again != digest {
		t.Fatal("the same picture has another digest")
	}
	f.advance(outputWindow + time.Minute)
	if pass := f.pass(t); pass.removed != 0 || !f.present(digest) {
		t.Fatalf("made again: %+v present=%v", pass, f.present(digest))
	}
	f.advance(5*24*time.Hour + outputWindow + time.Minute)
	if pass := f.pass(t); pass.removed != 1 || f.present(digest) {
		t.Fatalf("after the last making: %+v present=%v", pass, f.present(digest))
	}
}

// INF-V5, STO-T7: a job that has not ended and names the picture as input,
// an exact-digest write and a live upload each keep it past its window; a
// holder that cannot answer keeps everything.
func TestGeneratedOutputPassKeepsHeldPicture(t *testing.T) {
	f := newOutputFixture(t)
	byJob := f.publish(t, picture("edited by a running job"))
	byMark := f.publish(t, picture("uploaded by its application"))
	byUpload := f.publish(t, picture("uploading"))
	var named sync.Map
	named.Store(byJob, true)
	var failing bool
	f.content.addInputHolder(func() (map[string]bool, error) {
		if failing {
			return nil, errors.New("job store unreadable")
		}
		held := map[string]bool{}
		named.Range(func(k, v any) bool { held[k.(string)] = v.(bool); return true })
		return held, nil
	})
	if err := f.content.keepInput(context.Background(), byMark); err != nil {
		t.Fatal(err)
	}
	f.advance(outputWindow + time.Minute)
	writeUploadRecord(t, f.content, byUpload, f.content.clock().Add(5*time.Minute))

	failing = true
	if pass := f.pass(t); pass.skipped != "holder:unavailable" || pass.removed != 0 {
		t.Fatalf("holder unavailable: %+v", pass)
	}
	failing = false
	pass := f.pass(t)
	if pass.removed != 0 || pass.held != 3 {
		t.Fatalf("held: %+v", pass)
	}
	for _, digest := range []string{byJob, byMark, byUpload} {
		if !f.present(digest) {
			t.Fatalf("held picture %s removed", digest)
		}
	}
	named.Store(byJob, false)
	writeUploadRecord(t, f.content, byUpload, f.content.clock().Add(-time.Second))
	if pass := f.pass(t); pass.removed != 2 || f.present(byJob) || f.present(byUpload) || !f.present(byMark) {
		t.Fatalf("released: %+v", pass)
	}
}

// writeUploadRecord writes the storage writer's record of one upload of
// digest that expires at expires.
func writeUploadRecord(t *testing.T, content *runtimeContent, digest string, expires time.Time) {
	t.Helper()
	record := map[string]any{"profile": "abstraction.storage/writer-records@2", "records": []map[string]any{{
		"scope": "fixture", "request": "fixture-request", "digest": digest, "size": 1, "expires_unix_ms": expires.UnixMilli()}}}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(content.records, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// PICTURES-STORAGE ruling 1: a setting that cannot be read, or is not
// positive, removes nothing in that pass, and the pass says why.
func TestGeneratedOutputPassUnreadableSettingRemovesNothing(t *testing.T) {
	for _, name := range []string{"error", "zero", "negative", "no-reader"} {
		t.Run(name, func(t *testing.T) {
			f := newOutputFixture(t)
			digest := f.publish(t, picture("setting "+name))
			f.advance(outputWindow + 365*24*time.Hour)
			switch name {
			case "error":
				f.tmp = func() (time.Duration, error) { return 0, errors.New("config unavailable") }
			case "zero":
				f.tmp = func() (time.Duration, error) { return 0, nil }
			case "negative":
				f.tmp = func() (time.Duration, error) { return -time.Hour, nil }
			case "no-reader":
				f.content.readTmp = nil
			}
			pass := f.pass(t)
			if pass.skipped != "setting:unavailable" || pass.removed != 0 || pass.examined != 0 || !f.present(digest) {
				t.Fatalf("pass=%+v present=%v", pass, f.present(digest))
			}
			lines := f.log.find("abstraction.storage/retention.pass")
			if len(lines) != 1 || lines[0].Attrs["skipped"] != "setting:unavailable" || lines[0].Level != logging.LevelWarn {
				t.Fatalf("pass lines %+v", lines)
			}
		})
	}
}

// PICTURES-STORAGE ruling 1: provenance written before this change has no
// time. The first pass writes the current time and removes nothing; the clock
// then runs from that pass.
func TestGeneratedOutputPassStartsAnUntimedPictureAtTheUpgrade(t *testing.T) {
	f := newOutputFixture(t)
	data := picture("made before the upgrade")
	digest := digestOf(data)
	if outcome := f.content.commitBytes(digest, data); outcome != inference.ContentResolved {
		t.Fatalf("bytes=%v", outcome)
	}
	legacy := generatedOutputIndex{Version: 1, Outputs: []generatedOutput{{Account: f.subject.Account,
		Program: identity.NormalizeSubjectProgram(f.subject.Program), Digest: digest, Profile: "image"}}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.content.generatedPath(digest)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.content.generatedPath(digest), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	upgrade := 100 * 24 * time.Hour
	f.advance(upgrade)
	if pass := f.pass(t); pass.stamped != 1 || pass.removed != 0 || !f.present(digest) {
		t.Fatalf("first pass: %+v present=%v", pass, f.present(digest))
	}
	index, err := f.content.readGenerated(context.Background(), digest)
	if err != nil || len(index.Outputs) != 1 || index.Outputs[0].Published != f.content.stamp() || index.Outputs[0].RetentionMs != defaultOutputRetention.Milliseconds() {
		t.Fatalf("stamped provenance %+v %v", index, err)
	}
	// The provenance still answers a read after stamping.
	if _, outcome := f.content.read(context.Background(), f.subject, digest, int64(len(data))); outcome != inference.ContentResolved {
		t.Fatalf("read after stamping=%v", outcome)
	}
	f.advance(upgrade + outputWindow - time.Minute)
	if pass := f.pass(t); pass.removed != 0 || !f.present(digest) {
		t.Fatalf("within the window after the upgrade: %+v", pass)
	}
	f.advance(upgrade + outputWindow + time.Minute)
	if pass := f.pass(t); pass.removed != 1 || f.present(digest) {
		t.Fatalf("after the window from the upgrade: %+v", pass)
	}
}

// PICTURES-STORAGE ruling 1: the pass runs at most once an hour, and reads
// the setting on every pass it runs.
func TestGeneratedOutputPassRunsAtMostHourly(t *testing.T) {
	f := newOutputFixture(t)
	steps := []struct {
		at  time.Duration
		ran bool
	}{{0, true}, {time.Minute, false}, {59 * time.Minute, false}, {time.Hour, true}, {time.Hour + 30*time.Minute, false}, {2 * time.Hour, true}}
	for _, step := range steps {
		f.advance(step.at)
		if pass := f.content.serveOutputRetention(context.Background()); pass.ran != step.ran {
			t.Fatalf("at %s ran=%v", step.at, pass.ran)
		}
	}
	if f.reads != 3 || len(f.log.find("abstraction.storage/retention.pass")) != 3 {
		t.Fatalf("reads=%d lines=%d", f.reads, len(f.log.find("abstraction.storage/retention.pass")))
	}
}

// A pass reads at most outputPassLimit records; the next continues after the
// last one read.
func TestGeneratedOutputPassIsBounded(t *testing.T) {
	f := newOutputFixture(t)
	previous := outputPassLimit
	outputPassLimit = 2
	t.Cleanup(func() { outputPassLimit = previous })
	for _, name := range []string{"one", "two", "three"} {
		f.publish(t, picture(name))
	}
	f.advance(outputWindow + time.Minute)
	if pass := f.pass(t); pass.examined != 2 || pass.removed != 2 {
		t.Fatalf("first pass: %+v", pass)
	}
	if pass := f.pass(t); pass.examined != 1 || pass.removed != 1 {
		t.Fatalf("second pass: %+v", pass)
	}
	if pass := f.pass(t); pass.examined != 0 {
		t.Fatalf("third pass: %+v", pass)
	}
}

// The pass follows no link: a shard that is a link to a directory is not
// read.
func TestGeneratedOutputPassFollowsNoLink(t *testing.T) {
	f := newOutputFixture(t)
	digest := f.publish(t, picture("behind a link"))
	shard := filepath.Dir(f.content.generatedPath(digest))
	moved := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(shard, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, shard); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		// Without the symbolic-link privilege a junction is the link.
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", shard, moved).CombinedOutput(); err != nil {
			t.Skipf("no link available: %v %s", err, out)
		}
	}
	f.advance(outputWindow + time.Minute)
	if pass := f.pass(t); pass.examined != 0 || pass.removed != 0 || !f.present(digest) {
		t.Fatalf("pass through a link: %+v", pass)
	}
	if _, err := os.Stat(filepath.Join(moved, filepath.Base(f.content.generatedPath(digest)))); err != nil {
		t.Fatalf("linked provenance: %v", err)
	}
}

// STO-T7, INF-V5 regression: an application's exact-digest upload of a
// picture keeps it after the input sweep ends its records. Before the fix
// the sweep removed the mark after ten minutes and the generated-output pass
// then removed the application's bytes.
func TestGeneratedOutputPassKeepsAnApplicationsExactDigestUpload(t *testing.T) {
	f := newOutputFixture(t)
	digest := f.publish(t, picture("the application keeps it"))
	if err := f.content.keepInput(context.Background(), digest); err != nil {
		t.Fatal(err)
	}
	f.advance(20 * time.Minute)
	f.content.sweepInputs(context.Background())
	requireOnlyMark(t, f.content, digest)
	if _, err := os.Stat(f.content.inputPath(digest)); err != nil {
		t.Fatalf("the exact-digest mark went: %v", err)
	}
	f.advance(20*time.Minute + outputWindow + time.Minute)
	f.content.sweepInputs(context.Background())
	if pass := f.pass(t); pass.removed != 0 || pass.held != 1 || !f.present(digest) {
		t.Fatalf("pass=%+v present=%v", pass, f.present(digest))
	}
}

// A read in progress holds an expired picture: the storage reader decides
// each chunk again, and removing the provenance would refuse the rest of the
// read. Once the reader is idle the picture goes.
func TestGeneratedOutputPassKeepsAPictureBeingRead(t *testing.T) {
	f := newOutputFixture(t)
	data := picture("being read")
	digest := f.publish(t, data)
	f.advance(outputWindow + time.Minute)
	if got, outcome := f.content.read(context.Background(), f.subject, digest, int64(len(data))); outcome != inference.ContentResolved || string(got) != string(data) {
		t.Fatalf("read=%v", outcome)
	}
	if pass := f.pass(t); pass.held != 1 || pass.removed != 0 || !f.present(digest) {
		t.Fatalf("while read: %+v present=%v", pass, f.present(digest))
	}
	f.advance(outputWindow + time.Minute + storageservice.IdleLifetime)
	if pass := f.pass(t); pass.removed != 1 || f.present(digest) {
		t.Fatalf("after the reader: %+v present=%v", pass, f.present(digest))
	}
}

// A picture made again while a pass decides it stays: the removal reads the
// provenance again under the store lock.
func TestGeneratedOutputPassKeepsAPicturePublishedDuringThePass(t *testing.T) {
	f := newOutputFixture(t)
	data := picture("made again during the pass")
	digest := f.publish(t, data)
	f.advance(outputWindow + time.Minute)
	calls := 0
	f.content.addInputHolder(func() (map[string]bool, error) {
		calls++
		if calls == 2 { // the removal's own reading, after the pass found it expired
			f.publish(t, data)
		}
		return map[string]bool{}, nil
	})
	if pass := f.pass(t); pass.removed != 0 || pass.held != 1 || !f.present(digest) || calls != 2 {
		t.Fatalf("pass=%+v present=%v holder calls=%d", pass, f.present(digest), calls)
	}
	f.advance(2*outputWindow + 2*time.Minute)
	if pass := f.pass(t); pass.removed != 1 || f.present(digest) {
		t.Fatalf("after the new window: %+v", pass)
	}
}

// One index naming the bytes for two programs keeps them until the later
// entry's window has passed.
func TestGeneratedOutputPassWaitsForEveryEntry(t *testing.T) {
	f := newOutputFixture(t)
	other := otherProgram(f.subject, "second-painter.exe")
	for _, action := range []string{contentWriteAction, contentReadAction} {
		grantDigest(t, f.rights, other, action, "output:image")
	}
	data := picture("made by two programs")
	digest := f.publish(t, data)
	f.advance(5 * 24 * time.Hour)
	f.publishAs(t, other, data)
	index, err := f.content.readGenerated(context.Background(), digest)
	if err != nil || len(index.Outputs) != 2 {
		t.Fatalf("index %+v %v", index, err)
	}
	f.advance(outputWindow + time.Minute)
	if pass := f.pass(t); pass.kept != 1 || pass.removed != 0 || !f.present(digest) {
		t.Fatalf("one entry within its window: %+v", pass)
	}
	f.advance(5*24*time.Hour + outputWindow + time.Minute)
	if pass := f.pass(t); pass.removed != 1 || f.present(digest) {
		t.Fatalf("both entries past: %+v", pass)
	}
}

// The retention loop runs the input sweep and the generated-output pass on
// the content's clock.
func TestRetentionLoopRunsBothPasses(t *testing.T) {
	f := newOutputFixture(t)
	grantInput(t, f.rights, f.subject, inference.InputImage, contentWriteAction, contentReadAction)
	output := f.publish(t, picture("expired output"))
	input := digestOf(editInputPNG)
	if outcome := f.content.write(context.Background(), f.subject, input, editInputPNG); outcome != inference.ContentResolved {
		t.Fatalf("input write=%v", outcome)
	}
	f.advance(outputWindow + time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	wait := f.content.serveInputRetention(ctx)
	deadline := time.Now().Add(30 * time.Second)
	for (f.present(output) || f.present(input)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wait()
	if f.present(output) || f.present(input) {
		t.Fatalf("output present=%v input present=%v", f.present(output), f.present(input))
	}
	if lines := f.log.find("abstraction.storage/retention.pass"); len(lines) != 1 || lines[0].Attrs["removed"] != "1" || f.reads != 1 {
		t.Fatalf("pass lines %+v reads %d", lines, f.reads)
	}
}
