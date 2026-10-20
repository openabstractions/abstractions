package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// No serve test reads a real product record: every runtime a test composes
// probes an empty fixture machine unless the test sets its own, or composes
// through isolatedRuntime's ProductHosts option (supervised_test.go), which
// rebinds declarationEnv to defaultDeclarationEnv for one runtime.
func init() { declarationEnv = fixtureDeclarations(nil, nil) }

func fixtureDeclarations(files, env map[string]string) func(func(error)) router.ProbeEnv {
	return func(report func(error)) router.ProbeEnv {
		return router.ProbeEnv{GOOS: "linux", Home: "/fixture-home",
			Getenv: func(name string) string { return env[name] },
			ReadFile: func(path string) ([]byte, error) {
				if body, ok := files[path]; ok {
					return []byte(body), nil
				}
				return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
			},
			Status: func(context.Context, string, ...string) ([]byte, error) {
				return nil, errors.New("not on the fixture machine")
			},
			Log: func(product, reason string) {
				if report != nil {
					report(errors.New(product + ": " + reason))
				}
			}}
	}
}

// A runtime without local entries lists the hosts the products declare and
// the ones the installation declares, each with its declared_by; withdrawing
// an installed host disables it by name and keeps the rest.
func TestRuntimeDefaultListsProductDeclaredHosts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[],"models":[]}`))
	}))
	defer server.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	saved := declarationEnv
	declarationEnv = fixtureDeclarations(map[string]string{"/fixture-home/.lmstudio/.internal/http-server-config.json": `{"port":` + port + `}`},
		map[string]string{"OLLAMA_HOST": server.URL})
	t.Cleanup(func() { declarationEnv = saved })
	placeInstallationDeclarations(t, "lemonade", "lmstudio", "ollama", "comfyui")
	// Exercise default discovery at its actual constructor with fixture state,
	// without opening an account runtime or relying on a private-runtime waiver.
	p, err := openFixtureRuntimeProviders(t, runtimeFlags{}, t.TempDir(), inferenceHosts{}, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	byName := map[string]iwire.HostEntry{}
	for _, f := range p.hostFiles() {
		byName[f.Declaration.Name] = f.hostEntry()
	}
	ollamaBy, ollamaBase, studioBy, studioBase := "ollama", server.URL, "lmstudio", server.URL
	if !identity.LoopbackCeiling().Bindable && runtime.GOOS != "darwin" {
		ollamaBy, ollamaBase, studioBy, studioBase = "installation", router.OllamaDefaultBase, "installation", router.LMStudioDefaultBase
	}
	if h := byName["ollama"]; h.DeclaredBy != ollamaBy || h.Base != ollamaBase {
		t.Fatalf("ollama %+v", h)
	}
	if h := byName["lmstudio"]; h.DeclaredBy != studioBy || h.Base != studioBase {
		t.Fatalf("lmstudio %+v", h)
	}
	// Lemonade and ComfyUI record no address: the installation declares them.
	if h := byName["lemonade"]; h.DeclaredBy != "installation" || h.Base != router.LemonadeDefaultBase {
		t.Fatalf("lemonade %+v", h)
	}
	if h := byName["comfyui"]; h.DeclaredBy != "installation" || h.Base != router.ComfyUIDefaultBase {
		t.Fatalf("comfyui %+v", h)
	}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	if result := p.remove(revision, "lemonade"); result.Outcome != fwire.DeclarationEditOutcomeApplied {
		t.Fatal(result)
	}
	var names []string
	files := p.hostFiles()
	for _, f := range files {
		if !f.Disabled {
			names = append(names, f.Declaration.Name+"/"+f.DeclaredBy)
		}
	}
	slices.Sort(names)
	if strings.Join(names, " ") != "comfyui/installation lmstudio/"+studioBy+" ollama/"+ollamaBy {
		t.Fatalf("after removing lemonade: %v", names)
	}
	// The withdrawal is recorded, not performed: the installation's file
	// stays, and the registry lists it disabled.
	for _, s := range p.hostStates(files) {
		if s.Declaration.Name == "lemonade" && (s.Readiness != fwire.DeclarationReadinessDisabled || s.DeclaredBy != "installation") {
			t.Fatalf("lemonade after withdrawal %+v", s)
		}
	}
}

func TestProductPromotionRequiresLiveServerAndPreservesSavedDeclarations(t *testing.T) {
	if !identity.LoopbackCeiling().Bindable && runtime.GOOS != "darwin" {
		t.Skip("current listener-program identity is unsupported on this platform")
	}
	var listings atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		listings.Add(1)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	saved := declarationEnv
	declarationEnv = fixtureDeclarations(map[string]string{"/fixture-home/.lmstudio/.internal/http-server-config.json": `{"port":` + port + `}`}, nil)
	t.Cleanup(func() { declarationEnv = saved })
	_, installed := newInstallationDir(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeBundledDeclaration(t, installed, "managed", program, "managed", nil)
	p, err := openFixtureRuntimeProviders(t, runtimeFlags{}, t.TempDir(), inferenceHosts{}, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	files, revision, err := p.read()
	if err != nil || !slices.ContainsFunc(files, func(f providerFile) bool {
		return f.Declaration.Name == "lmstudio" && f.Source == declarationSourceProduct
	}) {
		t.Fatal("live empty listing was not promoted", files, err)
	}
	if !slices.ContainsFunc(files, func(f providerFile) bool {
		return f.Declaration.Name == "managed" && f.Declaration.Activation == "on_demand" && f.Source == declarationSourceInstallation
	}) {
		t.Fatal("on-demand installation lost", files)
	}
	// An explicit offline connection remains a person's saved registration.
	if result := p.add(revision, newHostFile(iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: server.URL}, "operator")); result.Outcome != fwire.DeclarationEditOutcomeApplied {
		t.Fatal(result)
	}
	if err := p.disable("lmstudio"); err != nil {
		t.Fatal(err)
	}
	if files := p.hostFiles(); !slices.ContainsFunc(files, func(f providerFile) bool { return f.Declaration.Name == "lmstudio" && f.Disabled }) {
		t.Fatal("live discovery bypassed disabled name", files)
	}
	if err := p.enable("lmstudio"); err != nil {
		t.Fatal(err)
	}
	surveyAt := time.Now().Add(-10 * time.Second)
	p.routerHostSnapshot = func() ([]router.HostState, time.Time) {
		return []router.HostState{{Host: "lmstudio", Base: server.URL, DeclaredBy: router.DeclaredByLMStudio, Up: false}}, surveyAt
	}
	before := listings.Load()
	for range 2 {
		p.forgetProducts()
		if got := p.productDeclarations(); len(got) != 0 || !p.productAt.Equal(surveyAt) || listings.Load() != before {
			t.Fatal("same-owner negative replay changed evidence or extended its lifetime", got, p.productAt, surveyAt, listings.Load()-before)
		}
	}
	// An unowned reading or an operator shadow is independent evidence. It
	// cannot suppress a fresh, process-bound listing of this product.
	for _, owner := range []string{"", "operator"} {
		p.routerHostSnapshot = func() ([]router.HostState, time.Time) {
			return []router.HostState{{Host: "lmstudio", Base: server.URL, DeclaredBy: owner, Up: false}}, surveyAt
		}
		p.forgetProducts()
		before := listings.Load()
		got := p.productDeclarations()
		if len(got) != 1 || got[0].DeclaredBy != router.DeclaredByLMStudio || !p.productAt.After(surveyAt) || listings.Load() <= before {
			t.Fatalf("owner %q suppressed fresh product evidence: %+v %v", owner, got, p.productAt)
		}
		if runtime.GOOS != "darwin" && (got[0].ProductPresence != "verified" || got[0].ProductProgram == "") {
			t.Fatalf("owner %q bypassed native product proof: %+v", owner, got)
		}
	}
	p.routerHostSnapshot = nil
	server.Close() // Product record remains after shutdown or uninstall.
	p.forgetProducts()
	files, _, err = p.read()
	if err != nil || slices.ContainsFunc(files, func(f providerFile) bool { return f.Declaration.Name == "lmstudio" }) {
		t.Fatal("stale product file registered an absent server", files, err)
	}
	for _, name := range []string{"ollama", "managed"} {
		if !slices.ContainsFunc(files, func(f providerFile) bool { return f.Declaration.Name == name }) {
			t.Fatal("offline saved or on-demand provider lost", name, files)
		}
	}
	// A fresh runtime sees the surviving file and still registers no product.
	fresh, err := openFixtureRuntimeProviders(t, runtimeFlags{}, t.TempDir(), inferenceHosts{}, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if files := fresh.hostFiles(); slices.ContainsFunc(files, func(f providerFile) bool { return f.Declaration.Name == "lmstudio" }) {
		t.Fatal("stale file was promoted at construction", files)
	}
}

func TestProductPromotionTimeoutIsolationAndSurveyReuse(t *testing.T) {
	if !identity.LoopbackCeiling().Bindable && runtime.GOOS != "darwin" {
		t.Skip("current listener-program identity is unsupported on this platform")
	}
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer live.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	candidates := []*router.Host{router.LMStudio(live.URL), router.Ollama(slow.URL)}
	// Native listener identity can take longer than a localhost HTTP round trip.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	got, _ := liveProductHosts(ctx, candidates, nil)
	if len(got) != 1 || got[0].Host.Name != "lmstudio" || time.Since(started) > 1750*time.Millisecond {
		t.Fatal("slow product discarded fast listing or exceeded batch", got, time.Since(started))
	}
	// An ordinary Up reading lacks process provenance and is freshly listed.
	surveyAt := time.Now().Add(-10 * time.Second)
	snapshot := func() ([]router.HostState, time.Time) {
		return []router.HostState{{Host: "lmstudio", Base: live.URL, Up: true}}, surveyAt
	}
	if got, observed := liveProductHosts(context.Background(), candidates[:1], snapshot); len(got) != 1 || !observed.After(surveyAt) {
		t.Fatal("ordinary router Up was not freshly observed", got, observed)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if got, _ := liveProductHosts(canceled, candidates[:1], snapshot); len(got) != 0 {
		t.Fatal("canceled discovery promoted a product", got)
	}
	live.Close()
	if got, _ := liveProductHosts(context.Background(), candidates[:1], snapshot); len(got) != 0 {
		t.Fatal("cached server reading bypassed current program presence", got)
	}
	stale := func() ([]router.HostState, time.Time) {
		states, _ := snapshot()
		return states, time.Now().Add(-productDeclarationAge - time.Second)
	}
	if got, _ := liveProductHosts(context.Background(), candidates[:1], stale); len(got) != 0 {
		t.Fatal("stale live reading promoted an absent product", got)
	}
	foreign := func() ([]router.HostState, time.Time) {
		states, at := snapshot()
		states[0].Hosted = true
		return states, at
	}
	if got, _ := liveProductHosts(context.Background(), candidates[:1], foreign); len(got) != 0 {
		t.Fatal("another transport's reading promoted an absent product", got)
	}
}

func TestProductRefreshLeavesRegistryLockAvailable(t *testing.T) {
	for _, operation := range []string{"list", "add", "replace", "remove"} {
		t.Run(operation, func(t *testing.T) { testProductRefreshRegistryLock(t, operation) })
	}
}

func testProductRefreshRegistryLock(t *testing.T, operation string) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), products: func() []providerFile {
		close(entered)
		<-release
		return nil
	}}
	done := make(chan struct{})
	go func() {
		switch operation {
		case "list":
			p.listPinned()
		case "add":
			p.add("stale", providerFile{})
		case "replace":
			p.replace("stale", providerFile{})
		case "remove":
			p.remove("stale", "unknown")
		}
		close(done)
	}()
	<-entered
	if !p.mu.TryLock() {
		close(release)
		<-done
		t.Fatal("product discovery held registry mutation lock")
	}
	// A read under the mutation lock uses a snapshot without joining discovery.
	_, _, err := p.readCached()
	p.mu.Unlock()
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
}

func TestProductInvalidationDiscardsRefreshInFlight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := &runtimeProviders{products: func() []providerFile {
		close(entered)
		<-release
		return []providerFile{{DeclaredBy: "obsolete"}}
	}}
	done := make(chan struct{})
	go func() { p.productDeclarations(); close(done) }()
	<-entered
	p.forgetProducts()
	close(release)
	<-done
	if len(p.cachedProducts()) != 0 || p.productHas {
		t.Fatal("invalidated discovery republished obsolete product")
	}
}

func TestProductPresenceRegistryReadback(t *testing.T) {
	for _, presence := range []string{"verified", "unverified"} {
		t.Run(presence, func(t *testing.T) {
			file := newHostFile(iwire.HostEntry{Name: "lmstudio", Kind: "lmstudio", Base: "http://127.0.0.1:12345"}, "lmstudio")
			file.Source, file.ProductPresence = declarationSourceProduct, presence
			p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), products: func() []providerFile { return []providerFile{file} }, routerHostStates: func() []router.HostState {
				return []router.HostState{{Host: "lmstudio", Base: file.Declaration.Host.Base, Up: true}}
			}}
			entries, _, err := p.listPinned()
			if err != nil || len(entries) != 1 {
				t.Fatal(entries, err)
			}
			state := entries[0].State
			if state.Readiness != fwire.DeclarationReadinessReady || state.Why != "host:presence_"+presence || state.Host == nil || !strings.Contains(state.Host.Why, "program presence "+presence) {
				t.Fatal("registry obscured observation provenance", state)
			}
		})
	}
}

func TestRuntimeSettingsIsolationPrivateDeclarationsNeverProbeAccount(t *testing.T) {
	saved := declarationEnv
	t.Cleanup(func() { declarationEnv = saved })
	declarationEnv = func(func(error)) router.ProbeEnv {
		return router.ProbeEnv{GOOS: "linux", Home: "/forbidden-owner", Getenv: func(string) string { panic("private product environment read") },
			ReadFile: func(string) ([]byte, error) { panic("private product record read") },
			Stat:     func(string) bool { panic("private product record stat") },
			Status:   func(context.Context, string, ...string) ([]byte, error) { panic("private product status execution") }}
	}
	placeInstallationDeclarations(t, "comfyui") // selected carrier artifact metadata
	// The old private-absent-config route called this unscoped constructor.
	// Its actual producer reaches the forbidden fake callback: an inert RED
	// control, without consulting any real record or executing a status command.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("legacy discovery route did not reach fake product callback")
			}
		}()
		p, _ := openFixtureProviders(t, t.TempDir(), true, func(error) {})
		if p != nil {
			p.Close()
		}
	}()
	state := t.TempDir()
	config, err := loadInferenceHosts(filepath.Join(state, "inference", inferenceHostsFile))
	if err != nil {
		t.Fatal(err)
	}
	p, err := openFixtureRuntimeProviders(t, runtimeFlags{privateState: true}, state, config, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	if files := p.hostFiles(); len(files) != 1 || files[0].Declaration.Name != "comfyui" || files[0].Source != declarationSourceInstallation {
		t.Fatalf("private absent config lost selected artifact or inherited products: %v", files)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	writeStateFile(t, filepath.Join(state, "inference", inferenceHostsFile), `{"local":[{"kind":"ollama","base":"http://127.0.0.1:11434"}]}`)
	if err := migrateProviderState(state, func(err error) { t.Error(err) }); err != nil {
		t.Fatal(err)
	}
	config, err = loadInferenceHosts(filepath.Join(state, "inference", inferenceHostsFile))
	if err != nil {
		t.Fatal(err)
	}
	p, err = openFixtureRuntimeProviders(t, runtimeFlags{privateState: true}, state, config, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	files := p.hostFiles()
	configured := false
	for _, file := range files {
		configured = configured || file.Declaration.Host.Base == "http://127.0.0.1:11434"
	}
	if len(files) != 2 || !configured {
		t.Fatalf("private configured host lost: %+v", files)
	}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	entry := iwire.HostEntry{Name: "lmstudio", Kind: "lmstudio", Base: "http://127.0.0.1:11435"}
	if result := p.add(revision, newHostFile(entry, "operator")); result.Outcome != fwire.DeclarationEditOutcomeApplied {
		t.Fatal(result)
	}
	if files := p.hostFiles(); len(files) != 3 {
		t.Fatalf("private explicit registration lost: %+v", files)
	}
}

func TestRuntimeSettingsIsolationExplicitDiscoveryChoiceIsPreserved(t *testing.T) {
	saved := declarationEnv
	t.Cleanup(func() { declarationEnv = saved })
	placeInstallationDeclarations(t, "comfyui")
	for _, choice := range []bool{false, true} {
		calls := 0
		declarationEnv = func(report func(error)) router.ProbeEnv {
			calls++
			return fixtureDeclarations(nil, nil)(report)
		}
		p, err := openFixtureRuntimeProviders(t, runtimeFlags{privateState: true}, t.TempDir(), inferenceHosts{Declared: &choice}, func(error) {})
		if err != nil {
			t.Fatal(err)
		}
		p.hostFiles()
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		if (calls > 0) != choice {
			t.Fatalf("explicit declared=%v invoked fixture producer %d times", choice, calls)
		}
	}
}

func hostListJSON(t *testing.T, endpoint []string) iwire.HostList {
	t.Helper()
	out, err := runInference(t, append([]string{"host", "list", "--json"}, endpoint...)...)
	if err != nil {
		t.Fatalf("host list: %v\n%s", err, out)
	}
	var list iwire.HostList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("host list json: %v\n%s", err, out)
	}
	return list
}
