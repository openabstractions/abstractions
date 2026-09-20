package main

import (
	"context"
	cascore "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// applicationTransportProvesSession measures the host property required by
// Announce, Withdraw and Activate. Darwin has no Program proof for the shared
// transport. Linux additionally requires a usable audit session; WSL reports
// the kernel's unavailable sentinel instead of a session that can be matched.
func applicationTransportProvesSession(t *testing.T) bool {
	t.Helper()
	if !statusTransportProvesProgram(t) {
		return false
	}
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/self/sessionid")
		if err != nil {
			t.Fatalf("read application audit session: %v", err)
		}
		session, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
		if err != nil {
			t.Fatalf("parse application audit session %q: %v", raw, err)
		}
		if session == 1<<32-1 {
			t.Log("measured application session proof unavailable: Linux audit session is unset")
			return false
		}
	}
	return true
}

func TestApplicationPresenceOwnsEpochAndPreservesInstalledDescriptor(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	operator := rights.Subject{Account: "account", Program: filepath.Join(state, "operator.exe")}
	app := rights.Subject{Account: "account", Program: filepath.Join(state, "app.exe")}
	policy := func(_ context.Context, s rights.Subject, action, resource string) (bool, error) {
		if action == ActionApplicationManage {
			return s.Program == operator.Program, nil
		}
		return s.Program == app.Program, nil
	}
	d, err := openApplications(state, "account", policy)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	d.now = func() time.Time { return now }
	v := wire.ApplicationDescriptor{Name: "editor", Program: app.Program, Title: "Editor", StartGuidance: "Open Editor"}
	if r := d.register(ctx, app, v); r.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatalf("self registration: %+v", r)
	}
	if r := d.register(ctx, operator, v); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	p := wire.ApplicationPresence{Application: "editor", LeaseMs: 1000, Interfaces: []wire.ApplicationInterface{{Name: "tools", Protocol: "mcp"}, {Name: "view", Protocol: "native"}}, Contexts: []wire.ApplicationContext{{Name: "document", Title: "One", Revision: "1"}}}
	first := d.announce(ctx, app, p)
	second := d.announce(ctx, app, p)
	if first.Outcome != wire.ApplicationOutcomeApplied || first.Instance == second.Instance {
		t.Fatal(first, second)
	}
	page := d.observe(ctx, app, "", 0)
	if len(page.Applications) != 1 || len(page.Applications[0].Instances) != 2 || len(page.Applications[0].Instances[0].Interfaces) != 2 {
		t.Fatal(page)
	}
	page.Applications[0].Instances[0].Contexts[0].Title = "mutated"
	if d.observe(ctx, app, "", 0).Applications[0].Instances[0].Contexts[0].Title != "One" {
		t.Fatal("snapshot aliases owned state")
	}
	forged := rights.Subject{Account: "account", Program: operator.Program}
	if r := d.withdraw(ctx, forged, "editor", first.Instance); r.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal(r)
	}
	now = now.Add(2 * time.Second)
	if page = d.observe(ctx, app, "", 0); len(page.Applications[0].Instances) != 0 {
		t.Fatal("expired presence returned")
	}
	p.Instance = first.Instance
	if r := d.announce(ctx, app, p); r.Outcome != wire.ApplicationOutcomeStale {
		t.Fatal("expired instance renewed", r)
	}
	p.Instance = ""
	fresh := d.announce(ctx, app, p)
	restarted, err := openApplications(state, "account", policy)
	if err != nil {
		t.Fatal(err)
	}
	page = restarted.observe(ctx, app, page.Cursor, 0)
	if len(page.Applications) != 1 || len(page.Applications[0].Instances) != 0 {
		t.Fatal("restart lost installed descriptor or invented presence", page)
	}
	p.Instance = fresh.Instance
	if r := restarted.announce(ctx, app, p); r.Outcome != wire.ApplicationOutcomeStale {
		t.Fatal("restart accepted old instance", r)
	}
}

