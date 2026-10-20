package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// engine check's refusal can quote what the engine printed. models host
// shows it only through redact.Line: a credential the engine printed and a
// terminal escape never reach the diagnostics, and the refusal still names
// the engine's words around them.
func TestModelsHostRedactsTheEngineCheckRefusal(t *testing.T) {
	program := refusingModelHost(t)
	var output, diagnostics bytes.Buffer
	err := modelsHostCommand("models host", []string{"store/object", "--engine", unpinnedEngine(t), "--program", program,
		"--endpoint", "modelhost-redacted-refusal-no-runtime", "--timeout", "2s"}, &output, &diagnostics)
	if err == nil {
		t.Fatal("a refused engine was accepted")
	}
	text := err.Error() + diagnostics.String()
	if strings.Contains(text, "fixtureSECRET") || strings.Contains(text, "fixtureTOKEN") || strings.ContainsRune(text, 0x1b) {
		t.Fatalf("the refusal carries the engine's credential or an escape: %q", text)
	}
	if !strings.Contains(text, "refused engine:none") || !strings.Contains(text, "the engine printed Authorization: [redacted]") {
		t.Fatalf("the refusal %q does not carry the engine's words", text)
	}
}

// The media engine check (video or PyTorch image) shows the model host's
// first line the same way.
func TestModelsHostRedactsTheMediaEngineCheckRefusal(t *testing.T) {
	program := refusingModelHost(t)
	var diagnostics bytes.Buffer
	settings := hostSettings{program: program, media: hostMedia{videoKind: videoKindSdCpp, videoModel: "fastwan2.2-ti2v-5b", videoEngine: "sd-server"}}
	err := proveMediaEngine("models host", settings, &diagnostics)
	if err == nil {
		t.Fatal("a refused media engine was accepted")
	}
	text := err.Error() + diagnostics.String()
	if strings.Contains(text, "fixtureSECRET") || strings.ContainsRune(text, 0x1b) ||
		!strings.Contains(text, "the engine printed Authorization: [redacted]") {
		t.Fatalf("the media refusal %q carries the engine's credential or drops its words", text)
	}
}

// refusingModelHost builds a stand-in model host whose engine check exits 1
// quoting an engine line with a credential and a terminal escape.
func refusingModelHost(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	main := `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprint(os.Stderr, "modelhostd engine check: the engine printed Authorization: Bearer fixtureSECRET\n\x1b[31mtoken=fixtureTOKEN refused\n")
	os.Exit(1)
}
`
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module refusing\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := modelHostProgram
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	program := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "-C", src, "build", "-o", program, ".")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build refusing stand-in: %v\n%s", err, out)
	}
	return program
}
