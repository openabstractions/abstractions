package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	asks "github.com/openabstractions/abstraction-asks/go"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	cas "github.com/openabstractions/abstraction-cas/go"
	store "github.com/openabstractions/abstraction-cas/go/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/manifest"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Private receiving shape; the reviewed @3 wire adapter remains a later join.
type applicationV3RegistrationChange struct {
	wire.ApplicationChange
	Revision             string
	messageReserved      bool
	installerExpectation string
}

func applicationV3Change(outcome wire.ApplicationOutcome, reason string) applicationV3RegistrationChange {
	return applicationV3RegistrationChange{ApplicationChange: appChange(outcome, reason)}
}

const applicationV3ManifestFileLimit = 512 << 10
const applicationV3LegacyIndexLimit = 64 << 10
const applicationV3IndexLimit = registrationIndexLimit

// The index is the publication point. Manifest files are immutable, selected
// by a random revision and checked against their exact encoded digest. An
// interrupted pre-publication file has no authority and can be reclaimed later.
type applicationV3Registration struct {
	Revision     string               `json:"revision"`
	FileRevision string               `json:"file_revision"`
	FileHash     string               `json:"file_hash"`
	Source       string               `json:"source"`
	State        string               `json:"state"`
	Question     string               `json:"question,omitempty"`
	KeepReceipt  installerKeepReceipt `json:"keep_receipt,omitzero"`
	KeepIdentity string               `json:"keep_identity,omitempty"`
	KeepPending  installerKeepPending `json:"keep_pending,omitzero"`
}

type applicationV3Source struct {
	Program     string         `json:"program"`
	FileKey     string         `json:"file_key"`
	Code        identity.Code  `json:"code"`
	Proof       identity.Proof `json:"proof"`
	ImageSHA256 string         `json:"image_sha256,omitempty"`
}

type applicationV3ManifestFile struct {
	Raw      []byte              `json:"raw"`
	Digest   string              `json:"digest"`
	Source   applicationV3Source `json:"source"`
	Accepted string              `json:"accepted,omitempty"`
	Verified string              `json:"verified,omitempty"`
	// Image and ImageSize pin an unsigned program: the SHA-256 and size of the
	// image the person kept. A signed program carries neither.
	Image     string `json:"image_sha256,omitempty"`
	ImageSize int64  `json:"image_size,omitempty"`
}

// decode is a private composition seam for the strict manifest owner. It must
// validate the complete closed manifest and canonical bound, and return only
// the safe legacy display projection and canonical digest. A nil owner refuses
// registration and loading; no wire caller supplies this function.
// held is nonnil only for the independently opened native registration read.
// Nil selects retained display/provenance validation and supplies no live proof.
// The callback performs pure checks of captured evidence; it never opens files.
type applicationV3ManifestDecoder func([]byte, string, *heldApplicationManifest) (wire.ApplicationDescriptor, string, error)

type applicationV3Registrations struct {
	describe               registrationProgramDescriptions
	dir                    string
	entries                map[string]applicationV3Registration
	files                  map[string]applicationV3ManifestFile
	decode                 applicationV3ManifestDecoder
	beforeWrite            func(context.Context, map[string]applicationV3Registration) error
	programs               map[string]registration.Registration
	removals               []registration.RemovalReceipt
	revision               string
	programFiles           map[string]registrationProgramFile
	removalFiles           []registrationProgramFile
	programSources         map[string]applicationV3Source
	programDeclarers       map[string]string
	programCompatibility   map[string][]providerFile
	programInputDigests    map[string]string
	programFacetSources    map[string]map[registration.FacetReference]registration.Source
	programFacetDigests    map[string]map[registration.FacetReference]string
	programFacetConditions map[string]map[registration.FacetReference]registrationFacetCondition
	legacyPrograms         map[string]registration.Registration
	legacyProgramFiles     map[string][]registrationProgramFile
	preparedFiles          []registrationProgramFile
	indexRaw               []byte
	programApprovals       map[string]registrationProgramApproval
}

func applicationV3Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func applicationV3Strict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	canonical, err := json.Marshal(v)
	if err != nil || !bytes.Equal(b, canonical) {
		return errors.New("applications@3: invalid retained encoding")
	}
	return nil
}

func (v applicationV3Source) valid() bool {
	return identity.ValidSubjectProgram(v.Program) && utf8.ValidString(v.Program) && v.FileKey != "" && len(v.FileKey) <= 1024 &&
		(v.signed() || applicationImageUnsigned(v.Code, v.Proof)) &&
		(v.ImageSHA256 == "" || applicationImageDigest(v.ImageSHA256))
}

// signed reports a trusted signature captured at registration. Any other
// accepted source is an unsigned program kept by its image pin.
func (v applicationV3Source) signed() bool {
	return v.Code.Trusted && v.Proof == identity.ProofBound
}

// valid requires exactly one continuity owner: a trusted signature, or the
// pin of an unsigned image.
func (f applicationV3ManifestFile) valid() bool {
	if !f.Source.valid() {
		return false
	}
	if f.Source.signed() {
		return f.Image == "" && f.ImageSize == 0
	}
	return applicationImageDigest(f.Image) && f.ImageSize > 0
}

// Retained comparisons use captured identity and the existing identity owner's
// missing-key name rule. They perform no filesystem or signature reads under CAS.
func applicationRegistrationPrograms(a, b, keyA, keyB string) bool {
	return identity.SameNormalizedSubjectProgramKeyed(a, b, func() string { return keyA }, func() string { return keyB })
}

func (v applicationV3Source) sameProgram(other applicationV3Source) bool {
	return applicationRegistrationPrograms(v.Program, other.Program, v.FileKey, other.FileKey)
}

// sameApplicationV3Continuity reports that next keeps old's continuity owner:
// the same trusted signer, or the same unsigned image pin. A signer is its
// subject, issuer and team, as the registry's shipped-image continuity
// compares it (shippedPublisherContinuous).
func sameApplicationV3Continuity(old, next applicationV3ManifestFile) bool {
	if old.Source.signed() || next.Source.signed() {
		a, b := old.Source.Code, next.Source.Code
		return old.Source.signed() && next.Source.signed() && a.Subject == b.Subject && a.Issuer == b.Issuer && a.TeamID == b.TeamID
	}
	return old.Image == next.Image && old.ImageSize == next.ImageSize
}

func (s *applicationV3Registrations) filePath(revision string) string {
	return filepath.Join(s.dir, revision+".json")
}

// setupModalityLocked projects only the confirmed signature-covered declaration
// already admitted by the registration owner. It conveys no live signer proof
// or service permission. The caller holds the directory lock and refreshed CAS.
func (s *applicationV3Registrations) setupModalityLocked(name, program, modality string) bool {
	e, exists := s.entries[name]
	f, held := s.files[name]
	if !exists || !held || (e.State != "enabled" && e.State != "disabled") || !f.valid() ||
		!identity.SameSubjectProgram(f.Source.Program, program) {
		return false
	}
	raw, _ := applicationV3RetainedManifest(f.Raw)
	m, err := registration.DecodeManifest(raw)
	if err != nil || m.Name != name {
		return false
	}
	for _, need := range m.Needs.Models {
		if need.Modality == modality {
			return true
		}
	}
	return false
}

