package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func reserveStart1871(d *applicationDirectory, r applicationMessageStartReservation) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reserveMessageStartLocked(context.Background(), r, func(map[string]wire.ApplicationDescriptor) error { return nil })
}

func TestApplicationMessageStartSchemaMissingDataRestartRefuses(t *testing.T) {
	for _, raw := range []string{"", "{}"} {
		t.Run(map[bool]string{true: "missing", false: "corrupt"}[raw == ""], func(t *testing.T) {
			d, _, _, _ := activationFixture(t, time.Second)
			path := filepath.Join(d.dir, applicationMessageStartsFile)
			var err error
			if raw == "" {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte(raw), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := openApplications(filepath.Dir(d.dir), d.owner, d.decide); err == nil {
				t.Fatal("recreated established quota")
			}
		})
	}
}

func TestApplicationMessageStartSchemaInterruptedInitPreservesReservations(t *testing.T) {
	d, _, app, _ := activationFixture(t, time.Second)
	r := applicationMessageStartReservation{Account: app.Account, Program: app.Program, Attempt: "a", Message: "m"}
	if ok, err := reserveStart1871(d, r); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := os.Remove(filepath.Join(d.dir, applicationMessageStartsSchemaFile)); err != nil {
		t.Fatal(err)
	}
	// A crash after the durable data write leaves the same on-disk shape.
	reopened, err := openApplications(filepath.Dir(d.dir), d.owner, d.decide)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := reserveStart1871(reopened, r); ok || err == nil {
		t.Fatal("reset interrupted initialization", ok, err)
	}
	marker, err := os.ReadFile(filepath.Join(d.dir, applicationMessageStartsSchemaFile))
	if err != nil || !validApplicationMessageStartSchema(marker) {
		t.Fatal("marker not completed", err)
	}
}

func TestApplicationMessageStartSchemaRejectsCorruptFutureAndMissingMarker(t *testing.T) {
	for _, raw := range []string{"{}", `{"version":2}`} {
		t.Run(raw, func(t *testing.T) {
			d, _, _, _ := activationFixture(t, time.Second)
			if err := os.WriteFile(filepath.Join(d.dir, applicationMessageStartsSchemaFile), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := openApplications(filepath.Dir(d.dir), d.owner, d.decide); err == nil {
				t.Fatal("accepted invalid schema")
			}
		})
	}
	d, _, app, _ := activationFixture(t, time.Second)
	if err := os.Remove(filepath.Join(d.dir, applicationMessageStartsSchemaFile)); err != nil {
		t.Fatal(err)
	}
	if ok, err := reserveStart1871(d, applicationMessageStartReservation{Account: app.Account, Program: app.Program, Attempt: "a", Message: "m"}); ok || err == nil {
		t.Fatal("admitted before schema durable", ok, err)
	}
}

func TestApplicationMessageStartSchemaFactoryResetClearsBoth(t *testing.T) {
	roots, paths := resetFixture(t)
	state := filepath.Join(roots.Local, "openabstractions", "runtime-v1")
	decide := func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil }
	d, err := openApplications(state, "account", decide)
	if err != nil {
		t.Fatal(err)
	}
	r := applicationMessageStartReservation{Account: "account", Program: filepath.Join(state, "receiver"), Attempt: "a", Message: "m"}
	if ok, err := reserveStart1871(d, r); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := moveSettings(context.Background(), paths, "message-start", os.Rename, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{applicationMessageStartsFile, applicationMessageStartsSchemaFile} {
		if _, err := os.Stat(filepath.Join(d.dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("reset retained", name, err)
		}
	}
	reopened, err := openApplications(state, "account", decide)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := reserveStart1871(reopened, r); !ok || err != nil {
		t.Fatal("reset did not initialize", ok, err)
	}
}

func TestApplicationMessageStartReservationSurvivesRestartAndHour(t *testing.T) {
	d, _, app, _ := activationFixture(t, time.Second)
	now := time.Now()
	d.now = func() time.Time { return now }
	r := applicationMessageStartReservation{Account: app.Account, Program: app.Program, Attempt: "attempt-a", Message: "message-a"}
	if ok, err := reserveStart1871(d, r); !ok || err != nil {
		t.Fatalf("first %v %v", ok, err)
	}
	state := filepath.Dir(d.dir)
	reopened, err := openApplications(state, d.owner, d.decide)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = d.now
	r.Attempt, r.Message = "attempt-b", "message-b"
	if ok, err := reserveStart1871(reopened, r); ok || err == nil {
		t.Fatalf("restart bypass %v %v", ok, err)
	}
	now = now.Add(time.Hour)
	if ok, err := reserveStart1871(reopened, r); !ok || err != nil {
		t.Fatalf("next hour %v %v", ok, err)
	}
	now = now.Add(time.Hour)
	r.Attempt, r.Message = "attempt-c", "message-c"
	if ok, err := reserveStart1871(reopened, r); !ok || err != nil {
		t.Fatalf("hourly reclamation %v %v", ok, err)
	}
}

func TestApplicationMessageStartReservationConcurrentDirections(t *testing.T) {
	d, _, app, _ := activationFixture(t, time.Second)
	var successes atomic.Int32
	var workers sync.WaitGroup
	for _, key := range []string{"sender-a", "sender-b"} {
		workers.Add(1)
		go func(key string) {
			defer workers.Done()
			ok, _ := reserveStart1871(d, applicationMessageStartReservation{Account: app.Account, Program: app.Program, Attempt: key, Message: key})
			if ok {
				successes.Add(1)
			}
		}(key)
	}
	workers.Wait()
	if successes.Load() != 1 {
		t.Fatalf("admitted %d", successes.Load())
	}
}

func TestApplicationMessageStartReservationFailsClosed(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "corrupt"}[corrupt], func(t *testing.T) {
			d, _, app, _ := activationFixture(t, time.Second)
			path := filepath.Join(d.dir, applicationMessageStartsFile)
			var err error
			if corrupt {
				err = os.WriteFile(path, []byte("{}"), 0600)
			} else {
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := reserveStart1871(d, applicationMessageStartReservation{Account: app.Account, Program: app.Program, Attempt: "a", Message: "a"}); ok || err == nil {
				t.Fatalf("admitted %v %v", ok, err)
			}
		})
	}
}

func TestApplicationRegisteredRecipeRechecksBeforeLaunch(t *testing.T) {
	d, _, _, descriptor := activationFixture(t, time.Second)
	called := false
	d.launch = func(string, []string) error { called = true; return nil }
	if err := d.launchRegisteredRecipe(context.Background(), descriptor, func(context.Context) error { return errors.New("revoked") }); err == nil || called {
		t.Fatal("launched revoked recipe")
	}
	if err := d.launchRegisteredRecipe(context.Background(), descriptor, nil); err == nil || called {
		t.Fatal("launched without authorization")
	}
}

func TestApplicationMessageStartRequiresApprovedQuestionProfile(t *testing.T) {
	d, _, _, _ := activationFixture(t, time.Second)
	if _, err := d.prepareMessageStart(context.Background(), "editor"); err == nil {
		t.Fatal("prepared without approved profile")
	}
	for _, kind := range []string{"note", "reply", ""} {
		result := d.startMessageReceiver(context.Background(), applicationMessageStartRequest{Kind: kind})
		if result.State != "refused" {
			t.Fatalf("%s: %+v", kind, result)
		}
	}
}

func messageStartFixture1871(t *testing.T) (*applicationDirectory, applicationMessageStartRequest) {
	t.Helper()
	run := agent1478Fixture(t)
	descriptor := wire.ApplicationDescriptor{Name: "receiver", Title: "Receiver", Program: run.f.answerer.Program, Activation: &wire.ApplicationActivationRecipe{Arguments: []string{"start", "--profile", "fixture", "--inbox-context", "ctx"}, Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "fixture/messages@1"}, ReadinessTimeoutMs: 100}}
	if got := run.d.register(run.ctx, run.f.panel, descriptor); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	agent1478Set(t, run, "receiver", "", true)
	q := agent1478Question(t, run, "receiver")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "receiver", "enrolled")
	run.d.messageStartProfile = func(context.Context, wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		return applicationMessageStartRequest{Session: "test", Context: "ctx", ProfileFingerprint: "fixture-approved-bounds", Validate: func(context.Context) error { return nil }}, nil
	}
	r, err := run.d.prepareMessageStart(run.ctx, "receiver")
	if err != nil {
		t.Fatal(err)
	}
	r.Kind, r.Attempt, r.Message = "question", "attempt", "canonical-question"
	// This fixture explicitly supplies an inert launcher; production uses the
	// owned-process admission and native child confirmation owner.
	run.d.launchMessageStart = func(ctx context.Context, r applicationMessageStartRequest, authority func(context.Context) error) error {
		if err := authority(ctx); err != nil {
			return err
		}
		return run.d.launchRegisteredRecipe(ctx, r.Descriptor, authority)
	}
	return run.d, r
}

