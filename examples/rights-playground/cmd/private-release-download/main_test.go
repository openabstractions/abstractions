package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/openabstractions/abstractions/examples/rights-playground/internal/personfolders"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

type fixtureJobs struct {
	body       []byte
	submitted  []api.Submission
	reconciled []api.RequestIdentity
	lost       bool
	answer     api.AcceptanceOutcome
	state      api.WorkState
}

func (j *fixtureJobs) GetHistoryWindow(context.Context) (api.HistoryWindow, error) {
	return api.HistoryWindow{HistoryEpoch: "fixture-epoch"}, nil
}
func (j *fixtureJobs) accepted(id api.RequestIdentity) api.AcceptanceResult {
	return api.AcceptanceResult{Outcome: api.AcceptanceOutcomeAccepted, Receipt: &api.Receipt{Identity: id, OperationID: "fixture-op", AcceptedGuarantees: guarantees()}}
}
func (j *fixtureJobs) Submit(_ context.Context, s api.Submission) (api.AcceptanceResult, error) {
	j.submitted = append(j.submitted, s)
	if j.lost {
		return api.AcceptanceResult{}, errors.New("untrusted transport diagnostic")
	}
	if j.answer != 0 {
		return api.AcceptanceResult{Outcome: j.answer}, nil
	}
	return j.accepted(s.Identity), nil
}
func (j *fixtureJobs) Reconcile(_ context.Context, id api.RequestIdentity) (api.AcceptanceResult, error) {
	j.reconciled = append(j.reconciled, id)
	if j.answer != 0 {
		return api.AcceptanceResult{Outcome: j.answer}, nil
	}
	return j.accepted(id), nil
}
func (j *fixtureJobs) ObserveWork(context.Context, api.RequestIdentity) (api.ObservationResult, error) {
	state := j.state
	if state == 0 {
		state = api.WorkStateComplete
	}
	return api.ObservationResult{Snapshot: &api.OperationSnapshot{State: state}}, nil
}
func (j *fixtureJobs) CopyResult(_ context.Context, _ api.RequestIdentity, w io.Writer) (int64, error) {
	n, e := w.Write(j.body)
	return int64(n), e
}
func fixtureOptions(t *testing.T, body []byte) options {
	t.Helper()
	sum := sha256.Sum256(body)
	return options{credential: "private-release", source: "https://api.github.com/repos/fixture/project/releases/assets/1", digest: hex.EncodeToString(sum[:]), size: int64(len(body)), output: filepath.Join(t.TempDir(), "asset.bin"), epoch: "fixture-epoch", key: "release-1"}
}

func TestPrivateReleaseAppNamesCredentialAndVerifiesBoundedArtifact(t *testing.T) {
	body := []byte("ordinary release asset\n")
	o := fixtureOptions(t, body)
	j := &fixtureJobs{body: body}
	var output bytes.Buffer
	if err := download(context.Background(), j, o, &output); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(o.output); err != nil || !bytes.Equal(got, body) {
		t.Fatal("verified artifact absent")
	}
	if len(j.submitted) != 1 || len(j.reconciled) != 0 || !slices.Contains(j.submitted[0].RequiredGuarantees, "abstraction.download/credentials@2") {
		t.Fatal("scoped admission missing")
	}
	spec, err := request.Decode(j.submitted[0].Spec)
	if err != nil || len(spec.Sources) != 1 || spec.Sources[0].Credential != o.credential || spec.Sources[0].Locator != o.source {
		t.Fatal("application changed source metadata")
	}
	// This sentinel exists only in the test's operator/receiver side. Neither
	// the app inputs nor its job/output contain it; the app reads no environment.
	secret := "operator-fixture-token-never-in-app"
	t.Setenv("PRIVATE_RELEASE_TOKEN", secret)
	raw, _ := json.Marshal(j.submitted)
	if bytes.Contains(raw, []byte(secret)) || strings.Contains(output.String(), secret) {
		t.Fatal("secret disclosed")
	}
	if err := download(context.Background(), j, o, io.Discard); err == nil {
		t.Fatal("existing output was overwritten")
	}
}