// readLocked always runs while descriptors.json's native CAS lock is held.
// A stored signature is provenance. Reading it never grants dispatch authority.
func (s *applicationV3Registrations) readLocked(ctx context.Context) error {
	index, err := cas.ReadLimitContext(ctx, filepath.Join(s.dir, "index.json"), applicationV3IndexLimit)
	if err != nil {
		return err
	}
	entries, err := s.readProgramIndex(ctx, index)
	if err != nil {
		return err
	}
	if entries == nil || len(entries) > 64 {
		return errors.New("applications@3: index capacity")
	}
	files := map[string]applicationV3ManifestFile{}
	pendingPrograms := []applicationV3Source{}
	for name, entry := range entries {
		if !providerName.MatchString(name) || len(entry.Revision) != 48 || len(entry.FileRevision) != 48 || len(entry.FileHash) != 64 ||
			(entry.Source != "self" && entry.Source != "operator" && entry.Source != "installation") ||
			(entry.State != "pending" && entry.State != "enabled" && entry.State != "disabled" && entry.State != "removing") ||
			len(entry.Question) > 1024 || !utf8.ValidString(entry.Question) ||
			entry.State == "pending" && entry.Source != "self" ||
			entry.State != "pending" && entry.State != "removing" && entry.Question != "" || entry.State == "removing" && entry.Question == "" ||
			entry.State == "removing" && entry.Source != "self" && !strings.HasPrefix(entry.Question, messageRemovalQuestion) {
			return errors.New("applications@3: invalid index entry")
		}
		if _, err := hex.DecodeString(entry.Revision); err != nil {
			return err
		}
		if _, err := hex.DecodeString(entry.FileRevision); err != nil {
			return err
		}
		if !entry.KeepPending.valid(entry) || !validInstallerKeepToken(entry.KeepIdentity) || !entry.KeepReceipt.valid(entry) {
			return errors.New("applications@3: invalid keep lineage")
		}
		if _, err := hex.DecodeString(entry.FileHash); err != nil {
			return err
		}
		b, err := cas.ReadLimitContext(ctx, s.filePath(entry.FileRevision), applicationV3ManifestFileLimit)
		if err != nil {
			return err
		}
		var f applicationV3ManifestFile
		if b == nil || applicationV3Hash(b) != entry.FileHash || applicationV3Strict(b, &f) != nil || len(f.Raw) == 0 || len(f.Raw) > manifest.MaxApplicationManifestBytes || !f.valid() || len(f.Digest) != 64 {
			return errors.New("applications@3: corrupt manifest file")
		}
		if s.decode == nil {
			return errors.New("applications@3: manifest owner unavailable")
		}
		projection, digest, err := s.decode(f.Raw, f.Source.Program, nil)
		if err != nil || !validApplicationDescriptor(projection) || client.ReservedApplicationTitle(projection.Title) || projection.Name != name || projection.Program != f.Source.Program || digest != f.Digest {
			return errors.New("applications@3: invalid retained manifest")
		}
		files[name] = f
		if entry.Source == "self" && (entry.State == "pending" || entry.State == "removing") {
			for _, p := range pendingPrograms {
				if p.sameProgram(f.Source) {
					return errors.New("applications@3: duplicate pending program")
				}
			}
			pendingPrograms = append(pendingPrograms, f.Source)
		}
	}
	s.entries, s.files = entries, files
	if err := s.projectUncoveredLocked(ctx); err != nil {
		return err
	}
	return s.reclaimLocked(ctx)
}

// The same descriptors CAS excludes all registration readers and writers.
// Readers capture manifest bytes before releasing it. At most 64 published
// files and one interrupted publication candidate may exist, each with its CAS
// lock, plus the index and its lock. Failed cleanup prevents another allocation.
func (s *applicationV3Registrations) reclaimLocked(ctx context.Context) error {
	root, err := fileid.Open(ctx, fileid.Self, s.dir, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return err
	}
	defer root.Close()
	names, err := root.File().Readdirnames(registrationDirectoryEntryLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > registrationDirectoryEntryLimit {
		return errors.New("applications@3: retained directory capacity")
	}
	referenced := map[string]bool{"index.json": true, "index.json.lock": true}
	bounds := map[string]int64{"index.json": applicationV3IndexLimit}
	prepared := map[string]registrationProgramFile{}
	for _, e := range s.entries {
		referenced[e.FileRevision+".json"] = true
		referenced[e.FileRevision+".json.lock"] = true
		bounds[e.FileRevision+".json"] = applicationV3ManifestFileLimit
		if prior := e.KeepPending.FileRevision; prior != "" {
			referenced[prior+".json"], referenced[prior+".json.lock"] = true, true
			bounds[prior+".json"] = applicationV3ManifestFileLimit
		}
	}
	for _, f := range s.programFiles {
		referenced[f.Revision+".json"] = true
		referenced[f.Revision+".json.lock"] = true
		bounds[f.Revision+".json"] = registrationRecordFileLimit
	}
	for _, f := range s.removalFiles {
		referenced[f.Revision+".json"] = true
		referenced[f.Revision+".json.lock"] = true
		bounds[f.Revision+".json"] = registrationRecordFileLimit
	}
	for _, files := range s.legacyProgramFiles {
		for _, f := range files {
			referenced[f.Revision+".json"] = true
			referenced[f.Revision+".json.lock"] = true
			bounds[f.Revision+".json"] = f.Bytes
		}
	}
	for _, f := range s.preparedFiles {
		bounds[f.Revision+".json"] = f.Bytes
		prepared[f.Revision+".json"] = f
	}
	orphans := []string{}
	files := 0
	// Validate the complete bounded inventory before deleting anything.
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		lock := strings.HasSuffix(name, ".lock")
		base := strings.TrimSuffix(name, ".lock")
		if base != "index.json" {
			revision := strings.TrimSuffix(base, ".json")
			decoded, decodeErr := hex.DecodeString(revision)
			if len(revision) != 48 || len(decoded) != 24 || decodeErr != nil || hex.EncodeToString(decoded) != revision || base != revision+".json" {
				return errors.New("applications@3: unexpected retained entry")
			}
			if !lock {
				files++
			}
		}
		h, err := root.Beneath(name, fileid.Options{Links: fileid.RefuseLinks, Read: true})
		if err != nil {
			return err
		}
		info, statErr := h.File().Stat()
		limit := int64(registrationRecordFileLimit)
		if observed, ok := bounds[name]; ok {
			limit = observed
		}
		if statErr != nil || h.FileID().IsDir() || h.FileID().Links() != 1 || !info.Mode().IsRegular() || info.Size() > limit || lock && info.Size() != 0 {
			h.Close()
			return errors.Join(errors.New("applications@3: unsafe retained entry"), statErr)
		}
		if !referenced[name] && !lock {
			b, readErr := io.ReadAll(io.LimitReader(h.File(), limit+1))
			var f applicationV3ManifestFile
			if readErr != nil || int64(len(b)) > limit {
				h.Close()
				return errors.Join(errors.New("applications@3: unrecognized orphan"), readErr)
			}
			if allocation, ok := prepared[name]; ok {
				if int64(len(b)) != allocation.Bytes || applicationV3Hash(b) != allocation.Hash {
					h.Close()
					return errors.New("registrations: interrupted payload changed")
				}
				h.Close()
				orphans = append(orphans, name)
				continue
			}
			if _, _, err := decodeProgramPayload(b); err == nil {
				h.Close()
				orphans = append(orphans, name)
				continue
			}
			if _, err := registration.DecodeRemovalReceipt(b); err == nil {
				h.Close()
				orphans = append(orphans, name)
				continue
			}
			if _, err := decodeLegacyFacetPayload(b); err == nil {
				h.Close()
				orphans = append(orphans, name)
				continue
			}
			if applicationV3Strict(b, &f) != nil || !f.valid() || len(f.Raw) == 0 || len(f.Raw) > manifest.MaxApplicationManifestBytes || len(f.Digest) != 64 {
				h.Close()
				return errors.New("applications@3: unrecognized orphan")
			}
			v, digest, decodeErr := s.decode(f.Raw, f.Source.Program, nil)
			if decodeErr != nil || !validApplicationDescriptor(v) || v.Program != f.Source.Program || digest != f.Digest {
				h.Close()
				return errors.New("applications@3: invalid orphan manifest")
			}
		}
		if err := h.Close(); err != nil {
			return err
		}
		if !referenced[name] {
			orphans = append(orphans, name)
		}
	}
	if files > registrationDirectoryPayloadLimit {
		return errors.New("applications@3: retained manifest capacity")
	}
	for _, name := range orphans {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
			return err
		}
	}
	return s.clearProgramPreparations(ctx)
}

func (s *applicationV3Registrations) persistLocked(ctx context.Context, entries map[string]applicationV3Registration) error {
	if s.beforeWrite != nil {
		if err := s.beforeWrite(ctx, entries); err != nil {
			return err
		}
	}
	b, err := s.encodeProgramIndex(ctx, entries)
	if err != nil || len(b) > applicationV3IndexLimit {
		return errors.New("applications@3: index capacity")
	}
	base, err := (store.BoundedFileStore{MaxBytes: applicationV3IndexLimit}).Read(filepath.Join(s.dir, "index.json"))
	if err != nil {
		return err
	}
	if err := (store.BoundedFileStore{MaxBytes: applicationV3IndexLimit}).WriteContext(ctx, filepath.Join(s.dir, "index.json"), base, b); err != nil {
		return err
	}
	if _, err := s.readProgramIndex(ctx, b); err != nil {
		return err
	}
	s.entries = entries
	return nil
}

func decodeLegacyApplications(b []byte) (map[string]wire.ApplicationDescriptor, error) {
	v := map[string]wire.ApplicationDescriptor{}
	if b != nil && json.Unmarshal(b, &v) != nil || v == nil || len(v) > 64 {
		return nil, errors.New("applications: invalid descriptors")
	}
	for name, descriptor := range v {
		if name != descriptor.Name || !validApplicationDescriptor(descriptor) {
			return nil, errors.New("applications: invalid descriptor")
		}
	}
	return v, nil
}

