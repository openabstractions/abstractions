package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// slowSink is a result writer whose first write takes delay: the stand-in
// for copying a many-gigabyte result.
type slowSink struct {
	io.Writer
	delay time.Duration
	slept bool
}

func (s *slowSink) Write(p []byte) (int, error) {
	if !s.slept {
		s.slept = true
		time.Sleep(s.delay)
	}
	return s.Writer.Write(p)
}

// `models host --get` stopped copying LTX-2.5's 22 GiB transformer with
// "context deadline exceeded": the result copy ran under the 30-second
// bound of one service call (R31, 2026-11-12). A copy now lasts as long as
// its work when the caller names no --timeout. With the per-call bound at
// one second and a copy that takes two, the download completes; with
// --timeout 1500ms the same copy ends the command non-zero.
func TestResultCopyLastsAsLongAsItsWork(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	endpoint := serviceRuntime(t)
	body := bytes.Repeat([]byte("openabstractions "), 4096)
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	savedBudget, savedSink, savedLink := callBudget, resultSink, linkResults
	t.Cleanup(func() { callBudget, resultSink, linkResults = savedBudget, savedSink, savedLink })
	callBudget, linkResults = time.Second, false
	resultSink = func(f *os.File) io.Writer { return &slowSink{Writer: f, delay: 2 * time.Second} }

	sink := t.TempDir()
	if err := downloadCommand([]string{served(t, body), "--endpoint", endpoint, "--out", sink, "--sha256", digest}, io.Discard, io.Discard); err != nil {
		t.Fatalf("a copy longer than one call's bound: %v", err)
	}
	entries, err := os.ReadDir(sink)
	if err != nil || len(entries) != 1 {
		t.Fatalf("sink %v %v", entries, err)
	}
	if got, err := os.ReadFile(filepath.Join(sink, entries[0].Name())); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("copied %d bytes: %v", len(got), err)
	}

	other := bytes.Repeat([]byte("another file "), 1<<19)
	sum = sha256.Sum256(other)
	bounded := t.TempDir()
	err = downloadCommand([]string{served(t, other), "--endpoint", endpoint, "--out", bounded, "--sha256", hex.EncodeToString(sum[:]), "--timeout", "1500ms"}, io.Discard, io.Discard)
	if err == nil || exitStatus(err) == 0 {
		t.Fatalf("a copy past the caller's --timeout: %v", err)
	}
}

// --get says where each file comes from and how far its download is.
func TestGetVideoSaysEachFilesSourceAndProgress(t *testing.T) {
	f := pinnedFile{name: "ltx-2.5-22b-distilled-transformer-Q8_0.gguf", size: 23604666752, sha256: strings.Repeat("e", 64), url: "https://huggingface.co/x"}
	var out bytes.Buffer
	g := setGetter{
		fetch: func(p pinnedFile, _, _ string) error {
			report := fileProgress(&out, p)
			for _, done := range []int64{0, p.size / 100, p.size / 10, p.size / 10, p.size} {
				if err := report(&api.OperationSnapshot{Progress: api.WorkProgress{Done: done, Total: p.size}}); err != nil {
					return err
				}
			}
			return nil
		},
		holds:   func(p pinnedFile) bool { return p.sha256 == f.sha256 },
		derived: func(pinnedFile) (pinnedFile, bool) { return pinnedFile{}, false },
		command: "models host", output: &out,
	}
	if err := g.place(f, filepath.Join(t.TempDir(), f.name), programfiles.Tree{}, "", 0); err != nil {
		t.Fatal(err)
	}
	other := f
	other.sha256 = strings.Repeat("a", 64)
	if err := g.place(other, filepath.Join(t.TempDir(), f.name), programfiles.Tree{}, "", 0); err != nil {
		t.Fatal(err)
	}
	said := out.String()
	if !strings.Contains(said, "getting "+f.name+" (22.0 GiB) from the download archive\n") ||
		!strings.Contains(said, "getting "+f.name+" (22.0 GiB) from the internet\n") ||
		strings.Count(said, ": 2.2 GiB of 22.0 GiB\n") != 2 || strings.Count(said, ": 22.0 GiB of 22.0 GiB\n") != 2 || strings.Contains(said, "MiB of") {
		t.Fatalf("said %q", said)
	}
}
