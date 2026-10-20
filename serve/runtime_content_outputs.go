package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	logging "github.com/openabstractions/abstraction-logging/go"
	service "github.com/openabstractions/abstraction-storage/go/service"
)

// Generated output is tmp (inference INF-V5, storage STO-T1). The runtime's
// copy of a picture, a speech clip or a video is held while the operation that
// published it keeps its result, while a request or job that has not ended
// names it as input, while request input or an exact-digest write records it,
// and while an upload of it is live. Afterwards tmp_max_retention runs from
// its last publication. Until the storage collector exists (W2-04), the input
// retention loop runs this pass at most once an hour
// (backlog/PICTURES-STORAGE.md ruling 1).

// outputSweepInterval is the least time between two generated-output passes.
const outputSweepInterval = time.Hour

// outputPassLimit bounds the provenance records one pass reads; the next pass
// continues after the last one read. Tests lower it.
var outputPassLimit = 2048

// outputSettingTimeout bounds the configuration read of one pass.
const outputSettingTimeout = 30 * time.Second

// outputLockedReadTimeout bounds the file reads and writes one decision makes
// while it holds the store lock.
const outputLockedReadTimeout = 10 * time.Second

// readLimit bounds the remembered read decisions; past it the stale ones are
// forgotten at the next decision.
const readLimit = 4096

// noteRead records a permitted read decision of digest.
func (c *runtimeContent) noteRead(digest string) {
	now := c.clock()
	c.readsMu.Lock()
	defer c.readsMu.Unlock()
	if c.reads == nil {
		c.reads = map[string]time.Time{}
	}
	if len(c.reads) >= readLimit {
		c.forgetReadsLocked(now)
	}
	c.reads[digest] = now
}

// reading reports whether a read of digest was decided within a storage
// reader's idle lifetime: a handle opened or read since then is still live.
func (c *runtimeContent) reading(digest string) bool {
	now := c.clock()
	c.readsMu.Lock()
	defer c.readsMu.Unlock()
	c.forgetReadsLocked(now)
	_, ok := c.reads[digest]
	return ok
}

func (c *runtimeContent) forgetReadsLocked(now time.Time) {
	for digest, at := range c.reads {
		if now.Sub(at) >= service.IdleLifetime {
			delete(c.reads, digest)
		}
	}
}

// defaultOutputRetention is an operation's result retention when the runtime
// was given none: a job's minimum retention (abstraction-job).
const defaultOutputRetention = time.Duration(acceptanceprovider.MinimumRetentionMs) * time.Millisecond

func (c *runtimeContent) outputRetentionMs() int64 {
	return max(c.outputRetention, defaultOutputRetention).Milliseconds()
}

// outputPass counts what one generated-output pass did.
type outputPass struct {
	ran                                              bool
	skipped                                          string
	examined, stamped, kept, held, removed, failures int
}

// serveOutputRetention runs the generated-output pass when an hour has passed
// since the last one. It reports whether a pass ran.
func (c *runtimeContent) serveOutputRetention(ctx context.Context) outputPass {
	if c == nil || c.provenance == "" {
		return outputPass{}
	}
	now := c.clock()
	if !c.lastOutputPass.IsZero() && now.Sub(c.lastOutputPass) < outputSweepInterval {
		return outputPass{}
	}
	c.lastOutputPass = now
	pass := c.sweepOutputs(ctx)
	c.logOutputPass(pass)
	return pass
}