// withRegistrationStorageLocked reuses the legacy descriptor CAS lock. Policy,
// questions, signature verification and mediation callbacks run outside it.
func (d *applicationDirectory) withRegistrationStorageLocked(ctx context.Context, edit func(map[string]wire.ApplicationDescriptor) error) error {
	return d.withRegistrationRoleFactsLocked(ctx, edit, nil, false)
}

// Read-only owner callbacks retain an unchanged complete projection while
// refreshing it, so concurrent requests do not manufacture metadata outages.
func (d *applicationDirectory) withRegistrationReadLocked(ctx context.Context, read func(map[string]wire.ApplicationDescriptor) error) error {
	return d.withRegistrationRoleFactsLocked(ctx, read, nil, true)
}

// withRegistrationMessageFactsLocked publishes prepared messaging facts only
// against the same exact registration/enrollment inputs under the existing CAS.
func (d *applicationDirectory) withRegistrationMessageFactsLocked(ctx context.Context, edit func(map[string]wire.ApplicationDescriptor) error, messaging *messagePartyFacts) error {
	return d.withRegistrationRoleFactsLocked(ctx, edit, messaging, true)
}

func (d *applicationDirectory) withRegistrationRoleFactsLocked(ctx context.Context, edit func(map[string]wire.ApplicationDescriptor) error, messaging *messagePartyFacts, readOnly bool) error {
	return d.withRegistrationLegacyEditLocked(ctx, edit, messaging, readOnly, false)
}

// rewriteLegacy is used only by the conditional activation owner. The same
// descriptor CAS serializes its journal and the existing enrollment writers.
func (d *applicationDirectory) withRegistrationLegacyEditLocked(ctx context.Context, edit func(map[string]wire.ApplicationDescriptor) error, messaging *messagePartyFacts, readOnly, rewriteLegacy bool) error {
	// Invalidate before a read or mutation can discover a different kept state.
	// A failed transaction never leaves an old allow-looking projection.
	prior := d.roles.Load()
	var peers *messagePeerFacts
	if prior != nil {
		peers = prior.messagePeers
	}
	if !readOnly || prior == nil || prior.messaging == nil || prior.messaging.current(ctx) != nil {
		d.roles.Store(nil)
		prior = nil
	}
	var roles *applicationRoles
	var changedLegacy map[string]wire.ApplicationDescriptor
	err := cas.ChangeLimitContext(ctx, filepath.Join(d.dir, "descriptors.json"), 600<<10, func(b []byte) ([]byte, error) {
		legacy, err := decodeLegacyApplications(b)
		if err != nil {
			return nil, err
		}
		if d.v3 != nil {
			if err := d.v3.readLocked(ctx); err != nil {
				return nil, err
			}
			for name := range d.v3.entries {
				if _, exists := legacy[name]; exists {
					return nil, errors.New("applications@3: legacy collision")
				}
			}
			if len(legacy)+len(d.v3.entries) > 64 {
				return nil, errors.New("applications@3: shared capacity")
			}
		} else {
			b, err := cas.ReadLimitContext(ctx, filepath.Join(filepath.Dir(d.dir), "applications-v3", "registrations", "index.json"), applicationV3IndexLimit)
			if err != nil {
				return nil, err
			}
			if b != nil {
				return nil, errors.New("applications@3: manifest owner unavailable")
			}
		}
		beforeLegacy := maps.Clone(legacy)
		if err := edit(legacy); err != nil {
			return nil, err
		}
		if rewriteLegacy {
			marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
			if err != nil {
				return nil, err
			}
			for name, m := range marks {
				if m.Agent != nil && m.Agent.UpdateReservation != "" && !reflect.DeepEqual(beforeLegacy[name], legacy[name]) {
					return nil, errors.New("applications: connection update reserved")
				}
			}
		}
		if rewriteLegacy {
			b, err = json.Marshal(legacy)
			if err != nil {
				return nil, err
			}
			changedLegacy = maps.Clone(legacy)
		}
		if d.v3 != nil && d.roleDecode != nil {
			roles, err = d.projectRolesLocked()
			if err != nil {
				return nil, err
			}
		}
		if messaging != nil {
			if roles == nil {
				return nil, errors.New("applications: messaging role owner unavailable")
			}
			if err := messaging.current(ctx); err != nil {
				return nil, err
			}
			roles.messaging = messaging
		}
		d.base = store.Value{Data: bytes.Clone(b)}
		return b, nil
	})
	if err == nil {
		if rewriteLegacy {
			if err = d.projectRegistrationsLocked(changedLegacy); err != nil {
				d.roles.Store(nil)
				return err
			}
		}
		if prior != nil && roles != nil && slices.Equal(prior.hosts, roles.hosts) && prior.messaging.current(ctx) == nil && (messaging == nil || prior.messaging.same(messaging)) {
			d.publishRoles(prior, peers)
		} else {
			d.publishRoles(roles, peers)
		}
	} else {
		d.roles.Store(nil)
	}
	return err
}

// applicationRoles is an immutable, at-most-64-entry read index of the kept
// manifest truth. Nil is unknown; a successful empty directory has an empty list.
type applicationRoles struct {
	hosts        []asks.DeclaredHost
	messaging    *messagePartyFacts
	messagePeers *messagePeerFacts
}

// Registration owns hosts/messaging; pairing owns the independent peer metadata.
// Retain a concurrent pairing publication when rebuilding registration facts.
func (d *applicationDirectory) publishRoles(roles *applicationRoles, peers *messagePeerFacts) {
	if roles != nil && roles.messaging != nil {
		d.agentClass.Store(roles.messaging)
	}
	for {
		current := d.roles.Load()
		if current != nil {
			peers = current.messagePeers
		}
		next := roles
		if roles != nil && roles.messagePeers != peers {
			copy := *roles
			copy.messagePeers = peers
			next = &copy
		}
		if d.roles.CompareAndSwap(current, next) {
			return
		}
	}
}

func (d *applicationDirectory) projectRolesLocked() (*applicationRoles, error) {
	if d.v3 == nil || d.roleDecode == nil {
		return nil, nil
	}
	roles := &applicationRoles{}
	for _, f := range d.v3.files {
		host, err := d.roleDecode(f.Raw, f.Source.Program)
		if err != nil {
			return nil, err
		}
		if host {
			roles.hosts = append(roles.hosts, asks.DeclaredHost{Program: f.Source.Program, FileKey: f.Source.FileKey})
		}
	}
	return roles, nil
}

func (d *applicationDirectory) refreshRoles(ctx context.Context) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			d.roles.Store(nil)
		}
	}()
	if !d.lock(ctx) {
		d.roles.Store(nil)
		return ctx.Err()
	}
	defer d.unlock()
	return d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		if d.v3 == nil {
			return errors.New("applications@3: role owner unavailable")
		}
		return d.projectApplicationV3Locked(legacy)
	})
}

// attachApplicationV3 installs the strict private decoder before serving. The
// runtime route join supplies it; its absence keeps this family unavailable.
func (d *applicationDirectory) attachApplicationV3(ctx context.Context, decode applicationV3ManifestDecoder) error {
	if decode == nil {
		return errors.New("applications@3: manifest owner unavailable")
	}
	if !d.lock(ctx) {
		return ctx.Err()
	}
	defer d.unlock()
	s := &applicationV3Registrations{dir: filepath.Join(filepath.Dir(d.dir), "applications-v3", "registrations"), decode: decode}
	s.beforeWrite = func(ctx context.Context, next map[string]applicationV3Registration) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		for name, m := range marks {
			if m.Agent != nil && m.Agent.UpdateReservation != "" && !reflect.DeepEqual(s.entries[name], next[name]) {
				return errors.New("applications: connection update reserved")
			}
		}
		return nil
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	d.v3 = s
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error { return d.projectApplicationV3Locked(legacy) })
	if err != nil {
		d.v3 = nil
	}
	return err
}

func (d *applicationDirectory) projectApplicationV3Locked(legacy map[string]wire.ApplicationDescriptor) error {
	next := maps.Clone(legacy)
	for name, f := range d.v3.files {
		v, _, err := d.v3.decode(f.Raw, f.Source.Program, nil)
		if err != nil {
			return err
		}
		// Activation through @2 is explicitly fenced; these are display values.
		v.Activation = nil
		v.StartGuidance = ""
		next[name] = v
	}
	d.descriptors = next
	return nil
}

// registerApplicationV3 independently opens the native source. No request
// frame supplies a trusted manifest, publisher, provenance or file key. An
// unsigned image registers only over its own connection, pinned by its held
// SHA-256 and size, and stays pending until the person keeps it.
func (d *applicationDirectory) registerApplicationV3(ctx context.Context, subject rights.Subject, peer fileid.Peer, program, expected string) applicationV3RegistrationChange {
	out, connect := d.registerApplicationV3Held(ctx, subject, peer, program, expected)
	if connect != nil {
		// The person's Connect click in the Panel is the Keep when this image
		// is the one it installed (application_connect_expectation.go). An
		// unchanged re-registration of a waiting entry matches the same way.
		// It runs once the registration's hold on the image is released.
		connect()
	}
	return out
}

