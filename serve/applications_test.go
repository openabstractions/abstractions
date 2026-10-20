package main

import (
	"context"
	"encoding/json"
	cascore "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir/privatetest"
	"github.com/openabstractions/abstraction-identity/listen"
	"os"
	"os/exec"
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

func TestApplicationPresenceNativeCaseSensitiveContexts(t *testing.T) {
	ctx := context.Background()
	state := privatetest.Dir(t)
	app := rights.Subject{Account: "account", Program: filepath.Join(state, "opencode.exe")}
	d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := d.register(ctx, app, wire.ApplicationDescriptor{Name: "opencode", Program: app.Program, Title: "OpenCode"}); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	// Exact official OpenCode 1.18.34 host978 receipt, with a case-distinct ID.
	id := "ses_e94dfb2daffemZukxH7YVqW6JH"
	p := wire.ApplicationPresence{Application: "opencode", LeaseMs: 30000, Contexts: []wire.ApplicationContext{{Name: id}, {Name: strings.ToLower(id)}}}
	first := d.announceInSession(ctx, app, "native-session", p)
	if first.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("native SDK context rejected: %+v", first)
	}
	page := d.observe(ctx, app, "", 0)
	if len(page.Applications) != 1 || len(page.Applications[0].Instances) != 1 || len(page.Applications[0].Instances[0].Contexts) != 2 {
		t.Fatalf("case-distinct contexts lost: %+v", page)
	}
	names := page.Applications[0].Instances[0].Contexts
	if names[0].Name == names[1].Name || !((names[0].Name == id) || (names[1].Name == id)) {
		t.Fatalf("native context identity changed: %+v", names)
	}
	p.Instance = first.Instance
	if got := d.announceInSession(ctx, app, "native-session", p); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("renewal: %+v", got)
	}
	for _, invalid := range []string{"", strings.Repeat("a", 65), "bad\nname", "bad.name", "_leading"} {
		bad := p
		bad.Contexts = []wire.ApplicationContext{{Name: invalid}}
		if got := d.announceInSession(ctx, app, "native-session", bad); got.Outcome != wire.ApplicationOutcomeInvalid {
			t.Fatalf("invalid context %q: %+v", invalid, got)
		}
	}
	duplicate := p
	duplicate.Contexts = []wire.ApplicationContext{{Name: id}, {Name: id}}
	if got := d.announceInSession(ctx, app, "native-session", duplicate); got.Outcome != wire.ApplicationOutcomeInvalid {
		t.Fatalf("duplicate accepted: %+v", got)
	}
	foreign := app
	foreign.Program = filepath.Join(state, "foreign.exe")
	if got := d.announceInSession(ctx, foreign, "native-session", p); got.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatalf("foreign program: %+v", got)
	}
	if got := d.announceInSession(ctx, app, "other-session", p); got.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatalf("foreign session: %+v", got)
	}
	restarted, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.announceInSession(ctx, app, "native-session", p); got.Outcome != wire.ApplicationOutcomeStale {
		t.Fatalf("old epoch: %+v", got)
	}
}