// sweepOutputs is one generated-output pass. Every holder is read once before
// anything is decided; a holder or setting that cannot answer removes nothing.
// A record written before generated output was tmp is stamped with the time of
// this pass and is left alone until a later one.
func (c *runtimeContent) sweepOutputs(ctx context.Context) outputPass {
	pass := outputPass{ran: true}
	if c.readTmp == nil {
		pass.skipped = "setting:unavailable"
		return pass
	}
	readCtx, cancel := context.WithTimeout(ctx, outputSettingTimeout)
	tmp, level, err := c.readTmp(readCtx)
	cancel()
	if err != nil || tmp <= 0 {
		if err != nil {
			c.reportErr(fmt.Errorf("generated output: %w", err))
		}
		pass.skipped = "setting:unavailable"
		return pass
	}
	named, ok := c.namedInputs()
	if !ok {
		pass.skipped = "holder:unavailable"
		return pass
	}
	uploads, err := c.liveUploads(ctx)
	if err != nil {
		c.reportErr(fmt.Errorf("generated output: uploads: %w", err))
		pass.skipped = "holder:unavailable"
		return pass
	}
	shards, err := os.ReadDir(c.provenance)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.reportErr(err)
			pass.skipped = "provenance:unavailable"
		}
		return pass
	}
	// A pass that reached its limit continues after the last name it read;
	// a pass that read to the end starts the next one from the beginning.
	after, last := c.outputCursor, ""
	c.outputCursor = ""
	for _, shard := range shards {
		// A link is never followed: only a real directory is a shard.
		if !shard.Type().IsDir() || len(shard.Name()) != 2 || after != "" && shard.Name() < after[:2] {
			continue
		}
		files, err := os.ReadDir(filepath.Join(c.provenance, shard.Name()))
		if err != nil {
			c.reportErr(err)
			pass.failures++
			continue
		}
		for _, file := range files {
			name, ok := strings.CutSuffix(file.Name(), ".json")
			digest := "sha256:" + name
			if !ok || !file.Type().IsRegular() || !validGeneratedDigest(digest) || name[:2] != shard.Name() || name <= after {
				continue
			}
			if ctx.Err() != nil {
				return pass
			}
			if pass.examined == outputPassLimit {
				c.outputCursor = last
				return pass
			}
			pass.examined++
			last = name
			c.decideOutput(ctx, digest, tmp, level, named[digest] || uploads[digest], &pass)
		}
	}
	return pass
}

// decideOutput stamps, keeps or removes one digest's generated output.
func (c *runtimeContent) decideOutput(ctx context.Context, digest string, tmp time.Duration, level string, held bool, pass *outputPass) {
	index, err := c.readGenerated(ctx, digest)
	if err != nil || len(index.Outputs) == 0 {
		if err != nil {
			c.reportErr(fmt.Errorf("generated output %s: %w", digest, err))
			pass.failures++
		}
		return
	}
	if !stamped(index) {
		if err := c.stampGenerated(ctx, digest); err != nil {
			c.reportErr(fmt.Errorf("generated output %s: %w", digest, err))
			pass.failures++
			return
		}
		pass.stamped++
		return
	}
	now := c.clock()
	if !outputExpired(index, tmp, now) {
		pass.kept++
		return
	}
	if held {
		pass.held++
		return
	}
	switch removed, err := c.removeOutput(ctx, digest, tmp, level); {
	case err != nil:
		c.reportErr(fmt.Errorf("generated output %s: %w", digest, err))
		pass.failures++
	case removed:
		pass.removed++
	default:
		pass.held++
	}
}

func (c *runtimeContent) readGenerated(ctx context.Context, digest string) (generatedOutputIndex, error) {
	raw, err := cas.ReadLimitContext(ctx, c.generatedPath(digest), generatedProvenanceLimit)
	if err != nil {
		return generatedOutputIndex{}, err
	}
	return decodeGeneratedOutputs(raw)
}

// stamped reports whether every entry records its publication.
func stamped(index generatedOutputIndex) bool {
	for _, output := range index.Outputs {
		if output.Published == "" {
			return false
		}
	}
	return true
}

// stampGenerated writes the current time into each entry that has none, so a
// runtime upgraded with older output starts every clock at the upgrade.
func (c *runtimeContent) stampGenerated(ctx context.Context, digest string) error {
	ctx, cancel := context.WithTimeout(ctx, outputLockedReadTimeout)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	return cas.ChangeLimitContext(ctx, c.generatedPath(digest), generatedProvenanceLimit, func(raw []byte) ([]byte, error) {
		index, err := decodeGeneratedOutputs(raw)
		if err != nil {
			return nil, err
		}
		if len(index.Outputs) == 0 {
			return raw, nil
		}
		for i := range index.Outputs {
			if index.Outputs[i].Published == "" {
				index.Outputs[i].Published = c.stamp()
				index.Outputs[i].RetentionMs = c.outputRetentionMs()
			}
		}
		return json.Marshal(index)
	})
}

// outputExpired reports whether every entry's result retention and then
// tmp_max_retention have passed since its publication. An unstamped or
// unreadable entry is never expired.
func outputExpired(index generatedOutputIndex, tmp time.Duration, now time.Time) bool {
	if len(index.Outputs) == 0 || tmp <= 0 {
		return false
	}
	for _, output := range index.Outputs {
		at, err := time.Parse(time.RFC3339Nano, output.Published)
		if output.Published == "" || err != nil || output.RetentionMs < 0 {
			return false
		}
		retention := max(time.Duration(output.RetentionMs)*time.Millisecond, defaultOutputRetention)
		if now.Before(at.Add(retention).Add(tmp)) {
			return false
		}
	}
	return true
}