// registerApplicationV3Held is registerApplicationV3 under the hold on the
// program's image. connect, when not nil, matches an applied own
// registration against the Panel's Connect click.
func (d *applicationDirectory) registerApplicationV3Held(ctx context.Context, subject rights.Subject, peer fileid.Peer, program, expected string) (applicationV3RegistrationChange, func()) {
	if d.v3 == nil {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "manifest_owner"), nil
	}
	held, err := openSignedApplicationManifest(ctx, peer, program)
	if err != nil {
		return applicationV3Change(wire.ApplicationOutcomeForbidden, "signed_source"), nil
	}
	defer held.Close()
	program = identity.NormalizeSubjectProgram(applicationV3HeldProgram(held))
	if !heldSigned(held) && (!applicationImageUnsigned(held.Code, held.Proof) || !applicationImageDigest(held.ImageSHA256) || held.ImageSize <= 0) {
		return applicationV3Change(wire.ApplicationOutcomeForbidden, "signed_source"), nil
	}
	if identity.SharedInterpreterProgram(program, held.Code) {
		return applicationV3Change(wire.ApplicationOutcomeForbidden, "script_host"), nil
	}
	v, digest, err := d.v3.decode(held.Bytes, program, held)
	if err != nil || len(held.Bytes) > manifest.MaxApplicationManifestBytes || len(digest) != 64 || !validApplicationDescriptor(v) || v.Program != program {
		return applicationV3Change(wire.ApplicationOutcomeInvalid, "manifest"), nil
	}
	own := samePrograms(subject.Program, program)
	action, source := ActionApplicationRegisterSelf, "self"
	if !own {
		action, source = ActionApplicationManage, "operator"
		if d.operator == nil || !d.operator(subject.Program) {
			return applicationV3Change(wire.ApplicationOutcomeForbidden, "source"), nil
		}
		if !heldSigned(held) && expected == "" {
			// The person keeps an unsigned program that registered itself.
			// An operator only re-pins an entry the person kept, by its
			// revision (applicationV3Repin).
			return applicationV3Change(wire.ApplicationOutcomeForbidden, "unsigned_source"), nil
		}
	}
	if out := d.authorized(ctx, subject, action, "account"); out != wire.ApplicationOutcomeApplied {
		return applicationV3Change(out, ""), nil
	}
	if held.Recheck(ctx) != nil {
		return applicationV3Change(wire.ApplicationOutcomeForbidden, "signed_source"), nil
	}
	f := applicationV3ManifestFile{Raw: bytes.Clone(held.Bytes), Digest: digest, Source: applicationV3Source{Program: program, FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof}, Accepted: registrationTime(d.now()), Verified: registrationTime(d.now())}
	if !heldSigned(held) {
		f.Image, f.ImageSize = held.ImageSHA256, held.ImageSize
	}
	if !f.valid() {
		return applicationV3Change(wire.ApplicationOutcomeForbidden, "signed_source"), nil
	}
	live, _ := peer.(*identity.Peer)
	out := d.publishApplicationV3WithKeep(ctx, v, f, source, expected, d.panelForConnect(ctx, live, subject.Program))
	if out.Outcome != wire.ApplicationOutcomeApplied {
		return out, nil
	}
	name, revision, sha256, fileKey := v.Name, out.Revision, held.ImageSHA256, f.Source.FileKey
	if applicationV3Repin(source, f) {
		// The installing operator's re-pin of a kept entry to OA's release
		// pin records it as a kept integration (matchInstallerUpdate).
		return out, func() { d.matchInstallerUpdate(ctx, name, program, sha256, out.installerExpectation) }
	}
	if source != "self" {
		return out, nil
	}
	return out, func() { d.matchConnect(ctx, name, revision, program, sha256, fileKey) }
}

// publishApplicationV3 receives only the native reader/strict decoder's result.
// Tests use this private seam for inert persistence and crash counterexamples.
func (d *applicationDirectory) publishApplicationV3(ctx context.Context, v wire.ApplicationDescriptor, f applicationV3ManifestFile, source, expected string) applicationV3RegistrationChange {
	return d.publishApplicationV3WithKeep(ctx, v, f, source, expected, false)
}

func (d *applicationDirectory) publishApplicationV3WithKeep(ctx context.Context, v wire.ApplicationDescriptor, f applicationV3ManifestFile, source, expected string, panel bool) applicationV3RegistrationChange {
	out := d.publishApplicationV3Candidate(ctx, v, f, source, expected, panel)
	if out.Outcome != wire.ApplicationOutcomeApplied || !out.messageReserved {
		return out
	}
	if err := d.messageBlock(ctx, v.Name); err != nil {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "message_binding")
	}
	if !d.lock(ctx) {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "")
	}
	oldRevision := out.Revision
	expectedFile, _ := json.Marshal(f)
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		entry, ok := d.v3.entries[v.Name]
		if !ok || entry.Revision != oldRevision || entry.State != "removing" || (entry.Question != messageRemovalQuestion+expected && !(panel && expected == oldRevision && entry.KeepPending.FileRevision != "")) || !(entry.FileHash == applicationV3Hash(expectedFile) || applicationV3Repin(source, f) && sameApplicationV3Candidate(d.v3.files[v.Name], f)) || entry.Source != applicationV3StoredSource(source, f) {
			return errors.New("applications: message reservation changed")
		}
		pending := entry.KeepPending
		installerExpectation := ""
		if pending.FileRevision != "" {
			d.connects.mu.Lock()
			defer d.connects.mu.Unlock()
			expectation := d.connects.pending[v.Name]
			now := d.clock()
			if !panel || !pending.matches(expectation, f, now) {
				return errors.New("applications: installer expectation changed")
			}
			if _, err := d.v3.readInstallerKeepPredecessor(ctx, pending); err != nil {
				return err
			}
			installerExpectation = expectation.id
			entry.KeepIdentity = pending.Identity
			entry.KeepReceipt = newInstallerKeepReceipt(expectation, now)
			entry.KeepPending = installerKeepPending{}
		}
		entry.State, entry.Question = "enabled", ""
		if source == "self" {
			entry.State = "pending"
		}
		revision, err := applicationToken()
		if err != nil {
			return err
		}
		entry.Revision = revision
		next := maps.Clone(d.v3.entries)
		next[v.Name] = entry
		if err := d.v3.persistLocked(ctx, next); err != nil {
			return err
		}
		out.Revision = revision
		out.installerExpectation = installerExpectation
		if pending.FileRevision != "" {
			_ = os.Remove(d.v3.filePath(pending.FileRevision))
		}
		return d.projectApplicationV3Locked(legacy)
	})
	if err == nil {
		d.generations[v.Name]++
		d.notifyLocked()
	}
	d.unlock()
	if err != nil {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "message_binding")
	}
	if d.v3Changed != nil {
		d.v3Changed(v.Name, oldRevision)
	}
	if source == "self" && d.registeredSelf != nil {
		// Registration asks nothing; the person acts on the entry in Apps.
		d.registeredSelf(v.Name)
	}
	out.messageReserved = false
	return out
}

// applicationV3Repin reports an operator's registration of an unsigned image:
// the installing operator re-pinning an entry the person kept to the image it
// installed, such as Setup after an update of an unsigned preview (architect
// ruling 2026-11-10). It names the entry's revision, applies only to an
// enabled entry the app registered itself, and keeps that entry's source and
// state, so the app's own republish of the same image then updates it
// silently (sameApplicationV3Continuity). A stale revision meets conflict and
// changes nothing; a pending or disabled entry, or one an operator
// registered, meets conflict "state".
func applicationV3Repin(source string, f applicationV3ManifestFile) bool {
	return source == "operator" && !f.Source.signed()
}

// applicationV3StoredSource is the source an entry records for a publication:
// the call's source, and for a re-pin the entry's own "self".
func applicationV3StoredSource(source string, f applicationV3ManifestFile) string {
	if applicationV3Repin(source, f) {
		return "self"
	}
	return source
}

