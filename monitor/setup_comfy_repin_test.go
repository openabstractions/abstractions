package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// createdOf reads Setup's registration as the earlier tests do: whether it
// created the entry.
func createdOf(done workspaceRegistration, err error) (bool, error) {
	return done == workspaceCreated, err
}

// nativeComfyService stands in for applications@3 under the registry's
// continuity rule (serve applicationV3Repin, sameApplicationV3Continuity):
// one comfyui entry with its program, image pin, state and revision. images
// holds the image each program path holds now.
type nativeComfyService struct {
	images   map[string]string
	entry    *nativeComfyEntry
	observed string
	// questions counts the times the person would be asked: an app's own
	// first registration waits pending for Keep.
	questions int
	// keepAsks counts AskConfirmation calls that asked the person: one per
	// call on a pending entry, as the runtime asks.
	keepAsks            int
	replaces            int
	removes             int
	loseReplaceReply    bool
	loseReplaceReadback bool
	deferReplacement    bool
	// expected is the Connect click the Panel recorded (ExpectConnect), nil
	// when none waits; expects counts the calls. noExpect is a runtime that
	// does not know the call.
	expected *nativeComfyExpectation
	expects  int
	noExpect bool
	// recorded is the pin of the runtime's installer_update record, which
	// an operator re-pin matching the expectation writes (serve
	// matchInstallerUpdate); records counts them.
	recorded string
	records  int
}

type nativeComfyExpectation struct{ name, program, sha256 string }

// ExpectConnect records the Panel's Connect click, as the runtime does for
// the Panel program.
func (s *nativeComfyService) ExpectConnect(_ context.Context, name, program, sha256 string) (fwire.ApplicationChange, error) {
	if s.noExpect {
		return fwire.ApplicationChange{}, errors.New("unknown_method")
	}
	s.expects++
	s.expected = &nativeComfyExpectation{name: name, program: program, sha256: sha256}
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

// matchExpected is the runtime's match after an applied own registration
// (serve matchConnect): the waiting entry whose program and image equal the
// recorded click is kept, and the click is used up.
func (s *nativeComfyService) matchExpected() {
	e := s.expected
	if e == nil || s.entry == nil || s.entry.state != "pending" || e.name != setup.ComfyApplicationName ||
		!identity.SameSubjectProgram(e.program, s.entry.program) || e.sha256 != s.images[s.entry.program] {
		return
	}
	s.expected = nil
	s.entry.state = "enabled"
	s.entry.revision++
}

// AskConfirmation is the runtime's Keep question: asked about a pending
// entry, applied without a question for a kept one.
func (s *nativeComfyService) AskConfirmation(_ context.Context, name string) (fwire.ApplicationChange, error) {
	if s.entry == nil || name != setup.ComfyApplicationName {
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeUnknown}, nil
	}
	if s.entry.state == "pending" {
		s.keepAsks++
	}
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

type nativeComfyEntry struct {
	program, pin, state, title string
	revision                   int
}

func (s *nativeComfyService) revision() string {
	if s.entry == nil {
		return ""
	}
	return strconv.Itoa(s.entry.revision)
}

func (s *nativeComfyService) observedRevision() string { return s.observed }

func (s *nativeComfyService) Observe(context.Context, string, int64) (fwire.ApplicationPage, error) {
	page := fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}
	s.observed = s.revision()
	if s.entry != nil {
		page.Applications = []fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: setup.ComfyApplicationName, Program: s.entry.program, Title: s.entry.title},
			SelfRegistered: s.entry.state == "pending"}}
	}
	return page, nil
}

func (s *nativeComfyService) Register(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	return fwire.ApplicationChange{}, errors.New("an unsigned native app registers itself")
}

