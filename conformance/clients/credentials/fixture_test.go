package credentialsfixture_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	cclient "github.com/openabstractions/abstraction-credentials/go/client"
	cservice "github.com/openabstractions/abstraction-credentials/go/service"
	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rc "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
)

const (
	secret   = "hf_FIXTURE_SECRET_7f3a9c_EXAMPLE"
	rotated  = "hf_FIXTURE_ROTATED_41be02_EXAMPLE"
	executor = "abstraction.download/http-execution@1"
)

// decider relays holder and applier questions to the rights service as its
// designated enforcer and records the subjects it was asked about.
type decider struct {
	client   *rc.Client
	mu       sync.Mutex
	subjects []cwire.Subject
}

func (d *decider) decide(ctx context.Context, s cwire.Subject, action, resource string) (string, error) {
	d.mu.Lock()
	d.subjects = append(d.subjects, s)
	d.mu.Unlock()
	v, err := d.client.DecideForContext(ctx, rc.Subject{Account: s.Account, Program: s.Program}, action, resource)
	if err != nil {
		return "", err
	}
	return string(v.Outcome), nil
}

// subjectFor returns the last holder subject whose program satisfies match.
func (d *decider) subjectFor(match func(string) bool) (cwire.Subject, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.subjects) - 1; i >= 0; i-- {
		if match(d.subjects[i].Program) {
			return d.subjects[i], true
		}
	}
	return cwire.Subject{}, false
}

