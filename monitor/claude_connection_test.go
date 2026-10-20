package main

import (
	"context"
	"encoding/json"
	"errors"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestClaudeConnectionsBackgroundLayout(t *testing.T) {
	if strings.Contains(claudeConnectionsPage, "claude-startup-settings") || strings.Contains(claudeConnectionsPage, claudeStartupScript) {
		t.Fatal("unapproved startup presentation is exposed by the connection page")
	}
	if !strings.Contains(claudeConnectionsPage, `id="agent-receiving"`) || !strings.Contains(claudeConnectionsPage, `id="agent-reset"`) {
		t.Fatal("shared receiving controls are missing")
	}
	if output := os.Getenv("OA_CLAUDE_LAYOUT_FIXTURE_OUTPUT"); output != "" {
		r := httptest.NewRequest("GET", "http://127.0.0.1/claude-connections?platform=windows&shell=native", nil)
		// The actual page and styles are rendered inertly: no receiver script,
		// native selection, connection request or owner state is evaluated.
		fragment := strings.Split(claudeConnectionsPage, "<script>")[0]
		doc := panelPage(r, "fixture", "claude-connections", "Claude Code connections", fragment)
		if err := os.WriteFile(output, []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

type claudePanelFixtureImage struct {
	closed       bool
	program, key string
}

func (f *claudePanelFixtureImage) Program() string {
	if f.program != "" {
		return f.program
	}
	return "fixture-gateway"
}
func (f *claudePanelFixtureImage) FileKey() string {
	if f.key != "" {
		return f.key
	}
	return "fixture-key"
}
func (f *claudePanelFixtureImage) Check(context.Context) error {
	if f.closed {
		return errors.New("closed")
	}
	return nil
}
func (f *claudePanelFixtureImage) Close() error { f.closed = true; return nil }

type claudePanelFixtureCLI struct{ claudePanelFixtureImage }

func (*claudePanelFixtureCLI) Version() string { return "2.1.308" }

func TestClaudeConnectionsPanelProjectUsesHeldImageAndConditionalKeep(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	project, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	image := &claudePanelFixtureImage{}
	prepared := 0
	enrolled := 0
	commands := 0
	var selected []*claudePanelFixtureCLI
	c := &claudePanelConnections{projectRequired: true, project: project, selection: 1, state: func() (string, error) { return path, nil }, prepare: func(context.Context) (claudePanelImage, error) {
		prepared++
		image = &claudePanelFixtureImage{}
		return image, nil
	}, enroll: func(ctx context.Context, got claudePanelImage) (string, error) {
		if got != image || image.closed {
			t.Fatal("different or closed image")
		}
		enrolled++
		return "enrolled", nil
	}, cli: func(context.Context) (claudePanelCLI, error) {
		cli := &claudePanelFixtureCLI{}
		selected = append(selected, cli)
		return cli, nil
	}, command: func(ctx context.Context, program string, args ...string) ([]byte, error) {
		commands++
		if image.closed || program != image.Program() {
			t.Fatal("helper was not held through the command")
		}
		want := []string{"plugin", "claude", "install", "--project", path, "--state-dir", path, "--claude", "fixture-gateway"}
		if !samePanelArgs(args, want) {
			t.Fatalf("command=%q", args)
		}
		return []byte(`{"outcome":"saved"}`), nil
	}}
	c.admit = func(_ context.Context, old claudePanelImage) (claudePanelImage, error) {
		image = &claudePanelFixtureImage{}
		return image, nil
	}
	c.register = c.saveProject
	for range 2 {
		result, err := c.connect(ctx, "1")
		if err != nil || result.Outcome != "saved" || result.Enrollment != "enrolled" {
			t.Fatalf("saved=%+v err=%v", result, err)
		}
	}
	if prepared != 2 || enrolled != 2 || commands != 2 || !image.closed || c.heldImage != nil {
		t.Fatalf("prepared=%d enrolled=%d commands=%d closed=%v", prepared, enrolled, commands, image.closed)
	}
	for _, cli := range selected {
		if !cli.closed {
			t.Fatal("CLI hold outlived the joined operation")
		}
	}
	c.close()
	if !image.closed {
		t.Fatal("window close retained native image hold")
	}
}

func TestClaudeConnectionsPanelRefusedNativeEnrollmentNeverRunsVendor(t *testing.T) {
	image := &claudePanelFixtureImage{}
	called := false
	c := &claudePanelConnections{prepare: func(context.Context) (claudePanelImage, error) { return image, nil }, enroll: func(context.Context, claudePanelImage) (string, error) { return "unknown", errors.New("signed_source") }, cli: func(context.Context) (claudePanelCLI, error) { called = true; return nil, nil }}
	if _, err := c.saveProject(context.Background(), fileid.FileID{}); err == nil || called {
		t.Fatal("native refusal reached CLI")
	}
	c.close()
}

type claudeEnrollmentFixture struct {
	read func() (applications.AgentEnrollmentResult, error)
	set  func(applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error)
}

func TestClaudeEnrollmentContinuationCancelsBlockingPoll(t *testing.T) {
	outer := context.Background()
	polled := false
	client := claudeContextEnrollmentClient{read: func(ctx context.Context, _ string) (applications.AgentEnrollmentResult, error) {
		polled = true
		if ctx == outer {
			t.Fatal("poll retained outer context")
		}
		<-ctx.Done()
		return applications.AgentEnrollmentResult{}, ctx.Err()
	}}
	err := waitClaudeEnrollment(outer, &claudePanelFixtureImage{}, client, applications.AgentEnrollment{}, 10*time.Millisecond)
	if !polled || !errors.Is(err, context.DeadlineExceeded) || outer.Err() != nil {
		t.Fatal(polled, err)
	}
}

func TestClaudeConnectionsSingleAddContinuesAfterSameKeep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	root := t.TempDir()
	program := filepath.Join(root, "oa-claude-integration.exe")
	if err := os.WriteFile(program, []byte("inert native image fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	image := &claudePanelFixtureImage{program: program, key: id.Key()}
	registered, requested := false, false
	registers, sets, commands := 0, 0, 0
	pendingRead, kept, cliCalled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	entry := applications.AgentEnrollment{Application: claudeIntegrationApplication, Program: program, Revision: "same-native-capture", State: applications.AgentEnrollmentStateNone}
	client := claudeEnrollmentFixture{read: func() (applications.AgentEnrollmentResult, error) {
		if !registered {
			return applications.AgentEnrollmentResult{Outcome: applications.OutcomeUnknown}, nil
		}
		current := entry
		if requested {
			current.State = applications.AgentEnrollmentStatePending
			select {
			case <-kept:
				current.State = applications.AgentEnrollmentStateEnrolled
			default:
			}
			select {
			case <-pendingRead:
			default:
				close(pendingRead)
			}
		}
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &current}, nil
	}, set: func(r applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
		if r.Application != entry.Application || r.ExpectedRevision != entry.Revision || !r.Enabled {
			t.Error("Keep request lost its native capture")
		}
		sets++
		requested = true
		pending := entry
		pending.State = applications.AgentEnrollmentStatePending
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomeApplied, Enrollment: &pending}, nil
	}}
	c := &claudePanelConnections{state: func() (string, error) { return root, nil }, prepare: func(context.Context) (claudePanelImage, error) { return image, nil }, cli: func(context.Context) (claudePanelCLI, error) { close(cliCalled); return &claudePanelFixtureCLI{}, nil }, command: func(_ context.Context, p string, args ...string) ([]byte, error) {
		commands++
		want := []string{"connect", "claude", "--state-dir", root, "--claude", "fixture-gateway"}
		if commands == 2 {
			want = []string{"watch-permission", "claude", "status", "--state-dir", root, "--claude", "fixture-gateway"}
		}
		if commands == 3 {
			want = []string{"plugin", "claude", "install", "--scope", "user", "--state-dir", root, "--claude", "fixture-gateway"}
		}
		if p != program || !reflect.DeepEqual(args, want) {
			t.Error("single Add left user scope", p, args)
		}
		if commands == 2 {
			return []byte(`{"outcome":"page","state":"unreviewed"}`), nil
		}
		return nil, nil
	}}
	c.enroll = func(ctx context.Context, img claudePanelImage) (string, error) {
		return requestClaudeEnrollmentWithWait(ctx, img, client, func(_ context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
			registers++
			registered = true
			return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
		}, true)
	}
	c.admit = func(_ context.Context, img claudePanelImage) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: img.Program(), key: img.FileKey()}, nil
	}
	c.register = c.saveProject
	defer c.close()
	type answer struct {
		result claudeConnectionSaved
		err    error
	}
	c.userMod = true
	done := make(chan answer, 1)
	go func() { r, err := c.connect(ctx, ""); done <- answer{r, err} }()
	select {
	case <-pendingRead:
	case <-ctx.Done():
		t.Fatal("Add did not wait for native Keep")
	}
	select {
	case <-cliCalled:
		t.Fatal("vendor setup ran before Keep")
	default:
	}
	select {
	case r := <-done:
		t.Fatal("single Add returned before Keep", r)
	default:
	}
	close(kept)
	select {
	case r := <-done:
		if r.err != nil || r.result.Outcome != "saved" || r.result.Enrollment != "enrolled" || r.result.Companion != "configured" || registers != 1 || sets != 1 || commands != 3 {
			t.Fatal(r, registers, sets, commands)
		}
	case <-ctx.Done():
		t.Fatal("same kept capture did not continue original Add")
	}
}

func (f claudeEnrollmentFixture) ReadAgentEnrollment(_ context.Context, name string) (applications.AgentEnrollmentResult, error) {
	if name != claudeIntegrationApplication {
		return applications.AgentEnrollmentResult{}, errors.New("wrong application")
	}
	return f.read()
}
func (f claudeEnrollmentFixture) SetAgentEnrollment(r applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
	return f.set(r)
}

func TestClaudeConnectionsPanelNativeRegistrationReadbackAndPersonKeep(t *testing.T) {
	ctx := context.Background()
	program := filepath.Join(t.TempDir(), "oa-claude-integration.exe")
	if err := os.WriteFile(program, []byte("inert image"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	image := &claudePanelFixtureImage{program: program, key: id.Key()}
	registered := false
	sets := 0
	entry := applications.AgentEnrollment{Application: claudeIntegrationApplication, Program: program, Revision: "observed-revision", State: applications.AgentEnrollmentStateNone}
	client := claudeEnrollmentFixture{read: func() (applications.AgentEnrollmentResult, error) {
		if !registered {
			return applications.AgentEnrollmentResult{Outcome: applications.OutcomeUnknown}, nil
		}
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &entry}, nil
	}, set: func(r applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
		sets++
		if r.Application != claudeIntegrationApplication || r.ExpectedRevision != entry.Revision || !r.Enabled {
			t.Fatalf("conditional request=%+v", r)
		}
		pending := entry
		pending.State = applications.AgentEnrollmentStatePending
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomeApplied, Enrollment: &pending}, nil
	}}
	register := func(_ context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
		if d.Name != claudeIntegrationApplication || d.Program != program || d.Activation != nil {
			t.Fatalf("message-agent descriptor=%+v", d)
		}
		registered = true
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
	}
	state, err := requestClaudeEnrollment(ctx, image, client, register)
	if err != nil || state != "pending" || sets != 1 {
		t.Fatalf("state=%s sets=%d err=%v", state, sets, err)
	}
	entry.State = applications.AgentEnrollmentStatePending
	if state, err := requestClaudeEnrollment(ctx, image, client, register); err != nil || state != "pending" || sets != 2 {
		t.Fatal("pending review was not re-requested", state, err, sets)
	}
	entry.State = applications.AgentEnrollmentStateNone
	if state, err := requestClaudeEnrollment(ctx, image, client, register); err != nil || state != "pending" || sets != 3 {
		t.Fatal("declined review was not re-requested", state, err, sets)
	}
	entry.Program = filepath.Join(t.TempDir(), "foreign.exe")
	if _, err := requestClaudeEnrollment(ctx, image, client, register); err == nil || sets != 3 {
		t.Fatal("foreign readback reached Keep mutation")
	}
}

func TestClaudeConnectionsPanelClearsFailedInventoryAndClosesHold(t *testing.T) {
	project := t.TempDir()
	program := filepath.Join(project, "gateway.exe")
	row := claudeSavedConnection{Project: project, ProjectKey: "project-key", Program: program, ProgramKey: "program-key", Name: "openabstractions", Registration: "saved"}
	good, _ := json.Marshal(map[string]any{"outcome": "page", "connections": []claudeSavedConnection{row}})
	raw := good
	var hold *claudePanelFixtureImage
	c := &claudePanelConnections{state: func() (string, error) { return project, nil }, image: func(context.Context) (claudePanelImage, error) { hold = &claudePanelFixtureImage{}; return hold, nil }, command: func(context.Context, string, ...string) ([]byte, error) { return raw, nil }}
	view, err := c.read(context.Background())
	if err != nil || view.Outcome != "page" || len(view.Connections) != 1 || !hold.closed {
		t.Fatalf("valid view=%+v err=%v closed=%v", view, err, hold.closed)
	}
	raw = []byte(`{"outcome":"page","connections":null}`)
	view, err = c.read(context.Background())
	if err == nil || view.Outcome != "unavailable" || len(c.rows) != 0 || !hold.closed {
		t.Fatalf("failed view retained facts: %+v %v", view, err)
	}
}

func TestClaudeConnectionsPanelSelectionCancelsLateSetup(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	project, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	joined := make(chan error, 1)
	c := &claudePanelConnections{projectRequired: true, project: project, selection: 3, register: func(ctx context.Context, got fileid.FileID) (claudeConnectionSaved, error) {
		if !got.Equal(project) {
			t.Error("changed selection")
		}
		close(entered)
		<-ctx.Done()
		return claudeConnectionSaved{}, ctx.Err()
	}}
	go func() { _, err := c.connect(ctx, "3"); joined <- err }()
	<-entered
	c.cancel()
	if err := <-joined; !errors.Is(err, context.Canceled) {
		t.Fatalf("late setup=%v", err)
	}
	if !c.project.IsZero() {
		t.Fatal("cancel retained a project")
	}
	called := false
	c.register = func(context.Context, fileid.FileID) (claudeConnectionSaved, error) {
		called = true
		return claudeConnectionSaved{}, nil
	}
	if _, err := c.connect(ctx, strconv.FormatUint(c.selection, 10)); err == nil || called {
		t.Fatal("missing selection reached registration")
	}
}

func TestClaudeConnectionsPanelPickerDiscardsEarlierSelection(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	path := t.TempDir()
	c := &claudePanelConnections{picker: func(context.Context) (string, error) { close(entered); <-release; return path, nil }}
	done := make(chan error, 1)
	go func() { _, _, err := c.chooseProject(context.Background()); done <- err }()
	<-entered
	c.cancel()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("late picker result was accepted")
	}
	if !c.project.IsZero() {
		t.Fatal("late picker replaced selection")
	}
}