func TestApplicationMessageStartExitAndAnnounceDoNotEstablishReadiness(t *testing.T) {
	d, r := messageStartFixture1871(t)
	var calls atomic.Int32
	d.launch = func(string, []string) error { calls.Add(1); return nil }
	got := d.startMessageReceiver(context.Background(), r)
	if got.State != "unknown" || calls.Load() != 1 {
		t.Fatalf("%+v calls %d", got, calls.Load())
	}
	if got := d.startMessageReceiver(context.Background(), r); got.State == "ready" || calls.Load() != 1 {
		t.Fatalf("duplicate %+v calls %d", got, calls.Load())
	}
}

func TestApplicationMessageStartRejectsChangedBindings(t *testing.T) {
	for _, mutate := range []string{"account", "program", "image", "revision", "recipe", "profile"} {
		t.Run(mutate, func(t *testing.T) {
			d, r := messageStartFixture1871(t)
			called := false
			d.launch = func(string, []string) error { called = true; return nil }
			switch mutate {
			case "account":
				r.Account = "other"
			case "program":
				r.Program = filepath.Join(t.TempDir(), "other")
			case "image":
				r.ProgramKey = "changed"
			case "revision":
				r.EnrollmentRevision = "changed"
			case "recipe":
				r.Descriptor.Activation.Arguments = []string{"wrong-folder"}
			case "profile":
				r.Validate = func(context.Context) error { return errors.New("folder/context bounds changed") }
			}
			if got := d.startMessageReceiver(context.Background(), r); got.State != "refused" || called {
				t.Fatalf("%+v launched %v", got, called)
			}
		})
	}
}