// removeOutput removes one expired generated output. It reads the requests
// and jobs that name input again, then holds the store lock every publication
// takes and the request input lock, and decides on fresh readings: the
// provenance, the request input record, the live uploads and the reads in
// progress. The bytes go first and the provenance after them, with one STO-T6
// record. A read in progress holds the bytes: the storage reader decides every
// chunk again, and without the provenance that decision refuses, on POSIX as
// on Windows. Bytes a reader still has open on Windows refuse their removal
// and stay, with their provenance, for a later pass.
func (c *runtimeContent) removeOutput(ctx context.Context, digest string, tmp time.Duration, level string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, outputLockedReadTimeout)
	defer cancel()
	// The requests and jobs are read again just before the locks: a holder
	// answers under its own lock, which the store lock never waits inside.
	named, ok := c.namedInputs()
	if !ok {
		return false, errors.New("request input holder unavailable")
	}
	if named[digest] {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputsMu.Lock()
	defer c.inputsMu.Unlock()
	index, err := c.readGenerated(ctx, digest)
	if err != nil {
		return false, err
	}
	if !stamped(index) || !outputExpired(index, tmp, c.clock()) {
		return false, nil
	}
	if c.reading(digest) {
		return false, nil
	}
	// Request input or an exact-digest write recorded these bytes: the input
	// sweep owns that record, and the bytes stay while it exists.
	if _, err := os.Lstat(c.inputPath(digest)); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	uploads, err := c.liveUploads(ctx)
	if err != nil {
		return false, err
	}
	if uploads[digest] {
		return false, nil
	}
	if ref, ok := c.store.Find(digest); ok {
		// The store's blob is removed only when it is a regular file; a link
		// is left alone and reported.
		info, err := os.Lstat(c.store.Path(ref))
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("content blob is no regular file")
		}
		if err := c.store.Remove(digest); err != nil {
			return false, err
		}
		c.logOutputRemoval(digest, level)
	}
	path := c.generatedPath(digest)
	for _, leftover := range []string{path, path + ".lock"} {
		if err := os.Remove(leftover); err != nil && !errors.Is(err, os.ErrNotExist) {
			return true, err
		}
	}
	return true, nil
}

// liveUploads reads the storage writer's request records and returns every
// digest an unexpired one names: an upload in progress, or one committed
// within the writer's identity retention. A missing record file names none.
func (c *runtimeContent) liveUploads(ctx context.Context) (map[string]bool, error) {
	live := map[string]bool{}
	if c.records == "" {
		return live, nil
	}
	raw, err := cas.ReadLimitContext(ctx, c.records, service.MaxRecordFileBytes)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return live, nil
	}
	var file struct {
		Records []struct {
			Digest  string `json:"digest"`
			Expires int64  `json:"expires_unix_ms"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("writer records: %w", err)
	}
	now := c.clock().UnixMilli()
	for _, record := range file.Records {
		if record.Expires > now {
			live[record.Digest] = true
		}
	}
	return live, nil
}

// logOutputRemoval writes the one record a generated-output removal leaves
// (STO-T6).
func (c *runtimeContent) logOutputRemoval(digest, level string) {
	if c.sink == nil {
		return
	}
	attrs := map[string]string{"class": "tmp", "key": digest, "kind": "generated-output", "level": level}
	if err := c.sink.Write(logging.Record{Time: logging.At(c.clock()), Level: logging.LevelInfo, Msg: "abstraction.storage/retention.removed", Attrs: attrs}); err != nil {
		c.reportErr(err)
	}
}

// logOutputPass writes one line per generated-output pass with its counts.
func (c *runtimeContent) logOutputPass(pass outputPass) {
	if c.sink == nil || !pass.ran {
		return
	}
	attrs := map[string]string{"class": "tmp", "kind": "generated-output"}
	level := logging.LevelInfo
	if pass.skipped != "" {
		attrs["skipped"] = pass.skipped
		level = logging.LevelWarn
	}
	for key, count := range map[string]int{"examined": pass.examined, "stamped": pass.stamped, "kept": pass.kept, "held": pass.held, "removed": pass.removed, "failures": pass.failures} {
		attrs[key] = strconv.Itoa(count)
	}
	if err := c.sink.Write(logging.Record{Time: logging.At(c.clock()), Level: level, Msg: "abstraction.storage/retention.pass", Attrs: attrs}); err != nil {
		c.reportErr(err)
	}
}
