package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	store "github.com/openabstractions/abstraction-cas/go/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
)

// An unsigned program is one Windows cannot attribute to a publisher: its
// image carries no signature, or a signature Windows refuses. It registers
// itself, stays pending until the person keeps it, and is pinned by the
// SHA-256 and size of the image the person kept. It differs from a signed
// program in two ways only: every changed image waits for the person's
// review (ProgramReviewKey, Keep or Not now), and only the person answers its
// first-use questions.

// errApplicationV3ImageChanged is an unsigned registration whose current image
// differs from its pin. The program is paused until the person reviews it.
var errApplicationV3ImageChanged = errors.New("applications@3: unsigned program changed since it was kept")

// applicationImageUnsigned reports a verdict that names no trusted publisher:
// no signature, or a signature Windows refused.
func applicationImageUnsigned(code identity.Code, proof identity.Proof) bool {
	return !code.Trusted && (proof == identity.ProofUnsigned || proof == identity.ProofInvalid)
}

// heldSigned reports a trusted signature bound to a held image.
func heldSigned(h *heldApplicationManifest) bool {
	return h != nil && h.Code.Trusted && h.Proof == identity.ProofBound
}

func applicationImageDigest(v string) bool {
	raw, err := hex.DecodeString(v)
	return err == nil && len(raw) == 32 && strings.ToLower(v) == v
}

// unsignedProgram reports whether OpenAbstractions registered program from
// an unsigned image, as an application or as a provider program. It reads the
// directory's current registrations without a storage transaction, so an
// answerer check never invalidates the role facts it was prepared with. A
// directory it cannot lock reports true, so the caller refuses.
func (d *applicationDirectory) unsignedProgram(ctx context.Context, program string) bool {
	if d == nil || program == "" {
		return false
	}
	if !d.lock(ctx) {
		return true
	}
	defer d.unlock()
	if d.v3 == nil {
		return false
	}
	for _, f := range d.v3.files {
		if !f.Source.signed() && identity.SameSubjectProgram(f.Source.Program, program) {
			return true
		}
	}
	for _, source := range d.v3.programSources {
		if !source.signed() && identity.SameSubjectProgram(source.Program, program) {
			return true
		}
	}
	return false
}

// unsignedAnnounceReask is how often a program announcing its own start
// re-asks about the same changed image the person answered Not now about.
const unsignedAnnounceReask = time.Hour

// unsignedImageReviews admits and follows the person's review of a changed
// unsigned image. One review is open per application at a time; Not now
// holds that exact image back until the runtime restarts, the image changes
// again or the program starts (launchAttempt).
//
// A review question is bound to this runtime's memory (asks ASK-S1): a
// restart withdraws a pending one before the runtime serves (withdrawStale),
// and the next revalidation of the changed image asks afresh.
type unsignedImageReviews struct {
	k        *selfRegisteredQuestions
	now      func() time.Time
	mu       sync.Mutex
	open     map[string]bool
	declined map[string]string
	// held is the file stamp of the image the open review asks about or the
	// person answered Not now about.
	held map[string]unsignedHeldImage
	// reasked is when a program's own start last lifted Not now, per
	// declined image occurrence.
	reasked map[string]time.Time
}

type unsignedHeldImage struct{ revision, stamp, occurrence string }

func newUnsignedImageReviews(k *selfRegisteredQuestions) *unsignedImageReviews {
	return &unsignedImageReviews{k: k, now: time.Now, open: map[string]bool{}, declined: map[string]string{},
		held: map[string]unsignedHeldImage{}, reasked: map[string]time.Time{}}
}

// heldBack reports that stamp names the image of name at revision that an
// open review read or the person answered Not now about.
func (r *unsignedImageReviews) heldBack(name, revision, stamp string) bool {
	if stamp == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.held[name]
	return ok && h.revision == revision && h.stamp == stamp && (r.open[name] || r.declined[name] == h.occurrence)
}

// review starts one review of name's current image unless one is open.
func (r *unsignedImageReviews) review(name, revision string) {
	r.mu.Lock()
	if r.open[name] {
		r.mu.Unlock()
		return
	}
	r.open[name] = true
	r.mu.Unlock()
	go func() {
		follow, err := r.ask(name, revision)
		if err != nil {
			r.k.a.report(fmt.Errorf("asks: reviewing the changed unsigned program %s: %w", name, err))
		}
		if follow == nil {
			r.close(name)
			return
		}
		follow()
	}()
}

