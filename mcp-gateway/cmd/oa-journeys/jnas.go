//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openabstractions/abstraction-download/go/archive"
)

// J-NAS (owner, R30, 2026-11-11): a model served from the download
// archive took 33 s where copying the same file took 5 s.
//
// The journey names a fake archive folder as the download archive on the
// Panel's Settings row, the setting Setup keeps in the runtime's state. A
// download through OA (openabstractions download with its SHA-256) from a
// loopback source is written back into the archive's sha256 folder. A second
// download of the same SHA-256, with the source gone, is served from the
// archive: the runtime's log says "download archive: served", and the
// command takes at most twice a plain copy of the file.
func init() { registerTimed(46, "J-NAS", nasBudget, runNAS) }

// nasFileBytes is the SmolLM2 file's size the owner measured with, above
// the archive's 50 MB write-back floor.
const nasFileBytes = 144_811_072

// Budget the source, two runtime job results, two command outputs, the
// archive entry and the timed plain copy. Outputs can be hard links, but
// count them as full copies for filesystems that require copy-out. Allow
// additional space for the isolated state, logs and allocation overhead.
const nasFixtureWriteBytes = 7*nasFileBytes + (16 << 20)

var nasCheckWriteSpace = archive.CheckWriteSpace

func nasPreflight(work string) error {
	if err := nasCheckWriteSpace(work, nasFixtureWriteBytes); err != nil {
		return fmt.Errorf("J-NAS requires %d bytes for fixture files above the archive reserve on the work volume: %w", nasFixtureWriteBytes, err)
	}
	return nil
}

// nasCommandFloor is the least the archive-served command is allowed: the
// command's own start, the job's admission and the archive's probe, which a
// plain copy of a file on a fast local disk does not pay. With the R31 fix
// the command took 2.2 s here against a 0.3 s copy; before it, 19-30 s.
const nasCommandFloor = 3 * time.Second

func nasBudget(*options) time.Duration { return 4 * time.Minute }

func runNAS(ctx context.Context, e *env, o *options, r *result) {
	work := filepath.Join(e.work, "nas")
	archive := filepath.Join(work, "archive")
	if err := os.MkdirAll(archive, 0o700); err != nil {
		r.check("archive folder", false, "%v", err)
		return
	}
	source := filepath.Join(work, "source.bin")
	sum, err := writeRandomFile(source, nasFileBytes)
	if !r.check("a model-sized file to download", err == nil, "%v", err) {
		return
	}
	var settings map[string]any
	_, err = e.panel(ctx, "POST", "/settings/archive", map[string]any{"folder": archive}, &settings)
	if !r.check("the Settings row names the archive folder", err == nil && fmt.Sprint(settings["folder"]) == archive, "%v %v", settings, err) {
		return
	}
	setting := filepath.Join(e.state, "setup", "download-archive.json")
	_, err = os.Stat(setting)
	r.check("the setting is in the runtime's state", err == nil, "%s: %v", setting, err)

	var requests atomic.Int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if !r.check("loopback source", err == nil, "%v", err) {
		return
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		http.ServeFile(w, req, source)
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	url := "http://" + ln.Addr().String() + "/smollm2-135m.gguf"

	download := func(name string) (time.Duration, string, error) {
		out := filepath.Join(work, name)
		_ = os.MkdirAll(out, 0o700)
		at := time.Now()
		text, err := e.cli(ctx, "download", url, "--sha256", sum, "--size", strconv.Itoa(nasFileBytes), "--out", out,
			"--key", "j-nas-"+name+"-"+newID()[:8], "--timeout", "3m", "--quiet")
		took := time.Since(at)
		if err == nil {
			if got, herr := hashFile(filepath.Join(out, "smollm2-135m.gguf")); herr != nil || got != sum {
				err = fmt.Errorf("result %s: %v sha256 %s", filepath.Join(out, "smollm2-135m.gguf"), herr, got)
			}
		}
		return took, strings.TrimSpace(text), err
	}

	// The runtime owns the transfer and the archive's write-back; the
	// command copies the result out. Before the R31 fix one copy-out call of
	// a file this size outlasted the command's 30 s call budget.
	took, text, err := download("first")
	r.check("the first download's command completes with the file", err == nil, "%.1fs: %v %s", took.Seconds(), err, clip(text, 300))
	if !r.check("the first download fetched its source", requests.Load() > 0, "%d source requests", requests.Load()) {
		_ = srv.Close()
		return
	}
	entry := filepath.Join(archive, "sha256", sum)
	kept := false
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(500 * time.Millisecond) {
		if info, err := os.Stat(entry); err == nil && info.Size() == nasFileBytes {
			kept = true
			break
		}
	}
	r.check("the first download is copied into the archive's sha256 folder", kept, "%s", entry)
	_ = srv.Close()
	if !kept {
		return
	}
	before := requests.Load()
	plain, err := plainCopy(entry, filepath.Join(work, "plain-copy.bin"))
	if !r.check("a plain copy of the archive file", err == nil, "%v", err) {
		return
	}
	took, text, err = download("second")
	log, _ := os.ReadFile(filepath.Join(e.work, "runtime.stderr.log"))
	r.check("the second download asks nothing of its source", requests.Load() == before, "%d new source requests", requests.Load()-before)
	r.check(`the host log says "download archive: served"`, strings.Contains(string(log), "download archive: served "+sum), "%s", clip(lastLines(string(log), "download archive", 4), 600))
	r.check("the second download's command completes with the file", err == nil, "%.1fs: %v %s", took.Seconds(), err, clip(text, 300))
	bound := max(2*plain, nasCommandFloor)
	r.check("the archive-served download takes at most twice a plain copy", err == nil && took <= bound,
		"%.2fs; plain copy %.2fs; bound %.2fs (twice the copy, at least %s)", took.Seconds(), plain.Seconds(), bound.Seconds(), nasCommandFloor)
}

func writeRandomFile(path string, size int64) (string, error) {
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(f, h), rand.Reader, size); err != nil {
		f.Close()
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), f.Close()
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// plainCopy copies from to to with its SHA-256 computed on the way, as the
// archive's fill does, and returns the time it took.
func plainCopy(from, to string) (time.Duration, error) {
	at := time.Now()
	in, err := os.Open(from)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	_, err = io.CopyBuffer(io.MultiWriter(out, h), in, make([]byte, 1<<20))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	took := time.Since(at)
	_ = os.Remove(to)
	return took, err
}

// lastLines is the last n lines of text that contain substr.
func lastLines(text, substr string, n int) string {
	var found []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, substr) {
			found = append(found, strings.TrimSpace(line))
		}
	}
	if len(found) > n {
		found = found[len(found)-n:]
	}
	return strings.Join(found, " | ")
}
