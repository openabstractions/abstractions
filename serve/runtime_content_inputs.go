package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	logging "github.com/openabstractions/abstraction-logging/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	service "github.com/openabstractions/abstraction-storage/go/service"
)

// Request input (storage STO-W6, STO-W12, STO-T7; inference INF-V4). An
// application uploads the input of a request under a bounded input profile:
// both content.write and content.read on input:image or input:audio. A writer
// admitted only by a profile proves the bytes: the store never answers it
// present. The runtime classifies the verified bytes into their profile,
// records the original subject durably before the object is published, and
// reads the object back only for a request of that subject which consumes
// that profile. The record owns the bytes only when its own write published
// them. Owned input is class tmp: held while the writers' identity retention
// lasts and while a request names it, then removed unless another hold names
// it.

const requestInputLimit int64 = 64 << 10

// inputSweepInterval is how often the runtime looks for request input whose
// lifetime rule has ended.
const inputSweepInterval = time.Minute

var errContentDenied = errors.New("content access denied")

type requestInput struct {
	Account string `json:"account"`
	Program string `json:"program"`
	Digest  string `json:"digest"`
	Profile string `json:"profile"`
	// Written is the last write under the profile, RFC 3339 in UTC.
	Written string `json:"written"`
	// Owned: this write published the bytes. A write that proved bytes the
	// store already held owns nothing (STO-W6).
	Owned bool `json:"owned,omitempty"`
}

type requestInputIndex struct {
	Version int            `json:"version"`
	Inputs  []requestInput `json:"inputs"`
	// Kept is set once an exact-digest write named the digest: the object is
	// then more than request input, and the input sweep leaves its bytes.
	Kept bool `json:"kept,omitempty"`
	// Touched is the last Begin or exact-digest write that named the digest,
	// RFC 3339 in UTC; the sweep waits the identity retention after it.
	Touched string `json:"touched,omitempty"`
}

func validInputProfile(profile string) bool {
	return profile == inference.InputImage || profile == inference.InputAudio
}

func validRecordTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func decodeRequestInputs(raw []byte) (requestInputIndex, error) {
	if len(raw) == 0 {
		return requestInputIndex{Version: 1}, nil
	}
	var index requestInputIndex
	if err := json.Unmarshal(raw, &index); err != nil || index.Version != 1 || (index.Touched != "" && !validRecordTime(index.Touched)) {
		return requestInputIndex{}, errors.New("content: invalid request input record")
	}
	for _, input := range index.Inputs {
		// The program was normalized when the entry was written; a read checks
		// its syntax only (rights validStoredSubject).
		if input.Account == "" || !identity.ValidSubjectProgram(input.Program) ||
			!validGeneratedDigest(input.Digest) || !validInputProfile(input.Profile) || !validRecordTime(input.Written) {
			return requestInputIndex{}, errors.New("content: invalid request input record")
		}
	}
	return index, nil
}

func (c *runtimeContent) inputPath(digest string) string {
	return filepath.Join(c.inputs, digest[7:9], digest[7:]+".json")
}

func (c *runtimeContent) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *runtimeContent) stamp() string { return c.clock().UTC().Format(time.RFC3339Nano) }

// decision is the runtime's rights decision; tests may replace it.
func (c *runtimeContent) decision(ctx context.Context, subject rwire.Subject, action, resource string) rwire.DecisionOutcome {
	if c.decide != nil {
		return c.decide(ctx, subject, action, resource)
	}
	return c.rights.decide(ctx, subject, action, resource).Outcome
}

// profileGrants decides both content rights on one input profile.
func (c *runtimeContent) profileGrants(ctx context.Context, subject rwire.Subject, profile string) rwire.DecisionOutcome {
	for _, action := range []string{contentWriteAction, contentReadAction} {
		switch outcome := c.decision(ctx, subject, action, inference.InputResource(profile)); outcome {
		case rwire.DecisionOutcomePermitted:
		case rwire.DecisionOutcomeUnavailable:
			return outcome
		default:
			return rwire.DecisionOutcomeNotGranted
		}
	}
	return rwire.DecisionOutcomePermitted
}

