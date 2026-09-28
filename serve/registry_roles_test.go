package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// No serve test reads the installation this machine actually has: a test that
// wants installation declarations places its own.
func init() {
	installationDeclarationPath = func() (string, error) { return "", nil }
}

// shippedDeclarations is the directory the installer ships the four default
// host declarations from.
func shippedDeclarations(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "installer", "declarations"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// The private source workspace owns installer and generated layer trees. A
// published charter checkout has neither, as declared by split.manifest.
func privateWorkspace(t *testing.T) bool {
	t.Helper()
	marker := filepath.Join("..", "openabstractions-flat")
	info, err := os.Stat(marker)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("private workspace marker %s is not a directory", marker)
	}
	return true
}

// placeInstallationDeclarations copies the shipped declaration files into a
// directory of this test's own and makes the runtime read it.
func placeInstallationDeclarations(t *testing.T, names ...string) string {
	t.Helper()
	shipped := shippedDeclarations(t)
	_, shippedErr := os.Stat(shipped)
	if shippedErr != nil && (!os.IsNotExist(shippedErr) || privateWorkspace(t)) {
		t.Fatal(shippedErr)
	}
	dir := filepath.Join(t.TempDir(), installationDeclarationsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(shipped, name+".json"))
		if err != nil {
			if shippedErr == nil || !os.IsNotExist(err) {
				t.Fatal(err)
			}
			// The published charter checkout has no installer tree. Keep the
			// runtime role tests self-contained using the router's public
			// default-host contract; the byte-for-byte installer check below
			// still runs in the private workspace that owns both packages.
			var found bool
			for _, h := range router.Installed() {
				if h.Name == name {
					file := newHostFile(iwire.HostEntry{Name: name, Kind: name, Base: h.Base}, declaredByInstallation)
					file.DeclaredAt = 0
					raw, err = json.Marshal(file)
					if err != nil {
						t.Fatal(err)
					}
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("no router default host named %q", name)
			}
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

// The installer ships one declaration file for each of the four runtimes the
// router's Installed() names, at the same address, and both packages ship the
// same bytes.
func TestTheInstallerShipsTheFourDefaultHostDeclarations(t *testing.T) {
	windows := shippedDeclarations(t)
	if _, err := os.Stat(windows); os.IsNotExist(err) {
		if privateWorkspace(t) {
			t.Fatalf("private workspace is missing installer declarations at %s", windows)
		}
		t.Skip("installer/posix declaration byte-identity requires the private installer tree")
	} else if err != nil {
		t.Fatal(err)
	}
	posix := filepath.Join(filepath.Dir(windows), "posix", "declarations")
	for _, h := range router.Installed() {
		raw, err := os.ReadFile(filepath.Join(windows, h.Name+".json"))
		if err != nil {
			t.Fatalf("the installer ships no declaration for %s: %v", h.Name, err)
		}
		var f providerFile
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", h.Name, err)
		}
		d := f.Declaration
		if f.Version != providerFileVersion || d.role() != wire.DeclarationRoleHost || d.Host == nil {
			t.Fatalf("%s is not a version %d host declaration: %+v", h.Name, providerFileVersion, f)
		}
		if d.Host.Base != h.Base || d.Host.Kind != h.Name || d.Host.Hosted {
			t.Fatalf("%s declares %+v, Installed() says %s", h.Name, d.Host, h.Base)
		}
		if field := validProviderDeclaration(d); field != "" {
			t.Fatalf("%s: invalid %s", h.Name, field)
		}
		other, err := os.ReadFile(filepath.Join(posix, h.Name+".json"))
		if err != nil || string(other) != string(raw) {
			t.Fatalf("the Linux and macOS package ships different bytes for %s: %v", h.Name, err)
		}
	}
	// Every other shipped file is a provider declaration the installer's
	// optional features carry (local-stores discovery today); hosts are
	// exactly Installed().
	entries, err := os.ReadDir(windows)
	if err != nil {
		t.Fatal(err)
	}
	hosts := 0
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(windows, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var f providerFile
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		switch f.Declaration.role() {
		case wire.DeclarationRoleHost:
			hosts++
		case wire.DeclarationRoleProvider:
			if field := validProviderDeclaration(f.Declaration); field != "" && field != "program" {
				t.Fatalf("%s: invalid %s", e.Name(), field)
			}
		default:
			t.Fatalf("%s ships with role %v", e.Name(), f.Declaration.role())
		}
	}
	if hosts != len(router.Installed()) {
		t.Fatalf("the installer ships %d host declarations, Installed() names %d", hosts, len(router.Installed()))
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
	p, err := openProviders(state, false, report)
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
	p, err := openProviders(state, false, report)
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
	reopened, err := openProviders(state, false, report)
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
	placeInstallationDeclarations(t, "ollama", "lemonade")
	state := t.TempDir()
	report := func(err error) { t.Errorf("providers reported %v", err) }
	p, err := openProviders(state, true, report)
	if err != nil {
		t.Fatal(err)
	}
	saved := declarationEnv
	declarationEnv = fixtureDeclarations(nil, map[string]string{"OLLAMA_HOST": "127.0.0.1:9999"})
	t.Cleanup(func() { declarationEnv = saved })
	p.forgetProducts()
	byName := map[string]providerFile{}
	for _, f := range p.hostFiles() {
		byName[f.Declaration.Name] = f
	}
	if f := byName["ollama"]; f.DeclaredBy != "ollama" || f.Declaration.Host.Base != "http://127.0.0.1:9999" {
		t.Fatalf("a product record did not shadow the installation: %+v", f)
	}
	if f := byName["lemonade"]; f.DeclaredBy != declaredByInstallation {
		t.Fatalf("lemonade %+v", f)
	}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	entry := iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:1", Profiles: []string{"chat"}}
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
	p, err := openProviders(state, false, report)
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
		{Name: "local-stores", Program: absoluteFixtureProgram(), Arguments: []string{}, Endpoint: "local-stores", Transport: transportNative,
			Contracts: []string{storageInventorySource}, Resources: []string{"store:ollama"}, Activation: wire.ActivationOnDemand.String()},
		{Name: "far", Program: "", Arguments: []string{}, Endpoint: "tls://far.invalid:7443", Transport: transportRemote,
			Contracts: slices.Clone(remoteContracts), Activation: wire.ActivationRemote.String(),
			Remote: &inferenceRemoteTrust{ServerName: "far.invalid", Roots: absoluteFixtureProgram(), Certificate: absoluteFixtureProgram(), Key: absoluteFixtureProgram()}},
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
