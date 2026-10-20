package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	engine := filepath.Join(t.TempDir(), "llama-server.exe")
	if err := os.WriteFile(engine, []byte("not the pinned release"), 0o700); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	err := modelsHostCommand("models host", []string{"store/object", "--engine", engine, "--program", program,
		"--endpoint", "modelhost-engine-check-no-runtime", "--timeout", "2s"}, &output, &diagnostics)
	if err == nil {
		t.Fatal("an unpinned engine was accepted")
	}
	text := err.Error() + diagnostics.String()
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
// with exit 2 and modelhostd's own words, and passes everything else.
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
	modelsHostCommand("models host", []string{"store/object", "--engine", engine, "--program", program,
		"--state", state, "--engine-arg", "--pooling", "--engine-arg", "mean",
		"--endpoint", "modelhost-state-no-runtime", "--timeout", "2s"}, &output, &diagnostics)
	runs := recordedChecks(t, record)
	want := []string{"engine", "check", "--engine", engine, "--state", state, "--engine-arg", "--pooling", "--engine-arg", "mean"}
	if len(runs) == 0 || strings.Join(runs[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("engine check ran with %q, wanted %q", runs, want)
	}

	var refusedOut, refusedDiag bytes.Buffer
	err := modelsHostCommand("models host", []string{"store/object", "--engine", engine, "--program", program,
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
	err := modelsHostCommand("models host", []string{"store/object", "--engine", unpinnedEngine(t), "--program", program,
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
