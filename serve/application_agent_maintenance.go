package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/trustedpath"
)

const agentReleaseResourceName = "OA_AGENT_RELEASE"
const agentReleaseComponent = "openabstractions-mcp"

// This producer-owned, signature-covered resource describes the common helper.
// Generated declaration records retain their canonical owners. Tools are the
// actual MCP catalogue, including schemas and annotations, rather than a second
// hand-maintained host-specific capability list. No publisher claim is input.
type agentReleaseDocument struct {
	Version   int               `json:"version"`
	Component string            `json:"component"`
	Channel   string            `json:"channel"`
	Release   [4]uint32         `json:"release"`
	Requests  []json.RawMessage `json:"requests"`
	Needs     json.RawMessage   `json:"needs"`
	Verbs     []json.RawMessage `json:"verbs"`
	Tools     []json.RawMessage `json:"tools"`
}

// Per-item canonical hashes are a bounded declaration snapshot. Subset checks
// need no second full manifest copy in each Keep and prepared batch member.
type agentDeclarationSnapshot struct {
	Requests, Files, Models, Verbs, Tools []string
}

type agentNativeRelease struct {
	Component     string
	PublisherKey  string
	Channel       string
	Release       [4]uint32
	Declaration   agentDeclarationSnapshot
	ImageKey      string
	ImageHash     string
	ImageBytes    int64
	Authenticated bool
	// Origin is empty for a signed release. An unsigned image's facts are its
	// own declaration bytes, held through one handle (architect 2026-11-11).
	Origin string `json:",omitempty"`
}

// Unsigned release-fact origins. Installed facts belong to bytes equal to
// OA's installed tools copy in an admin-only folder: the update source.
// Kept facts belong to a kept image's own bytes: the baseline an update is
// compared with, never a source. Neither origin carries a version order.
const (
	agentReleaseOriginInstalled = "installed"
	agentReleaseOriginKept      = "kept"
)

type agentKeptRelease struct {
	Question, Revision, FileKey string
	AnsweredMs                  int64
	Current                     agentNativeRelease
}

func (kept *agentKeptRelease) valid() bool {
	return kept != nil && kept.Question != "" && agentHex(kept.Revision, 24) && kept.FileKey != "" && kept.AnsweredMs > 0 && (reflect.DeepEqual(kept.Current, agentNativeRelease{}) || kept.Current.proven())
}

// Retained native person provenance is compared again in the existing update
// CAS. It is never an Asks answer and supplies no resource/responder grant.
// Legacy marks a Keep recorded before Keep facts existed (its mark has no
// KeptRelease). Derived marks Kept.Current read at Prepare from the kept
// image's own bytes; the mark itself still records none.
type agentMaintenanceCapture struct {
	Question, UpdateProof, AnsweredBy string
	AnsweredMs                        int64
	Kept                              agentKeptRelease
	CompanionDigest                   string
	Legacy, Derived                   bool `json:",omitempty"`
}

func (d *applicationDirectory) captureAgentMaintenance(ctx context.Context, name string, revision string) (*agentMaintenanceCapture, error) {
	if !d.lock(ctx) {
		return nil, ctx.Err()
	}
	var captured *agentMaintenanceCapture
	var legacy selfMark
	err := d.withRegistrationReadLocked(ctx, func(_ map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		m := marks[name]
		if m.Agent == nil || m.RegistrationRevision != revision || m.Agent.Account != d.owner || m.Agent.State != "enrolled" {
			return nil
		}
		if m.Agent.KeptRelease == nil {
			legacy = m
			return nil
		}
		kept := *m.Agent.KeptRelease
		kept.Current = *cloneAgentRelease(&kept.Current)
		captured = &agentMaintenanceCapture{Question: m.Question, UpdateProof: m.Agent.UpdateProof, AnsweredBy: m.Agent.AnsweredBy, AnsweredMs: m.Agent.AnsweredMs, Kept: kept, CompanionDigest: companionDeclarationDigest(m.Agent.Companions)}
		return nil
	})
	d.unlock()
	// A legacy Keep, or a person's answered Update, is an authentic person
	// event even when no release facts were recorded with it. It qualifies
	// once Prepare reads the kept image's facts from its own bytes.
	if err == nil && captured == nil && legacy.Agent != nil && d.agentQuestions != nil {
		r, found, readErr := d.agentQuestions.a.book.ApplicationRecord(legacy.Question)
		if readErr != nil {
			return nil, readErr
		}
		if found && agentPersonAnswer(legacy, r) {
			captured = &agentMaintenanceCapture{Question: legacy.Question, UpdateProof: legacy.Agent.UpdateProof, AnsweredBy: legacy.Agent.AnsweredBy, AnsweredMs: legacy.Agent.AnsweredMs, Kept: agentKeptRelease{Question: legacy.Question, Revision: legacy.RegistrationRevision, FileKey: legacy.ProgramKey, AnsweredMs: legacy.Agent.AnsweredMs}, CompanionDigest: companionDeclarationDigest(legacy.Agent.Companions), Legacy: true}
		}
	}
	return captured, err
}

