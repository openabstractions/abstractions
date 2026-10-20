package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// No serve test reads the installation this machine actually has: a test that
// wants installation declarations places its own.
func init() {
	installationDeclarationPath = func() (string, error) { return "", nil }
}

// shippedDeclarations holds the OA provider declarations supplied by optional
// installer features.
func shippedDeclarations(t *testing.T) string {
	t.Helper()
	var dir string
	// A published checkout still runs the owned fallback-host assertions below.
	// Only its private shipped-source lookup is skipped.
	t.Run("private-shipped-source", func(t *testing.T) {
		privateInput(t, "..", "openabstractions-flat")
		dir = privateInput(t, "..", "installer", "declarations")
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("private installer declarations %s are not a directory: %v", dir, err)
		}
	})
	return dir
}

// privateWorkspace preserves the caller's choice between a published skip and
// an unexpected fixture failure. Only the private marker lookup may skip.
func privateWorkspace(t *testing.T) bool {
	t.Helper()
	found := false
	t.Run("private-workspace-source", func(t *testing.T) {
		marker := privateInput(t, "..", "openabstractions-flat")
		info, err := os.Stat(marker)
		if err != nil || !info.IsDir() {
			t.Fatalf("private workspace marker %s is not a directory: %v", marker, err)
		}
		found = true
	})
	return found
}

