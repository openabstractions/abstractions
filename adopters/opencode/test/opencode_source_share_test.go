// Black-box OpenCode harness for the logging source reader and Rights: it
// serves both over native IPC and runs the plugin's Bun fixture against them.
// Run the six-file package through the existing gateway module with GOWORK=off;
// test/README.md gives the complete command and real-process prerequisites.
package opencode_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	loggingservice "github.com/openabstractions/abstraction-logging/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstraction-rights/go/authorization"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

// This migrated OpenCode integration fixture composes the existing services at
// the facade boundary. Logging still owns native source.read. Rights controls
// the application's distinct disclosure of buffered records to its assistant.
// The resolver response is controlled test routing; both service hosts and the
// generated clients use actual native Program-bound IPC.
type sourceShareFixture struct {
	id       string
	describe atomic.Int32
	read     atomic.Int32
	endCalls atomic.Int32
	entries  []loggingservice.SourceEntry
}

func (f *sourceShareFixture) ID() string { return f.id }
func (f *sourceShareFixture) Describe(context.Context) (wire.SourceDescriptor, error) {
	f.describe.Add(1)
	return wire.SourceDescriptor{ID: f.id, Availability: wire.SourceAvailabilityAvailable,
		Limitations: []wire.SourceLimitation{wire.SourceLimitationRetention, wire.SourceLimitationNativeRedaction}}, nil
}
func (f *sourceShareFixture) CurrentCursor(context.Context) (string, error) {
	f.endCalls.Add(1)
	return "", nil
}
func (f *sourceShareFixture) Read(ctx context.Context, cursor string, _ int64, _ int64) (loggingservice.NativePage, error) {
	f.read.Add(1)
	if err := ctx.Err(); err != nil {
		return loggingservice.NativePage{}, err
	}
	if cursor != "" {
		return loggingservice.NativePage{Outcome: wire.PageOutcomePage, Next: cursor, AtEnd: true}, nil
	}
	return loggingservice.NativePage{Outcome: wire.PageOutcomePage, Entries: append([]loggingservice.SourceEntry(nil), f.entries...), Next: "fixture:1", AtEnd: true}, nil
}

type sourceShareDiscardSink struct{}

func (sourceShareDiscardSink) Write(logging.Record) error { return nil }

const sourceWatchCaptureLimit = 64 << 10

// Drain each owned child stream while retaining bounded, separate diagnostics.
// Overflow fails the fixture even if a result marker appears in the prefix.
type sourceWatchCapture struct {
	data     bytes.Buffer
	overflow bool
}

func (c *sourceWatchCapture) Write(value []byte) (int, error) {
	n := len(value)
	remaining := sourceWatchCaptureLimit - c.data.Len()
	if n > remaining {
		c.overflow = true
		value = value[:remaining]
	}
	_, _ = c.data.Write(value)
	return n, nil
}

func (c *sourceWatchCapture) Bytes() []byte { return c.data.Bytes() }

func TestSourceWatchCaptureBoundsAndDrains(t *testing.T) {
	var capture sourceWatchCapture
	input := bytes.Repeat([]byte("x"), sourceWatchCaptureLimit+17)
	if n, err := io.Copy(&capture, bytes.NewReader(input)); err != nil || n != int64(len(input)) {
		t.Fatal(n, err)
	}
	if !capture.overflow || len(capture.Bytes()) != sourceWatchCaptureLimit {
		t.Fatal(capture.overflow, len(capture.Bytes()))
	}
	if n, err := capture.Write([]byte("late result marker")); err != nil || n != 18 || len(capture.Bytes()) != sourceWatchCaptureLimit {
		t.Fatal(n, err, len(capture.Bytes()))
	}
	var exact sourceWatchCapture
	if _, err := exact.Write(input[:sourceWatchCaptureLimit]); err != nil || exact.overflow {
		t.Fatal(err, exact.overflow)
	}
}

