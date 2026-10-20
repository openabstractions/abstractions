//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

func TestCatalogueHostPrivateRegistration(t *testing.T) {
	ctx := t.Context()
	state, localData := t.TempDir(), t.TempDir()
	d := registration1459Directory(t, state)
	official, _ := product.OfficialAgentProgram("opencode")
	program := filepath.Join(localData, filepath.FromSlash(official.LocalAppData))
	other := filepath.Join(localData, "other.exe")
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{program, other} {
		if err := os.WriteFile(path, []byte("fixture image"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The proof seam substitutes only the platform signer verdict. File
	// custody, owner write, retained index, and read projections are real.
	signer, selected := official.Signer, program
	open := func(ctx context.Context, peer fileid.Peer, path string) (*heldApplicationManifest, error) {
		if path != program {
			t.Fatalf("catalogue owner requested a different image: %q", path)
		}
		held, err := openHeldApplicationImage(ctx, peer, selected)
		if err == nil {
			held.Code = identity.Code{Subject: signer, Trusted: true}
			held.Proof = identity.ProofBound
		}
		return held, err
	}
	if _, _, err := holdCatalogueHost(ctx, fileid.Self, "opencode", localData, openRegistryProgramImage); err == nil {
		t.Fatal("native image owner accepted an unsigned fixture")
	}
	pin, image, err := holdCatalogueHost(ctx, fileid.Self, "opencode", localData, open)
	if err != nil {
		t.Fatal(err)
	}
	pin.Close()
	ref, err := applicationToken()
	if err != nil {
		t.Fatal(err)
	}
	record := catalogueHostRecord{Reference: ref, Source: catalogueHostSource, Image: image}
	revision := "0123456789abcdef0123456789abcdef0123456789abcdef"
	value := catalogueEnrollmentCompanion{Version: 1, App: catalogueAgentApplication,
		Revision: revision, Question: "test-question", Image: record.Image.FileKey, Host: record}
	if err := writeCatalogueCompanion(ctx, state, value, selfMark{}); err != nil {
		t.Fatal(err)
	}
	mark := selfMark{Kind: "agent", RegistrationRevision: revision, Question: value.Question,
		ProgramKey: value.Image, Agent: &applicationAgentMark{State: "pending"}}
	checkCompanion := func(want catalogueHostRecord) {
		t.Helper()
		if got, err := readCatalogueCompanion(ctx, state, value.App, mark); err != nil || got != want {
			t.Fatalf("companion got %+v, want %+v: %v", got, want, err)
		}
	}
	checkCompanion(catalogueHostRecord{})
	pending := value
	pending.Question = ""
	if err := writeCatalogueCompanion(ctx, state, pending, selfMark{}); err != nil {
		t.Fatal(err)
	}
	checkCompanion(catalogueHostRecord{})
	if err := writeCatalogueCompanion(ctx, state, value, selfMark{}); err != nil {
		t.Fatal(err)
	}
	if err := writeCatalogueCompanion(ctx, state, value, selfMark{}); err != nil {
		t.Fatal(err)
	}
	checkCompanion(catalogueHostRecord{})
	mark.Agent.State, mark.Agent.AnsweredBy = "enrolled", "operator"
	checkCompanion(record)
	successor := value
	successor.Revision = "abcdef0123456789abcdef0123456789abcdef0123456789"
	successor.Image += "-updated"
	if err := writeCatalogueCompanion(ctx, state, successor, mark); err != nil {
		t.Fatal(err)
	}
	if err := writeCatalogueCompanion(ctx, state, successor, mark); err != nil {
		t.Fatal(err)
	}
	checkCompanion(record) // crash before the mark CAS
	mark.RegistrationRevision, mark.ProgramKey = successor.Revision, successor.Image
	checkCompanion(record) // crash after the mark CAS
	mark.Removing = true
	checkCompanion(catalogueHostRecord{}) // Remove reserves the same revision.
	mark.Removing = false
	checkCompanion(record)
	mark.RegistrationRevision = "different-enrollment"
	checkCompanion(catalogueHostRecord{})
	mark = selfMark{}
	checkCompanion(catalogueHostRecord{})
	proof, err := holdKeptCatalogueHost(ctx, fileid.Self, "opencode", localData, record.Reference, record, open)
	if err != nil {
		t.Fatal(err)
	}
	expected := keptCatalogueEffectIdentity{record.Reference, "enrollment", "target"}
	for _, current := range []keptCatalogueEffectIdentity{
		{"replacement-host", expected.enrollment, expected.target},
		{expected.host, "", expected.target},
		{expected.host, expected.enrollment, "replacement-target"},
		expected,
	} {
		called := false
		err := withKeptCatalogueEffect(ctx, proof, expected, func(context.Context) (keptCatalogueEffectIdentity, error) {
			return current, nil
		}, func() error {
			called = true
			return proof.Recheck(ctx)
		})
		if called != (current == expected) || (err == nil) != called {
			t.Fatalf("effect with current identity %+v called=%v err=%v", current, called, err)
		}
	}
	if err := proof.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := holdKeptCatalogueHost(ctx, fileid.Self, "opencode", localData, "", record, open); err == nil {
		t.Fatal("removed enrollment reference admitted")
	}
	replaced := record
	replaced.Image.FileKey += "-different"
	if _, err := holdKeptCatalogueHost(ctx, fileid.Self, "opencode", localData, record.Reference, replaced, open); err == nil {
		t.Fatal("different held file admitted")
	}
	signer = "another publisher"
	if _, _, err := holdCatalogueHost(ctx, fileid.Self, "opencode", localData, open); err == nil {
		t.Fatal("wrong publisher accepted")
	}
	signer, selected = official.Signer, other
	if _, _, err := holdCatalogueHost(ctx, fileid.Self, "opencode", localData, open); err == nil {
		t.Fatal("wrong path accepted")
	}
	selected = program
	if err := os.Rename(program, program+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("another fixture image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := holdKeptCatalogueHost(ctx, fileid.Self, "opencode", localData, record.Reference, record, open); err == nil {
		t.Fatal("replacement inherited the prior reference")
	}
	if _, found, err := d.applicationV3Declaration(ctx, "opencode"); found || err != nil {
		t.Fatalf("private host acquired a live declaration: %v, %v", found, err)
	}
	rows, _, _, outcome := d.applicationV3Declarations(ctx, rights.Subject{Account: d.owner, Program: program})
	if outcome != wire.ApplicationOutcomePage || len(rows) != 0 {
		t.Fatalf("private host escaped declaration projection: %v, %+v", outcome, rows)
	}
}

func TestCatalogueHostKeepRequestRefusesConnectorOutsideSetup(t *testing.T) {
	run := newSelfRegisteredRun(t)
	agent1478Attach(t, run)
	program := filepath.Join(t.TempDir(), "oa-opencode-integration.exe")
	if err := os.WriteFile(program, []byte("inert connector"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := run.d.register(run.ctx, run.f.panel, wire.ApplicationDescriptor{Name: catalogueAgentApplication, Title: "OpenCode", Program: program}); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("register fixture:", out)
	}
	run.d.connectPanel = func(program string) bool { return program == run.f.panel.Program }
	bind := true
	out := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: catalogueAgentApplication, Enabled: true, BindCatalogueHost: &bind})
	if out.Outcome != applications.OutcomeUnavailable || out.Reason != "catalogue_host" {
		t.Fatalf("untrusted connector Keep: %+v", out)
	}
}

func TestCatalogueHostMaintenanceRequiresInstalledSource(t *testing.T) {
	hash := strings.Repeat("a", 64)
	source := agentUpdateImage{Key: "installed", Hash: hash, Bytes: 7}
	stage := agentUpdateImage{Key: "staged", Hash: hash, Bytes: 7}
	release := &agentNativeRelease{Component: agentReleaseComponent, ImageKey: source.Key,
		ImageHash: hash, ImageBytes: 7, Channel: "stable", Origin: agentReleaseOriginInstalled}
	u := &applicationAgentUpdate{Maintenance: true, Source: source, SourceRelease: release}
	capture := agentUpdateCapture{Application: catalogueAgentApplication, Staged: stage, Release: agentStagedRelease(release, stage)}
	if !catalogueMaintenanceSource(u, capture) {
		t.Fatal("installed source rejected")
	}
	signed := *release
	signed.Origin, signed.Authenticated = "", true
	signed.PublisherKey = "authenticode-dn-sha256:" + hash
	signed.Release = [4]uint32{0, 8, 0, 1}
	stagedSigned := signed
	stagedSigned.ImageKey = stage.Key
	u.SourceRelease, capture.Release = &signed, &stagedSigned
	if !signed.provenFor(source) || !stagedSigned.provenFor(stage) || catalogueMaintenanceSource(u, capture) {
		t.Fatal("signed channel was treated as the installed-tools source")
	}
}