func TestClaudeConnectionsPanelDisconnectUsesCurrentCapturedRow(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	project, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	image := &claudePanelFixtureImage{}
	management := &claudePanelFixtureImage{program: "installed-management", key: "manager-key"}
	calls := 0
	cli := &claudePanelFixtureCLI{}
	c := &claudePanelConnections{revision: 4, heldImage: image, image: func(context.Context) (claudePanelImage, error) { return management, nil }, rows: []claudeSavedConnection{{Project: path, ProjectKey: project.Key(), Program: image.Program(), ProgramKey: image.FileKey(), Name: "openabstractions", Registration: "saved"}}, cli: func(context.Context) (claudePanelCLI, error) { return cli, nil }, state: func() (string, error) { return path, nil }, command: func(_ context.Context, program string, args ...string) ([]byte, error) {
		calls++
		if program != management.Program() || args[0] != "disconnect" || args[len(args)-1] != image.Program() {
			t.Fatalf("disconnect=%s %q", program, args)
		}
		return nil, nil
	}}
	request := claudeDisconnectRequest{Revision: "3", ProjectKey: project.Key(), ProgramKey: image.FileKey(), Name: "openabstractions"}
	if err := c.disconnect(ctx, request); err == nil || calls != 0 {
		t.Fatal("stale row reached the vendor")
	}
	request.Revision = "4"
	if err := c.disconnect(ctx, request); err != nil || calls != 1 || !cli.closed || image.closed {
		t.Fatalf("disconnect calls=%d err=%v", calls, err)
	}
	c.close()
}