func platformBackend(t *testing.T) credentials.Backend {
	t.Helper()
	var b credentials.Backend
	var err error
	switch runtime.GOOS {
	case "windows":
		b, err = credentials.NewCredentialManager()
	case "linux":
		b, err = credentials.NewFileBackend(filepath.Join(t.TempDir(), "items"))
	default:
		t.Skip("Program proof unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func resolve(ctx context.Context, t *testing.T, runtimeEndpoint, contract string) listen.FrameClient {
	t.Helper()
	request := fwire.ResolveRequest{Capability: "abstraction.credentials", Contracts: []string{contract}, Guarantees: []string{}, Scope: fwire.ScopeAny}
	result, err := resolution.NewUnverifiedClient(runtimeEndpoint, 2*time.Second).Resolve(ctx, request)
	if err != nil || result.Status != fwire.ResolutionStatusResolved {
		t.Fatalf("resolve %s: %+v %v", contract, result, err)
	}
	bound, err := resolution.BindLocal(ctx, *result.Reference, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

// must stops the test goroutine with the setup error; the panic fails the test.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestResolvedCredentials hosts the credentials service in the facade runtime
// with a real rights decision service and the platform store, resolves both
// contracts, and drives the lifecycle through the typed Go clients.
func TestResolvedCredentials(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-credentials-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	account := must(user.Current()).Uid
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	policy := must(rights.LoadDecisionPolicy(filepath.Join(dir, "rights.json"), []string{"abstraction.logging/history.read"}))
	rightsEndpoint := endpoint("rights")
	// The decision service runs as its own host so the fixture can stop it for the outage case.
	decisionHost := must(rightsservice.Listen(rightsEndpoint, policy, func(ctx context.Context, p *identity.Peer, a, r string) bool {
		process, e := p.Process.AtLeast(listen.Program.Process)
		return e == nil && process.PID == os.Getpid() && ctx.Err() == nil
	}))
	rightsDone := make(chan error, 1)
	go func() { rightsDone <- decisionHost.Serve(ctx) }()
	defer func() { decisionHost.Close(); <-rightsDone }()

	var logMu sync.Mutex
	var logged bytes.Buffer
	onError := func(err error) { logMu.Lock(); fmt.Fprintln(&logged, err); logMu.Unlock() }
	store := platformBackend(t)
	// A test-unique store namespace: the fixture never reads or writes the items
	// of a real installation, and revocation below removes what it wrote.
	namespace := fmt.Sprintf("oa-test-fixture-%d", time.Now().UnixNano())
	holder := must(credentials.Open(credentials.Config{Backend: store, Namespace: namespace, StatePath: filepath.Join(dir, "credentials", "state.json"), OnError: onError}))
	d := &decider{client: rc.New(rightsEndpoint)}
	credentialsEndpoint := endpoint("credentials")
	// The designated consuming service is this process, standing in for the download executor.
	service := must(cservice.Listen(credentialsEndpoint, holder, d.decide, func(ctx context.Context, p *identity.Peer, consumer, name string) bool {
		process, e := p.Process.AtLeast(listen.Program.Process)
		return e == nil && process.PID == os.Getpid()
	}))
	service.OnError = onError
	sinkPath := filepath.Join(dir, "runtime-log.jsonl")
	sink := must(logging.OpenFileSink(sinkPath))
	defer sink.Close()
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), Sink: sink, OnError: onError,
		RightsPolicy: policy, RightsActions: cwire.ResourceActions, RightsEndpoint: endpoint("runtime-rights"),
		Credentials: service, CredentialsEndpoint: credentialsEndpoint}
	h := must(host.Listen(o))
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime did not drain")
		}
	}()

	holderClient := cclient.NewHolderWithTransport(resolve(ctx, t, o.Endpoint, credentials.HolderContract))
	applier := cclient.NewApplierWithTransport(resolve(ctx, t, o.Endpoint, credentials.ApplierContract))
	registration := cwire.Registration{Name: "hf", Kind: "bearer", Secret: []byte(secret),
		Scope: cwire.Scope{Targets: []string{"huggingface.co"}, Consumers: []string{executor}}}

	// 1. No holder.manage rule: forbidden, and the store holds nothing.
	if r := must(holderClient.Store(ctx, "", registration)); r.Outcome != "forbidden" {
		t.Fatalf("store before grant: %+v", r)
	}
	cli, ok := d.subjectFor(func(p string) bool { return true })
	if !ok || cli.Account != account {
		t.Fatalf("holder subject not bound: %+v", cli)
	}
	for _, action := range []string{credentials.ActionManage, credentials.ActionRead} {
		if err := policy.Set(rc.Subject(cli), action, credentials.ResourceAccount, true); err != nil {
			t.Fatal(err)
		}
	}
	stored := must(holderClient.Store(ctx, "", registration))
	if stored.Outcome != "stored" {
		t.Fatalf("store: %+v", stored)
	}
	t.Cleanup(func() {
		page := holder.List(account, "", 64)
		for _, r := range page.Records {
			holder.Revoke(cli, r.Revision, r.Name)
		}
	})

	// 2. Application subject without an apply rule, then with one.
	app := cwire.Subject{Account: account, Program: filepath.Join(dir, "comfyui", "python.exe")}
	use := cwire.Use{Subject: app, Consumer: executor, Name: "hf", Target: "cdn-lfs.huggingface.co"}
	if r := must(applier.Apply(ctx, use)); r.Outcome != "not_permitted" || len(r.Headers) != 0 {
		t.Fatalf("apply before rule: %+v", r.Outcome)
	}
	if err := policy.Set(rc.Subject(app), credentials.ActionApply, credentials.ResourceFor("hf"), true); err != nil {
		t.Fatal(err)
	}
	if r := must(applier.Check(ctx, use)); r.Outcome != "applied" || r.Revision != stored.Revision {
		t.Fatalf("check: %+v", r)
	}
	applied := must(applier.Apply(ctx, use))
	if applied.Outcome != "applied" || applied.Headers["Authorization"] != "Bearer "+secret {
		t.Fatalf("apply to allowed consumer: %s", applied.Outcome)
	}

	// 3. Wrong consumer and wrong target.
	wrongConsumer := use
	wrongConsumer.Consumer = "abstraction.model/resolver@1"
	if r := must(applier.Apply(ctx, wrongConsumer)); r.Outcome != "consumer_refused" || len(r.Headers) != 0 {
		t.Fatalf("wrong consumer: %s", r.Outcome)
	}
	wrongTarget := use
	wrongTarget.Target = "attacker.example"
	if r := must(applier.Apply(ctx, wrongTarget)); r.Outcome != "target_refused" || len(r.Headers) != 0 {
		t.Fatalf("wrong target: %s", r.Outcome)
	}

	// 4. Rotation: the next request carries the new bytes; a stale revision conflicts.
	rot := must(holderClient.Rotate(ctx, stored.Revision, cwire.Rotation{Name: "hf", Secret: []byte(rotated)}))
	if rot.Outcome != "rotated" {
		t.Fatalf("rotate: %+v", rot)
	}
	if r := must(holderClient.Rotate(ctx, stored.Revision, cwire.Rotation{Name: "hf", Secret: []byte("x")})); r.Outcome != "conflict" {
		t.Fatalf("stale rotate: %+v", r)
	}
	if r := must(applier.Apply(ctx, use)); r.Headers["Authorization"] != "Bearer "+rotated {
		t.Fatalf("rotation not applied: %s", r.Outcome)
	}

	// 5. Installed C++ application: a holder client and a refused applier call.
	if probe := os.Getenv("OA_CPP_CREDENTIALS_PROBE"); probe != "" {
		run := func(mode string) string {
			t.Helper()
			cmd := exec.CommandContext(ctx, probe, o.Endpoint, mode, app.Account)
			cmd.Dir = t.TempDir()
			out, err := fixture.Output(ctx, cmd)
			if err != nil {
				t.Fatalf("%s: %v\n%s", mode, err, out)
			}
			t.Log(strings.TrimSpace(string(out)))
			return string(out)
		}
		out := run("denied")
		isProbe := func(p string) bool { return filepath.Clean(p) == filepath.Clean(probe) || strings.EqualFold(filepath.Base(p), filepath.Base(probe)) }
		probeSubject, ok := d.subjectFor(isProbe)
		if !ok {
			t.Fatal("probe subject not observed by the holder")
		}
		if err := policy.Set(rc.Subject(probeSubject), credentials.ActionRead, credentials.ResourceAccount, true); err != nil {
			t.Fatal(err)
		}
		out += run("listed")
		if strings.Contains(out, secret) || strings.Contains(out, rotated) {
			t.Fatal("C++ application output carries a secret")
		}
	} else {
		t.Log("OA_CPP_CREDENTIALS_PROBE unset: installed C++ consumer not run")
	}

	// 5b. A JavaScript application through the pure facade and generated clients.
	if node := os.Getenv("OA_JS_CREDENTIALS_NODE"); node != "" {
		script, err := filepath.Abs("js_consumer.mjs")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, node, script, o.Endpoint, account, secret, rotated)
		out, err := fixture.Output(ctx, cmd)
		if err != nil {
			t.Fatalf("javascript consumer: %v\n%s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	} else {
		t.Log("OA_JS_CREDENTIALS_NODE unset: JavaScript consumer not run")
	}

	// 5c. A Python application through the facade and the shared native transport.
	if python := os.Getenv("OA_PY_CREDENTIALS_PYTHON"); python != "" {
		script, err := filepath.Abs("py_consumer.py")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, python, script, o.Endpoint, account, secret, rotated)
		out, err := fixture.Output(ctx, cmd)
		if err != nil {
			t.Fatalf("python consumer: %v\n%s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	} else {
		t.Log("OA_PY_CREDENTIALS_PYTHON unset: Python consumer not run")
	}

	// 5d. A Rust application through the facade and the native transport.
	if probe := os.Getenv("OA_RUST_CREDENTIALS_PROBE"); probe != "" {
		cmd := exec.CommandContext(ctx, probe, o.Endpoint, account, secret, rotated)
		out, err := fixture.Output(ctx, cmd)
		if err != nil {
			t.Fatalf("rust consumer: %v\n%s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	} else {
		t.Log("OA_RUST_CREDENTIALS_PROBE unset: Rust consumer not run")
	}

	// 6. Revocation: tombstone reads revoked.
	current := must(holderClient.List(ctx, "", 64))
	if current.Outcome != "page" || len(current.Records) != 1 || current.Limits.SecureStore != store.Name() {
		t.Fatalf("list: %+v", current)
	}
	if r := must(holderClient.Revoke(ctx, current.Records[0].Revision, "hf")); r.Outcome != "revoked" {
		t.Fatalf("revoke: %+v", r)
	}
	if r := must(applier.Apply(ctx, use)); r.Outcome != "revoked" || len(r.Headers) != 0 {
		t.Fatalf("revoked credential: %s", r.Outcome)
	}

	// 7. Audit readback.
	audit := must(holderClient.Audit(ctx, "", 256))
	if audit.Outcome != "page" {
		t.Fatalf("audit: %+v", audit)
	}
	seen := map[string]bool{}
	for _, e := range audit.Entries {
		seen[e.Event+":"+e.Outcome] = true
	}
	for _, want := range []string{"stored:stored", "refused:not_granted", "checked:applied", "applied:applied", "refused:consumer_refused", "refused:target_refused", "rotated:rotated", "revoked:revoked", "refused:revoked"} {
		if !seen[want] {
			t.Fatalf("audit lacks %s: %v", want, seen)
		}
	}
	listing := must(holderClient.List(ctx, "", 64))

	// 8. Machine scope refuses with no_secure_store whatever store is configured.
	machine := must(credentials.Open(credentials.Config{Backend: store, Namespace: namespace, MachineScope: true}))
	machineEndpoint := endpoint("machine")
	machineHost := must(cservice.Listen(machineEndpoint, machine, d.decide, nil))
	machineDone := make(chan error, 1)
	go func() { machineDone <- machineHost.Serve(ctx) }()
	if r := must(cclient.NewHolder(machineEndpoint).Store(ctx, "", registration)); r.Outcome != "no_secure_store" {
		t.Fatalf("machine scope: %+v", r)
	}
	machineHost.Close()
	<-machineDone

	// 9. Rights outage: application and holder reads are unavailable.
	if err := policy.Set(rc.Subject(app), credentials.ActionApply, credentials.ResourceFor("hf2"), true); err != nil {
		t.Fatal(err)
	}
	second := registration
	second.Name, second.Secret = "hf2", []byte(secret)
	if r := must(holderClient.Store(ctx, "", second)); r.Outcome != "stored" {
		t.Fatalf("second store: %+v", r)
	}
	decisionHost.Close()
	<-rightsDone
	rightsDone <- nil
	outage := use
	outage.Name = "hf2"
	if r := must(applier.Apply(ctx, outage)); r.Outcome != "unavailable" || len(r.Headers) != 0 {
		t.Fatalf("rights outage apply: %s", r.Outcome)
	}
	if r := must(holderClient.List(ctx, "", 8)); r.Outcome != "unavailable" {
		t.Fatalf("rights outage list: %s", r.Outcome)
	}

	// 10. No raw secret in list, audit, error log or runtime log output.
	sink.Close()
	logFile, _ := os.ReadFile(sinkPath)
	listJSON, _ := json.Marshal(listing)
	auditJSON, _ := json.Marshal(audit)
	logMu.Lock()
	errorsLogged := logged.String()
	logMu.Unlock()
	for name, text := range map[string]string{"list": string(listJSON), "audit": string(auditJSON), "errors": errorsLogged, "runtime log": string(logFile)} {
		if strings.Contains(text, secret) || strings.Contains(text, rotated) {
			t.Fatalf("raw secret in %s output", name)
		}
	}
	t.Logf("PASS resolved credentials on %s: store, apply, consumer and target refusals, rotation, revocation, audit, machine scope, rights outage", store.Name())
}