func TestApplicationDirectoryFiltersHiddenChangesAndRevokedAccess(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	operator := rights.Subject{Account: "account", Program: filepath.Join(state, "operator.exe")}
	app := rights.Subject{Account: "account", Program: filepath.Join(state, "app.exe")}
	reader := rights.Subject{Account: "account", Program: filepath.Join(state, "reader.exe")}
	var mu sync.Mutex
	revoked := false
	policy := func(_ context.Context, s rights.Subject, action, resource string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if action == ActionApplicationManage {
			return s.Program == operator.Program, nil
		}
		if action == ActionApplicationAnnounce {
			return s.Program == app.Program, nil
		}
		return !revoked && s.Program == reader.Program && resource == "app:visible", nil
	}
	d, err := openApplications(state, "account", policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"visible", "hidden"} {
		if r := d.register(ctx, operator, wire.ApplicationDescriptor{Name: name, Program: app.Program, Title: name}); r.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(r)
		}
	}
	before := d.observe(ctx, reader, "", 0)
	if len(before.Applications) != 1 || before.Applications[0].Descriptor.Name != "visible" {
		t.Fatal(before)
	}
	d.announce(ctx, app, wire.ApplicationPresence{Application: "hidden", LeaseMs: 1000})
	after := d.observe(ctx, reader, "", 0)
	if after.Cursor != before.Cursor {
		t.Fatal("hidden state leaked through cursor")
	}
	returned := make(chan wire.ApplicationPage, 1)
	go func() { returned <- d.observe(ctx, reader, before.Cursor, 100) }()
	mu.Lock()
	revoked = true
	mu.Unlock()
	select {
	case result := <-returned:
		if len(result.Applications) != 0 {
			t.Fatal("revoked data returned", result)
		}
	case <-time.After(time.Second):
		t.Fatal("observation exceeded wait budget")
	}
	if r := d.observe(ctx, rights.Subject{Account: "other", Program: reader.Program}, "", 0); r.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal(r)
	}
}

func TestApplicationPresenceBoundsAndOwnerValidation(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	operator := rights.Subject{Account: "a", Program: filepath.Join(state, "operator")}
	app := rights.Subject{Account: "a", Program: filepath.Join(state, "app")}
	d, err := openApplications(state, "a", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if r := d.register(ctx, operator, wire.ApplicationDescriptor{Name: "app", Program: app.Program, Title: "App"}); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	p := wire.ApplicationPresence{Application: "app", LeaseMs: 60000}
	if r := d.announce(ctx, operator, p); r.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal("policy alone impersonated descriptor program", r)
	}
	for range 128 {
		if r := d.announce(ctx, app, p); r.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(r)
		}
	}
	if r := d.announce(ctx, app, p); r.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatal("unbounded instances", r)
	}
	p.Interfaces = make([]wire.ApplicationInterface, 17)
	if r := d.announce(ctx, app, p); r.Outcome != wire.ApplicationOutcomeInvalid {
		t.Fatal(r)
	}
	if r := d.observe(ctx, app, "", 30001); r.Outcome != wire.ApplicationOutcomeInvalid {
		t.Fatal(r)
	}
}