func TestClaudeConnectionsPanelPluginRemovalUsesPluginOwner(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	project, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	image := &claudePanelFixtureImage{}
	cli := &claudePanelFixtureCLI{}
	row := claudeSavedConnection{Project: path, ProjectKey: project.Key(), Program: image.Program(), ProgramKey: image.FileKey(), Name: "openabstractions@oa-local-fixture", Kind: "plugin", Registration: "saved"}
	calls := 0
	c := &claudePanelConnections{revision: 1, rows: []claudeSavedConnection{row}, image: func(context.Context) (claudePanelImage, error) { return image, nil }, cli: func(context.Context) (claudePanelCLI, error) { return cli, nil }, state: func() (string, error) { return path, nil }, command: func(_ context.Context, program string, args ...string) ([]byte, error) {
		calls++
		want := []string{"plugin", "claude", "remove", "--project", path, "--state-dir", path, "--claude", cli.Program(), "--registered-program", image.Program()}
		if program != image.Program() || !samePanelArgs(args, want) {
			t.Fatal(program, args)
		}
		return nil, nil
	}}
	if err := c.disconnect(ctx, claudeDisconnectRequest{Revision: "1", ProjectKey: row.ProjectKey, ProgramKey: row.ProgramKey, Name: row.Name, Kind: row.Kind}); err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
}

