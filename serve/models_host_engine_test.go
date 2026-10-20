package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// models host proves --engine against the model host's pinned llama.cpp
// release before it reaches the runtime or writes a declaration (modelhost
// CONTRACT.md MODELHOST-L3). An engine outside the pin is a configuration
// mistake that names modelhostd engine install; no runtime is contacted.
func TestModelsHostRefusesAnUnpinnedEngineBeforeDeclaring(t *testing.T) {
	program := buildModelhostd(t)
	name := "llama-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	engine := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(engine, []byte("not the pinned release"), 0o700); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	err := modelsHostCommandOn("windows", "models host", []string{"store/object", "--engine", engine, "--program", program,
		"--endpoint", "modelhost-engine-check-no-runtime", "--timeout", "2s"}, &output, &diagnostics)
	if err == nil {
		t.Fatal("an unpinned engine was accepted")
	}
	text := err.Error() + diagnostics.String()
	// The model host pins llama.cpp for windows/amd64 alone (host.PinnedEngine).
	// Elsewhere engine install refuses too, so the refusal names the missing
	// pin and no install remedy.
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		if !strings.Contains(text, "no reviewed llama.cpp release is pinned for "+runtime.GOOS+"/"+runtime.GOARCH) {
			t.Fatalf("the refusal does not name the missing pin: %s", text)
		}
		return
	}
	if !strings.Contains(text, "modelhostd engine install") || !strings.Contains(text, "pinned") {
		t.Fatalf("the refusal does not name the pin and modelhostd engine install: %s", text)
	}
}

// standInModelHost builds a program that records it ran in the file
// $STANDIN_MARKER names, prints a multi-line usage to stderr and exits 2, the
// way a model host without engine check answers an unknown command.
func standInModelHost(t *testing.T, dir string) string {
	t.Helper()
	src := t.TempDir()
	main := `package main

import (
	"fmt"
	"os"
)

func main() {
	if marker := os.Getenv("STANDIN_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("ran"), 0o600)
	}
	fmt.Fprint(os.Stderr, "modelhostd: unknown command \"engine\"\n\nUsage:\n  modelhostd serve ...\n  modelhostd help\n")
	os.Exit(2)
}
`
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module standin\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := modelHostProgram
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	build := exec.Command("go", "-C", src, "build", "-o", target, ".")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stand-in: %v\n%s", err, out)
	}
	return target
}

// recordingModelHost builds a program that answers `engine check` the way
// modelhostd does for its arguments alone: it appends its argument list, one
// per line, to the file $STANDIN_ARGS names, refuses an --engine-arg of --rpc
// with exit 2 and modelhostd's own words, and can refuse STANDIN_REFUSE_SET.
func recordingModelHost(t *testing.T, dir string) string {
	t.Helper()
	src := t.TempDir()
	main := `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	if record := os.Getenv("STANDIN_ARGS"); record != "" {
		f, err := os.OpenFile(record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, strings.Join(os.Args[1:], "\n"))
			fmt.Fprintln(f, "--end--")
			f.Close()
		}
	}
	if len(os.Args) > 4 && os.Args[1] == "engine" && os.Args[2] == "check" &&
		os.Args[3] == "--set" && os.Args[4] == os.Getenv("STANDIN_REFUSE_SET") {
		fmt.Fprintln(os.Stderr, "pytorchd engine check: fixture refused "+os.Args[4])
		os.Exit(1)
	}
	for i, a := range os.Args {
		if a == "--engine-arg" && i+1 < len(os.Args) && os.Args[i+1] == "--rpc" {
			fmt.Fprintln(os.Stderr, "modelhostd engine check: modelhost: engine argument outside the reviewed list: --rpc is not on the reviewed llama-server list")
			os.Exit(2)
		}
	}
}
`
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module recording\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := modelHostProgram
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	build := exec.Command("go", "-C", src, "build", "-o", target, ".")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build recording stand-in: %v\n%s", err, out)
	}
	return target
}

// recordedChecks reads the argument lists recordingModelHost appended.
func recordedChecks(t *testing.T, record string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the model host was never run: %v", err)
	}
	var runs [][]string
	var current []string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if line == "--end--" {
			runs = append(runs, current)
			current = nil
			continue
		}
		if line != "" {
			current = append(current, line)
		}
	}
	return runs
}