func agentMaintenanceCurrent(m selfMark, c *agentMaintenanceCapture) bool {
	if c == nil || m.Agent == nil {
		return false
	}
	// The mark keeps what it recorded; derived facts live in the intent.
	var recorded *agentKeptRelease
	if !c.Legacy {
		kept := c.Kept
		if c.Derived {
			kept.Current = agentNativeRelease{}
		}
		recorded = &kept
	}
	return m.Question == c.Question && m.Agent.UpdateProof == c.UpdateProof && m.Agent.AnsweredBy == c.AnsweredBy && m.Agent.AnsweredMs == c.AnsweredMs && reflect.DeepEqual(m.Agent.KeptRelease, recorded) && companionDeclarationDigest(m.Agent.Companions) == c.CompanionDigest
}

// deriveKeptRelease reads the kept image's declaration from its own bytes when
// its Keep recorded no release facts. Undo restores against these facts.
func deriveKeptRelease(ctx context.Context, c *agentMaintenanceCapture, old agentUpdateImage) {
	if c == nil || !reflect.DeepEqual(c.Kept.Current, agentNativeRelease{}) {
		return
	}
	if facts, err := agentImageRelease(ctx, old.Program, old, agentReleaseOriginKept); err == nil {
		c.Kept.Current = *facts
		c.Derived = true
	}
}

// Missing authenticated legacy facts retain explicit native Update. Known
// foreign publishers/channels, invalid signatures and downgrades refuse. Equal
// ordered version with different bytes also retains fresh review.
func agentMaintenancePolicy(c *agentMaintenanceCapture, old agentUpdateImage, next applicationAgentImage, pin agentUpdateImage) (eligible, refuse bool) {
	if c == nil || !c.Kept.Current.provenFor(old) {
		return false, false
	}
	if next.Proof == identity.ProofInvalid {
		return false, true
	}
	if !next.Release.provenFor(pin) || next.Release.Origin == agentReleaseOriginKept {
		return false, false
	}
	prior, release := &c.Kept.Current, next.Release
	if prior.Origin != "" || release.Origin != "" {
		// Unsigned facts carry no version order. The source is OA's own
		// installed copy; the update applies when it declares nothing new.
		return prior.Component == release.Component && agentReleaseSubset(release.Declaration, prior.Declaration), false
	}
	if prior.Component != release.Component || prior.PublisherKey != release.PublisherKey || prior.Channel != release.Channel {
		return false, true
	}
	for i := range prior.Release {
		if release.Release[i] < prior.Release[i] {
			return false, true
		}
		if release.Release[i] > prior.Release[i] {
			return agentReleaseSubset(release.Declaration, prior.Declaration), false
		}
	}
	return false, false
}

func agentReleaseSameContent(a, b *agentNativeRelease) bool {
	if !a.proven() || !b.proven() {
		return false
	}
	x, y := *a, *b
	x.ImageKey, y.ImageKey = "", ""
	return reflect.DeepEqual(x, y)
}

// agentSourceRelease returns the update source's release facts: its signed
// release, or else the installed-copy facts of its own bytes.
func (d *applicationDirectory) agentSourceRelease(ctx context.Context, native applicationAgentImage, source agentUpdateImage) *agentNativeRelease {
	if native.Release.validFor(source) {
		return cloneAgentRelease(native.Release)
	}
	if !d.agentMaintenance || native.Proof == identity.ProofInvalid {
		return nil
	}
	return d.installedAgentRelease(ctx, source)
}

// installedAgentRelease admits an unsigned source only as OA's installed tools
// copy: the installation's own component file, in a folder whose owner and
// every folder above it admit only SYSTEM, Administrators and TrustedInstaller
// to change it. Its facts are read from the same bytes the pin hashed.
func (d *applicationDirectory) installedAgentRelease(ctx context.Context, source agentUpdateImage) *agentNativeRelease {
	if d.agentExcluded == nil {
		return nil
	}
	installed, err := d.agentExcluded(ctx, source.Program, source.Key)
	if err != nil || !installed {
		return nil
	}
	trusted := d.agentInstalledTrusted
	if trusted == nil {
		trusted = func(program string) error { return trustedpath.CheckFile(program, trustedpath.MachineOwners()) }
	}
	if trusted(source.Program) != nil {
		return nil
	}
	release, err := agentImageRelease(ctx, source.Program, source, agentReleaseOriginInstalled)
	if err != nil {
		return nil
	}
	return release
}