func TestClaudeConnectionsPanelPluginUpdateUsesHeldNativeImage(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	project, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	image := &claudePanelFixtureImage{program: "admitted-helper", key: "new-key"}
	cli := &claudePanelFixtureCLI{}
	calls := 0
	c := &claudePanelConnections{cli: func(context.Context) (claudePanelCLI, error) { return cli, nil }, state: func() (string, error) { return path, nil }, command: func(ctx context.Context, program string, args ...string) ([]byte, error) {
		calls++
		want := []string{"plugin", "claude", "update", "--project", path, "--state-dir", path, "--claude", cli.Program()}
		if program != image.Program() || !samePanelArgs(args, want) || image.Check(ctx) != nil || cli.Check(ctx) != nil {
			t.Fatal(program, args)
		}
		return nil, nil
	}}
	row := claudeSavedConnection{Project: path, ProjectKey: project.Key()}
	if err := c.updatePlugin(ctx, row, image); err != nil || calls != 1 || !cli.closed || image.closed {
		t.Fatal(calls, err)
	}
	row.ProjectKey = "foreign"
	if err := c.updatePlugin(ctx, row, image); err == nil || calls != 1 {
		t.Fatal("changed project reached plugin owner", err)
	}
}

func TestClaudeConnectionsPanelCompletedSetupAfterRevocationUsesManager(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	project, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	nativeState := "enrolled"
	image := &claudePanelFixtureImage{}
	manager := &claudePanelFixtureImage{program: "installed-management", key: "manager-key"}
	connected, removed := 0, 0
	c := &claudePanelConnections{revision: 1, prepare: func(context.Context) (claudePanelImage, error) { return image, nil }, enroll: func(context.Context, claudePanelImage) (string, error) { return nativeState, nil }, image: func(context.Context) (claudePanelImage, error) { return manager, nil }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, state: func() (string, error) { return path, nil }, command: func(_ context.Context, program string, args ...string) ([]byte, error) {
		if args[0] == "connect" {
			if !reflect.DeepEqual(args, []string{"connect", "claude", "--state-dir", path, "--claude", "fixture-gateway"}) {
				t.Fatal("basic setup required a project or plugin", args)
			}
			if nativeState != "enrolled" || program != image.Program() {
				t.Fatal("bad initial connection")
			}
			connected++
		} else {
			if nativeState != "none" || program != manager.Program() {
				t.Fatal("revoked retained image executed")
			}
			removed++
		}
		return nil, nil
	}}
	c.admit = func(_ context.Context, old claudePanelImage) (claudePanelImage, error) {
		image = &claudePanelFixtureImage{program: old.Program(), key: old.FileKey()}
		return image, nil
	}
	c.register = c.saveProject
	if _, err := c.connect(ctx, ""); err != nil {
		t.Fatal(err)
	}
	nativeState = "none"
	if !image.closed || c.heldImage != nil {
		t.Fatal("completed setup kept the helper locked")
	}
	c.rows = []claudeSavedConnection{{Project: path, ProjectKey: project.Key(), Program: image.Program(), ProgramKey: image.FileKey(), Name: "OpenAbstractions", Kind: "user", Registration: "saved"}}
	if err := c.disconnect(ctx, claudeDisconnectRequest{Revision: "1", ProjectKey: project.Key(), ProgramKey: image.FileKey(), Name: "OpenAbstractions", Kind: "user"}); err != nil || connected != 1 || removed != 1 {
		t.Fatalf("connect=%d remove=%d err=%v", connected, removed, err)
	}
	c.close()
}