func TestApplicationMessageStartAuthorityRequiresDurableCanonicalAdmission(t *testing.T) {
	d, r := messageStartFixture1871(t)
	if err := d.recheckReservedMessageStart(context.Background(), r); err == nil {
		t.Fatal("unreserved authority")
	}
	if ok, err := reserveStart1871(d, applicationMessageStartReservation{Account: r.Account, Program: r.Program, Attempt: r.Attempt, Message: r.Message}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := d.recheckReservedMessageStart(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	wrong := r
	wrong.Message = "other-question"
	if err := d.recheckReservedMessageStart(context.Background(), wrong); err == nil {
		t.Fatal("borrowed another canonical admission")
	}
	wrong = r
	wrong.Validate = func(context.Context) error { return errors.New("question expired or pair receipt revoked") }
	if err := d.recheckReservedMessageStart(context.Background(), wrong); err == nil {
		t.Fatal("stale live authority")
	}
	if err := os.Remove(filepath.Join(d.dir, applicationMessageStartsFile)); err != nil {
		t.Fatal(err)
	}
	if err := d.recheckReservedMessageStart(context.Background(), r); err == nil {
		t.Fatal("lost durable admission remained valid")
	}
}

func TestApplicationMessageStartNeverFallsBackToLegacyLauncher(t *testing.T) {
	d, r := messageStartFixture1871(t)
	d.launchMessageStart = nil
	called := false
	d.launch = func(string, []string) error { called = true; return nil }
	if got := d.startMessageReceiver(context.Background(), r); got.State != "refused" || called {
		t.Fatalf("%+v legacy launch %v", got, called)
	}
}