// authorizeWrite decides one content write. An exact-digest rule decides first;
// only its absence lets an input profile decide, so an explicit denial or a
// policy outage never falls back to a profile (STO-W12). A writer admitted only
// by a profile must prove the bytes (service.ErrRequireBytes at Begin,
// STO-W6). At publication the verified head names the one profile that must
// be held, and the original subject is recorded before anything is
// published, owning the bytes only when the store did not hold them already.
// A call that names no stage is decided by digest alone.
func (c *runtimeContent) authorizeWrite(ctx context.Context, subject rwire.Subject, digest string, call service.WriteCall) error {
	if c == nil || c.rights == nil || subject.Account != c.rights.owner || !identity.ValidSubjectProgram(subject.Program) {
		return errContentDenied
	}
	switch c.decision(ctx, subject, contentWriteAction, digest) {
	case rwire.DecisionOutcomePermitted:
		if call.Stage == service.WriteBegin || call.Stage == service.WritePublish {
			if err := c.keepInput(ctx, digest); err != nil {
				return service.ErrPolicyUnavailable
			}
		}
		return nil
	case rwire.DecisionOutcomeNotGranted:
	case rwire.DecisionOutcomeUnavailable:
		return service.ErrPolicyUnavailable
	default:
		return errContentDenied
	}
	if !validGeneratedDigest(digest) {
		return errContentDenied
	}
	switch call.Stage {
	case service.WriteBegin, service.WriteAppend, service.WriteCommit:
		unavailable := false
		for _, profile := range inference.InputProfiles {
			switch c.profileGrants(ctx, subject, profile) {
			case rwire.DecisionOutcomePermitted:
				if call.Stage != service.WriteBegin {
					return nil
				}
				// A known upload defers the sweep; the writer proves its bytes.
				if err := c.touchInput(ctx, digest); err != nil {
					return service.ErrPolicyUnavailable
				}
				return service.ErrRequireBytes
			case rwire.DecisionOutcomeUnavailable:
				unavailable = true
			}
		}
		if unavailable {
			return service.ErrPolicyUnavailable
		}
		return errContentDenied
	case service.WritePublish:
	default:
		return errContentDenied
	}
	profile := inference.InputProfileOf(call.Head)
	if profile == "" {
		return errContentDenied
	}
	switch c.profileGrants(ctx, subject, profile) {
	case rwire.DecisionOutcomePermitted:
	case rwire.DecisionOutcomeUnavailable:
		return service.ErrPolicyUnavailable
	default:
		return errContentDenied
	}
	entry := requestInput{Account: subject.Account, Program: identity.NormalizeSubjectProgram(subject.Program), Digest: digest, Profile: profile,
		Written: c.stamp(), Owned: !call.Existing}
	if err := c.recordInput(ctx, entry); err != nil {
		return service.ErrPolicyUnavailable
	}
	return nil
}

// recordInput durably records one write under an input profile, replacing the
// same subject's earlier record of the same profile. Ownership, once a write
// of this subject published the bytes, stays with the record.
func (c *runtimeContent) recordInput(ctx context.Context, entry requestInput) error {
	return c.changeInput(ctx, entry.Digest, true, func(index *requestInputIndex) {
		kept := index.Inputs[:0]
		for _, existing := range index.Inputs {
			if existing.Account == entry.Account && existing.Program == entry.Program && existing.Profile == entry.Profile {
				entry.Owned = entry.Owned || existing.Owned
				continue
			}
			kept = append(kept, existing)
		}
		index.Inputs = append(kept, entry)
	})
}

// touchInput stamps an existing record of digest with the time of a Begin.
func (c *runtimeContent) touchInput(ctx context.Context, digest string) error {
	return c.changeInput(ctx, digest, false, func(index *requestInputIndex) { index.Touched = c.stamp() })
}