func (d *applicationDirectory) publishApplicationV3Candidate(ctx context.Context, v wire.ApplicationDescriptor, f applicationV3ManifestFile, source, expected string, panel bool) applicationV3RegistrationChange {
	repin, stored := applicationV3Repin(source, f), applicationV3StoredSource(source, f)
	if !f.valid() || len(f.Raw) == 0 || len(f.Raw) > manifest.MaxApplicationManifestBytes || !validApplicationDescriptor(v) || client.ReservedApplicationTitle(v.Title) || (source != "self" && source != "operator") || repin && expected == "" {
		return applicationV3Change(wire.ApplicationOutcomeInvalid, "manifest")
	}
	if source == "self" && connectorApplicationName(v.Name) {
		return applicationV3Change(wire.ApplicationOutcomeInvalid, "name")
	}
	if d.v3 == nil || d.v3.decode == nil {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "manifest_owner")
	}
	checked, digest, decodeErr := d.v3.decode(f.Raw, f.Source.Program, nil)
	if decodeErr != nil || checked.Name != v.Name || checked.Program != v.Program || checked.Title != v.Title || digest != f.Digest {
		return applicationV3Change(wire.ApplicationOutcomeInvalid, "manifest")
	}
	encoded, err := json.Marshal(f)
	if err != nil || len(encoded) > applicationV3ManifestFileLimit {
		return applicationV3Change(wire.ApplicationOutcomeInvalid, "manifest")
	}
	if !d.lock(ctx) {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "")
	}
	applied, published, obsolete := false, false, ""
	reservedPublish := false
	defer func() {
		d.unlock()
		if published {
			if obsolete != "" && d.v3Changed != nil {
				d.v3Changed(v.Name, obsolete)
			}
			// Registration asks nothing; the person acts on the entry in
			// Apps (AskConfirmation).
			if applied && source == "self" && !reservedPublish && d.registeredSelf != nil {
				d.registeredSelf(v.Name)
			}
		}
	}()
	if d.v3 == nil {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "manifest_owner")
	}
	out := applicationV3Change(wire.ApplicationOutcomeUnavailable, "persistence")
	err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		old, exists := d.v3.entries[v.Name]
		if exists {
			obsolete = old.Revision
		}
		if exists && old.State == "removing" {
			resumePinned := panel && repin && expected == old.Revision && old.KeepPending.FileRevision != ""
			if resumePinned && d.clock().UnixNano() >= old.KeepPending.Expires {
				out = applicationV3Change(wire.ApplicationOutcomeConflict, "installer_expectation_expired")
				return nil
			}
			if d.messageBlock != nil && (old.Question == messageRemovalQuestion+expected || resumePinned) && old.Source == stored && (old.FileHash == applicationV3Hash(encoded) || repin && sameApplicationV3Candidate(d.v3.files[v.Name], f)) {
				out = applicationV3Change(wire.ApplicationOutcomeApplied, "")
				out.Revision, out.messageReserved = old.Revision, true
				obsolete = ""
			} else {
				out = applicationV3Change(wire.ApplicationOutcomeConflict, "removing")
			}
			return nil
		}
		if _, collision := legacy[v.Name]; collision {
			out = applicationV3Change(wire.ApplicationOutcomeConflict, "name")
			return nil
		}
		if !exists && expected != "" {
			out = applicationV3Change(wire.ApplicationOutcomeUnknown, "")
			return nil
		}
		// Register is create-only. The same program registering its name
		// again from the same source updates it silently, keeping the
		// person's kept state, only when it carries the recorded signer or
		// unsigned image pin. The same path holding another identity is a
		// different program: conflict, and the entry stays as it is. Any
		// other program meets conflict below.
		current, silent := expected, false
		if exists && expected == "" && old.Source == source && d.v3.files[v.Name].Source.sameProgram(f.Source) {
			if !sameApplicationV3Continuity(d.v3.files[v.Name], f) {
				out = applicationV3Change(wire.ApplicationOutcomeConflict, "identity")
				return nil
			}
			current, silent = old.Revision, true
		}
		if exists && current != old.Revision {
			out = applicationV3Change(wire.ApplicationOutcomeConflict, "revision")
			return nil
		}
		if repin && (old.Source != "self" || old.State != "enabled") {
			out = applicationV3Change(wire.ApplicationOutcomeConflict, "state")
			return nil
		}
		if repin && d.descriptors[v.Name].Title != v.Title {
			// The person kept the app by its title; an image declaring
			// another title is no continuation (as in its review).
			out = applicationV3Change(wire.ApplicationOutcomeConflict, "identity")
			return nil
		}
		if exists && source == "self" && old.Source != "self" {
			out = applicationV3Change(wire.ApplicationOutcomeForbidden, "source")
			return nil
		}
		if exists && source == "self" && !d.v3.files[v.Name].Source.sameProgram(f.Source) {
			out = applicationV3Change(wire.ApplicationOutcomeForbidden, "program")
			return nil
		}
		if !exists && len(legacy)+len(d.v3.entries) >= 64 {
			out = applicationV3Change(wire.ApplicationOutcomeUnavailable, "capacity")
			return nil
		}
		if source == "self" {
			selfBytes, err := cas.ReadLimitContext(ctx, filepath.Join(d.dir, selfRegisteredFile), 600<<10)
			if err != nil {
				return err
			}
			marks := map[string]selfMark{}
			if selfBytes != nil && decodeApplicationSelfMarks(selfBytes, &marks) != nil || marks == nil {
				return errors.New("applications: invalid self registrations")
			}
			for name, e := range d.v3.entries {
				if name != v.Name && (e.State == "pending" || e.State == "removing") && d.v3.files[name].Source.sameProgram(f.Source) {
					out = applicationV3Change(wire.ApplicationOutcomeConflict, "unconfirmed")
					return nil
				}
			}
			for name, mark := range marks {
				if descriptor, ok := legacy[name]; ok && mark.pendingOwnership() && applicationRegistrationPrograms(mark.boundProgram(descriptor), f.Source.Program, mark.ProgramKey, f.Source.FileKey) {
					out = applicationV3Change(wire.ApplicationOutcomeConflict, "unconfirmed")
					return nil
				}
			}
		}
		revision, err := applicationToken()
		if err != nil {
			return err
		}
		b, err := json.Marshal(f)
		if err != nil || len(b) > applicationV3ManifestFileLimit {
			return errors.New("applications@3: manifest file capacity")
		}
		var installerExpectation string
		var installerLineage string
		var expectation connectExpectation
		var installerAt time.Time
		if panel && repin {
			d.connects.mu.Lock()
			defer d.connects.mu.Unlock()
			expectation = d.connects.pending[v.Name]
			installerAt = d.clock()
			if proof, ok := reserveInstallerKeep(v.Name, expectation, old, expected, f, installerAt); ok {
				if lineage, ok := proof.carry(v.Name, expectation, old, old.KeepIdentity, f, installerAt); ok {
					installerExpectation = expectation.id
					installerLineage = lineage
				}
			}
		}
		// An exact installer migration can retain the same manifest bytes
		// while committing its first Keep receipt.
		if exists && old.Source == stored && old.FileHash == applicationV3Hash(b) && (installerExpectation == "" || old.KeepReceipt.ID != "") {
			out = applicationV3Change(wire.ApplicationOutcomeApplied, "")
			out.Revision = old.Revision
			obsolete = ""
			return d.projectApplicationV3Locked(legacy)
		}
		if err := (store.BoundedFileStore{MaxBytes: applicationV3ManifestFileLimit}).WriteContext(ctx, d.v3.filePath(revision), store.Value{}, b); err != nil {
			return err
		}
		state := "enabled"
		question := ""
		if source == "self" {
			state = "pending"
		}
		if silent && (old.State == "enabled" || old.State == "disabled") {
			state = old.State
		}
		reservedPublish = d.messageBlock != nil && (!exists || !d.v3.files[v.Name].Source.sameProgram(f.Source))
		if reservedPublish {
			state, question = "removing", messageRemovalQuestion+expected
		}
		next := maps.Clone(d.v3.entries)
		entry := applicationV3Registration{Revision: revision, FileRevision: revision, FileHash: applicationV3Hash(b), Source: stored, State: state, Question: question}
		if silent && sameApplicationV3Candidate(d.v3.files[v.Name], f) {
			entry.KeepIdentity = old.KeepIdentity
			entry.KeepReceipt = old.KeepReceipt
		}
		if installerExpectation != "" {
			if reservedPublish {
				entry.KeepPending = installerKeepPending{Revision: old.Revision, FileRevision: old.FileRevision, FileHash: old.FileHash, Identity: installerLineage, Expires: expectation.expires.UnixNano()}
			} else {
				entry.KeepIdentity = installerLineage
				entry.KeepReceipt = newInstallerKeepReceipt(expectation, installerAt)
			}
		}
		next[v.Name] = entry
		if err := d.v3.persistLocked(ctx, next); err != nil {
			return err
		}
		published = true
		d.v3.files[v.Name] = f
		if err := d.projectApplicationV3Locked(legacy); err != nil {
			return err
		}
		if exists && entry.KeepPending.FileRevision == "" {
			// Publication is durable. A failed cleanup leaves inert unindexed
			// provenance; it cannot restore the old revision's authority.
			_ = os.Remove(d.v3.filePath(old.FileRevision))
		}
		out = applicationV3Change(wire.ApplicationOutcomeApplied, "")
		out.Revision = revision
		out.messageReserved = reservedPublish
		out.installerExpectation = installerExpectation
		return nil
	})
	if published {
		d.generations[v.Name]++
		d.notifyLocked()
	}
	if err != nil {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "persistence")
	}
	applied = out.Outcome == wire.ApplicationOutcomeApplied
	return out
}