func TestApplicationNativeNodePresenceUsesBoundProgram(t *testing.T) {
	addon := os.Getenv("OA_IPC_NODE")
	if addon == "" {
		t.Skip("set OA_IPC_NODE to the current source-built fixture addon")
	}
	if runtime.GOOS != "windows" || !applicationTransportProvesSession(t) {
		t.Skip("Windows native program and session proof required")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(addon); err != nil {
		t.Fatalf("current source-built addon required: %v", err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d, err := openApplications(privatetest.Dir(t), account.Uid, func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	operator := rights.Subject{Account: account.Uid, Program: filepath.Join(privatetest.Dir(t), "operator.exe")}
	if got := d.register(ctx, operator, wire.ApplicationDescriptor{Name: "opencode", Program: node, Title: "Owned Node presence fixture"}); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	endpoint := fullEndpoint(t, "node-apps-"+d.epoch[:12])
	h, err := listenApplications(endpoint, d, account.Uid, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() {
		cancel()
		h.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("native Applications host did not join")
		}
	}()
	staging := privatetest.Dir(t)
	copyTree(t, filepath.Join(root, "openabstractions-flat", "abstraction-identity", "javascript"), filepath.Join(staging, "node_modules", "@openabstractions", "ipc"))
	connector, err := os.ReadFile(filepath.Join(root, "adopters", "opencode", "runtime-connector.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "runtime-connector.mjs"), connector, 0o600); err != nil {
		t.Fatal(err)
	}
	expected := runtimeExpectation(t)
	config, err := json.Marshal(map[string]any{"endpoint": endpoint, "server": map[string]any{"principalKind": 1, "principal": account.Uid, "program": expected.Program},
		"facade":   fileURL(filepath.Join(root, "openabstractions-flat", "abstraction-facade", "javascript", "index.js")),
		"protocol": fileURL(filepath.Join(root, "openabstractions-flat", "abstraction-facade", "javascript", "js", "abstraction", "facade", "index.mjs"))})
	if err != nil {
		t.Fatal(err)
	}
	script := `import child from 'node:child_process'; import net from 'node:net'; import {syncBuiltinESMExports} from 'node:module';
const deny=()=>{throw new Error('fixture process/network effect refused');};
for(const key of ['spawn','spawnSync','exec','execSync','execFile','execFileSync','fork'])child[key]=deny;
net.connect=net.createConnection=deny;net.Server.prototype.listen=deny;globalThis.fetch=deny;syncBuiltinESMExports();
const config=JSON.parse(process.argv[2]);
const [{Binding},{ApplicationsClient},{runtimeConnector}]=await Promise.all([import(config.facade),import(config.protocol),import('./runtime-connector.mjs')]);
const client=new ApplicationsClient(new Binding(await runtimeConnector(),config.endpoint,{server:config.server,timeout:3000}));
const contexts=[{name:'ses_e94dfb2daffemZukxH7YVqW6JH',title:'',revision:''},{name:'ses_e94dfb2daffemzukxh7yvqw6jh',title:'',revision:''}];
const presence={application:'opencode',instance:'',interfaces:[],contexts,leaseMs:30000n};
const first=await client.announce(presence);if(first.outcome!=='applied')throw new Error('announce '+JSON.stringify(first));
const renewed=await client.announce({...presence,instance:first.instance});if(renewed.outcome!=='applied'||renewed.instance!==first.instance)throw new Error('renewal refused');
const page=await client.observe('',0n);const instance=page.applications.find(v=>v.descriptor.name==='opencode')?.instances.find(v=>v.instance===first.instance);
if(instance?.contexts.length!==2||!contexts.every(c=>instance.contexts.some(v=>v.name===c.name)))throw new Error('native contexts lost');
const refused=await client.announce({...presence,application:'unregistered'});if(refused.outcome!=='forbidden')throw new Error('unknown enrollment accepted');
const withdrawn=await client.withdraw('opencode',first.instance);if(withdrawn.outcome!=='applied')throw new Error('withdrawal refused');
const after=await client.observe('',0n);if(after.applications.find(v=>v.descriptor.name==='opencode')?.instances.length!==0)throw new Error('presence retained');
console.log(JSON.stringify({pid:process.pid,program:process.execPath,contexts:contexts.map(v=>v.name),instance:first.instance,outcome:'native-announce-renew-withdraw'}));`
	scriptPath := filepath.Join(staging, "presence.mjs")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, node, scriptPath, string(config))
	cmd.Dir = staging
	cmd.Env = append(os.Environ(), "ABSTRACTION_IPC_NODE="+addon)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native Node presence: %v\n%s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
	// The same native endpoint binds this Go process as a different program.
	foreign := wire.NewApplicationsClient(listen.FrameClient{Endpoint: endpoint, Server: &expected, Timeout: time.Second, MaxFrame: 1 << 20}.WithContext(ctx))
	if got, err := foreign.Announce(wire.ApplicationPresence{Application: "opencode", LeaseMs: 1000}); err != nil || got.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatalf("foreign native program: %+v %v", got, err)
	}
}

func TestApplicationsLoginUsesOnlyConfirmedDescriptors(t *testing.T) {
	ctx := context.Background()
	dir := privatetest.Dir(t)
	operator := rights.Subject{Account: "account", Program: filepath.Join(dir, "panel.exe")}
	app := rights.Subject{Account: "account", Program: filepath.Join(dir, "editor.exe")}
	d, err := openApplications(dir, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) applicationLoginState {
		t.Helper()
		s, err := d.loginState(ctx)
		if err != nil || s.enabled != want || s.changed == nil {
			t.Fatalf("login snapshot = %+v, %v; want %v", s, err, want)
		}
		return s
	}
	initial := check(false)
	v := wire.ApplicationDescriptor{Name: "editor", Program: app.Program, Title: "Editor", StartGuidance: "Open Editor"}
	if r := d.register(ctx, app, v); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	pending := check(false)
	select {
	case <-initial.changed:
	default:
		t.Fatal("committed registration did not notify")
	}
	if r := d.announce(ctx, app, wire.ApplicationPresence{Application: "editor", LeaseMs: 1000}); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	check(false) // Live presence does not turn a pending registration into Keep.
	if !d.setQuestion(ctx, "editor", app.Program, "keep-question") || !d.keepOwned(ctx, "editor", app.Program, "keep-question") {
		t.Fatal("keep did not commit")
	}
	check(true)
	select {
	case <-pending.changed:
	default:
		t.Fatal("Keep did not notify")
	}
	if r := d.remove(ctx, operator, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	check(false)
	if r := d.register(ctx, operator, v); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	check(true) // The person's operator registration is already confirmed.
	other, err := openApplications(privatetest.Dir(t), "other-account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	otherState, err := other.loginState(ctx)
	if err != nil || otherState.enabled {
		t.Fatal("another account inherited registration", otherState, err)
	}
}

func TestApplicationsLoginFailedPersistenceAndCanceledSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := privatetest.Dir(t)
	d, err := openApplications(dir, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	operator := rights.Subject{Account: "account", Program: filepath.Join(dir, "panel.exe")}
	v := wire.ApplicationDescriptor{Name: "editor", Program: filepath.Join(dir, "editor.exe"), Title: "Editor", StartGuidance: "Open"}
	if r := d.register(ctx, operator, v); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	before, err := d.loginState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A conflicting durable writer refuses removal; login facts stay committed.
	if err := os.WriteFile(filepath.Join(d.dir, "descriptors.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := d.remove(ctx, operator, "editor"); r.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatal("conflicting persistence did not refuse", r)
	}
	after, err := d.loginState(ctx)
	if err != nil || !after.enabled || before.changed != after.changed {
		t.Fatal("failed remove altered login state", after, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.loginState(canceled); err != context.Canceled {
		t.Fatalf("canceled snapshot: %v", err)
	}
}

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
	state := privatetest.Dir(t)
	operator := rights.Subject{Account: "account", Program: filepath.Join(state, "operator.exe")}
	app := rights.Subject{Account: "account", Program: filepath.Join(state, "app.exe")}
	policy := func(_ context.Context, s rights.Subject, action, resource string) (bool, error) {
		switch action {
		case ActionApplicationManage:
			return s.Program == operator.Program, nil
		case ActionApplicationRegisterSelf:
			// The registration default is off for this app.
			return false, nil
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

func TestApplicationRegistrationRejectsShortAliasOfCallingProgram(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DOS short aliases are Windows-specific")
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program = filepath.Clean(program)
	short, hasAlias := shortSubjectProgramAlias(t, program)
	if !hasAlias {
		t.Skip("the filesystem did not provide a distinct DOS short alias")
	}
	state := privatetest.Dir(t)
	// Only application.manage is held: a descriptor naming the caller through
	// its short alias is the caller's own, decided under register_self.
	d, err := openApplications(state, "account", func(_ context.Context, _ rights.Subject, action, _ string) (bool, error) {
		return action == ActionApplicationManage, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := rights.Subject{Account: "account", Program: program}
	descriptor := wire.ApplicationDescriptor{Name: "self-alias", Program: short, Title: "Self", StartGuidance: "Open"}
	if got := d.register(context.Background(), caller, descriptor); got.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatalf("registration through short alias of caller: %+v", got)
	}
}

// APPLICATIONS.md, rights RIGHTS-R22: a program registers its own descriptor
// under application.register_self, which the registration default permits;
// application.manage alone never covers it, register_self never covers
// another program's descriptor, and a self-registration hands its name to the
// runtime so its operators can list and start it.
func TestApplicationSelfRegistrationUnderTheRegistrationDefault(t *testing.T) {
	ctx := context.Background()
	state := privatetest.Dir(t)
	app := rights.Subject{Account: "account", Program: filepath.Join(state, "app.exe")}
	other := filepath.Join(state, "other.exe")
	var mu sync.Mutex
	on := true
	var decided []string
	d, err := openApplications(state, "account", func(_ context.Context, s rights.Subject, action, resource string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		decided = append(decided, action+" "+resource)
		return action == ActionApplicationRegisterSelf && resource == "account" && on && s == app, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var registered []string
	d.registeredSelf = func(name string) { registered = append(registered, name) }
	foreign := wire.ApplicationDescriptor{Name: "foreign", Program: other, Title: "Foreign", StartGuidance: "Open"}
	if r := d.register(ctx, app, foreign); r.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal("register_self covered another program's descriptor", r)
	}
	mu.Lock()
	on = false
	mu.Unlock()
	own := wire.ApplicationDescriptor{Name: "own", Program: app.Program, Title: "Own", StartGuidance: "Open"}
	if r := d.register(ctx, app, own); r.Outcome != wire.ApplicationOutcomeForbidden || len(registered) != 0 {
		t.Fatal("switch off still registered", r, registered)
	}
	mu.Lock()
	on = true
	mu.Unlock()
	if r := d.register(ctx, app, own); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	if len(registered) != 1 || registered[0] != "own" {
		t.Fatal("self-registration did not reach the runtime", registered)
	}
	// The same program registering its name again is a silent update.
	if r := d.register(ctx, app, own); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("second self-registration", r, registered)
	}
	want := []string{ActionApplicationManage + " account", ActionApplicationRegisterSelf + " account", ActionApplicationRegisterSelf + " account", ActionApplicationRegisterSelf + " account"}
	if strings.Join(decided, "|") != strings.Join(want, "|") {
		t.Fatalf("decisions = %v, want %v", decided, want)
	}
}

func TestApplicationRegistrationPersistsNormalizedShortProgramAlias(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DOS short aliases are Windows-specific")
	}
	appDir := filepath.Join(privatetest.Dir(t), "registered program")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	appProgram := filepath.Clean(copyTestBinaryTo(t, appDir, "oa-registered-app"))
	appInput, hasAlias := shortSubjectProgramAlias(t, appProgram)
	if !hasAlias {
		t.Skip("the filesystem did not provide a distinct DOS short alias")
	}
	state := privatetest.Dir(t)
	d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	caller := rights.Subject{Account: "account", Program: filepath.Join(privatetest.Dir(t), "operator.exe")}
	registered := wire.ApplicationDescriptor{Name: "registered-alias", Program: appInput, Title: "App", StartGuidance: "Open"}
	if got := d.register(context.Background(), caller, registered); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("register a distinct application through short alias: %+v", got)
	}
	if got := d.descriptors[registered.Name].Program; got != identity.NormalizeSubjectProgram(appProgram) {
		t.Fatalf("persisted program = %q, want normalized %q", got, appProgram)
	}
}

func TestApplicationDirectoryFiltersHiddenChangesAndRevokedAccess(t *testing.T) {
	ctx := context.Background()
	state := privatetest.Dir(t)
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
	state := privatetest.Dir(t)
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
	program = identity.CanonicalProgramPath(filepath.Clean(program))
	operator := rights.Subject{Account: account.Uid, Program: filepath.Join(privatetest.Dir(t), "operator.exe")}
	app := rights.Subject{Account: account.Uid, Program: program}
	d, err := openApplications(privatetest.Dir(t), account.Uid, func(_ context.Context, s rights.Subject, action, resource string) (bool, error) {
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
	state := privatetest.Dir(t)
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
	state := privatetest.Dir(t)
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
	state := privatetest.Dir(t)
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
	state := privatetest.Dir(t)
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
	state := privatetest.Dir(t)
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