func TestApplicationNativeDirectoryUsesBoundProgram(t *testing.T) {
	if !applicationTransportProvesSession(t) {
		t.Skip("application session proof unavailable on this host")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	operator := rights.Subject{Account: account.Uid, Program: filepath.Join(t.TempDir(), "operator.exe")}
	app := rights.Subject{Account: account.Uid, Program: program}
	d, err := openApplications(t.TempDir(), account.Uid, func(_ context.Context, s rights.Subject, action, resource string) (bool, error) {
		if action == ActionApplicationManage {
			return s.Program == operator.Program, nil
		}
		return samePrograms(s.Program, app.Program), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	readiness := wire.ApplicationInterface{Name: "tools", Protocol: "mcp"}
	if r := d.register(ctx, operator, wire.ApplicationDescriptor{Name: "editor", Program: program, Title: "Editor", Activation: &wire.ApplicationActivationRecipe{Arguments: []string{"--fixture"}, Readiness: readiness, ReadinessTimeoutMs: 1000}}); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	endpoint := fullEndpoint(t, "apps-"+d.epoch[:12])
	h, err := listenApplications(endpoint, d, account.Uid, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	expectation := runtimeExpectation(t)
	c := wire.NewApplicationsClient(listen.FrameClient{Endpoint: endpoint, Server: &expectation, Timeout: time.Second, MaxFrame: 1 << 20}.WithContext(ctx))
	result, err := c.Announce(wire.ApplicationPresence{Application: "editor", LeaseMs: 1000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{}})
	if err != nil || result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result, err)
	}
	page, err := c.Observe("", 0)
	if err != nil || len(page.Applications) != 1 || page.Applications[0].Scope != wire.ScopeLocal || len(page.Applications[0].Instances) != 1 {
		t.Fatal(page, err)
	}
	refused, err := c.Remove("editor")
	if err != nil || refused.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal(refused, err)
	}
	removed, err := c.Withdraw("editor", result.Instance)
	if err != nil || removed.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(removed, err)
	}
	page, err = c.Observe("", 0)
	if err != nil || len(page.Applications) != 1 || len(page.Applications[0].Instances) != 0 {
		t.Fatal(page, err)
	}
	launched := make(chan struct{})
	d.launch = func(got string, arguments []string) error {
		if got != program || len(arguments) != 1 || arguments[0] != "--fixture" {
			t.Errorf("launch %q %q", got, arguments)
		}
		go func() {
			d.mu.Lock()
			session := d.attempts["editor"].session
			d.mu.Unlock()
			d.announceInSession(ctx, app, session, wire.ApplicationPresence{Application: "editor", LeaseMs: 1000, Interfaces: []wire.ApplicationInterface{readiness}})
			close(launched)
		}()
		return nil
	}
	activated, err := c.Activate("editor")
	if err != nil || activated.Outcome != wire.ApplicationActivationOutcomeReady || !activated.Started || activated.Instance == "" {
		t.Fatal(activated, err)
	}
	<-launched
	cancel()
	h.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("host shutdown blocked")
	}
}
func TestApplicationSnapshotEpochCancellationAndEncodedBounds(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	s := rights.Subject{Account: "account", Program: filepath.Join(state, "operator")}
	d, err := openApplications(state, s.Account, func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	old := d.observe(ctx, s, "", 0)
	restarted, err := openApplications(state, s.Account, d.decide)
	if err != nil {
		t.Fatal(err)
	}
	if page := restarted.observe(ctx, s, old.Cursor, 30000); page.Outcome != wire.ApplicationOutcomeStale || page.Cursor == old.Cursor {
		t.Fatal(page)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if page := d.observe(cancelled, s, "", 0); page.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatal(page)
	}
	p := wire.ApplicationPresence{Application: "editor", LeaseMs: 1000, Contexts: []wire.ApplicationContext{}}
	for i := 0; i < 10; i++ {
		p.Contexts = append(p.Contexts, wire.ApplicationContext{Name: string(rune('a' + i)), Title: strings.Repeat("x", 256)})
	}
	if validApplicationPresence(p) {
		t.Fatal("oversized aggregate metadata accepted")
	}
}

func TestApplicationRuntimeCompositionAndOperatorAuthority(t *testing.T) {
	options := gatedRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	apps, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveApplications(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := wire.ApplicationDescriptor{Name: "editor", Program: filepath.Join(options.stateDir, "editor.exe"), Title: "Editor"}
	result, err := apps.Register(ctx, descriptor)
	if err != nil || result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result, err)
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil || page.Outcome != wire.ApplicationOutcomePage || len(page.Applications) != 0 {
		t.Fatal("operator management must not imply visibility", page, err)
	}
	activation, err := apps.Activate(ctx, "editor")
	if err != nil || activation.Outcome != wire.ApplicationActivationOutcomeForbidden {
		t.Fatal("operator management must not imply activation", activation, err)
	}
	presence, err := apps.Announce(ctx, wire.ApplicationPresence{Application: "editor", LeaseMs: 1000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{}})
	if err != nil || presence.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal(presence, err)
	}
	result, err = apps.Remove(ctx, "editor")
	if err != nil || result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result, err)
	}
}