func (d *applicationDirectory) applicationV3SelfEntryLocked(name string, e applicationV3Registration) (wire.ApplicationDescriptor, selfMark, bool) {
	f := d.v3.files[name]
	m := newSelfMark(f.Source.Program, asks.Publisher{Subject: f.Source.Code.Subject, Trusted: f.Source.Code.Trusted, Description: f.Source.Code.Description, OriginalName: f.Source.Code.OriginalName})
	m.Question, m.V3Revision, m.ProgramKey = e.Question, e.Revision, f.Source.FileKey
	if strings.HasPrefix(m.Question, messageRemovalQuestion) {
		m.Question = ""
	}
	m.Removing = e.State == "removing"
	return d.descriptors[name], m, (e.State == "pending" || e.State == "removing") && e.Source == "self"
}

func (d *applicationDirectory) changeApplicationV3QuestionLocked(ctx context.Context, name, program, expected, question, state string) bool {
	programKey := identity.ProgramKey(program)
	changed := false
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		e, ok := d.v3.entries[name]
		if !ok || e.State != "pending" || e.Source != "self" || !applicationRegistrationPrograms(d.v3.files[name].Source.Program, program, d.v3.files[name].Source.FileKey, programKey) || expected != "" && e.Question != expected {
			return nil
		}
		e.State, e.Question = state, question
		if state == "enabled" && e.KeepIdentity == "" {
			e.KeepIdentity = e.FileRevision
		}
		if state != "pending" {
			revision, err := applicationToken()
			if err != nil {
				return err
			}
			e.Revision = revision
		}
		next := maps.Clone(d.v3.entries)
		next[name] = e
		if err := d.v3.persistLocked(ctx, next); err != nil {
			return err
		}
		changed = true
		return d.projectApplicationV3Locked(legacy)
	})
	if err != nil {
		return false
	}
	if changed {
		d.generations[name]++
		d.notifyLocked()
	}
	return changed
}

func (d *applicationDirectory) setQuestionForRevision(ctx context.Context, name, program, question, revision string) bool {
	programKey := identity.ProgramKey(program)
	if revision == "" {
		return d.setQuestion(ctx, name, program, question)
	}
	if !d.lock(ctx) {
		return false
	}
	defer d.unlock()
	changed := false
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		if d.v3 == nil || d.v3.entries[name].Revision == "" {
			marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
			if err != nil {
				return err
			}
			v, active := legacy[name]
			m, pending := marks[name]
			if !active || !pending || !m.genericPending() || m.Removing || m.RegistrationRevision != revision || !applicationRegistrationPrograms(m.boundProgram(v), program, m.ProgramKey, programKey) {
				return nil
			}
			m.Question = question
			next := maps.Clone(marks)
			next[name] = m
			d.selfBase = base
			if err := d.persistUnconfirmedAtDescriptorLocked(ctx, next, legacy); err != nil {
				return err
			}
			changed = true
			return d.projectRegistrationsLocked(legacy)
		}
		e, ok := d.v3.entries[name]
		if !ok || e.Revision != revision || e.State != "pending" || e.Source != "self" || !applicationRegistrationPrograms(d.v3.files[name].Source.Program, program, d.v3.files[name].Source.FileKey, programKey) {
			return nil
		}
		e.Question = question
		next := maps.Clone(d.v3.entries)
		next[name] = e
		if err := d.v3.persistLocked(ctx, next); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return err == nil && changed
}

// changeApplicationV3Locked requires the current revision and an actual owner
// decision. Its callback fences mediation ownership; it never kills a process.
func (d *applicationDirectory) changeApplicationV3Locked(ctx context.Context, name, revision, program, question, state string, messageReserved bool) bool {
	programKey := identity.ProgramKey(program)
	changed := false
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		e, ok := d.v3.entries[name]
		if !ok || e.Revision != revision || e.State == "removing" && !messageReserved {
			return nil
		}
		if question != "" && (e.State != "pending" || e.Question != question || !applicationRegistrationPrograms(d.v3.files[name].Source.Program, program, d.v3.files[name].Source.FileKey, programKey)) {
			return nil
		}
		next := maps.Clone(d.v3.entries)
		if state == "remove" {
			if err := d.retireAgentUpdateMemberLocked(ctx, name, legacy); err != nil {
				return err
			}
			delete(next, name)
		} else {
			e.State, e.Question = state, ""
			revision, err := applicationToken()
			if err != nil {
				return err
			}
			e.Revision = revision
			next[name] = e
		}
		if err := d.v3.persistLocked(ctx, next); err != nil {
			return err
		}
		if state == "remove" {
			delete(d.v3.files, name)
			_ = os.Remove(d.v3.filePath(e.FileRevision))
		}
		changed = true
		return d.projectApplicationV3Locked(legacy)
	})
	if err != nil {
		return false
	}
	if changed {
		d.generations[name]++
		d.notifyLocked()
	}
	return changed
}

func (d *applicationDirectory) changeApplicationV3(ctx context.Context, subject rights.Subject, name, revision, state string) applicationV3RegistrationChange {
	if state != "enabled" && state != "disabled" && state != "remove" {
		return applicationV3Change(wire.ApplicationOutcomeInvalid, "state")
	}
	if out := d.authorized(ctx, subject, ActionApplicationManage, "account"); out != wire.ApplicationOutcomeApplied {
		return applicationV3Change(out, "")
	}
	if d.operator == nil || !d.operator(subject.Program) {
		return applicationV3Change(wire.ApplicationOutcomeForbidden, "source")
	}
	messageReserved := false
	if state == "remove" && d.messageBlock != nil {
		var err error
		messageReserved, err = d.reserveMessageV3(ctx, name, revision, "", nil)
		if err != nil {
			return applicationV3Change(wire.ApplicationOutcomeUnavailable, "message_binding")
		}
	}
	if !d.lock(ctx) {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "")
	}
	changed := false
	defer func() {
		d.unlock()
		if changed && d.v3Changed != nil {
			d.v3Changed(name, revision)
		}
	}()
	if d.v3 == nil {
		return applicationV3Change(wire.ApplicationOutcomeUnavailable, "manifest_owner")
	}
	if d.changeApplicationV3Locked(ctx, name, revision, "", "", state, messageReserved) {
		changed = true
		out := applicationV3Change(wire.ApplicationOutcomeApplied, "")
		if entry, ok := d.v3.entries[name]; ok {
			out.Revision = entry.Revision
		}
		return out
	}
	return applicationV3Change(wire.ApplicationOutcomeConflict, "revision_or_persistence")
}

// Revalidation is deliberately a fresh native read. The retained code and file
// identity are compared as provenance; the new held result owns current proof.
func (d *applicationDirectory) revalidateApplicationV3(ctx context.Context, peer fileid.Peer, name, revision string) (*heldApplicationManifest, error) {
	if !d.lock(ctx) {
		return nil, ctx.Err()
	}
	if d.v3 == nil {
		d.unlock()
		return nil, errors.New("applications@3: unavailable")
	}
	e, exists := d.v3.entries[name]
	f := d.v3.files[name]
	d.unlock()
	if !exists || e.Revision != revision || e.State != "enabled" {
		return nil, errors.New("applications@3: stale or disabled")
	}
	held, err := openSignedApplicationManifest(ctx, peer, f.Source.Program)
	if err != nil {
		return nil, err
	}
	if identity.SharedInterpreterProgram(f.Source.Program, held.Code) {
		held.Close()
		return nil, fmt.Errorf("applications@3: signed source changed")
	}
	if f.Source.signed() {
		if !heldSigned(held) || applicationV3HeldKey(held) != f.Source.FileKey || !bytes.Equal(held.Bytes, f.Raw) {
			held.Close()
			return nil, fmt.Errorf("applications@3: signed source changed")
		}
	} else {
		// An unsigned program continues only with the exact image the person
		// kept. Any other image waits for the person's review. The image
		// already under review or answered Not now is refused by its file
		// stamp, before the full read.
		if d.unsignedHeldBack != nil && d.unsignedHeldBack(name, revision, applicationHeldStamp(held)) {
			held.Close()
			return nil, errApplicationV3ImageChanged
		}
		image, size, err := d.heldPin(ctx, held)
		if err != nil {
			held.Close()
			return nil, err
		}
		if image != f.Image || size != f.ImageSize || !bytes.Equal(held.Bytes, f.Raw) {
			held.Close()
			if d.reviewUnsigned != nil {
				d.reviewUnsigned(name, revision)
			}
			return nil, errApplicationV3ImageChanged
		}
		held.Pinned = true
	}
	if !d.lock(ctx) {
		held.Close()
		return nil, ctx.Err()
	}
	current := false
	err = d.withRegistrationStorageLocked(ctx, func(map[string]wire.ApplicationDescriptor) error {
		e, ok := d.v3.entries[name]
		current = ok && e.Revision == revision && e.State == "enabled"
		return nil
	})
	d.unlock()
	if err != nil || !current {
		held.Close()
		return nil, errors.New("applications@3: registration changed during revalidation")
	}
	return held, nil
}