func TestClaudeConnectionsPanelRevokedRemovalUsesInstalledManagementOnly(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	project, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(path, "deleted-integration.exe")
	management := &claudePanelFixtureImage{program: "installed-management", key: "management-key"}
	cli := &claudePanelFixtureCLI{}
	calls := 0
	c := &claudePanelConnections{revision: 2, rows: []claudeSavedConnection{{Project: path, ProjectKey: project.Key(), Program: target, Name: "openabstractions", Registration: "saved"}}, image: func(context.Context) (claudePanelImage, error) { return management, nil }, cli: func(context.Context) (claudePanelCLI, error) { return cli, nil }, state: func() (string, error) { return path, nil }, command: func(_ context.Context, program string, args ...string) ([]byte, error) {
		calls++
		if program != management.Program() || args[len(args)-2] != "--registered-program" || args[len(args)-1] != target {
			t.Fatalf("removal executed target: %s %q", program, args)
		}
		return nil, nil
	}}
	if err := c.disconnect(ctx, claudeDisconnectRequest{Revision: "2", ProjectKey: project.Key(), Name: "openabstractions"}); err != nil || calls != 1 || !management.closed || !cli.closed {
		t.Fatalf("removal calls=%d err=%v", calls, err)
	}
}

func TestClaudeConnectionsPanelPageSuppressesLateReplies(t *testing.T) {
	testAgentConnectionController(t, "claude", claudeConnectionsPage)
}