// launchAttempt lifts Not now for name, so the start is asked about the
// changed image again. A start the person makes always lifts it. A program
// announcing its own start lifts it once per unsignedAnnounceReask for the
// same image, so a program in a restart loop does not ask on every start.
func (r *unsignedImageReviews) launchAttempt(name string, person bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	occurrence := r.declined[name]
	if occurrence == "" {
		return
	}
	if !person {
		now := r.now()
		if at, ok := r.reasked[occurrence]; ok {
			if since := now.Sub(at); since >= 0 && since < unsignedAnnounceReask {
				return
			}
		}
		for o, at := range r.reasked {
			if now.Sub(at) >= unsignedAnnounceReask {
				delete(r.reasked, o)
			}
		}
		r.reasked[occurrence] = now
	}
	delete(r.declined, name)
	delete(r.held, name)
}

func (r *unsignedImageReviews) close(name string) {
	r.mu.Lock()
	delete(r.open, name)
	if r.declined[name] != r.held[name].occurrence {
		delete(r.held, name)
	}
	r.mu.Unlock()
}

// unsignedKept is the kept unsigned registration of name at revision.
func (d *applicationDirectory) unsignedKept(ctx context.Context, name, revision string) (applicationV3ManifestFile, wire.ApplicationDescriptor, applicationV3ManifestDecoder, bool) {
	if !d.lock(ctx) {
		return applicationV3ManifestFile{}, wire.ApplicationDescriptor{}, nil, false
	}
	defer d.unlock()
	if d.v3 == nil {
		return applicationV3ManifestFile{}, wire.ApplicationDescriptor{}, nil, false
	}
	var f applicationV3ManifestFile
	var v wire.ApplicationDescriptor
	ok := false
	err := d.withRegistrationReadLocked(ctx, func(map[string]wire.ApplicationDescriptor) error {
		e, exists := d.v3.entries[name]
		f = d.v3.files[name]
		v = d.descriptors[name]
		ok = exists && e.Revision == revision && (e.State == "enabled" || e.State == "disabled") && f.valid() && !f.Source.signed()
		return nil
	})
	return f, v, d.v3.decode, err == nil && ok && d.v3.decode != nil
}

// capturedUnsignedImage opens the program's current image and reads it as a
// continuation of the kept registration: the same name, program and title.
// The review names the program by the title the person kept, so an image that
// declares another title is no continuation; it registers anew.
func (d *applicationDirectory) capturedUnsignedImage(ctx context.Context, f applicationV3ManifestFile, kept wire.ApplicationDescriptor, decode applicationV3ManifestDecoder) (held *heldApplicationManifest, next applicationV3ManifestFile, image string, err error) {
	held, err = openSignedApplicationManifest(ctx, fileid.Self, f.Source.Program)
	if err != nil {
		return nil, next, "", err
	}
	// Every refusal below returns a nil hold; release the one opened here.
	h := held
	defer func() {
		if err != nil {
			err = errors.Join(err, h.Close())
		}
	}()
	program := identity.NormalizeSubjectProgram(applicationV3HeldProgram(held))
	if program != f.Source.Program || identity.SharedInterpreterProgram(program, held.Code) {
		return nil, next, "", errors.New("the program file is no longer the kept program")
	}
	image, size, err := d.heldPin(ctx, held)
	if err != nil {
		return nil, next, "", err
	}
	v, digest, err := decode(held.Bytes, program, held)
	if err != nil || v.Name != kept.Name || v.Program != program || v.Title != kept.Title || client.ReservedApplicationTitle(v.Title) || !applicationImageDigest(digest) {
		return nil, next, "", errors.Join(errors.New("the changed program declares another application"), err)
	}
	next = applicationV3ManifestFile{Raw: bytes.Clone(held.Bytes), Digest: digest, Source: applicationV3Source{Program: program, FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof}, Accepted: f.Accepted}
	if !heldSigned(held) {
		next.Image, next.ImageSize = image, size
	}
	if !next.valid() {
		return nil, next, "", errors.New("the changed program has no admissible identity")
	}
	return held, next, image, nil
}