// Replace is the operator's re-pin under the observed revision.
func (s *nativeComfyService) Replace(_ context.Context, program string) (fwire.ApplicationChange, error) {
	s.replaces++
	switch {
	case s.entry == nil:
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeUnknown}, nil
	case s.observed != s.revision():
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeConflict, Reason: "revision"}, nil
	case s.entry.state != "enabled":
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeConflict, Reason: "state"}, nil
	}
	s.entry.program, s.entry.pin = program, s.images[program]
	s.entry.revision++
	if e := s.expected; e != nil {
		s.expected = nil
		if e.name == setup.ComfyApplicationName && identity.SameSubjectProgram(e.program, program) && e.sha256 == s.images[program] {
			s.recorded = e.sha256
			s.records++
		}
	}
	s.observed = s.revision()
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

func (s *nativeComfyService) replaceAndVerifyRevision(ctx context.Context, program string) (fwire.ApplicationChange, string, error) {
	if s.deferReplacement {
		return fwire.ApplicationChange{}, "", errors.New("fixture unavailable before Register")
	}
	change, err := s.Replace(ctx, program)
	if s.loseReplaceReply {
		return fwire.ApplicationChange{}, "", errors.New("the fixture lost the Register response")
	}
	if s.loseReplaceReadback {
		return change, s.observedRevision(), errors.New("the fixture lost the Observe response")
	}
	return change, s.observedRevision(), err
}

func (s *nativeComfyService) Remove(context.Context, string) (fwire.ApplicationChange, error) {
	s.removes++
	s.entry = nil
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

// Activate opens a kept entry whose program still holds its pinned image.
func (s *nativeComfyService) Activate(context.Context, string) (fwire.ApplicationActivationResult, error) {
	if s.entry == nil || s.entry.state != "enabled" || s.images[s.entry.program] != s.entry.pin {
		return fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeNotReady}, nil
	}
	return fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeReady, Started: true}, nil
}

// registerSelf is the app's own register-self from program: the same program
// carrying the entry's pin updates silently; the same program with another
// image meets conflict "identity"; a new entry waits for the person's Keep.
func (s *nativeComfyService) registerSelf(program string) func(context.Context) error {
	return func(context.Context) error {
		switch {
		case s.entry == nil:
			s.entry = &nativeComfyEntry{program: program, pin: s.images[program], state: "pending", title: setup.ComfyApplicationTitle, revision: 1}
			s.questions++
			s.matchExpected()
			return nil
		case !identity.SameSubjectProgram(s.entry.program, program):
			return errors.New("self-registration conflict: revision")
		case s.entry.pin != s.images[program]:
			return fmt.Errorf("the ComfyUI launch helper's register-self stopped (exit 2): %w", setup.ErrRegistrationIdentity)
		}
		s.entry.revision++
		return nil
	}
}

// comfyVersionProgram is the launch helper of one version in Setup's
// isolated install root.
func comfyVersionProgram(t *testing.T, version string) string {
	t.Helper()
	home, err := setup.CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home.Data, "OA", "a", "cu", version, "launch-helper.exe")
}

// ownerComfyState is the R22/R23-shaped state: the person kept ComfyUI
// 0.8.0-r17, unsigned, at cu\0.8.0-r17\launch-helper.exe, and Setup has
// installed 0.8.0-r22 into cu\0.8.0-r22.
func ownerComfyState(t *testing.T) (service *nativeComfyService, r17, r22 fwire.ApplicationDescriptor) {
	r17 = fwire.ApplicationDescriptor{Name: setup.ComfyApplicationName, Program: comfyVersionProgram(t, "0.8.0-r17"), Title: setup.ComfyApplicationTitle}
	r22 = fwire.ApplicationDescriptor{Name: setup.ComfyApplicationName, Program: comfyVersionProgram(t, "0.8.0-r22"), Title: setup.ComfyApplicationTitle}
	service = &nativeComfyService{images: map[string]string{r17.Program: "r17-image", r22.Program: "r22-image"},
		entry: &nativeComfyEntry{program: r17.Program, pin: "r17-image", state: "enabled", title: setup.ComfyApplicationTitle, revision: 7}}
	return service, r17, r22
}