func legacyApplicationRemovalRevision(v wire.ApplicationDescriptor, m selfMark) (string, error) {
	b, err := json.Marshal(struct {
		Descriptor                      wire.ApplicationDescriptor
		Program, Question, Registration string
	}{v, m.boundProgram(v), m.Question, m.RegistrationRevision})
	if err != nil {
		return "", err
	}
	return applicationV3Hash(b), nil
}

func (d *applicationDirectory) readSelfMarksAtDescriptorLocked(ctx context.Context) (map[string]selfMark, store.Value, error) {
	b, err := cas.ReadLimitContext(ctx, filepath.Join(d.dir, selfRegisteredFile), 600<<10)
	if err != nil {
		return nil, store.Value{}, err
	}
	marks := map[string]selfMark{}
	if b != nil && decodeApplicationSelfMarks(b, &marks) != nil || marks == nil || len(marks) > 64 {
		return nil, store.Value{}, errors.New("applications: invalid self registrations")
	}
	for name, m := range marks {
		if !providerName.MatchString(name) || !m.valid() || m.Kind == "agent" && m.Agent.Account != d.owner {
			return nil, store.Value{}, errors.New("applications: invalid self registration")
		}
	}
	return marks, store.Value{Data: b}, nil
}

func (d *applicationDirectory) projectRegistrationsLocked(legacy map[string]wire.ApplicationDescriptor) error {
	if d.v3 != nil {
		return d.projectApplicationV3Locked(legacy)
	}
	d.descriptors = maps.Clone(legacy)
	return nil
}

// removeSelfRegistered consumes only the existing authenticated question owner.
// Both families reserve current metadata under descriptors CAS before Rights.
// An uncertain Rights result retains the reservation for authoritative replay.
func (d *applicationDirectory) removeSelfRegistered(ctx context.Context, name, program, question string, block func() error) error {
	programKey := identity.ProgramKey(program)
	if block == nil {
		return errors.New("applications: Rights owner unavailable")
	}
	if !d.lock(ctx) {
		return ctx.Err()
	}
	kind, intent, obsolete := "", "", ""
	reserved, completed := false, false
	legacyTargets := map[string]string{}
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		d.unconfirmed, d.selfBase = marks, base
		// A descriptor removed after an authoritative Block can leave its mark if
		// cleanup was interrupted. Its actual retained answer may clean it without
		// reapplying a Block that the person might subsequently have lifted.
		if m, ok := marks[name]; ok && m.Removing && m.Question == question && applicationRegistrationPrograms(m.Program, program, m.ProgramKey, programKey) {
			if _, active := legacy[name]; !active {
				next := maps.Clone(marks)
				delete(next, name)
				if err := d.persistUnconfirmedAtDescriptorLocked(ctx, next, legacy); err != nil {
					return err
				}
				completed = true
				return d.projectRegistrationsLocked(legacy)
			}
		}
		if d.v3 != nil {
			if e, exists := d.v3.entries[name]; exists {
				kind = "v3"
				if e.Source != "self" || (e.State != "pending" && e.State != "removing") || question == "" || e.Question != question || !applicationRegistrationPrograms(d.v3.files[name].Source.Program, program, d.v3.files[name].Source.FileKey, programKey) {
					return errors.New("applications@3: stale Remove answer")
				}
				if e.State == "pending" {
					obsolete = e.Revision
					revision, err := applicationToken()
					if err != nil {
						return err
					}
					e.Revision, e.State = revision, "removing"
					next := maps.Clone(d.v3.entries)
					next[name] = e
					if err := d.v3.persistLocked(ctx, next); err != nil {
						return err
					}
					reserved = true
				}
				intent = e.Revision
				return d.projectRegistrationsLocked(legacy)
			}
		}
		descriptor, active := legacy[name]
		mark, pending := marks[name]
		if !active || !pending || !mark.genericPending() || question == "" || mark.Question != question || mark.RegistrationRevision == "" || !applicationRegistrationPrograms(descriptor.Program, program, mark.ProgramKey, programKey) || !applicationRegistrationPrograms(mark.boundProgram(descriptor), program, mark.ProgramKey, programKey) {
			return errors.New("applications: stale Remove answer")
		}
		kind = "legacy"
		next := maps.Clone(marks)
		for target, m := range marks {
			v, exists := legacy[target]
			if !exists || !m.genericPending() {
				continue
			}
			// An entry kept before marks carried a file key compares by its
			// normalized name, the identity owner's missing-key rule, which
			// already reads an 8.3 alias in its long form; nothing is read
			// under CAS.
			if !applicationRegistrationPrograms(v.Program, program, m.ProgramKey, programKey) {
				continue
			}
			if m.Removing && m.Question != question {
				return errors.New("applications: another Remove intent owns the program")
			}
			if m.RegistrationRevision == "" {
				return errors.New("applications: registration revision unavailable")
			}
			if !m.Removing {
				m.Program = program
				m.Question = question
				m.Removing = true
				revision, err := legacyApplicationRemovalRevision(v, m)
				if err != nil {
					return err
				}
				m.RemovalRevision = revision
				next[target] = m
				reserved = true
			}
			revision, err := legacyApplicationRemovalRevision(v, m)
			if err != nil || revision != m.RemovalRevision {
				return errors.New("applications: Remove intent changed")
			}
			legacyTargets[target] = revision
		}
		if len(legacyTargets) == 0 {
			return errors.New("applications: current Remove target unavailable")
		}
		if reserved {
			if err := d.persistUnconfirmedAtDescriptorLocked(ctx, next, legacy); err != nil {
				return err
			}
		}
		intent = next[name].RemovalRevision
		return d.projectRegistrationsLocked(legacy)
	})
	if reserved || completed {
		d.generations[name]++
		d.notifyLocked()
	}
	d.unlock()
	if reserved && obsolete != "" && d.v3Changed != nil {
		d.v3Changed(name, obsolete)
	}
	if err != nil {
		return err
	}
	if completed {
		return nil
	}
	if d.messageBlock != nil {
		if kind == "legacy" {
			for target := range legacyTargets {
				if err := d.messageBlock(ctx, target); err != nil {
					return err
				}
			}
		} else if err := d.messageBlock(ctx, name); err != nil {
			return err
		}
	}
	// The existing Rights implementation reads an existing Block or writes one
	// with its finite CAS retry bound. An error never authorizes finalization.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := block(); err != nil {
		return err
	}
	if !d.lock(ctx) {
		return ctx.Err()
	}
	finalized := false
	if kind == "v3" {
		err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
			e, exists := d.v3.entries[name]
			if !exists || e.State != "removing" || e.Revision != intent || e.Question != question || !applicationRegistrationPrograms(d.v3.files[name].Source.Program, program, d.v3.files[name].Source.FileKey, programKey) {
				return errors.New("applications@3: Remove intent changed")
			}
			next := maps.Clone(d.v3.entries)
			delete(next, name)
			if err := d.v3.persistLocked(ctx, next); err != nil {
				return err
			}
			finalized = true
			delete(d.v3.files, name)
			_ = os.Remove(d.v3.filePath(e.FileRevision))
			return d.projectRegistrationsLocked(legacy)
		})
	} else {
		var nextLegacy map[string]wire.ApplicationDescriptor
		var nextRaw []byte
		err = cas.ChangeLimitContext(ctx, filepath.Join(d.dir, "descriptors.json"), 600<<10, func(current []byte) ([]byte, error) {
			legacy, err := decodeLegacyApplications(current)
			if err != nil {
				return nil, err
			}
			marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
			if err != nil {
				return nil, err
			}
			nextLegacy = maps.Clone(legacy)
			for target, revision := range legacyTargets {
				v, active := legacy[target]
				m, pending := marks[target]
				if !active || !pending || !m.Removing || m.Question != question || m.RemovalRevision != revision || !applicationRegistrationPrograms(m.boundProgram(v), program, m.ProgramKey, programKey) {
					return nil, errors.New("applications: Remove intent changed")
				}
				actual, e := legacyApplicationRemovalRevision(v, m)
				if e != nil || actual != revision {
					return nil, errors.New("applications: Remove intent changed")
				}
				delete(nextLegacy, target)
			}
			d.unconfirmed, d.selfBase = marks, base
			nextRaw, err = json.Marshal(nextLegacy)
			return nextRaw, err
		})
		if err == nil {
			finalized = true
			d.base = store.Value{Data: nextRaw}
			err = d.projectRegistrationsLocked(nextLegacy)
			if err == nil {
				nextMarks := maps.Clone(d.unconfirmed)
				for target := range legacyTargets {
					delete(nextMarks, target)
				}
				err = d.persistUnconfirmedLocked(ctx, nextMarks)
			}
		}
	}
	if finalized {
		d.generations[name]++
		d.notifyLocked()
	}
	d.unlock()
	if finalized && kind == "v3" && d.v3Changed != nil {
		d.v3Changed(name, intent)
	}
	return err
}

