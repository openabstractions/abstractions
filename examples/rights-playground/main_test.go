package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

func TestTypedUseRequiresExplicitRouteAndFreshIntent(t *testing.T) {
	f, _ := fixtureFor("alpha")
	j := &completedJobs{body: f.artifact}
	var output bytes.Buffer
	intentPath := filepath.Join(t.TempDir(), "alpha.json")
	for range 2 {
		if err := submitUse(context.Background(), j, "http://127.0.0.1:1234", f, useGuarantees(false), intentPath, &output); err != nil {
			t.Fatal(err)
		}
	}
	if len(j.submitted) != 2 || j.submitted[0].Identity.Key == j.submitted[1].Identity.Key {
		t.Fatal("a new operation must not replay completed work after a permission edit")
	}
	for _, submission := range j.submitted {
		if !slices.Contains(submission.RequiredGuarantees, typedCredentialGuarantee) {
			t.Fatal("typed Use must select credentials@2 at admission")
		}
		for _, old := range request.CredentialGuarantees {
			if slices.Contains(submission.RequiredGuarantees, old) {
				t.Fatal("typed Use must not accept the legacy credential route")
			}
		}
		if bytes.Contains(submission.Spec, f.secret) || strings.Contains(output.String(), string(f.secret)) {
			t.Fatal("application submission/progress disclosed dummy credential bytes")
		}
	}
	if !slices.Contains(useGuarantees(true), request.CredentialGuarantees[0]) {
		t.Fatal("explicit legacy development command lost its separate selection")
	}
}

func TestLostSubmitUnknownReconcilesSameRetainedIntent(t *testing.T) {
	f, _ := fixtureFor("alpha")
	j := &uncertainJobs{completedJobs: completedJobs{body: f.artifact}}
	path := filepath.Join(t.TempDir(), "pending.json")
	var output bytes.Buffer
	err := submitUse(context.Background(), j, "http://127.0.0.1:1234", f, useGuarantees(false), path, &output)
	if !errors.Is(err, errAcceptanceUncertain) || strings.Contains(err.Error(), "refused") {
		t.Fatal("unknown after a lost Submit must preserve uncertainty")
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, f.secret) {
		t.Fatal("pending identity must survive without credential bytes")
	}
	if err := submitUse(context.Background(), j, "http://127.0.0.1:1234", f, useGuarantees(false), path, &output); err != nil {
		t.Fatal(err)
	}
	if len(j.submitted) != 1 || len(j.reconciled) != 2 || j.reconciled[0] != j.submitted[0].Identity || j.reconciled[1] != j.submitted[0].Identity {
		t.Fatal("recovery must reconcile the exact original identity without a fresh submission")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("known completion must clear pending identity before a future new operation")
	}
}

func TestLostSubmitReconcileRefusalKeepsUncertainty(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome api.AcceptanceOutcome
	}{{"forbidden", api.AcceptanceOutcomeForbidden}, {"invalid", api.AcceptanceOutcomeInvalid}} {
		t.Run(test.name, func(t *testing.T) {
			f, _ := fixtureFor("alpha")
			j := &reconcileRefusedJobs{uncertainJobs: uncertainJobs{completedJobs: completedJobs{body: f.artifact}}, firstOutcome: test.outcome}
			path := filepath.Join(t.TempDir(), "pending.json")
			err := submitUse(context.Background(), j, "http://127.0.0.1:1234", f, useGuarantees(false), path, io.Discard)
			if !errors.Is(err, errAcceptanceUncertain) {
				t.Fatal("a refused read after a lost submission cannot prove nonacceptance")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("the original identity must survive a refused reconciliation")
			}
			if err := submitUse(context.Background(), j, "http://127.0.0.1:1234", f, useGuarantees(false), path, io.Discard); err != nil {
				t.Fatal(err)
			}
			if len(j.submitted) != 1 || len(j.reconciled) != 2 || j.reconciled[0] != j.submitted[0].Identity || j.reconciled[1] != j.submitted[0].Identity {
				t.Fatal("a later accepted reconciliation must use the original identity without another Submit")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("observed completion must clear the retained identity")
			}
		})
	}
}

type reconcileRefusedJobs struct {
	uncertainJobs
	firstOutcome api.AcceptanceOutcome
}

func (j *reconcileRefusedJobs) Reconcile(_ context.Context, id api.RequestIdentity) (api.AcceptanceResult, error) {
	j.reconciled = append(j.reconciled, id)
	if len(j.reconciled) == 1 {
		return api.AcceptanceResult{Outcome: j.firstOutcome}, nil
	}
	return api.AcceptanceResult{Outcome: api.AcceptanceOutcomeAccepted, Receipt: &api.Receipt{Identity: id, OperationID: "same-op"}}, nil
}

type uncertainJobs struct {
	completedJobs
	reconciled []api.RequestIdentity
}

func (j *uncertainJobs) Submit(_ context.Context, s api.Submission) (api.AcceptanceResult, error) {
	j.submitted = append(j.submitted, s)
	return api.AcceptanceResult{}, errors.New("injected lost reply")
}
func (j *uncertainJobs) Reconcile(_ context.Context, id api.RequestIdentity) (api.AcceptanceResult, error) {
	j.reconciled = append(j.reconciled, id)
	if len(j.reconciled) == 1 {
		return api.AcceptanceResult{Outcome: api.AcceptanceOutcomeUnknown}, nil
	}
	return api.AcceptanceResult{Outcome: api.AcceptanceOutcomeAccepted, Receipt: &api.Receipt{Identity: id, OperationID: "same-op"}}, nil
}