func TestClaudeConnectionsPanelFirstRunRealInventoryCommand(t *testing.T) {
	program := os.Getenv("OA_CLAUDE_INVENTORY_TEST_GATEWAY")
	if program == "" {
		t.Skip("focused receiving supplies the compiled gateway")
	}
	state := filepath.Join(t.TempDir(), "OpenAbstractions", "setup", "claude-connections")
	c := &claudePanelConnections{image: func(context.Context) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: program}, nil
	}, cli: func(context.Context) (claudePanelCLI, error) { return nil, errors.New("no verified configuration CLI") }, state: func() (string, error) { return state, nil }, picker: func(context.Context) (string, error) { return "", nil }, register: func(context.Context, fileid.FileID) (claudeConnectionSaved, error) {
		t.Fatal("read performed setup")
		return claudeConnectionSaved{}, nil
	}, command: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, executable, args...).Output()
	}}
	view, err := c.read(context.Background())
	if err != nil || view.Outcome != "page" || view.Connections == nil || len(view.Connections) != 0 || !view.PickerAvailable || view.CLIAvailable || view.CLIReason == "" {
		t.Fatal(view, err)
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("read created state", err)
	}
}

func TestClaudeConnectionsPanelSameNameScopesChooseExactRemoval(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project, err := fileid.Resolve(ctx, fileid.Self, root, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	rows := []claudeSavedConnection{{Project: root, ProjectKey: project.Key(), Program: filepath.Join(root, "helper.exe"), ProgramKey: "same-image", Name: "OpenAbstractions", Kind: "user", Registration: "saved"}, {Project: root, ProjectKey: project.Key(), Program: filepath.Join(root, "helper.exe"), ProgramKey: "same-image", Name: "OpenAbstractions", Registration: "saved"}}
	for _, kind := range []string{"", "user"} {
		calls := 0
		c := &claudePanelConnections{revision: 1, state: func() (string, error) { return root, nil }, image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }}
		c.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "connections" {
				raw, err := json.Marshal(map[string]any{"outcome": "page", "connections": rows})
				return raw, err
			}
			calls++
			hasProject := false
			for _, arg := range args {
				if arg == "--project" {
					hasProject = true
				}
			}
			if hasProject != (kind == "") {
				t.Fatal("wrong scope selected", kind, args)
			}
			return nil, nil
		}
		view, err := c.read(ctx)
		if err != nil || len(view.Connections) != 2 {
			t.Fatal("scope collision rejected", view, err)
		}
		if err := c.disconnect(ctx, claudeDisconnectRequest{Revision: view.Revision, ProjectKey: project.Key(), ProgramKey: "same-image", Name: "OpenAbstractions", Kind: kind}); err != nil || calls != 1 {
			t.Fatal(err, calls)
		}
	}
}