// These are copied declaration/provenance views for the native adapter. Source
// Code/Proof are retained history; live admission uses revalidateApplicationV3.
type applicationV3RegistrationView struct {
	Name, Revision, ManifestDigest, Source, State string
	KeepIdentity                                  string
	Program                                       string
	Raw                                           []byte
	Instances                                     []wire.ApplicationInstance
}

// applicationV3Snapshot reuses the directory's actual subject/read and lease
// owners. Native framing, whole-record packing and caller-filtered epoch cursor
// encoding belong to the @3 receiver. No parsed-manifest cache is introduced.
func (d *applicationDirectory) applicationV3Snapshot(ctx context.Context, subject rights.Subject, application string) ([]applicationV3RegistrationView, <-chan struct{}, time.Duration, wire.ApplicationOutcome) {
	return d.applicationV3SnapshotForAction(ctx, subject, application, ActionApplicationRead)
}

func (d *applicationDirectory) applicationV3CommandSnapshot(ctx context.Context, subject rights.Subject, application string) ([]applicationV3RegistrationView, <-chan struct{}, time.Duration, wire.ApplicationOutcome) {
	return d.applicationV3SnapshotForAction(ctx, subject, application, "abstraction.facade/command.read")
}

// applicationV3Declarations lists every kept declaration with its live
// instances for the gateway's oa_applications_describe, which decides each
// application's command.read itself (FAC-M22) and names to a kept agent the
// applications it may open (FAC-M32). Nothing here reaches a caller unfiltered.
func (d *applicationDirectory) applicationV3Declarations(ctx context.Context, subject rights.Subject) ([]applicationV3RegistrationView, <-chan struct{}, time.Duration, wire.ApplicationOutcome) {
	return d.applicationV3SnapshotWhere(ctx, subject, "", true, func(string) wire.ApplicationOutcome { return wire.ApplicationOutcomeApplied })
}

func (d *applicationDirectory) applicationV3SnapshotForAction(ctx context.Context, subject rights.Subject, application, action string) ([]applicationV3RegistrationView, <-chan struct{}, time.Duration, wire.ApplicationOutcome) {
	if action != ActionApplicationRead && action != "abstraction.facade/command.read" {
		return nil, nil, 0, wire.ApplicationOutcomeForbidden
	}
	return d.applicationV3SnapshotWhere(ctx, subject, application, false, func(name string) wire.ApplicationOutcome {
		return d.authorized(ctx, subject, action, "app:"+name)
	})
}

func (d *applicationDirectory) applicationV3SnapshotWhere(ctx context.Context, subject rights.Subject, application string, showPredecessor bool, authorized func(string) wire.ApplicationOutcome) ([]applicationV3RegistrationView, <-chan struct{}, time.Duration, wire.ApplicationOutcome) {
	if application != "" && !providerName.MatchString(application) {
		return nil, nil, 0, wire.ApplicationOutcomeInvalid
	}
	if !d.lock(ctx) {
		return nil, nil, 0, wire.ApplicationOutcomeUnavailable
	}
	defer d.unlock()
	if d.v3 == nil {
		return nil, d.changed, 0, wire.ApplicationOutcomeUnavailable
	}
	if subject.Account != d.owner || subject.Program == "" {
		return nil, d.changed, 0, wire.ApplicationOutcomeForbidden
	}
	if err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error { return d.projectApplicationV3Locked(legacy) }); err != nil {
		return nil, d.changed, 0, wire.ApplicationOutcomeUnavailable
	}
	d.expireLocked()
	until := 30 * time.Second
	views := []applicationV3RegistrationView{}
	for name, e := range d.v3.entries {
		if application != "" && name != application {
			continue
		}
		out := authorized(name)
		if out == wire.ApplicationOutcomeUnavailable {
			return nil, d.changed, until, out
		}
		if out != wire.ApplicationOutcomeApplied {
			continue
		}
		f := d.v3.files[name]
		if showPredecessor && e.KeepPending.FileRevision != "" {
			prior, err := d.v3.readInstallerKeepPredecessor(ctx, e.KeepPending)
			if err != nil {
				return nil, d.changed, until, wire.ApplicationOutcomeUnavailable
			}
			f = prior
			e.Revision = e.KeepPending.Revision
		}
		v := applicationV3RegistrationView{Name: name, Revision: e.Revision, ManifestDigest: f.Digest, Source: e.Source, State: e.State, KeepIdentity: e.KeepIdentity, Program: f.Source.Program, Raw: bytes.Clone(f.Raw)}
		var remaining time.Duration
		v.Instances, remaining = d.applicationV3InstancesLocked(name)
		if remaining < until {
			until = remaining
		}
		views = append(views, v)
	}
	if ctx.Err() != nil {
		return nil, d.changed, until, wire.ApplicationOutcomeUnavailable
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, d.changed, until, wire.ApplicationOutcomePage
}

// applicationV3InstancesLocked copies the unexpired leases of one application,
// sorted by instance, and the time until the earliest of them lapses (at most
// 30 seconds). The caller holds the directory lock and has expired leases.
func (d *applicationDirectory) applicationV3InstancesLocked(name string) ([]wire.ApplicationInstance, time.Duration) {
	until := 30 * time.Second
	instances := []wire.ApplicationInstance{}
	for _, lease := range d.instances {
		if lease.application != name {
			continue
		}
		copy := lease.value
		copy.Interfaces = append([]wire.ApplicationInterface{}, copy.Interfaces...)
		copy.Contexts = append([]wire.ApplicationContext{}, copy.Contexts...)
		instances = append(instances, copy)
		if remaining := lease.deadline.Sub(d.now()); remaining < until {
			until = remaining
		}
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Instance < instances[j].Instance })
	return instances, until
}

// applicationV3Declaration is internal FAC-M32 declaration-before-rights input
// for the gateway's single-application load (Describe of one name, Commands,
// Call, Activate, Announce). It carries the application's live instances, as
// the listing snapshot does: the gateway's Commands and Call admit only an
// instance listed here, and Describe of one name lists them. It supplies no
// response permission, live signer proof or process authority.
var applicationV3LookupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func (d *applicationDirectory) applicationV3Declaration(ctx context.Context, name string) (applicationV3RegistrationView, bool, error) {
	// Registered names are lowercase ASCII identifiers. Fold only ASCII case
	// on lookup, so titles and Unicode lookalikes cannot become aliases.
	if !applicationV3LookupName.MatchString(name) {
		return applicationV3RegistrationView{}, false, errors.New("applications@3: invalid name")
	}
	name = strings.ToLower(name)
	if !d.lock(ctx) {
		return applicationV3RegistrationView{}, false, ctx.Err()
	}
	defer d.unlock()
	if d.v3 == nil {
		return applicationV3RegistrationView{}, false, errors.New("applications@3: manifest owner unavailable")
	}
	if err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error { return d.projectRegistrationsLocked(legacy) }); err != nil {
		return applicationV3RegistrationView{}, false, err
	}
	e, exists := d.v3.entries[name]
	if !exists {
		return applicationV3RegistrationView{}, false, nil
	}
	f := d.v3.files[name]
	d.expireLocked()
	instances, _ := d.applicationV3InstancesLocked(name)
	return applicationV3RegistrationView{Name: name, Revision: e.Revision, ManifestDigest: f.Digest, Source: e.Source, State: e.State, KeepIdentity: e.KeepIdentity, Program: f.Source.Program, Raw: bytes.Clone(f.Raw), Instances: instances}, true, nil
}