// placeInstallationDeclarations copies the shipped declaration files into a
// directory of this test's own and makes the runtime read it.
func placeInstallationDeclarations(t *testing.T, names ...string) string {
	t.Helper()
	shipped := shippedDeclarations(t)
	dir := filepath.Join(t.TempDir(), installationDeclarationsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		var raw []byte
		err := os.ErrNotExist
		if shipped != "" {
			raw, err = os.ReadFile(filepath.Join(shipped, name+".json"))
		}
		if os.IsNotExist(err) {
			// These tests explicitly install fixture host declarations to exercise
			// precedence and withdrawal. The OA package supplies none of them.
			bases := map[string]string{"lemonade": router.LemonadeDefaultBase, "lmstudio": router.LMStudioDefaultBase,
				"ollama": router.OllamaDefaultBase, "comfyui": router.ComfyUIDefaultBase}
			base, ok := bases[name]
			if !ok {
				t.Fatalf("no fixture declaration named %q", name)
			}
			file := newHostFile(iwire.HostEntry{Name: name, Kind: name, Base: base}, declaredByInstallation)
			file.DeclaredAt = 0
			raw, err = json.Marshal(file)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	saved := installationDeclarationPath
	installationDeclarationPath = func() (string, error) { return dir, nil }
	t.Cleanup(func() { installationDeclarationPath = saved })
	return dir
}

// Generic OA installation ships its own providers, with no presumed third-party
// HTTP servers. An explicitly installed host declaration remains supported.
func TestTheInstallerShipsOnlyItsOwnProviderDeclarations(t *testing.T) {
	privateInput(t, "..", "openabstractions-flat")
	windows := privateInput(t, "..", "installer", "declarations")
	for _, dir := range []string{windows, filepath.Join(filepath.Dir(windows), "posix", "declarations")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		localStores := false
		for _, entry := range entries {
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			f := authoredInstallerSpec(t, raw)
			if len(f.Providers) == 0 || len(f.Servers)+len(f.Remotes)+len(f.Applications) != 0 {
				t.Fatalf("generic install assumes third-party host: %s", entry.Name())
			}
			for _, provider := range f.Providers {
				if provider.Name == "local-stores" && f.Program != "" {
					localStores = true
				}
			}
		}
		if !localStores {
			t.Fatalf("%s lost the OA local-stores provider", dir)
		}
	}
}

// Registration is independent from reachability: a clean machine has no fake
// hosts, and an explicitly saved offline host retains its exact operator identity.
func TestCleanInstallHasNoAssumedHostsAndRetainsExplicitOfflineHost(t *testing.T) {
	placeInstallationDeclarations(t)
	p, err := openFixtureProviders(t, t.TempDir(), true, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	files, revision, err := p.read()
	if err != nil || len(files) != 0 {
		t.Fatalf("clean registration: %+v %v", files, err)
	}
	entry := iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:45678"}
	if change := p.add(revision, newHostFile(entry, declaredByOperator)); change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("save offline host: %+v", change)
	}
	states, _, err := p.list()
	if err != nil || len(states) != 1 {
		t.Fatalf("offline registration: %+v %v", states, err)
	}
	if state := states[0]; state.Declaration.Name != entry.Name || state.DeclaredBy != declaredByOperator || state.Readiness != wire.DeclarationReadinessUnreachable {
		t.Fatalf("offline operator host lost: %+v", state)
	}
}

// Reset removes the selected account settings. Reopening the ordinary runtime
// must not reconstruct the four old port presets as registrations, and it must
// still read an actually installed on-demand provider without activating it.
func TestFreshAndResetRegistriesHaveNoAssumedThirdPartyHosts(t *testing.T) {
	saved := declarationEnv
	declarationEnv = fixtureDeclarations(nil, nil)
	t.Cleanup(func() { declarationEnv = saved })
	installation := placeInstallationDeclarations(t)
	program := filepath.Join(filepath.Dir(installation), "managed-provider")
	resetWrite(t, program, "inert installed program fixture")
	writeBundledDeclaration(t, installation, "managed", "managed-provider", "managed-fixture", []string{})
	roots, _ := resetFixture(t)
	state := filepath.Join(roots.Local, layout.Name, "runtime-v1")
	model := filepath.Join(roots.Local, layout.Name, "models", "model.bin")
	resetWrite(t, model, "owned model fixture")
	open := func() *runtimeProviders {
		t.Helper()
		config, err := loadInferenceHosts(filepath.Join(state, "inference", inferenceHostsFile))
		if err != nil {
			t.Fatal(err)
		}
		p, err := openFixtureRuntimeProviders(t, runtimeFlags{}, state, config, func(err error) { t.Error(err) })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		return p
	}
	checkEmpty := func(p *runtimeProviders) {
		t.Helper()
		if hosts := p.hostFiles(); len(hosts) != 0 {
			t.Fatalf("uninstalled hosts registered: %+v", hosts)
		}
		files, _, err := p.read()
		if err != nil || len(files) != 1 || files[0].Declaration.Name != "managed" ||
			files[0].Declaration.Program != program || files[0].DeclaredBy != declaredByInstallation ||
			files[0].Declaration.Activation != wire.ActivationOnDemand.String() {
			t.Fatalf("installed managed provider lost: %+v %v", files, err)
		}
	}
	p := open()
	checkEmpty(p)
	for name, base := range map[string]string{"comfyui": router.ComfyUIDefaultBase, "lemonade": router.LemonadeDefaultBase,
		"lmstudio": router.LMStudioDefaultBase, "ollama": router.OllamaDefaultBase} {
		_, revision, err := p.read()
		if err != nil {
			t.Fatal(err)
		}
		entry := iwire.HostEntry{Name: name, Kind: name, Base: base}
		if change := p.add(revision, newHostFile(entry, declaredByOperator)); change.Outcome != wire.DeclarationEditOutcomeApplied {
			t.Fatalf("explicit offline host %s: %+v", name, change)
		}
	}
	for _, host := range p.hostStates(p.hostFiles()) {
		if host.DeclaredBy != declaredByOperator || host.Readiness != wire.DeclarationReadinessUnreachable {
			t.Fatalf("offline configuration changed: %+v", host)
		}
	}
	if len(p.hostFiles()) != 4 {
		t.Fatal("explicit offline hosts were removed without a reset")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	paths, err := settingsPaths(roots)
	if err != nil {
		t.Fatal(err)
	}
	if err := moveSettings(context.Background(), paths, "host-fixture", os.Rename, io.Discard); err != nil {
		t.Fatal(err)
	}
	checkEmpty(open())
	resetRead(t, model, "owned model fixture")
}

func TestResetJournalExceptionDoesNotHideInvalidDeclarations(t *testing.T) {
	dir := t.TempDir()
	resetWrite(t, filepath.Join(dir, "ollama.json.oa-reset-fixture.manifest.json"), `{"version":1,"entries":[]}`)
	var reports []error
	p := &runtimeProviders{report: func(err error) { reports = append(reports, err) }}
	if files, err := p.readDeclarationDir(dir, nil, false); err != nil || len(files) != 0 || len(reports) != 0 {
		t.Fatalf("account reset journal: %+v %v %v", files, err, reports)
	}
	resetWrite(t, filepath.Join(dir, "unrelated.manifest.json"), "invalid JSON")
	if _, err := p.readDeclarationDir(dir, nil, false); err != nil || len(reports) != 1 {
		t.Fatalf("unrelated invalid file was hidden: %v %v", err, reports)
	}
	reports = nil
	if _, err := p.readDeclarationDir(dir, nil, true); err != nil || len(reports) != 2 {
		t.Fatalf("installation reader lost strict diagnostics: %v %v", err, reports)
	}
}

// Each role validates its own fields: a host carries an engine and no OA
// program, endpoint or contract, and a provider carries no engine.
func TestEachRoleValidatesItsOwnFields(t *testing.T) {
	host := func(change func(*providerDeclaration)) providerDeclaration {
		d := hostDeclaration(iwire.HostEntry{Name: "lab", Hosted: true, Kind: router.WireOpenAICompatible, Base: "https://lab.invalid/v1",
			Credential: "lab-key", Profiles: []string{"chat"}})
		if change != nil {
			change(&d)
		}
		return d
	}
	provider := func(change func(*providerDeclaration)) providerDeclaration {
		d := providerDeclaration{Name: "chatty", Program: absoluteFixtureProgram(), Arguments: []string{}, Endpoint: "chatty",
			Transport: transportNative, Contracts: []string{"abstraction.inference/chat@1"}, Activation: wire.ActivationAttach.String()}
		if change != nil {
			change(&d)
		}
		return d
	}
	for _, c := range []struct {
		name  string
		d     providerDeclaration
		field string
	}{
		{"a hosted engine", host(nil), ""},
		{"a local engine", hostDeclaration(iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:11434"}), ""},
		{"a host with a program", host(func(d *providerDeclaration) { d.Program = absoluteFixtureProgram() }), "program"},
		{"a host with an endpoint", host(func(d *providerDeclaration) { d.Endpoint = "lab" }), "endpoint"},
		{"a host with a contract", host(func(d *providerDeclaration) { d.Contracts = []string{"abstraction.inference/chat@1"} }), "contracts"},
		{"a host with a model", host(func(d *providerDeclaration) { d.Models = []string{"llama"} }), "models"},
		{"a host that is launched", host(func(d *providerDeclaration) { d.Activation = wire.ActivationOnDemand.String() }), "activation"},
		{"a host without an engine", host(func(d *providerDeclaration) { d.Host = nil }), "host"},
		{"a host at no address", host(func(d *providerDeclaration) { d.Host.Base = "http://lab.invalid/v1" }), "host"},
		{"a host of no wire", host(func(d *providerDeclaration) { d.Host.Kind = "invented" }), "host"},
		{"a local engine with a credential", hostDeclaration(iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:11434", Credential: "k"}), "host"},
		{"a host of no profile", host(func(d *providerDeclaration) { d.Resources = []string{"profile:invented@1"} }), "resources"},
		{"a host whose role denies its transport", host(func(d *providerDeclaration) { d.Transport = transportNative }), "role"},
		{"a provider", provider(nil), ""},
		{"a provider with an engine", provider(func(d *providerDeclaration) { d.Host = &providerHost{Base: "http://127.0.0.1:1", Kind: "ollama"} }), "host"},
		{"a provider that claims the host role", provider(func(d *providerDeclaration) { d.Role = wire.DeclarationRoleHost.String() }), "role"},
		{"a provider that claims the remote role", provider(func(d *providerDeclaration) { d.Role = wire.DeclarationRoleRemote.String() }), "role"},
		{"a declaration of an unknown role", provider(func(d *providerDeclaration) { d.Role = "engine" }), "role"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if field := validProviderDeclaration(c.d); field != c.field {
				t.Fatalf("invalid %q, want %q", field, c.field)
			}
		})
	}
}

// absoluteFixtureProgram is an absolute program path on this platform.
func absoluteFixtureProgram() string {
	if os.PathSeparator == '\\' {
		return `C:\fixture\chatty.exe`
	}
	return "/fixture/chatty"
}

// A state directory whose hosts.json carries the operator's local and hosted
// entries starts on this build: each becomes a declaration of role host with
// its declared_by kept, hosts.json keeps no entry, and a second start
// rewrites nothing.
func TestHostsJSONEntriesBecomeHostDeclarationsOnce(t *testing.T) {
	state := t.TempDir()
	hostsPath := filepath.Join(state, "inference", inferenceHostsFile)
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{
  "local": [{"kind":"ollama","base":"http://127.0.0.1:11434","declared_by":"ollama"},
            {"kind":"comfyui","base":"http://127.0.0.1:8188","profiles":["image"],"declared_by":"operator"}],
  "hosted": [{"name":"lab","base":"https://lab.invalid/v1","wire":"openai-compatible","credential":"lab-key","profiles":["chat"],"declared_by":"operator"}],
  "ceilings": {"lab-key": {"tokens_per_day": 1000}}
}
`
	if err := os.WriteFile(hostsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	report := func(err error) { t.Errorf("migration reported %v", err) }
	if err := migrateProviderState(state, report); err != nil {
		t.Fatal(err)
	}
	p, err := openFixtureProviders(t, state, false, report)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]providerFile{}
	for _, f := range p.hostFiles() {
		byName[f.Declaration.Name] = f
	}
	if len(byName) != 3 {
		t.Fatalf("the migration made %d host declarations: %+v", len(byName), byName)
	}
	if f := byName["ollama"]; f.DeclaredBy != "ollama" || f.Declaration.Host.Base != "http://127.0.0.1:11434" || f.Declaration.Host.Hosted {
		t.Fatalf("ollama %+v", f)
	}
	if f := byName["comfyui"]; f.DeclaredBy != declaredByOperator || !slices.Equal(f.Declaration.Resources, []string{"profile:image"}) {
		t.Fatalf("comfyui %+v", f)
	}
	lab := byName["lab"]
	if lab.DeclaredBy != declaredByOperator || !lab.Declaration.Host.Hosted || lab.Declaration.Host.Credential != "lab-key" ||
		lab.Declaration.Host.Ceiling == nil || lab.Declaration.Host.Ceiling.TokensPerDay != 1000 {
		t.Fatalf("lab %+v", lab)
	}
	// hosts.json keeps the ceilings and the declarations switch, and no entry.
	config, err := loadInferenceHosts(hostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.Local == nil || len(*config.Local) != 0 || len(config.Hosted) != 0 || config.Declared == nil || *config.Declared {
		t.Fatalf("hosts.json after the migration %+v", config)
	}
	if config.Ceilings["lab-key"].TokensPerDay != 1000 {
		t.Fatalf("the ceilings left hosts.json: %+v", config.Ceilings)
	}
	// The router reaches exactly the three declared hosts.
	var names []string
	for _, h := range p.hostRouterHosts() {
		names = append(names, h.Name+"/"+h.DeclaredBy)
	}
	slices.Sort(names)
	if strings.Join(names, " ") != "comfyui/operator lab/operator ollama/ollama" {
		t.Fatalf("router hosts %v", names)
	}
	// A second start rewrites nothing.
	before := map[string][]byte{}
	files, _ := os.ReadDir(filepath.Join(state, providersDir))
	for _, e := range files {
		raw, err := os.ReadFile(filepath.Join(state, providersDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		before[e.Name()] = raw
	}
	raw, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatal(err)
	}
	before[inferenceHostsFile] = raw
	if err := migrateProviderState(state, report); err != nil {
		t.Fatal(err)
	}
	for name, was := range before {
		path := filepath.Join(state, providersDir, name)
		if name == inferenceHostsFile {
			path = hostsPath
		}
		now, err := os.ReadFile(path)
		if err != nil || string(now) != string(was) {
			t.Fatalf("%s changed on the second start: %v\nbefore:\n%s\nafter:\n%s", name, err, was, now)
		}
	}
}

// The runtime reads the installation's declarations, lists them as declared
// by the installation, disables one an operator withdraws rather than
// removing it, and a reinstall that writes the file again leaves it disabled.
func TestAnInstallationDeclarationIsDisabledAndNotResurrected(t *testing.T) {
	dir := placeInstallationDeclarations(t, "lemonade", "ollama", "comfyui")
	state := t.TempDir()
	report := func(err error) { t.Errorf("providers reported %v", err) }
	p, err := openFixtureProviders(t, state, false, report)
	if err != nil {
		t.Fatal(err)
	}
	listed := func() map[string]wire.DeclarationState {
		out := map[string]wire.DeclarationState{}
		states, _, err := p.list()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range states {
			out[s.Declaration.Name] = s
		}
		return out
	}
	states := listed()
	if len(states) != 3 {
		t.Fatalf("the installation declared %d: %+v", len(states), states)
	}
	for _, name := range []string{"lemonade", "ollama", "comfyui"} {
		s := states[name]
		if s.DeclaredBy != declaredByInstallation || s.Role != wire.DeclarationRoleHost {
			t.Fatalf("%s %+v", name, s)
		}
	}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	change := p.remove(revision, "ollama")
	if change.Outcome != wire.DeclarationEditOutcomeApplied || change.Reason != "disabled" {
		t.Fatalf("withdrawing an installation declaration %+v", change)
	}
	// The file is still the installation's, and the runtime reaches it no more.
	if _, err := os.Stat(filepath.Join(dir, "ollama.json")); err != nil {
		t.Fatalf("the withdrawal deleted the installation's file: %v", err)
	}
	if s := listed()["ollama"]; s.Readiness != wire.DeclarationReadinessDisabled || s.Why != "operator" {
		t.Fatalf("ollama after the withdrawal %+v", s)
	}
	for _, h := range p.hostRouterHosts() {
		if h.Name == "ollama" {
			t.Fatal("the router still reaches a withdrawn host")
		}
	}
	// A reinstall rewrites the file; a restart still reads it as disabled.
	raw, err := os.ReadFile(filepath.Join(dir, "ollama.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ollama.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := openFixtureProviders(t, state, false, report)
	if err != nil {
		t.Fatal(err)
	}
	after := map[string]wire.DeclarationState{}
	reopenedStates, _, err := reopened.list()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range reopenedStates {
		after[s.Declaration.Name] = s
	}
	if s := after["ollama"]; s.Readiness != wire.DeclarationReadinessDisabled {
		t.Fatalf("a reinstall resurrected a withdrawn declaration: %+v", s)
	}
	// Declaring the name again enables it, and the operator's own declaration
	// shadows the installation's.
	_, revision, err = reopened.read()
	if err != nil {
		t.Fatal(err)
	}
	entry := iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:1234", Profiles: []string{"chat"}}
	if change := reopened.add(revision, newHostFile(entry, declaredByOperator)); change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("declaring a disabled name again %+v", change)
	}
	reopenedStates, _, err = reopened.list()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range reopenedStates {
		if s.Declaration.Name != "ollama" {
			continue
		}
		if s.Readiness == wire.DeclarationReadinessDisabled || s.DeclaredBy != declaredByOperator || s.Declaration.Host.Base != "http://127.0.0.1:1234" {
			t.Fatalf("ollama after the operator declared it %+v", s)
		}
	}
}

// A product record shadows the installation's default address, and an
// operator's declaration shadows both.
func TestADeclarationShadowsTheOneBelowIt(t *testing.T) {
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("native product precedence requires loopback Program proof")
	}
	engine := startGatewayEngine(t)
	placeInstallationDeclarations(t, "lmstudio", "lemonade")
	state := t.TempDir()
	report := func(err error) { t.Errorf("providers reported %v", err) }
	p, err := openFixtureProviders(t, state, true, report)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	useGatewayProductRecord(t, engine.home)
	p.forgetProducts()
	byName := map[string]providerFile{}
	for _, f := range p.hostFiles() {
		byName[f.Declaration.Name] = f
	}
	if f := byName["lmstudio"]; f.DeclaredBy != "lmstudio" || f.Declaration.Host.Base != engine.base ||
		f.Source != declarationSourceProduct || f.ProductPresence != "verified" || !identity.SameSubjectProgram(f.ProductProgram, engine.program) {
		t.Fatalf("a product record did not shadow the installation: %+v", f)
	}
	if f := byName["lemonade"]; f.DeclaredBy != declaredByInstallation {
		t.Fatalf("lemonade %+v", f)
	}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	entry := iwire.HostEntry{Name: "lmstudio", Kind: "lmstudio", Base: "http://127.0.0.1:1", Profiles: []string{"chat"}}
	if change := p.add(revision, newHostFile(entry, declaredByOperator)); change.Outcome != wire.DeclarationEditOutcomeConflict || change.Reason != "name" {
		t.Fatalf("declaring over a product's host %+v", change)
	}
}

// provider list prints one directory holding the three roles, each with the
// provenance the runtime asserts for it.
func TestProviderListPrintsTheThreeRolesAndTheirProvenance(t *testing.T) {
	placeInstallationDeclarations(t, "lemonade", "comfyui")
	state := t.TempDir()
	report := func(err error) { t.Errorf("providers reported %v", err) }
	p, err := openFixtureProviders(t, state, false, report)
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	// An operator's hosted host, a provider and a remote runtime.
	hosted := iwire.HostEntry{Name: "lab", Hosted: true, Kind: router.WireOpenAICompatible, Base: "https://lab.invalid/v1",
		Credential: "lab-key", Profiles: []string{"chat"}}
	if change := p.add(revision, newHostFile(hosted, declaredByOperator)); change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("declaring a hosted host %+v", change)
	}
	for _, d := range []providerDeclaration{
		{Name: "local-stores", Program: presentFixtureProgram(), Arguments: []string{}, Endpoint: "local-stores", Transport: transportNative,
			Contracts: []string{storageInventorySource}, Resources: []string{"store:ollama"}, Activation: wire.ActivationOnDemand.String()},
		{Name: "far", Program: "", Arguments: []string{}, Endpoint: "tls://far.invalid:7443", Transport: transportRemote,
			Contracts: slices.Clone(remoteContracts), Activation: wire.ActivationRemote.String(),
			Remote: &inferenceRemoteTrust{ServerName: "far.invalid", Roots: absoluteFixtureProgram(), Certificate: absoluteFixtureProgram(), Key: absoluteFixtureProgram(), ServerKeySHA256: strings.Repeat("a", 64)}},
	} {
		if field := validProviderDeclaration(d); field != "" {
			t.Fatalf("%s: invalid %s", d.Name, field)
		}
		_, revision, err = p.read()
		if err != nil {
			t.Fatal(err)
		}
		file := providerFile{Version: providerFileVersion, Declaration: d, DeclaredBy: "/opt/oa/openabstractions", DeclaredAt: 1789000000000}
		if change := p.add(revision, file); change.Outcome != wire.DeclarationEditOutcomeApplied {
			t.Fatalf("declaring %s %+v", d.Name, change)
		}
	}
	_, revision, err = p.read()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	states, revision, err := p.list()
	if err != nil {
		t.Fatal(err)
	}
	if err := printDeclarations(&out, wire.DeclarationList{Outcome: wire.DeclarationListOutcomePage, Revision: revision, Declarations: states}); err != nil {
		t.Fatal(err)
	}
	t.Logf("provider list\n%s", out.String())
	printed := out.String()
	for _, want := range []string{"comfyui", "far", "lab", "lemonade", "local-stores", "provider", "host", "remote", "installation"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("provider list lacks %q:\n%s", want, printed)
		}
	}
}