func TestApplicationPersistenceRefusesConcurrentWriterWithoutLosingState(t *testing.T) {
	state := t.TempDir()
	ctx := context.Background()
	s := rights.Subject{Account: "account", Program: filepath.Join(state, "operator")}
	policy := func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil }
	first, err := openApplications(state, s.Account, policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := openApplications(state, s.Account, policy)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := wire.ApplicationDescriptor{Name: "editor", Program: filepath.Join(state, "editor"), Title: "Editor"}
	if r := first.register(ctx, s, descriptor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	descriptor.Name = "other"
	if r := second.register(ctx, s, descriptor); r.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatal("concurrent writer overwrote state", r)
	}
	if len(second.descriptors) != 0 {
		t.Fatal("failed persistence changed live state")
	}
	reopened, err := openApplications(state, s.Account, policy)
	if err != nil || len(reopened.descriptors) != 1 {
		t.Fatal(reopened, err)
	}
	if r := reopened.remove(ctx, s, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	reopened, err = openApplications(state, s.Account, policy)
	if err != nil || len(reopened.descriptors) != 0 {
		t.Fatal("removed descriptor returned after restart", reopened, err)
	}
}

func TestApplicationCancellationDuringPolicyReturnsNoPartialSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := t.TempDir()
	s := rights.Subject{Account: "account", Program: filepath.Join(state, "operator")}
	calls := 0
	policy := func(_ context.Context, _ rights.Subject, action, resource string) (bool, error) {
		if action == ActionApplicationRead {
			calls++
			if calls == 2 {
				cancel()
			}
		}
		return true, nil
	}
	d, err := openApplications(state, s.Account, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"editor", "viewer"} {
		if r := d.register(ctx, s, wire.ApplicationDescriptor{Name: name, Program: filepath.Join(state, name), Title: name}); r.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(r)
		}
	}
	if p := d.observe(ctx, s, "", 0); p.Outcome != wire.ApplicationOutcomeUnavailable || p.Cursor != "" || len(p.Applications) != 0 {
		t.Fatal("partial cancelled snapshot", p)
	}
}
func TestApplicationLocalDirectoryRejectsRemoteRequirement(t *testing.T) {
	_, err := client.NewUnverified("unused").ResolveApplications(context.Background(), client.Requirements{Scope: client.ScopeRemote})
	if err == nil {
		t.Fatal("local directory accepted remote placement")
	}
	state := t.TempDir()
	v := wire.ApplicationDescriptor{Name: "editor", Program: state + string(filepath.Separator) + "sub" + string(filepath.Separator) + ".." + string(filepath.Separator) + "editor", Title: "Editor"}
	if validApplicationDescriptor(v) {
		t.Fatal("unnormalized program accepted")
	}
	d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) {
		t.Fatal("invalid input reached policy")
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := d.withdraw(context.Background(), rights.Subject{}, "../invalid", ""); r.Outcome != wire.ApplicationOutcomeInvalid {
		t.Fatal(r)
	}
}

func TestApplicationHeldPersistenceLockHonorsCancellation(t *testing.T) {
	state := t.TempDir()
	s := rights.Subject{Account: "account", Program: filepath.Join(state, "operator")}
	d, err := openApplications(state, s.Account, func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- cascore.ChangeLimit(filepath.Join(d.dir, "descriptors.json"), 600*1024, func(raw []byte) ([]byte, error) { close(entered); <-release; return raw, nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	registered := make(chan wire.ApplicationChange, 1)
	go func() {
		registered <- d.register(ctx, s, wire.ApplicationDescriptor{Name: "editor", Program: filepath.Join(state, "editor"), Title: "Editor"})
	}()
	var result wire.ApplicationChange
	select {
	case result = <-registered:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("registration ignored context while waiting for CAS lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result.Outcome != wire.ApplicationOutcomeUnavailable || len(d.descriptors) != 0 {
		t.Fatal("blocked write changed state", result)
	}
	reopened, err := openApplications(state, s.Account, d.decide)
	if err != nil || len(reopened.descriptors) != 0 {
		t.Fatal("cancelled registration committed", reopened, err)
	}
	d.mu.Lock()
	blockedCtx, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer stop()
	observed := make(chan wire.ApplicationPage, 1)
	go func() { observed <- d.observe(blockedCtx, s, "", 0) }()
	select {
	case p := <-observed:
		if p.Outcome != wire.ApplicationOutcomeUnavailable {
			d.mu.Unlock()
			t.Fatal(p)
		}
	case <-time.After(time.Second):
		d.mu.Unlock()
		t.Fatal("observation ignored cancellation behind directory lock")
	}
	d.mu.Unlock()
}