// keepInput records that an exact-digest write named digest: request input
// never removes the bytes of such an object. The mark is written whether or
// not request input has named the digest yet, so a later input write cannot
// claim the bytes of an exact-digest upload still publishing.
func (c *runtimeContent) keepInput(ctx context.Context, digest string) error {
	if !validGeneratedDigest(digest) {
		return nil
	}
	return c.changeInput(ctx, digest, true, func(index *requestInputIndex) { index.Kept, index.Touched = true, c.stamp() })
}

// changeInput edits the record of digest under the record lock. create false
// leaves an absent record absent.
func (c *runtimeContent) changeInput(ctx context.Context, digest string, create bool, edit func(*requestInputIndex)) error {
	c.inputsMu.Lock()
	defer c.inputsMu.Unlock()
	if !create {
		if _, err := os.Stat(c.inputPath(digest)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return cas.ChangeLimitContext(ctx, c.inputPath(digest), requestInputLimit, func(raw []byte) ([]byte, error) {
		index, err := decodeRequestInputs(raw)
		if err != nil {
			return nil, err
		}
		edit(&index)
		return json.Marshal(index)
	})
}

// authorizeInputRead permits the original subject to read its own request
// input for a request that consumes the recorded profile, while it holds
// content.read on that profile (STO-W12, INF-V4). It runs only after the
// exact-digest rule was absent.
func (c *runtimeContent) authorizeInputRead(ctx context.Context, subject rwire.Subject, digest, profile string) (bool, error) {
	raw, err := cas.ReadLimitContext(ctx, c.inputPath(digest), requestInputLimit)
	if err != nil {
		return false, service.ErrPolicyUnavailable
	}
	index, err := decodeRequestInputs(raw)
	if err != nil {
		return false, service.ErrPolicyUnavailable
	}
	unavailable := false
	for _, entry := range index.Inputs {
		if entry.Account != subject.Account || !identity.SameSubjectProgram(entry.Program, subject.Program) || entry.Digest != digest || entry.Profile != profile {
			continue
		}
		switch c.decision(ctx, subject, contentReadAction, inference.InputResource(entry.Profile)) {
		case rwire.DecisionOutcomePermitted:
			return true, nil
		case rwire.DecisionOutcomeUnavailable:
			unavailable = true
		}
	}
	if unavailable {
		return false, service.ErrPolicyUnavailable
	}
	return false, nil
}

// inputHolder takes one reading of the requests or jobs that have not ended
// and returns every digest they name as input. An error keeps every object: an
// unknown answer is never read as "unheld".
type inputHolder func() (map[string]bool, error)

func (c *runtimeContent) addInputHolder(holder inputHolder) {
	c.inputsMu.Lock()
	defer c.inputsMu.Unlock()
	c.holders = append(c.holders, holder)
}

// namedInputs reads every holder once for a sweep. ok is false when one
// could not answer.
func (c *runtimeContent) namedInputs() (map[string]bool, bool) {
	c.inputsMu.Lock()
	holders := append([]inputHolder(nil), c.holders...)
	c.inputsMu.Unlock()
	named := map[string]bool{}
	for _, holder := range holders {
		digests, err := holder()
		if err != nil {
			c.reportErr(fmt.Errorf("request input holder: %w", err))
			return nil, false
		}
		for digest, held := range digests {
			named[digest] = named[digest] || held
		}
	}
	return named, true
}

// inputExpired reports whether every record of index, and its last touch,
// are past the writers' identity retention at now.
func inputExpired(index requestInputIndex, now time.Time) bool {
	stamps := []string{index.Touched}
	for _, input := range index.Inputs {
		stamps = append(stamps, input.Written)
	}
	for _, stamp := range stamps {
		if stamp == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || now.Before(at.Add(service.RequestRetention)) {
			return false
		}
	}
	return true
}

// ownsBytes reports whether request input alone holds the object: some write
// under a profile published it and no exact-digest write named it.
func ownsBytes(index requestInputIndex) bool {
	if index.Kept {
		return false
	}
	for _, input := range index.Inputs {
		if input.Owned {
			return true
		}
	}
	return false
}

// sweepInputs ends request input whose lifetime rule has ended: every
// writer's identity retention has passed and no request or job names it
// (STO-T7). Its record goes; its bytes go when the record owned them and no
// generated output names them. It returns how many objects it removed. A
// holder that cannot answer keeps everything for this sweep.
func (c *runtimeContent) sweepInputs(ctx context.Context) int {
	if c == nil || c.inputs == "" {
		return 0
	}
	shards, err := os.ReadDir(c.inputs)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.reportErr(err)
		}
		return 0
	}
	named, ok := c.namedInputs()
	if !ok {
		return 0
	}
	removed := 0
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(c.inputs, shard.Name()))
		if err != nil {
			c.reportErr(err)
			continue
		}
		for _, file := range files {
			name, ok := strings.CutSuffix(file.Name(), ".json")
			if !ok || file.IsDir() || ctx.Err() != nil {
				continue
			}
			digest := "sha256:" + name
			if !validGeneratedDigest(digest) || name[:2] != shard.Name() || named[digest] {
				continue
			}
			if c.removeInput(ctx, digest) {
				removed++
			}
		}
	}
	return removed
}