// models host proves the engine with the same --state and --engine-arg list
// the declaration carries: the state folder holds the engine record engine
// check reads, and a refused engine argument is a configuration mistake
// before any runtime is asked (modelhost MODELHOST-L5).
func TestModelsHostCarriesStateAndEngineArgumentsToEngineCheck(t *testing.T) {
	program := recordingModelHost(t, t.TempDir())
	record := filepath.Join(t.TempDir(), "args")
	t.Setenv("STANDIN_ARGS", record)
	state := filepath.Join(t.TempDir(), "modelhost state")
	engine := unpinnedEngine(t)
	var output, diagnostics bytes.Buffer
	//unchecked: no runtime answers this endpoint; the check below reads what engine check was given
	modelsHostCommandOn("windows", "models host", []string{"store/object", "--engine", engine, "--program", program,
		"--state", state, "--engine-arg", "--pooling", "--engine-arg", "mean",
		"--endpoint", "modelhost-state-no-runtime", "--timeout", "2s"}, &output, &diagnostics)
	runs := recordedChecks(t, record)
	want := []string{"engine", "check", "--engine", engine, "--state", state, "--engine-arg", "--pooling", "--engine-arg", "mean"}
	if len(runs) == 0 || strings.Join(runs[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("engine check ran with %q, wanted %q", runs, want)
	}

	var refusedOut, refusedDiag bytes.Buffer
	err := modelsHostCommandOn("windows", "models host", []string{"store/object", "--engine", engine, "--program", program,
		"--engine-arg", "--rpc", "--engine-arg", "10.0.0.2:50052",
		"--endpoint", "modelhost-refused-arg-no-runtime", "--timeout", "2s"}, &refusedOut, &refusedDiag)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(refusedDiag.String(), "--rpc is not on the reviewed llama-server list") {
		t.Fatalf("a refused --engine-arg: %v, diagnostics %q; wanted a flag mistake naming --rpc", err, refusedDiag.String())
	}
}

// The declaration carries --state, and reading it back gives the same folder,
// so the declared serve reads the record engine install wrote there.
func TestModelsHostDeclarationCarriesState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "modelhost state")
	settings := hostSettings{program: "modelhostd", endpoint: "modelhost", engine: "llama-server", resource: "resource-endpoint",
		state: state, extra: []string{"--pooling", "mean"}}
	arguments := hostArguments(settings, []hostedModel{{Name: "tiny", Store: "fake", Object: "tiny"}}, []string{"runtime"})
	found := false
	for i := 0; i+1 < len(arguments); i++ {
		if arguments[i] == argState && arguments[i+1] == state {
			found = true
		}
	}
	if !found {
		t.Fatalf("the declared serve arguments %q carry no --state %s", arguments, state)
	}
	list := wire.DeclarationList{Declarations: []wire.DeclarationState{{Declaration: wire.Declaration{Name: modelHostName, Arguments: arguments}}}}
	read, _ := readHostDeclaration(list)
	if read.state != state || strings.Join(read.extra, " ") != "--pooling mean" {
		t.Fatalf("read back state %q extra %q", read.state, read.extra)
	}
}

// Owner's preview, 2026-11-01: native:object:unknown:oa-chat-…. A hosted
// model in a store only an inventory source's own --config names (Setup's
// model folder) brings that configuration onto modelhostd's serve line, so
// the provider's own store reading resolves the object. A hand-written
// configuration stays; a source's configuration leaves with its last
// hosted model; a line read back keeps the configurations it carried.
func TestModelsHostDeclarationCarriesTheStoreSourcesConfig(t *testing.T) {
	setupConfig := filepath.Join(t.TempDir(), "setup", "inventoryd.json")
	handConfig := filepath.Join(t.TempDir(), "hand.json")
	list := wire.DeclarationList{Declarations: []wire.DeclarationState{
		{Declaration: wire.Declaration{Name: "oa-modelhost-chat-1", Contracts: []string{inventorySourceContract}, Resources: []string{"store:oa-chat-1"},
			Arguments: []string{"serve", "--endpoint", "{endpoint}", "--config", setupConfig, "--config-roots-only"}}},
		{Declaration: wire.Declaration{Name: "ambient", Contracts: []string{inventorySourceContract}, Resources: []string{"store:huggingface"},
			Arguments: []string{"serve", "--endpoint", "{endpoint}"}}},
	}}
	setupModel := hostedModel{Name: "qwen3.6-35b-a3b", Store: "oa-chat-1", Object: "sha256:945bbf"}
	hubModel := hostedModel{Name: "smol", Store: "huggingface", Object: "smol"}
	if got := hostStoreConfigs(list, []string{handConfig}, []hostedModel{setupModel, hubModel}); !slices.Equal(got, []string{handConfig, setupConfig}) {
		t.Fatalf("configs %q", got)
	}
	if got := hostStoreConfigs(list, []string{handConfig, setupConfig}, []hostedModel{hubModel}); !slices.Equal(got, []string{handConfig}) {
		t.Fatalf("configs after the Setup model left %q", got)
	}
	settings := hostSettings{program: "modelhostd", endpoint: "modelhost", engine: "llama-server", configs: hostStoreConfigs(list, nil, []hostedModel{setupModel})}
	arguments := hostArguments(settings, []hostedModel{setupModel}, []string{"runtime"})
	if i := slices.Index(arguments, argConfig); i < 0 || i+1 >= len(arguments) || arguments[i+1] != setupConfig {
		t.Fatalf("the serve line %q carries no --config %s", arguments, setupConfig)
	}
	read, models := readServeArguments(arguments)
	if !slices.Equal(read.configs, []string{setupConfig}) || len(read.passthrough) != 0 || len(models) != 1 || models[0] != setupModel {
		t.Fatalf("read back configs %q passthrough %q models %+v", read.configs, read.passthrough, models)
	}
}

