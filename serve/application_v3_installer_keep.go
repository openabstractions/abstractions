package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
)

// installerKeepReservation binds a release expectation to one predecessor and
// one held candidate. Its caller must own the native operator registration.
// The reservation conveys no authority until checked in the publication CAS.
type installerKeepReservation struct {
	application string
	expected    connectExpectation
	predecessor applicationV3Registration
	candidate   applicationV3ManifestFile
}

func reserveInstallerKeep(application string, expected connectExpectation, predecessor applicationV3Registration, revision string, candidate applicationV3ManifestFile, now time.Time) (installerKeepReservation, bool) {
	if !providerName.MatchString(application) || predecessor.State != "enabled" ||
		predecessor.Source != "self" || predecessor.Revision != revision ||
		predecessor.FileRevision == "" || revision == "" ||
		!applicationV3Repin("operator", candidate) || !candidate.valid() ||
		!now.Before(expected.expires) || expected.sha256 == "" ||
		!strings.EqualFold(expected.sha256, candidate.Image) ||
		!identity.SameSubjectProgram(expected.program, candidate.Source.Program) {
		return installerKeepReservation{}, false
	}
	return installerKeepReservation{application: application, expected: expected, predecessor: predecessor, candidate: candidate}, true
}

// carry verifies the predecessor and exact candidate at publication, together
// with the still-current expectation. The caller supplies the previously
// carried identity, if any, from the same predecessor registry entry.
func (r installerKeepReservation) carry(application string, expected connectExpectation, predecessor applicationV3Registration, predecessorIdentity string, candidate applicationV3ManifestFile, now time.Time) (string, bool) {
	if r.application == "" || application != r.application || expected != r.expected ||
		!now.Before(r.expected.expires) || predecessor != r.predecessor ||
		!sameApplicationV3Candidate(candidate, r.candidate) {
		return "", false
	}
	if predecessorIdentity == "" {
		predecessorIdentity = predecessor.FileRevision
	}
	return predecessorIdentity, true
}

// sameApplicationV3Candidate compares the held image and declaration. A
// retry opens the image again and gives the same candidate new timestamps.
func sameApplicationV3Candidate(left, right applicationV3ManifestFile) bool {
	return left.Source == right.Source && left.Digest == right.Digest &&
		left.Image == right.Image && left.ImageSize == right.ImageSize &&
		bytes.Equal(left.Raw, right.Raw)
}

type installerKeepPending struct {
	Revision     string `json:"revision"`
	FileRevision string `json:"file_revision"`
	FileHash     string `json:"file_hash"`
	Identity     string `json:"identity"`
	Expires      int64  `json:"expires"`
}

func validInstallerKeepToken(token string) bool {
	if token == "" {
		return true
	}
	_, err := hex.DecodeString(token)
	return len(token) == 48 && err == nil
}

func (p installerKeepPending) valid(entry applicationV3Registration) bool {
	if p == (installerKeepPending{}) {
		return true
	}
	_, err := hex.DecodeString(p.FileHash)
	return entry.State == "removing" && entry.Source == "self" && strings.HasPrefix(entry.Question, messageRemovalQuestion) &&
		entry.Question == messageRemovalQuestion+p.Revision && p.Revision != "" && validInstallerKeepToken(p.Revision) &&
		entry.KeepIdentity == "" && p.FileRevision != "" && p.FileRevision != entry.FileRevision && validInstallerKeepToken(p.FileRevision) &&
		len(p.FileHash) == 64 && err == nil && p.Identity != "" && validInstallerKeepToken(p.Identity) && p.Expires > 0
}

// An authenticated Panel may reissue the exact pin after a restart. The
// original deadline remains the ceiling for the staged replacement.
func (p installerKeepPending) matches(expectation connectExpectation, candidate applicationV3ManifestFile, now time.Time) bool {
	return expectation.id != "" && now.UnixNano() < p.Expires && now.Before(expectation.expires) &&
		strings.EqualFold(expectation.sha256, candidate.Image) && identity.SameSubjectProgram(expectation.program, candidate.Source.Program)
}

func (s *applicationV3Registrations) readInstallerKeepPredecessor(ctx context.Context, pending installerKeepPending) (applicationV3ManifestFile, error) {
	var f applicationV3ManifestFile
	raw, err := cas.ReadLimitContext(ctx, s.filePath(pending.FileRevision), applicationV3ManifestFileLimit)
	if err != nil || applicationV3Hash(raw) != pending.FileHash || applicationV3Strict(raw, &f) != nil {
		return f, errors.New("applications@3: predecessor unavailable")
	}
	return f, nil
}

type installerKeepReceipt struct {
	ID string `json:"id"`
	At int64  `json:"at"`
}

func newInstallerKeepReceipt(expectation connectExpectation, now time.Time) installerKeepReceipt {
	return installerKeepReceipt{ID: expectation.id, At: now.UnixNano()}
}

func (r installerKeepReceipt) valid(entry applicationV3Registration) bool {
	return r == (installerKeepReceipt{}) ||
		r.ID != "" && validInstallerKeepToken(r.ID) && r.At > 0 && entry.KeepIdentity != ""
}

// Reconcile under the registry lock. The receipt and the selected pinned file
// were committed together, and a later replacement cannot replay this receipt.
func (d *applicationDirectory) reconcileInstallerKeepReceipts(ctx context.Context) error {
	if !d.lock(ctx) {
		return ctx.Err()
	}
	defer d.unlock()
	if d.v3 == nil {
		return nil
	}
	return d.withRegistrationReadLocked(ctx, func(map[string]wire.ApplicationDescriptor) error {
		for name, entry := range d.v3.entries {
			receipt := entry.KeepReceipt
			if receipt.ID == "" || entry.State != "enabled" || entry.Source != "self" {
				continue
			}
			file := d.v3.files[name]
			if file.Image == "" || file.Source.FileKey == "" {
				return errors.New("applications: installer receipt has no pinned image")
			}
			keep := connectKept{Decision: installerUpdateSource, At: time.Unix(0, receipt.At).UTC(), Program: file.Source.Program, SHA256: file.Image, FileKey: file.Source.FileKey, Receipt: receipt.ID}
			if err := d.recordConnectKeepLocked(ctx, name, keep); err != nil {
				return err
			}
		}
		return nil
	})
}