// heldReceiptProgram is Setup's hold of the installed update: the program its
// receipt names, matching it.
func heldReceiptProgram(program string) func(context.Context) (setup.HeldProgram, error) {
	return func(context.Context) (setup.HeldProgram, error) { return setup.HeldProgram{Path: program}, nil }
}

func repinPanel(t *testing.T, apps setupApplications) *servicePanel {
	return &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}, appRequests: applicationRequestsFor(t, nil),
		appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
}

// The owner's update, 2026-11-10: ComfyUI 0.8.0-r17 is kept, unsigned, and
// Setup installs 0.8.0-r22 into its own versioned folder. Setup re-pins the
// entry to r22's receipt-checked helper, the helper's own registration then
// updates it silently, ComfyUI opens through OA, Activity says "Updated
// ComfyUI", and the person is asked nothing.
func TestSetupRepinsTheOwnersUnsignedComfyUpdateAcrossVersionFolders(t *testing.T) {
	fakeStartMenu(t, filepath.Join(t.TempDir(), "Abstraction Panel.exe"))
	service, r17, r22 := ownerComfyState(t)
	p := repinPanel(t, service)
	native := &workspaceNative{register: service.registerSelf(r22.Program), hold: func(context.Context) (setup.HeldProgram, error) {
		return setup.HeldProgram{Path: r22.Program, SHA256: service.images[r22.Program]}, nil
	}}
	var log []string
	done, err := officialPicture.completeWorkspace(context.Background(), service, &orderedReads{log: &log}, r22, []fwire.ApplicationDescriptor{r17}, native)
	officialPicture.settleWorkspaceRegistration(p, r22, done, err)
	if err != nil || done != workspaceUpdated {
		t.Fatalf("the update: %v %v", done, err)
	}
	if service.replaces != 1 || service.removes != 0 || service.questions != 0 {
		t.Fatalf("the update replaced=%d removed=%d asked=%d", service.replaces, service.removes, service.questions)
	}
	if service.expects != 1 || service.records != 1 || service.recorded != service.images[r22.Program] {
		t.Fatalf("Setup did not submit the held receipt pin before Replace: %d %d %q", service.expects, service.records, service.recorded)
	}
	if e := service.entry; !identity.SameSubjectProgram(e.program, r22.Program) || e.pin != "r22-image" || e.state != "enabled" {
		t.Fatalf("the entry after the update: %+v", e)
	}
	if started, err := service.Activate(context.Background(), setup.ComfyApplicationName); err != nil || !started.Started {
		t.Fatalf("ComfyUI does not open through OA: %+v %v", started, err)
	}
	lines := p.appActivity.lines()
	if len(lines) != 1 || lines[0].App != "ComfyUI" || lines[0].What != "Updated ComfyUI" || lines[0].Outcome != "Updated" {
		t.Fatalf("Activity: %+v", lines)
	}
	if rows, err := p.pendingApplications(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("Apps waits on %+v %v", rows, err)
	}
	// A second run finds the entry current and changes nothing.
	done, err = officialPicture.completeWorkspace(context.Background(), service, &orderedReads{log: &log}, r22, []fwire.ApplicationDescriptor{r17}, native)
	if err != nil || done != workspaceUnchanged || service.replaces != 1 {
		t.Fatalf("second run: %v %v replaces=%d", done, err, service.replaces)
	}
}