func TestClaudeConnectionsCurrencyUsesCapturedSavedImage(t *testing.T) {
	root := t.TempDir()
	rows := []claudeSavedConnection{{Project: root, ProjectKey: "project", Program: filepath.Join(root, "helper.exe"), ProgramKey: "image", Name: "OpenAbstractions", Kind: "user", Registration: "saved", HelperState: "current"}}
	c := &claudePanelConnections{state: func() (string, error) { return root, nil }, image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }}
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		return json.Marshal(map[string]any{"outcome": "page", "connections": rows})
	}
	view, err := c.read(context.Background())
	if err != nil || view.Connections[0].HelperState != "unknown" {
		t.Fatal(view, err)
	}
	calls := 0
	c.helperState = func(_ context.Context, program, key string) agentHelperReading {
		calls++
		if program != rows[0].Program || key != rows[0].ProgramKey {
			t.Fatal(program, key)
		}
		return agentHelperReading{State: "update"}
	}
	view, err = c.read(context.Background())
	if err != nil || calls != 1 || view.Connections[0].HelperState != "update" {
		t.Fatal(view, calls, err)
	}
	rows[0].Registration = "changed"
	view, err = c.read(context.Background())
	if err != nil || calls != 1 || view.Connections[0].HelperState != "unknown" {
		t.Fatal(view, calls, err)
	}
}