func TestPrivateReleaseUncertaintyReconcilesSameIdentity(t *testing.T) {
	o := fixtureOptions(t, []byte("asset"))
	j := &fixtureJobs{body: []byte("asset"), lost: true, answer: api.AcceptanceOutcomeUnknown}
	if err := download(context.Background(), j, o, io.Discard); !errors.Is(err, errUncertain) {
		t.Fatal("lost submission was called terminal")
	}
	for _, answer := range []api.AcceptanceOutcome{api.AcceptanceOutcomeForbidden, api.AcceptanceOutcomeInvalid} {
		j.answer = answer
		o.reconcile = true
		if err := download(context.Background(), j, o, io.Discard); !errors.Is(err, errUncertain) {
			t.Fatal("refused reconciliation asserted nonacceptance")
		}
	}
	j.answer = 0
	if err := download(context.Background(), j, o, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(j.submitted) != 1 {
		t.Fatal("reconciliation resubmitted")
	}
	for _, id := range j.reconciled {
		if id != j.submitted[0].Identity {
			t.Fatal("identity changed")
		}
	}
}

func TestPrivateReleaseRejectsUnverifiedResultsAndRefusals(t *testing.T) {
	for _, mode := range []string{"digest", "overflow", "failed", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte("asset")
			o := fixtureOptions(t, body)
			j := &fixtureJobs{body: body}
			switch mode {
			case "digest":
				j.body = []byte("wrong")
			case "overflow":
				j.body = []byte("too many bytes")
			case "failed":
				j.state = api.WorkStateFailed
			case "forbidden":
				j.answer = api.AcceptanceOutcomeForbidden
			}
			var output bytes.Buffer
			if err := download(context.Background(), j, o, &output); err == nil {
				t.Fatal("invalid result succeeded")
			}
			if _, err := os.Stat(o.output); !os.IsNotExist(err) {
				t.Fatal("invalid result wrote output")
			}
		})
	}
}

func TestPrivateReleaseCLIRejectsSecretInputsAndUnsafeSources(t *testing.T) {
	o := fixtureOptions(t, []byte("asset"))
	base := []string{"--endpoint", "fixture", "--runtime-program", filepath.Join(t.TempDir(), "runtime.exe"), "--credential", o.credential, "--url", o.source, "--sha256", o.digest, "--bytes", "5", "--out", o.output, "--epoch", o.epoch, "--key", o.key}
	if _, err := parse(base, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{{"--token", "secret-reflection-sentinel"}, {"--secret", "secret-reflection-sentinel"}, {"--header", "Authorization: secret-reflection-sentinel"}, {"--from-stdin"}, {"--url", "https://user:secret-reflection-sentinel@api.github.com/file"}, {"--url", "https://api.github.com/file?token=secret-reflection-sentinel"}, {"--url", "http://external.invalid/file"}} {
		_, err := parse(append(slices.Clone(base), extra...), io.Discard)
		if err == nil || strings.Contains(err.Error(), "secret-reflection-sentinel") {
			t.Fatal("unsafe argument accepted or reflected")
		}
	}
	// Source-only isolation: this command has no OS credential API or process
	// launcher. Existing rights-playground's source/operator/Read stay separate.
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"os.Getenv(", "os.Environ(", "os.Stdin", "os/exec", "ResolveCredentials(", "ResolveCredentialReader(", "fixtureFor("} {
		if bytes.Contains(source, []byte(forbidden)) {
			t.Fatal("token-free app acquired an unrelated input path")
		}
	}
}

func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }

func TestPersonFoldersStandIn(t *testing.T) {
	person := os.Getenv("OA_TEST_PERSON")
	if person == "" {
		t.Fatal("test package has no stand-in person")
	}
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		rel, err := filepath.Rel(person, os.Getenv(key))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			t.Fatalf("%s escapes stand-in: %q (%v)", key, rel, err)
		}
	}
}