// Files that do not match the receipt are never re-pinned: the entry and its
// pin stay, and the Activity has no update line.
func TestSetupDoesNotRepinAProgramThatFailsItsReceipt(t *testing.T) {
	service, r17, r22 := ownerComfyState(t)
	p := repinPanel(t, service)
	native := &workspaceNative{register: service.registerSelf(r22.Program),
		hold: func(context.Context) (setup.HeldProgram, error) {
			return setup.HeldProgram{}, errors.New("the installed program does not match its package")
		}}
	var log []string
	done, err := officialPicture.registerWorkspaceEntry(context.Background(), service, &orderedReads{log: &log}, r22, []fwire.ApplicationDescriptor{r17}, native)
	officialPicture.settleWorkspaceRegistration(p, r22, done, err)
	if err == nil || done != workspaceUnchanged || service.replaces != 0 || service.removes != 0 {
		t.Fatalf("a program failing its receipt: %v %v replaces=%d removes=%d", done, err, service.replaces, service.removes)
	}
	if e := service.entry; e.program != r17.Program || e.pin != "r17-image" || e.revision != 7 {
		t.Fatalf("the entry changed: %+v", e)
	}
	if lines := p.appActivity.lines(); len(lines) != 0 {
		t.Fatalf("Activity: %+v", lines)
	}
}

// Setup re-pins only within ComfyUI's install root, and only to the program
// the receipt names.
func TestSetupRepinsOnlyInsideTheInstallRootAndToTheReceiptsProgram(t *testing.T) {
	service, r17, r22 := ownerComfyState(t)
	outside := filepath.Join(t.TempDir(), "elsewhere", "launch-helper.exe")
	native := &workspaceNative{register: service.registerSelf(r22.Program), hold: heldReceiptProgram(outside)}
	var log []string
	if _, err := officialPicture.registerWorkspaceEntry(context.Background(), service, &orderedReads{log: &log}, r22, []fwire.ApplicationDescriptor{r17}, native); err == nil || service.replaces != 0 {
		t.Fatalf("a held program the receipt does not name was re-pinned: %v replaces=%d", err, service.replaces)
	}
	// An entry outside the root is no update Setup installed: no re-pin.
	service.entry.program = outside
	native.hold = heldReceiptProgram(r22.Program)
	if repinned, err := officialPicture.retainedWorkspaceRegistration().repin(context.Background(), service, fwire.ApplicationDescriptor{Name: "comfyui", Program: outside}, r22, native); repinned || err != nil || service.replaces != 0 {
		t.Fatalf("an entry outside the install root: %v %v replaces=%d", repinned, err, service.replaces)
	}
}

// A re-pin under a stale revision meets conflict and changes nothing.
func TestSetupRepinWithAStaleRevisionChangesNothing(t *testing.T) {
	service, r17, r22 := ownerComfyState(t)
	native := &workspaceNative{register: service.registerSelf(r22.Program),
		hold: func(context.Context) (setup.HeldProgram, error) {
			// Another writer changes the entry after Setup read it.
			service.entry.revision++
			return setup.HeldProgram{Path: r22.Program}, nil
		}}
	if _, err := officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), service, r22, []fwire.ApplicationDescriptor{r17}, native); err == nil {
		t.Fatal("a stale re-pin succeeded")
	}
	if e := service.entry; e.program != r17.Program || e.pin != "r17-image" || e.revision != 8 || service.removes != 0 {
		t.Fatalf("the stale re-pin changed the entry: %+v removes=%d", e, service.removes)
	}
}