// ask admits the review of name's changed image and returns the follower
// that applies the person's answer, or nil when nothing is asked.
func (r *unsignedImageReviews) ask(name, revision string) (func(), error) {
	k := r.k
	ctx := k.ctx
	f, kept, decode, ok := k.d.unsignedKept(ctx, name, revision)
	if !ok {
		return nil, nil
	}
	held, next, image, err := k.d.capturedUnsignedImage(ctx, f, kept, decode)
	if err != nil {
		return nil, err
	}
	stamp := applicationHeldStamp(held)
	if err := held.Close(); err != nil {
		return nil, err
	}
	if image == f.Image && bytes.Equal(next.Raw, f.Raw) {
		return nil, nil
	}
	target := asks.ProgramReviewTarget{Program: f.Source.Program, Revision: revision, Image: image, FileKey: next.Source.FileKey, Spec: next.Digest,
		Cause: asks.ProgramReviewUnsignedChanged, Supported: true, Title: selfRegisteredTitle(kept)}
	slots, err := asks.ProgramReviewSlots(asks.ProgramReviewFacts{Targets: []asks.ProgramReviewTarget{target}})
	if err != nil {
		return nil, err
	}
	occurrence := slots["occurrence"]
	r.mu.Lock()
	back := unsignedHeldImage{revision: revision, stamp: stamp, occurrence: occurrence}
	declined := r.declined[name] == occurrence
	if declined {
		r.held[name] = back
	}
	r.mu.Unlock()
	if declined {
		return nil, nil
	}
	request, err := selfRegisteredRequestKey()
	if err != nil {
		return nil, err
	}
	record, outcome, err := k.a.book.AskApplicationFrom(selfRegistrationScope(k.a.scope, occurrence), k.a.display, request, asks.ProgramReviewKey, slots,
		listen.Seen{Why: "admitted by the runtime for a changed unsigned program the person kept"}, asks.Publisher{})
	if err != nil {
		return nil, err
	}
	if outcome != "pending" {
		return nil, fmt.Errorf("review question %s", outcome)
	}
	r.mu.Lock()
	r.held[name] = back
	r.mu.Unlock()
	decided := k.a.book.Decided(record.ID)
	return func() {
		defer r.close(name)
		select {
		case <-ctx.Done():
			return
		case <-decided:
		}
		answered, found, err := k.a.book.ApplicationRecord(record.ID)
		if err != nil || !found || answered.Pending() || answered.Key != asks.ProgramReviewKey || answered.AskerProgram != "" ||
			answered.AnsweredBy != asks.AnsweredByOperator || answered.About != occurrence {
			return
		}
		switch answered.Option {
		case "keep":
			if err := k.d.keepUnsignedImage(ctx, name, target); err != nil {
				k.a.report(fmt.Errorf("asks: keeping the changed unsigned program %s: %w", name, err))
			}
		case "not_now":
			r.mu.Lock()
			r.declined[name] = occurrence
			r.mu.Unlock()
		}
	}, nil
}

// keepUnsignedImage pins the image the person reviewed. It re-reads that
// image under a fresh hold, requires the reviewed SHA-256, file and
// declaration, and replaces only the registration revision it reviewed. The
// entry keeps its state; the obsolete revision's mediation closes.
func (d *applicationDirectory) keepUnsignedImage(ctx context.Context, name string, target asks.ProgramReviewTarget) error {
	f, kept, decode, ok := d.unsignedKept(ctx, name, target.Revision)
	if !ok || kept.Name != name || selfRegisteredTitle(kept) != target.Title {
		return errors.New("the reviewed registration changed")
	}
	held, next, image, err := d.capturedUnsignedImage(ctx, f, kept, decode)
	if err != nil {
		return err
	}
	defer held.Close()
	if image != target.Image || next.Source.FileKey != target.FileKey || next.Digest != target.Spec {
		return errors.New("the program changed again after the review")
	}
	next.Verified = registrationTime(d.now())
	encoded, err := json.Marshal(next)
	if err != nil || len(encoded) > applicationV3ManifestFileLimit {
		return errors.New("applications@3: manifest file capacity")
	}
	if err := held.Recheck(ctx); err != nil {
		return err
	}
	if !d.lock(ctx) {
		return ctx.Err()
	}
	changed := false
	err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		e, exists := d.v3.entries[name]
		current := d.v3.files[name]
		if !exists || e.Revision != target.Revision || (e.State != "enabled" && e.State != "disabled") || current.Source.signed() || current.Source.Program != next.Source.Program {
			return errors.New("the reviewed registration changed")
		}
		revision, err := applicationToken()
		if err != nil {
			return err
		}
		if err := (store.BoundedFileStore{MaxBytes: applicationV3ManifestFileLimit}).WriteContext(ctx, d.v3.filePath(revision), store.Value{}, encoded); err != nil {
			return err
		}
		old := e.FileRevision
		e.Revision, e.FileRevision, e.FileHash = revision, revision, applicationV3Hash(encoded)
		entries := maps.Clone(d.v3.entries)
		entries[name] = e
		if err := d.v3.persistLocked(ctx, entries); err != nil {
			return err
		}
		changed = true
		d.v3.files[name] = next
		_ = os.Remove(d.v3.filePath(old))
		return d.projectApplicationV3Locked(legacy)
	})
	if changed {
		d.generations[name]++
		d.notifyLocked()
	}
	d.unlock()
	if changed && d.v3Changed != nil {
		d.v3Changed(name, target.Revision)
	}
	return err
}

// heldPin is the held image's SHA-256 and size.
func (d *applicationDirectory) heldPin(ctx context.Context, h *heldApplicationManifest) (string, int64, error) {
	if d.imagePin != nil {
		return d.imagePin(ctx, h)
	}
	return applicationHeldPin(ctx, h)
}