// The model-label digest guard at the owner of hosted names (owner,
// 2026-11-01: OpenCode listed a model by its SHA-256). A store name that is a
// file digest gives way to the family the store's descriptor states, else its
// architecture with size label; a digest that remains, or one --as names, is
// refused, so no hosted name a client lists is a digest.
func TestModelsHostDigestLabelGuard(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	for _, c := range []struct {
		name       string
		descriptor map[string]string
		want       string
	}{
		{"qwen3:8b", nil, "qwen3:8b"},
		{digest + ".gguf", map[string]string{"family": "qwen3.6-35b-a3b"}, "qwen3.6-35b-a3b"},
		{digest + ".gguf", map[string]string{"family": digest, "architecture": "qwen35moe", "size_label": "35B-A3B"}, "qwen35moe-35B-A3B"},
		{digest + ".gguf", map[string]string{"family": digest}, digest + ".gguf"},
	} {
		if got := plainHostedName(c.name, c.descriptor); got != c.want {
			t.Errorf("plainHostedName(%q, %v) = %q, want %q", c.name, c.descriptor, got, c.want)
		}
	}
	var out, diag bytes.Buffer
	err := modelsHostCommand("models host", []string{"store/object", "--as", digest, "--endpoint", "modelhost-digest-label-no-runtime", "--timeout", "2s"}, &out, &diag)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(diag.String(), "never by a file digest") {
		t.Fatalf("--as a digest: %v, diagnostics %q", err, diag.String())
	}
}

func unpinnedEngine(t *testing.T) string {
	t.Helper()
	engine := filepath.Join(t.TempDir(), "llama-server.exe")
	if err := os.WriteFile(engine, []byte("not the pinned release"), 0o700); err != nil {
		t.Fatal(err)
	}
	return engine
}

// A --program that is no absolute path is a flag mistake before anything
// runs: exec would otherwise look the bare name up on PATH.
func TestModelsHostRefusesARelativeProgramWithoutRunningIt(t *testing.T) {
	pathDir := t.TempDir()
	standInModelHost(t, pathDir)
	marker := filepath.Join(t.TempDir(), "ran")
	t.Setenv("STANDIN_MARKER", marker)
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var output, diagnostics bytes.Buffer
	err := modelsHostCommand("models host", []string{"store/object", "--engine", unpinnedEngine(t), "--program", modelHostProgram,
		"--endpoint", "modelhost-relative-program-no-runtime", "--timeout", "2s"}, &output, &diagnostics)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(diagnostics.String(), "--program") {
		t.Fatalf("a bare --program: %v, diagnostics %q; wanted a --program flag mistake", err, diagnostics.String())
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the bare --program was looked up on PATH and run")
	}
}

// A model host without engine check exits 2 with its usage. The refusal
// carries its first line and the remedy, not the whole usage.
func TestModelsHostNamesAModelHostWithoutEngineCheck(t *testing.T) {
	program := standInModelHost(t, t.TempDir())
	var output, diagnostics bytes.Buffer
	err := modelsHostCommandOn("windows", "models host", []string{"store/object", "--engine", unpinnedEngine(t), "--program", program,
		"--endpoint", "modelhost-old-program-no-runtime", "--timeout", "2s"}, &output, &diagnostics)
	if err == nil {
		t.Fatal("a model host without engine check was accepted")
	}
	text := err.Error()
	if !strings.Contains(text, "has no `engine check`; install the current model host provider") ||
		!strings.Contains(text, `unknown command "engine"`) {
		t.Fatalf("the refusal %q does not carry the first line and the remedy", text)
	}
	if strings.Contains(text, "Usage:") || strings.Contains(text, "modelhostd help") {
		t.Fatalf("the refusal %q carries the whole usage", text)
	}
}

// The help names how the engine is staged.
func TestModelsHostHelpNamesEngineInstall(t *testing.T) {
	if !strings.Contains(modelsUsage, "modelhostd engine install") {
		t.Fatal("models help does not say how the pinned engine is staged")
	}
	if strings.Contains(modelsUsage, "llama.cpp this machine already") || strings.Contains(modelsUsage, "llama-server this machine already") {
		t.Fatal("models help still offers any llama-server this machine has")
	}
}