// After Setup's re-pin, an image at the new path other than the one the
// receipt pinned meets conflict "identity" on the app's own registration. It
// waits in Apps as Not connected with the title it replaces, and Connect
// re-pins only on the click.
func TestAnUnsignedImageOffTheReceiptWaitsAsNotConnectedAndConnectReplacesOnTheClick(t *testing.T) {
	service, r17, r22 := ownerComfyState(t)
	p := repinPanel(t, service)
	h := p.handler(applicationTestKey)
	native := &workspaceNative{hold: heldReceiptProgram(r22.Program)}
	register := service.registerSelf(r22.Program)
	native.register = func(ctx context.Context) error {
		// The helper's file changes between Setup's hold and its run.
		service.images[r22.Program] = "another-image"
		return register(ctx)
	}
	var log []string
	done, err := officialPicture.registerWorkspaceEntry(context.Background(), service, &orderedReads{log: &log}, r22, []fwire.ApplicationDescriptor{r17}, native)
	officialPicture.settleWorkspaceRegistration(p, r22, done, err)
	if !errors.Is(err, setup.ErrRegistrationIdentity) {
		t.Fatalf("the changed image: %v %v", done, err)
	}
	if e := service.entry; e.pin != "r22-image" {
		t.Fatalf("the conflict changed the pin: %+v", e)
	}
	rows := pendingRows(t, h)
	if len(rows) != 1 || rows[0].Name != "comfyui" || rows[0].Replaces != setup.ComfyApplicationTitle || rows[0].Program != "launch-helper" {
		t.Fatalf("the refused image does not wait in Apps: %+v", rows)
	}
	// Listing changed nothing; Connect re-pins to the image the path holds.
	if service.replaces != 1 {
		t.Fatalf("listing replaced: %d", service.replaces)
	}
	if rec := connectRow(t, h, rows[0]); rec.Code != http.StatusOK {
		t.Fatalf("Connect %d: %s", rec.Code, rec.Body.String())
	}
	if e := service.entry; e.pin != "another-image" || e.state != "enabled" || service.replaces != 2 {
		t.Fatalf("Connect did not re-pin: %+v", e)
	}
	if rows := pendingRows(t, h); len(rows) != 0 {
		t.Fatalf("still waiting after Connect: %+v", rows)
	}
}

// The Connect of a native row whose replaced entry changed since Apps showed
// it changes nothing.
func TestANativeIdentityRequestsStaleConnectChangesNothing(t *testing.T) {
	service, _, r22 := ownerComfyState(t)
	service.entry.program = r22.Program
	p := repinPanel(t, service)
	h := p.handler(applicationTestKey)
	p.recordNativeIdentityRequest("comfyui", r22.Program)
	rows := pendingRows(t, h)
	if len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	service.entry.title = "ComfyUI renamed"
	if rec := connectRow(t, h, rows[0]); rec.Code != http.StatusConflict || service.replaces != 0 {
		t.Fatalf("a stale row's Connect %d replaces=%d: %s", rec.Code, service.replaces, rec.Body.String())
	}
}

// A conflict "identity" names the entry's own path. A request naming another
// program than the entry's, stale or written by another process, ends
// unlisted, and Connect never moves the entry to another path.
func TestANativeIdentityRequestForAnotherPathIsNotListed(t *testing.T) {
	service, r17, r22 := ownerComfyState(t)
	p := repinPanel(t, service)
	h := p.handler(applicationTestKey)
	p.recordNativeIdentityRequest("comfyui", r22.Program)
	if rows := pendingRows(t, h); len(rows) != 0 {
		t.Fatalf("a request for another path waits in Apps: %+v", rows)
	}
	if _, waits := p.appRequests.nativeRefused()["comfyui"]; waits {
		t.Fatal("the request for another path was kept")
	}
	if e := service.entry; e.program != r17.Program || service.replaces != 0 {
		t.Fatalf("listing changed the entry: %+v replaces=%d", e, service.replaces)
	}
}

// An entry the person has not kept meets conflict "state": Setup falls back
// to its earlier Remove and register, and the person's Keep is asked again.
func TestSetupRepinOfAPendingEntryFallsBackToRemoveAndRegister(t *testing.T) {
	service, r17, r22 := ownerComfyState(t)
	service.entry.state = "pending"
	p := repinPanel(t, service)
	native := &workspaceNative{register: service.registerSelf(r22.Program), hold: heldReceiptProgram(r22.Program)}
	var log []string
	done, err := officialPicture.registerWorkspaceEntry(context.Background(), service, &orderedReads{log: &log}, r22, []fwire.ApplicationDescriptor{r17}, native)
	officialPicture.settleWorkspaceRegistration(p, r22, done, err)
	if err != nil || done != workspaceCreated || service.replaces != 1 || service.removes != 1 || service.questions != 1 {
		t.Fatalf("the fallback: %v %v replaces=%d removes=%d asked=%d", done, err, service.replaces, service.removes, service.questions)
	}
	if e := service.entry; !identity.SameSubjectProgram(e.program, r22.Program) || e.state != "pending" {
		t.Fatalf("the entry after the fallback: %+v", e)
	}
	if lines := p.appActivity.lines(); len(lines) != 0 {
		t.Fatalf("Activity: %+v", lines)
	}
}