// agentStagedRelease gives a staged copy the installed source's facts when its
// bytes equal that source's bytes.
func agentStagedRelease(source *agentNativeRelease, stage agentUpdateImage) *agentNativeRelease {
	if source == nil || source.Origin != agentReleaseOriginInstalled || source.ImageHash != stage.Hash || source.ImageBytes != stage.Bytes {
		return nil
	}
	release := cloneAgentRelease(source)
	release.ImageKey = stage.Key
	return release
}

// agentImageRelease reads an unsigned image's declaration through one held
// handle: the bytes it parses are the bytes whose size and hash equal pin.
// An image without the release resource declares nothing.
func agentImageRelease(ctx context.Context, program string, pin agentUpdateImage, origin string) (release *agentNativeRelease, err error) {
	if pin.Bytes < 1 || pin.Bytes > 256<<20 {
		return nil, errors.New("agent release: bounded image required")
	}
	h, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, h.Close()) }()
	if h.FileID().IsDir() || h.FileID().Links() != 1 || h.FileID().Key() != pin.Key {
		return nil, errors.New("agent release: image changed")
	}
	raw, err := io.ReadAll(io.LimitReader(h.File(), pin.Bytes+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != pin.Bytes || hex.EncodeToString(sum[:]) != pin.Hash {
		return nil, errors.New("agent release: image changed")
	}
	release = &agentNativeRelease{Component: agentReleaseComponent, ImageKey: pin.Key, ImageHash: pin.Hash, ImageBytes: pin.Bytes, Origin: origin}
	resource, err := readApplicationNamedResource(bytes.NewReader(raw), int64(len(raw)), agentReleaseResourceName)
	if errors.Is(err, errApplicationManifestMissing) {
		return release, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	document, snapshot, err := agentReleaseSnapshot(resource)
	if err != nil {
		return nil, err
	}
	release.Channel, release.Release, release.Declaration = document.Channel, document.Release, snapshot
	return release, ctx.Err()
}

// agentImageFacts returns the facts an image proves now: its signed release,
// or the unsigned facts of origin read again from the pinned bytes at program.
// A forward update also matches them with the re-proven installed source.
func agentImageFacts(ctx context.Context, native applicationAgentImage, program string, pin agentUpdateImage, origin string) *agentNativeRelease {
	if native.Release.validFor(pin) {
		return native.Release
	}
	if origin != agentReleaseOriginInstalled && origin != agentReleaseOriginKept {
		return nil
	}
	release, err := agentImageRelease(ctx, program, pin, origin)
	if err != nil {
		return nil
	}
	return release
}

func (d *applicationDirectory) agentMaintenanceProof(ctx context.Context, u *applicationAgentUpdate) bool {
	if !d.agentMaintenance || !u.SourceRelease.provenFor(u.Source) || u.SourceRelease.Origin == agentReleaseOriginKept {
		return false
	}
	undo := u.Request.UndoOf != ""
	if undo && !d.agentUndoProof(ctx, u) {
		return false
	}
	read := d.agentImage
	if read == nil {
		read = readApplicationAgentImage
	}
	sourceNative, err := read(ctx, u.Source.Program, "")
	if err != nil {
		return false
	}
	sourceRelease := d.agentSourceRelease(ctx, sourceNative, u.Source)
	if !agentReleaseSameContent(sourceRelease, u.SourceRelease) {
		return false
	}
	source := applicationAgentImage{Release: sourceRelease}
	for _, c := range u.Members {
		if c.Maintenance == nil {
			return false
		}
		proof := c.Maintenance
		r, found, err := d.agentQuestions.a.book.ApplicationRecord(proof.Question)
		mark := selfMark{Question: proof.Question, Agent: &applicationAgentMark{AnsweredBy: proof.AnsweredBy, AnsweredMs: proof.AnsweredMs, UpdateProof: proof.UpdateProof}}
		if err != nil || !found || !agentPersonAnswer(mark, r) {
			return false
		}
		origin, found, err := d.agentQuestions.a.book.ApplicationRecord(proof.Kept.Question)
		// The first Keep, or a person's answered Update for a Keep recorded
		// before release facts existed, is the person event updates rest on.
		keep := origin.Key == asks.AgentEnrollmentKey && origin.Option == "keep" || origin.Key == asks.AgentUpdateKey && origin.Option == "update"
		if err != nil || !found || origin.Pending() || !origin.Yes || !keep || origin.AnsweredBy != asks.AnsweredByOperator || origin.Answered.UnixMilli() != proof.Kept.AnsweredMs {
			return false
		}
		program := c.Staged.Program
		if updateImageMatches(ctx, c.Staged, c.Old.Program) {
			program = c.Old.Program
		}
		native, err := read(ctx, program, c.Binding)
		if err != nil || c.Release == nil {
			return false
		}
		native.Release = agentImageFacts(ctx, native, program, c.Staged, c.Release.Origin)
		if !native.Release.provenFor(c.Staged) || !agentReleaseSameContent(native.Release, c.Release) {
			return false
		}
		if !undo {
			eligible, refuse := agentMaintenancePolicy(proof, c.Old, native, c.Staged)
			if !eligible || refuse || !agentReleaseSameContent(native.Release, source.Release) {
				return false
			}
		}
	}
	return true
}

func canonicalAgentJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > applicationManifestMaxBytes {
		return nil, errors.New("agent release: JSON bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("agent release: trailing JSON")
	}
	return json.Marshal(value)
}

func agentDeclarationHash(raw []byte) (string, error) {
	canonical, err := canonicalAgentJSON(raw)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}

func agentReleaseSnapshot(raw []byte) (agentReleaseDocument, agentDeclarationSnapshot, error) {
	var document agentReleaseDocument
	var snapshot agentDeclarationSnapshot
	canonical, err := canonicalAgentJSON(raw)
	if err != nil || !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		return document, snapshot, errors.New("agent release: noncanonical or ambiguous document")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return document, snapshot, err
	}
	var shape struct{ Release []uint32 }
	if json.Unmarshal(raw, &shape) != nil || len(shape.Release) != 4 {
		return document, snapshot, errors.New("agent release: four ordered version components required")
	}
	if document.Version != 1 || document.Component != agentReleaseComponent || !agentReleaseChannel(document.Channel) || document.Release == [4]uint32{} || document.Requests == nil || document.Verbs == nil || document.Tools == nil || len(document.Requests) > 128 || len(document.Verbs) > 128 || len(document.Tools) > 128 {
		return document, snapshot, errors.New("agent release: incomplete declaration")
	}
	for _, part := range document.Release {
		if part > 65535 {
			return document, snapshot, errors.New("agent release: ordered version bound")
		}
	}
	add := func(set *[]string, encoded []byte) error {
		hash, err := agentDeclarationHash(encoded)
		if err != nil || slices.Contains(*set, hash) {
			return errors.New("agent release: invalid or repeated declaration")
		}
		*set = append(*set, hash)
		return nil
	}
	for _, raw := range document.Requests {
		request, err := registration.DecodeRequestedPermission(raw)
		if err != nil {
			return document, snapshot, err
		}
		if err := add(&snapshot.Requests, registration.EncodeRequestedPermission(request)); err != nil {
			return document, snapshot, err
		}
	}
	needs, err := registration.DecodeNeeds(document.Needs)
	if err != nil {
		return document, snapshot, err
	}
	// Needs' generated encoding is projected into its existing typed members;
	// each exact requested file/model is independently retained in the snapshot.
	encodedNeeds, err := canonicalAgentJSON(registration.EncodeNeeds(needs))
	if err != nil {
		return document, snapshot, err
	}
	var members struct{ Files, Models []json.RawMessage }
	if err := json.Unmarshal(encodedNeeds, &members); err != nil {
		return document, snapshot, err
	}
	for _, group := range []struct {
		values []json.RawMessage
		set    *[]string
	}{{members.Files, &snapshot.Files}, {members.Models, &snapshot.Models}} {
		for _, raw := range group.values {
			if err := add(group.set, raw); err != nil {
				return document, snapshot, err
			}
		}
	}
	for _, raw := range document.Verbs {
		verb, err := registration.DecodeActivationVerb(raw)
		if err != nil {
			return document, snapshot, err
		}
		if err := add(&snapshot.Verbs, registration.EncodeActivationVerb(verb)); err != nil {
			return document, snapshot, err
		}
	}
	names := map[string]bool{}
	for _, raw := range document.Tools {
		var tool map[string]json.RawMessage
		if err := json.Unmarshal(raw, &tool); err != nil || tool == nil {
			return document, snapshot, errors.New("agent release: tool object required")
		}
		var name string
		if json.Unmarshal(tool["name"], &name) != nil || name == "" || len(name) > 128 || names[name] {
			return document, snapshot, errors.New("agent release: unique tool name required")
		}
		names[name] = true
		if len(tool["inputSchema"]) == 0 {
			return document, snapshot, errors.New("agent release: tool input schema required")
		}
		// Top-level display copy supplies no capability. Schemas, annotations
		// and every other tool field remain exact comparison inputs.
		delete(tool, "description")
		delete(tool, "title")
		encoded, err := json.Marshal(tool)
		if err != nil {
			return document, snapshot, err
		}
		if err := add(&snapshot.Tools, encoded); err != nil {
			return document, snapshot, err
		}
	}
	for _, set := range []*[]string{&snapshot.Requests, &snapshot.Files, &snapshot.Models, &snapshot.Verbs, &snapshot.Tools} {
		slices.Sort(*set)
	}
	return document, snapshot, nil
}

func agentReleaseChannel(channel string) bool {
	if len(channel) < 1 || len(channel) > 32 || channel[0] < 'a' || channel[0] > 'z' {
		return false
	}
	for _, c := range channel {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// validFor accepts signed release facts only: the publisher signature proves
// them. Setup and enrollment consumers rely on that.
func (release *agentNativeRelease) validFor(pin agentUpdateImage) bool {
	if release == nil || release.Origin != "" || !release.Authenticated || !strings.HasPrefix(release.PublisherKey, "authenticode-dn-sha256:") || !agentHex(strings.TrimPrefix(release.PublisherKey, "authenticode-dn-sha256:"), 32) || !agentReleaseChannel(release.Channel) || release.Release == [4]uint32{} {
		return false
	}
	return release.boundTo(pin)
}

// provenFor also accepts unsigned facts read from the pinned bytes themselves.
// Only connection maintenance compares them.
func (release *agentNativeRelease) provenFor(pin agentUpdateImage) bool {
	if release.validFor(pin) {
		return true
	}
	if release == nil || release.Authenticated || release.PublisherKey != "" || release.Origin != agentReleaseOriginInstalled && release.Origin != agentReleaseOriginKept || release.Channel != "" && !agentReleaseChannel(release.Channel) {
		return false
	}
	return release.boundTo(pin)
}

func (release *agentNativeRelease) proven() bool {
	return release != nil && release.provenFor(agentUpdateImage{Key: release.ImageKey, Hash: release.ImageHash, Bytes: release.ImageBytes})
}

func (release *agentNativeRelease) boundTo(pin agentUpdateImage) bool {
	if release.Component != agentReleaseComponent || release.ImageKey != pin.Key || release.ImageHash != pin.Hash || release.ImageBytes != pin.Bytes || pin.Bytes <= 0 || !agentHex(pin.Hash, 32) {
		return false
	}
	for _, part := range release.Release {
		if part > 65535 {
			return false
		}
	}
	for _, set := range [][]string{release.Declaration.Requests, release.Declaration.Files, release.Declaration.Models, release.Declaration.Verbs, release.Declaration.Tools} {
		if len(set) > 128 || !slices.IsSorted(set) {
			return false
		}
		for i, hash := range set {
			if !agentHex(hash, 32) || i > 0 && set[i-1] == hash {
				return false
			}
		}
	}
	return true
}

func cloneAgentRelease(value *agentNativeRelease) *agentNativeRelease {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Declaration.Requests = slices.Clone(value.Declaration.Requests)
	copy.Declaration.Files = slices.Clone(value.Declaration.Files)
	copy.Declaration.Models = slices.Clone(value.Declaration.Models)
	copy.Declaration.Verbs = slices.Clone(value.Declaration.Verbs)
	copy.Declaration.Tools = slices.Clone(value.Declaration.Tools)
	return &copy
}

func (release *agentNativeRelease) valid() bool {
	return release != nil && release.validFor(agentUpdateImage{Key: release.ImageKey, Hash: release.ImageHash, Bytes: release.ImageBytes})
}

func agentReleaseSubset(next, kept agentDeclarationSnapshot) bool {
	for _, group := range [][2][]string{{next.Requests, kept.Requests}, {next.Files, kept.Files}, {next.Models, kept.Models}, {next.Verbs, kept.Verbs}, {next.Tools, kept.Tools}} {
		for _, item := range group[0] {
			if !slices.Contains(group[1], item) {
				return false
			}
		}
	}
	return true
}