// removeInput ends one record whose lifetime rule has ended. It holds the
// store lock the gateway's write takes from its presence check to its
// publication, then the record lock, and decides on a fresh reading.
func (c *runtimeContent) removeInput(ctx context.Context, digest string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputsMu.Lock()
	defer c.inputsMu.Unlock()
	path := c.inputPath(digest)
	raw, err := cas.ReadLimitContext(ctx, path, requestInputLimit)
	if err != nil {
		c.reportErr(err)
		return false
	}
	index, err := decodeRequestInputs(raw)
	if err != nil {
		c.reportErr(fmt.Errorf("request input %s: %w", digest, err))
		return false
	}
	if !inputExpired(index, c.clock()) {
		return false
	}
	removedBytes := false
	if ownsBytes(index) && !c.isGeneratedOutput(ctx, digest) {
		if err := c.store.Remove(digest); err != nil {
			c.reportErr(fmt.Errorf("request input %s: %w", digest, err))
			return false
		}
		removedBytes = true
		c.logRemoval(digest)
	}
	for _, leftover := range []string{path, path + ".lock"} {
		if err := os.Remove(leftover); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.reportErr(err)
		}
	}
	return removedBytes
}

// isGeneratedOutput reports whether generated-output provenance names digest.
// An unreadable record keeps the content.
func (c *runtimeContent) isGeneratedOutput(ctx context.Context, digest string) bool {
	raw, err := cas.ReadLimitContext(ctx, c.generatedPath(digest), generatedProvenanceLimit)
	if err != nil {
		return true
	}
	index, err := decodeGeneratedOutputs(raw)
	return err != nil || len(index.Outputs) > 0
}

// logRemoval writes the one record a retention removal leaves (STO-T6).
func (c *runtimeContent) logRemoval(digest string) {
	if c.sink == nil {
		return
	}
	attrs := map[string]string{"class": "tmp", "key": digest, "kind": "request-input"}
	if err := c.sink.Write(logging.Record{Time: logging.At(c.clock()), Level: logging.LevelInfo, Msg: "abstraction.storage/retention.removed", Attrs: attrs}); err != nil {
		c.reportErr(err)
	}
}

func (c *runtimeContent) reportErr(err error) {
	if err != nil && c.report != nil {
		c.report(err)
	}
}

// serveInputRetention sweeps request input every inputSweepInterval until ctx
// ends. The returned function waits for the loop to stop.
func (c *runtimeContent) serveInputRetention(ctx context.Context) func() {
	done := make(chan struct{})
	if c == nil {
		close(done)
		return func() {}
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(inputSweepInterval)
		defer ticker.Stop()
		for {
			c.sweepInputs(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { <-done }
}
