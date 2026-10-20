package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	cas "github.com/openabstractions/abstraction-cas/go/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The start log is the application directory's record of when each
// registered application became present (APPLICATIONS.md, "Start log"): the
// Panel's Activity timeline reads it through ListStarts. It is kept in
// starts.json beside descriptors.json, oldest first, each entry seven days,
// at most maxStarts.

const (
	startsFile     = "starts.json"
	maxStarts      = 256
	startRetention = 7 * 24 * time.Hour
)

// startLog is starts.json: the retained starts, oldest first, and the time
// at or before which a start was evicted by the bound, zero for none.
type startLog struct {
	Starts  []wire.ApplicationStart `json:"starts"`
	SinceMs int64                   `json:"since_ms,omitempty"`
}

func validStart(s wire.ApplicationStart) bool {
	return providerName.MatchString(s.Application) && s.Program != "" && len(s.Program) <= 4096 && utf8.ValidString(s.Program) &&
		len(s.Title) <= 1024 && utf8.ValidString(s.Title) && s.StartedUnixMs > 0
}

// loadStarts reads starts.json. A missing or unreadable log starts empty and
// complete only from now: the log never stops the directory from opening.
func (d *applicationDirectory) loadStarts() {
	value, err := d.store.Read(filepath.Join(d.dir, startsFile))
	if err != nil {
		d.startsSince = d.now().UnixMilli()
		return
	}
	d.startsBase = value
	if value.Data == nil {
		return
	}
	var log startLog
	if json.Unmarshal(value.Data, &log) != nil || len(log.Starts) > maxStarts || log.SinceMs < 0 ||
		slices.ContainsFunc(log.Starts, func(s wire.ApplicationStart) bool { return !validStart(s) }) {
		d.startsSince = d.now().UnixMilli()
		return
	}
	d.starts, d.startsSince = log.Starts, log.SinceMs
}

// collectStartsLocked drops starts older than their retention.
func (d *applicationDirectory) collectStartsLocked() {
	now := d.now().UnixMilli()
	d.starts = slices.DeleteFunc(d.starts, func(s wire.ApplicationStart) bool { return now-s.StartedUnixMs >= startRetention.Milliseconds() })
	if d.startsSince != 0 && now-d.startsSince >= startRetention.Milliseconds() {
		d.startsSince = 0
	}
}

// recordStartLocked appends one start when no other live instance of the
// application is in the same session, evicting the oldest past the bound. The
// presence it describes is already recorded, so a failed write keeps the
// start in memory and the next start writes it again.
func (d *applicationDirectory) recordStartLocked(ctx context.Context, application, session, instance string, v wire.ApplicationDescriptor) {
	for id, l := range d.instances {
		if id != instance && l.application == application && l.session == session {
			return
		}
	}
	attempt := d.attempts[application]
	launched := attempt != nil && !attempt.completed && attempt.session == session && attempt.generation == d.generations[application]
	d.collectStartsLocked()
	d.starts = append(d.starts, wire.ApplicationStart{Application: application, Program: v.Program, Title: v.Title, StartedUnixMs: d.now().UnixMilli(), Launched: launched})
	for len(d.starts) > maxStarts {
		d.startsSince = max(d.startsSince, d.starts[0].StartedUnixMs)
		d.starts = slices.Delete(d.starts, 0, 1)
	}
	raw, err := json.Marshal(startLog{Starts: d.starts, SinceMs: d.startsSince})
	if err != nil {
		return
	}
	path := filepath.Join(d.dir, startsFile)
	if err := d.store.WriteContext(ctx, path, d.startsBase, raw); err == nil {
		d.startsBase = cas.Value{Data: raw}
		return
	}
	// The base may be stale: a read that failed at open, or a file changed
	// under the directory. Read it again and write once more over it, since
	// this runtime's in-memory log is the one it serves.
	if base, err := d.store.Read(path); err == nil {
		d.startsBase = base
		if d.store.WriteContext(ctx, path, base, raw) == nil {
			d.startsBase = cas.Value{Data: raw}
		}
	}
}

// startsRevision names the log's current content for a cursor.
func (d *applicationDirectory) startsRevisionLocked() string {
	//unchecked: the log is plain marshalable data; a failed marshal only weakens the cursor hash
	raw, _ := json.Marshal(startLog{Starts: d.starts, SinceMs: d.startsSince})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// listStarts pages the start log newest first for this runtime's operator
// programs only.
func (d *applicationDirectory) listStarts(ctx context.Context, s rights.Subject, cursor string, limit int64) wire.ApplicationStartPage {
	refused := func(outcome wire.ApplicationOutcome) wire.ApplicationStartPage {
		return wire.ApplicationStartPage{Outcome: outcome, Starts: []wire.ApplicationStart{}}
	}
	if len(cursor) > 256 || !utf8.ValidString(cursor) || limit < 1 || limit > 64 {
		return refused(wire.ApplicationOutcomeInvalid)
	}
	if ctx.Err() != nil {
		return refused(wire.ApplicationOutcomeUnavailable)
	}
	if s.Account != d.owner || s.Program == "" || d.operator == nil || !d.operator(s.Program) {
		return refused(wire.ApplicationOutcomeForbidden)
	}
	if !d.lock(ctx) {
		return refused(wire.ApplicationOutcomeUnavailable)
	}
	defer d.mu.Unlock()
	d.collectStartsLocked()
	prefix := d.epoch + "." + d.startsRevisionLocked() + "."
	offset := 0
	if cursor != "" {
		word, ok := strings.CutPrefix(cursor, prefix)
		n, err := strconv.Atoi(word)
		if !ok || err != nil || n < 0 || n > len(d.starts) || strconv.Itoa(n) != word {
			return refused(wire.ApplicationOutcomeStale)
		}
		offset = n
	}
	page := wire.ApplicationStartPage{Outcome: wire.ApplicationOutcomePage, Starts: []wire.ApplicationStart{}, CompleteSinceUnixMs: d.startsSince}
	for offset < len(d.starts) && int64(len(page.Starts)) < limit {
		page.Starts = append(page.Starts, d.starts[len(d.starts)-1-offset])
		offset++
	}
	page.Complete = offset == len(d.starts)
	if !page.Complete {
		page.Cursor = prefix + strconv.Itoa(offset)
	}
	return page
}