type completedJobs struct {
	body      []byte
	submitted []api.Submission
}

func (j *completedJobs) GetHistoryWindow(context.Context) (api.HistoryWindow, error) {
	return api.HistoryWindow{HistoryEpoch: "fixture-epoch"}, nil
}
func (j *completedJobs) Submit(_ context.Context, s api.Submission) (api.AcceptanceResult, error) {
	j.submitted = append(j.submitted, s)
	return api.AcceptanceResult{Outcome: api.AcceptanceOutcomeAccepted, Receipt: &api.Receipt{Identity: s.Identity, OperationID: "dummy-op"}}, nil
}
func (j *completedJobs) Reconcile(context.Context, api.RequestIdentity) (api.AcceptanceResult, error) {
	panic("definite submission must not be retried or reconciled")
}
func (j *completedJobs) ObserveWork(context.Context, api.RequestIdentity) (api.ObservationResult, error) {
	return api.ObservationResult{Snapshot: &api.OperationSnapshot{State: api.WorkStateComplete}}, nil
}
func (j *completedJobs) CopyResult(_ context.Context, _ api.RequestIdentity, w io.Writer) (int64, error) {
	n, err := w.Write(j.body)
	return int64(n), err
}

func TestReceiverDistinctNamesAndSecretFreeStats(t *testing.T) {
	r := newReceiver()
	alpha, _ := fixtureFor("alpha")
	beta, _ := fixtureFor("beta")
	for _, call := range []struct {
		path   string
		secret []byte
		want   int
	}{{"/alpha", nil, 401}, {"/alpha", beta.secret, 401}, {"/alpha", alpha.secret, 200}, {"/beta", beta.secret, 200}} {
		req := httptest.NewRequest(http.MethodGet, call.path, nil)
		if call.secret != nil {
			req.Header.Set("Authorization", "Bearer "+string(call.secret))
		}
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		if response.Code != call.want {
			t.Fatalf("%s: status %d", call.path, response.Code)
		}
		for _, value := range [][]byte{alpha.secret, beta.secret} {
			if bytes.Contains(response.Body.Bytes(), value) {
				t.Fatal("receiver response echoed a credential")
			}
		}
	}
	stats := httptest.NewRecorder()
	r.ServeHTTP(stats, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if r.requests["alpha"] != 3 || r.accepted["alpha"] != 1 || r.accepted["beta"] != 1 || bytes.Contains(stats.Body.Bytes(), alpha.secret) || bytes.Contains(stats.Body.Bytes(), beta.secret) || strings.Contains(stats.Body.String(), "Authorization") {
		t.Fatal("receiver evidence lost counts or retained credential headers")
	}
}

func TestConnectionRefusesInstalledEndpointAndIdentityAliases(t *testing.T) {
	dir := t.TempDir()
	c := connection{Version: 1, Isolated: "rights-playground-unit", RuntimeProgram: filepath.Join(dir, "openabstractions"), StateDir: filepath.Join(dir, "state"), AppA: filepath.Join(dir, "A"), AppB: filepath.Join(dir, "B"), Origin: "http://127.0.0.1:1234"}
	c.Endpoint, _ = playgroundEndpoint(c.Isolated, runtime.GOOS)
	for _, p := range []string{c.AppA, c.AppB} {
		if err := os.WriteFile(p, []byte("same build, two files"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "connection.json")
	write := func() {
		data, _ := json.Marshal(c)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := loadConnection(path); err != nil {
		t.Fatal(err)
	}
	c.Endpoint, _ = bootstrap.Endpoint(c.Isolated)
	write()
	if _, err := loadConnection(path); err == nil {
		t.Fatal("isolated bootstrap endpoint must use the runtime owner's -runtime suffix")
	}
	c.Endpoint = "the-installed-runtime"
	write()
	if _, err := loadConnection(path); err == nil {
		t.Fatal("setup must not accept a connection to an unrelated runtime")
	}
	c.Endpoint, _ = playgroundEndpoint(c.Isolated, runtime.GOOS)
	c.AppB = c.AppA
	write()
	if _, err := loadConnection(path); err == nil {
		t.Fatal("A/B alias must not count as independent identities")
	}
}

func TestPlaygroundUsesIsolatedNativeMacEndpoint(t *testing.T) {
	name := "rights-playground-unit"
	got, err := playgroundEndpoint(name, "darwin")
	if err != nil || got != "xpc:com.openabstractions.isolated."+name+".runtime-v1" {
		t.Fatal("Mac connection must retain isolated XPC native Program proof")
	}
}

func TestReadRefusalAndMenuEOFDoNotInventAnswer(t *testing.T) {
	if err := readSecret(context.Background(), connection{}, "alpha", io.Discard); err == nil {
		t.Fatal("unbound requester must refuse before resolving any runtime")
	}
	var out bytes.Buffer
	if err := menu(connection{}, strings.NewReader("quit\n"), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Allow") || strings.Contains(out.String(), "decision recorded") {
		t.Fatal("ordinary app exit must not answer a permission")
	}
	buffer := &boundedArtifact{Buffer: &bytes.Buffer{}, remaining: 2}
	if _, err := buffer.Write([]byte("oversized")); err == nil || buffer.Len() != 0 {
		t.Fatal("result copying must remain bounded before output")
	}
}