func TestSourceWatchFixtureBunPreparation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for inert source-watch fixture controls")
	}
	provider := filepath.Dir(stageOpenCodeProvider(t))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test", "--test-name-pattern",
		"Bun fixture startup text|native source-watch fixture setup|fixture JSON diagnostics", filepath.Join(provider, "test", "source-watch.test.mjs"))
	cmd.Dir = provider
	cmd.Env = fixture.EnvironmentWithout(os.Environ(), "ABSTRACTION_IPC_NODE", "ABSTRACTION_IPC_LIBRARY", "NODE_OPTIONS", "NODE_PATH")
	var output, diagnostics sourceWatchCapture
	cmd.Stdout, cmd.Stderr = &output, &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil || output.overflow || diagnostics.overflow {
		t.Fatalf("inert source-watch Node pid=%d executable=%q exit=%d error=%v stdout_overflow=%t stderr_overflow=%t\nstdout:\n%s\nstderr:\n%s",
			cmd.Process.Pid, node, cmd.ProcessState.ExitCode(), err, output.overflow, diagnostics.overflow, output.Bytes(), diagnostics.Bytes())
	}
	t.Logf("inert Node producer/consumer controls:\n%s", output.Bytes())
}

func TestOpenCodeWatchUsesNativeIPCAndSourceRights(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation prevents the native IPC test")
	}
	bun := os.Getenv("OA_SOURCE_WATCH_BUN_EXECUTABLE")
	if bun == "" {
		var err error
		bun, err = exec.LookPath("bun")
		if err != nil {
			t.Skip("Bun is unavailable; set OA_SOURCE_WATCH_BUN_EXECUTABLE to run the cross-language fixture")
		}
	}
	library := os.Getenv("ABSTRACTION_IPC_LIBRARY")
	if library == "" || !filepath.IsAbs(library) {
		t.Skip("an absolute ABSTRACTION_IPC_LIBRARY is required for native Bun IPC")
	}
	if _, err := os.Stat(library); err != nil {
		t.Skipf("native IPC library is unavailable: %v", err)
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate source fixture test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	// The same shared adopter fixture stages the declared public JS packages.
	// The Bun script needs no private openabstractions-flat import or npm fetch.
	fixtureScript := filepath.Join(filepath.Dir(stageOpenCodeProvider(t)), "fixtures", "source-watch-ipc-fixture.mjs")
	if _, err := os.Stat(fixtureScript); err != nil {
		t.Fatalf("missing Bun fixture script: %v", err)
	}
	// Publish the exact packaged declaration after the explicit operator fills
	// its final feature application. This descriptive publication creates no rule.
	declarationFile := filepath.Join(root, "adopters", "opencode", "rights-action-description.template.json")
	declarationBytes, err := os.ReadFile(declarationFile)
	if err != nil {
		t.Fatal(err)
	}
	var declaration struct {
		Action       string             `json:"action"`
		FeatureApp   rightswire.Subject `json:"feature_app"`
		AppLabel     string             `json:"app_label"`
		Description  string             `json:"description"`
		DeclaredRisk string             `json:"declared_risk"`
	}
	decoder := json.NewDecoder(bytes.NewReader(declarationBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&declaration); err != nil {
		t.Fatal(err)
	}
	sourceID := "windows/application"
	if runtime.GOOS == "linux" {
		sourceID = "linux/system-journal"
	}

	for _, mode := range []string{"allow", "deny", "not-granted", "share-denied", "wrong-resource", "wrong-program", "revoke", "outage", "cancel", "partial-error"} {
		t.Run(mode, func(t *testing.T) {
			denied := mode == "deny"
			fixture := &sourceShareFixture{id: sourceID, entries: []loggingservice.SourceEntry{{
				Record: wire.Record{Schema: 1, Time: "2026-09-29T10:11:12.000000Z", Level: 9007199254740993, Msg: "synthetic fixture event", Attrs: map[string]string{
					"source_field": "fixture-value", "API_KEY": "synthetic-secret-field",
				}},
				Producer: &wire.ProducerClaim{Provider: "fixture-provider", Program: "fixture-writer", PID: 9007199254740995},
			}}}
			endpoint := listen.Endpoint(fmt.Sprintf("ls-opencode-watch-%d-%s", os.Getpid(), mode))
			host, err := loggingservice.Listen(endpoint, sourceShareDiscardSink{})
			if err != nil {
				t.Fatal(err)
			}
			var identityObserved atomic.Bool
			if err := host.EnableSourceReader([]loggingservice.SourceAdapter{fixture}, func(_ context.Context, peer *identity.Peer, action, resource string) error {
				if action != loggingservice.SourceReadAction || resource != loggingservice.SourceReadResource(fixture.id) {
					t.Errorf("wrong source authorization %q/%q", action, resource)
				}
				if _, err := peer.Process.AtLeast(identity.ProofKernel); err != nil {
					t.Errorf("OA caller process identity was not kernel-bound: %v", err)
				} else {
					identityObserved.Store(true)
				}
				if denied {
					return errors.New("fixture source permission denied")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			serveDone := make(chan error, 1)
			go func() { serveDone <- host.Serve(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-serveDone:
					if err != nil {
						t.Errorf("fixture host stopped with error: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Error("fixture logging host did not stop")
				}
			})
			// The explicitly configured operator is this Go test process. The
			// Bun plugin is an ordinary direct-decision subject, never an operator.
			account, err := user.Current()
			if err != nil {
				t.Fatal(err)
			}
			program, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			publisher, err := rights.NormalizeDecisionSubject(rightswire.Subject{Account: account.Uid, Program: program})
			if err != nil {
				t.Fatal(err)
			}
			subject, err := rights.NormalizeDecisionSubject(rightswire.Subject{Account: account.Uid, Program: bun})
			if err != nil {
				t.Fatal(err)
			}
			policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), []string{loggingservice.SourceReadAction})
			if err != nil {
				t.Fatal(err)
			}
			rightsEndpoint := listen.Endpoint(fmt.Sprintf("rights-opencode-watch-%d-%s", os.Getpid(), mode))
			rightsHost, err := authorization.Listen(rightsEndpoint, policy, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := rightsHost.EnableOperator(func(_ context.Context, peer *identity.Peer) error {
				got, err := rightsclient.SubjectFromPeer(peer)
				if err != nil || got != publisher {
					return authorization.ErrOperatorForbidden
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			rightsDone := make(chan error, 1)
			go func() { rightsDone <- rightsHost.Serve(ctx) }()
			t.Cleanup(func() {
				rightsHost.Close()
				select {
				case err := <-rightsDone:
					if err != nil {
						t.Errorf("fixture Rights host stopped with error: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Error("fixture Rights host did not stop")
				}
			})
			operator := rightsclient.NewOperator(rightsEndpoint)
			const shareAction = "opencode/log-source.share"
			page, err := operator.ListPolicyContext(ctx, "", 1)
			if err != nil {
				t.Fatal(err)
			}
			description := rightsclient.ActionDescription{Action: declaration.Action, FeatureApp: subject,
				AppLabel: declaration.AppLabel, Description: declaration.Description, DeclaredRisk: declaration.DeclaredRisk}
			if description.Action != shareAction {
				t.Fatalf("packaged declaration action changed: %q", description.Action)
			}
			registered, err := operator.RegisterDescribedActionContext(ctx, page.Revision, description)
			if err != nil || registered.Outcome != rightswire.ActionEditOutcomeApplied ||
				registered.Current == nil || registered.Current.Description == nil || *registered.Current.Description != description ||
				registered.Current.PublishedBy == nil || *registered.Current.PublishedBy != publisher {
				t.Fatal(registered, err)
			}
			published, err := operator.ListPolicyContext(ctx, "", 64)
			if err != nil || published.Outcome != rightswire.PolicyPageOutcomePage || !published.Complete || len(published.Rules) != 0 {
				t.Fatal("description publication created a rule", published, err)
			}
			if mode != "not-granted" {
				rule := rightswire.PolicyRule{Subject: subject, Action: shareAction, Resource: sourceID, Permit: mode != "share-denied"}
				if mode == "wrong-resource" {
					rule.Resource += "/other"
				}
				if mode == "wrong-program" {
					rule.Subject = publisher
				}
				edit, err := operator.SetRuleContext(ctx, registered.Revision, rule)
				if err != nil || edit.Outcome != rightswire.PolicyEditOutcomeApplied {
					t.Fatal(edit, err)
				}
			}
			childCtx, childCancel := context.WithTimeout(ctx, 20*time.Second)
			defer childCancel()
			cmd := exec.CommandContext(childCtx, bun, fixtureScript, endpoint, mode, sourceID)
			cmd.Dir = filepath.Join(root, "adopters", "opencode")
			cmd.Env = append(os.Environ(), "OA_SOURCE_WATCH_FIXTURE_ENDPOINT="+endpoint,
				"OA_SOURCE_WATCH_RIGHTS_ENDPOINT="+rightsEndpoint,
				"OA_SOURCE_WATCH_FEATURE_ACCOUNT="+subject.Account, "OA_SOURCE_WATCH_FEATURE_PROGRAM="+subject.Program,
				"OA_SOURCE_WATCH_PUBLISHER_ACCOUNT="+publisher.Account, "OA_SOURCE_WATCH_PUBLISHER_PROGRAM="+publisher.Program)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var output, diagnostics sourceWatchCapture
			cmd.Stderr = &diagnostics
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				line := scanner.Text()
				fmt.Fprintln(&output, line)
				if line == "OA_SOURCE_WATCH_FIXTURE_MUTATE=revoke" {
					current, err := operator.ListPolicyContext(ctx, "", 1)
					if err != nil {
						t.Error(err)
						break
					}
					edit, err := operator.RevokeRuleContext(ctx, current.Revision, subject, shareAction, sourceID)
					if err != nil || edit.Outcome != rightswire.PolicyEditOutcomeApplied {
						t.Error(edit, err)
						break
					}
					if _, err := io.WriteString(stdin, "changed\n"); err != nil {
						t.Error(err)
					}
				}
				if line == "OA_SOURCE_WATCH_FIXTURE_MUTATE=outage" {
					if err := rightsHost.Close(); err != nil {
						t.Error(err)
					}
					if _, err := io.WriteString(stdin, "changed\n"); err != nil {
						t.Error(err)
					}
				}
			}
			stdin.Close()
			if err := scanner.Err(); err != nil {
				t.Error(err)
			}
			if err := cmd.Wait(); err != nil || output.overflow || diagnostics.overflow {
				t.Fatalf("Bun source-watch mode=%s pid=%d executable=%q exit=%d error=%v stdout_overflow=%t stderr_overflow=%t\nstdout:\n%s\nstderr:\n%s",
					mode, cmd.Process.Pid, bun, cmd.ProcessState.ExitCode(), err, output.overflow, diagnostics.overflow, output.Bytes(), diagnostics.Bytes())
			}
			if !bytes.Contains(output.Bytes(), []byte("OA_SOURCE_WATCH_FIXTURE_RESULT=")) {
				t.Fatalf("Bun fixture returned no result marker: %s", output.Bytes())
			}
			if bytes.Contains(output.Bytes(), []byte("synthetic-secret-field")) {
				t.Fatal("tool output exposed a synthetic secret-bearing native attribute")
			}
			if !identityObserved.Load() {
				t.Fatal("source policy never observed a kernel-bound OA caller")
			}
			if denied {
				if fixture.describe.Load() != 0 || fixture.read.Load() != 0 || fixture.endCalls.Load() != 0 {
					t.Fatalf("denied source touched adapter: describe=%d read=%d start-at-end=%d", fixture.describe.Load(), fixture.read.Load(), fixture.endCalls.Load())
				}
			} else {
				want := int32(2)
				if mode == "not-granted" || mode == "share-denied" || mode == "wrong-resource" || mode == "wrong-program" {
					want = 1
				}
				if fixture.describe.Load() != want || fixture.read.Load() != want || fixture.endCalls.Load() != want {
					t.Fatalf("wrong count-only/share source calls: describe=%d read=%d start-at-end=%d want=%d", fixture.describe.Load(), fixture.read.Load(), fixture.endCalls.Load(), want)
				}
			}
		})
	}
}

// The failure-log share permit (A-F19) is one exact described-action rule.
// The packaged OpenCode declaration is published and the permit granted
// through the real Rights operator over native IPC; the same subject still
// holds no repair, management or log-read right.
func TestFailureLogSharePermitGrantsNoRepairAuthority(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation prevents the native IPC test")
	}
	const (
		shareAction = "opencode/log-source.share"
		history     = "oa/application-history" // failure-logs.js historyResource
	)
	// Actions a diagnosis could be followed by. Each is known to the policy,
	// so not_granted is a real refusal rather than an unknown action.
	repairs := []string{
		"abstraction.inference/host.manage",
		"abstraction.facade/provider.manage",
		"abstraction.job/acceptance.cancel",
		"abstraction.logging/history.read",
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate source fixture test")
	}
	declarationBytes, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "rights-action-description.template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var declaration struct {
		Action       string             `json:"action"`
		FeatureApp   rightswire.Subject `json:"feature_app"`
		AppLabel     string             `json:"app_label"`
		Description  string             `json:"description"`
		DeclaredRisk string             `json:"declared_risk"`
	}
	decoder := json.NewDecoder(bytes.NewReader(declarationBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&declaration); err != nil {
		t.Fatal(err)
	}
	if declaration.Action != shareAction {
		t.Fatalf("packaged declaration action changed: %q", declaration.Action)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// This test process is both the configured operator and the deciding
	// subject; the rule names exactly that subject.
	self, err := rights.NormalizeDecisionSubject(rightswire.Subject{Account: account.Uid, Program: program})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), repairs)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := listen.Endpoint(fmt.Sprintf("rights-opencode-failure-share-%d", os.Getpid()))
	host, err := authorization.Listen(endpoint, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.EnableOperator(func(_ context.Context, peer *identity.Peer) error {
		got, err := rightsclient.SubjectFromPeer(peer)
		if err != nil || got != self {
			return authorization.ErrOperatorForbidden
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() {
		host.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Rights host stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Rights host did not stop")
		}
	})
	operator := rightsclient.NewOperator(endpoint)
	page, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := operator.RegisterDescribedActionContext(ctx, page.Revision, rightsclient.ActionDescription{
		Action: declaration.Action, FeatureApp: self, AppLabel: declaration.AppLabel,
		Description: declaration.Description, DeclaredRisk: declaration.DeclaredRisk})
	if err != nil || registered.Outcome != rightswire.ActionEditOutcomeApplied {
		t.Fatal(registered, err)
	}
	edit, err := operator.SetRuleContext(ctx, registered.Revision, rightswire.PolicyRule{Subject: self, Action: shareAction, Resource: history, Permit: true})
	if err != nil || edit.Outcome != rightswire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	rules, err := operator.ListPolicyContext(ctx, "", 64)
	if err != nil || rules.Outcome != rightswire.PolicyPageOutcomePage || !rules.Complete || len(rules.Rules) != 1 {
		t.Fatal("the share permit is not exactly one rule", rules, err)
	}

	decide := rightsclient.New(endpoint)
	if got, err := decide.DecideContext(ctx, shareAction, history); err != nil || got.Outcome != rightswire.DecisionOutcomePermitted {
		t.Fatal("exact share permit not effective", got, err)
	}
	if got, err := decide.DecideContext(ctx, shareAction, "windows/application"); err != nil || got.Outcome != rightswire.DecisionOutcomeNotGranted {
		t.Fatal("share permit widened to another resource", got, err)
	}
	for _, action := range repairs {
		for _, resource := range []string{history, "lemonade"} {
			got, err := decide.DecideContext(ctx, action, resource)
			if err != nil || got.Outcome != rightswire.DecisionOutcomeNotGranted {
				t.Fatalf("share permit conferred %s on %q: %v %v", action, resource, got, err)
			}
		}
	}
}