func TestSetupRepinRefusedExpectationPreservesEarlierRegistration(t *testing.T) {
	service, before, after := ownerComfyState(t)
	service.noExpect = true
	native := &workspaceNative{hold: func(context.Context) (setup.HeldProgram, error) {
		return setup.HeldProgram{Path: after.Program, SHA256: service.images[after.Program]}, nil
	}}
	if repinned, err := officialPicture.retainedWorkspaceRegistration().repin(context.Background(), service, before, after, native); repinned || err == nil || service.replaces != 0 || service.entry.program != before.Program {
		t.Fatalf("a refused pin reached Replace: %v, %v, %+v", repinned, err, service.entry)
	}
}

func TestSetupRepinResumesObservedDisabledCandidate(t *testing.T) {
	_, _, candidate := ownerComfyState(t)
	pin := strings.Repeat("a", 64)
	staged := true
	var steps []string
	apps := &nativeApplications{application: setup.ComfyApplicationName, transport: func(context.Context) applications.ApplicationsTransport {
		return nativeApplicationExchange(func(raw []byte) ([]byte, error) {
			var frame struct {
				Service, Method string
				Arguments       json.RawMessage
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				return nil, err
			}
			steps = append(steps, frame.Method)
			var value []byte
			switch frame.Method {
			case "Observe":
				manifest := registrationwire.Manifest{Version: "applications@3", Name: candidate.Name, Title: candidate.Title, Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}}
				row := nativeRegistrationFixture(manifest, candidate.Program, "staged-revision", pin, nil)
				if staged {
					row.State = registrationwire.StateDisabled
				}
				value = applications.EncodePage(&applications.Page{Outcome: applications.OutcomePage, Registrations: []registrationwire.Registration{row}})
			case "ExpectConnect":
				if !bytes.Contains(frame.Arguments, []byte(pin)) {
					t.Fatalf("wrong receipt pin: %s", frame.Arguments)
				}
				value = fwire.EncodeApplicationChange(&fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied})
			case "Register":
				var args struct{ Request json.RawMessage }
				if err := json.Unmarshal(frame.Arguments, &args); err != nil {
					return nil, err
				}
				request, err := applications.DecodeRegisterRequest(args.Request)
				if err != nil || request.ExpectedRevision != "staged-revision" || !identity.SameSubjectProgram(request.Program, candidate.Program) {
					t.Fatalf("wrong staged retry: %s, %v", frame.Arguments, err)
				}
				staged = false
				value = applications.EncodeChange(&applications.Change{Outcome: applications.OutcomeApplied})
			default:
				t.Fatalf("unexpected method %s", frame.Method)
			}
			return json.Marshal(map[string]any{"version": 1, "service": frame.Service, "method": frame.Method, "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
		})
	}}
	native := &workspaceNative{hold: func(context.Context) (setup.HeldProgram, error) {
		return setup.HeldProgram{Path: candidate.Program, SHA256: pin}, nil
	}, register: func(context.Context) error { return nil }}
	got, err := officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, candidate, nil, native)
	if err != nil || got != workspaceUpdated || staged || !slices.Equal(steps, []string{"Observe", "ExpectConnect", "Register", "Observe", "Observe"}) {
		t.Fatalf("staged retry: %v, %v, calls=%v", got, err, steps)
	}
}
