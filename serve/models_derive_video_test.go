package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
)

// convertMemoryLimit caps the fake converter's job object in these tests.
const convertMemoryLimit = 1 << 30

// openACL stands in for the read-only engine folder ACL, so a test can
// change a staged file the way another program could.
type openACL struct{}

func (openACL) Restrict([]string) error                  { return nil }
func (openACL) Check(string, *programfiles.Handle) error { return nil }

// fakeConverter builds fakesdcli once per test as sd-cli.
func fakeConverter(t *testing.T) []byte {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("the conversion runs supervised on Windows")
	}
	program := filepath.Join(t.TempDir(), sdCLIName())
	if out, err := exec.Command("go", "build", "-o", program, "./testdata/fakesdcli").CombinedOutput(); err != nil {
		t.Fatalf("build fakesdcli: %v\n%s", err, out)
	}
	data, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// stageEngine writes an engine folder holding sd-cli and extra, and
// installs it with files as modelhostd engine install does.
func stageEngine(t *testing.T, files *programfiles.Verifier, converter []byte, extra map[string][]byte) programfiles.Tree {
	t.Helper()
	dir := t.TempDir()
	contents := map[string][]byte{sdCLIName(): converter}
	for name, data := range extra {
		contents[name] = data
	}
	var manifest programfiles.Manifest
	for name, data := range contents {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, programfiles.File{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	tree := programfiles.Tree{Name: "test sd.cpp", Root: dir, Manifest: manifest}
	installed := tree
	installed.Record = filepath.Join(t.TempDir(), "engine.files")
	if err := files.Install(t.Context(), installed); err != nil {
		t.Fatal(err)
	}
	return tree
}

// convertedFile is the pinned file fakesdcli makes from data.
func convertedFile(name string, data []byte) pinnedFile {
	converted := bytes.ToUpper(data)
	sum := sha256.Sum256(converted)
	return pinnedFile{name: name, sha256: hex.EncodeToString(sum[:]), size: int64(len(converted))}
}

// LTX addendum, amendment 1: the converter runs as exactly
// `-M convert -m <source> --type q8_0 -o <partial>` in the engine folder with
// a clean environment, and its output is kept at the set's name only when
// it is the pinned file; a differing output keeps nothing.
func TestConvertedEncoderIsKeptOnlyWhenItIsThePinnedFile(t *testing.T) {
	converter := fakeConverter(t)
	files := programfiles.NewVerifier(programfiles.Options{ACL: openACL{}})
	engine := stageEngine(t, files, converter, nil)
	models := t.TempDir()
	input := filepath.Join(models, "encoder-bf16.safetensors")
	if err := os.WriteFile(input, []byte("gemma weights"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := convertedFile("encoder-Q8_0.gguf", []byte("gemma weights"))
	t.Setenv("GGML_VK_VISIBLE_DEVICES", "1")
	t.Setenv("HF_TOKEN", "never passed")
	dest := filepath.Join(models, f.name)
	if err := convertAndKeep(files, engine, input, models, dest, f, convertMemoryLimit); err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(dest); !bytes.Equal(kept, bytes.ToUpper([]byte("gemma weights"))) {
		t.Fatal("the pinned conversion was not kept")
	}
	raw, err := os.ReadFile(filepath.Join(models, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	if want := convertArgs(input, filepath.Join(models, f.name+".partial")); !slices.Equal(lines[:len(want)], want) {
		t.Fatalf("sd-cli ran with %q", lines)
	}
	if !slices.Contains(lines, "cwd="+engine.Root) {
		t.Fatalf("sd-cli did not run in the engine folder: %q", lines)
	}
	for _, line := range lines {
		if name, ok := strings.CutPrefix(line, "env="); ok && (strings.HasPrefix(strings.ToUpper(name), "GGML_") || strings.HasPrefix(strings.ToUpper(name), "HF_")) {
			t.Fatalf("sd-cli received %s", name)
		}
	}
	wrong := stageEngine(t, files, converter, map[string][]byte{"FAKESDCLI_WRONG": []byte("1")})
	other := filepath.Join(models, "other-Q8_0.gguf")
	g := f
	g.name = "other-Q8_0.gguf"
	if err := convertAndKeep(files, wrong, input, models, other, g, convertMemoryLimit); err == nil {
		t.Fatal("a differing conversion was kept")
	}
	for _, left := range []string{other, other + ".partial"} {
		if _, err := os.Stat(left); !os.IsNotExist(err) {
			t.Fatalf("%s is left: %v", left, err)
		}
	}
}

// Amendment 1 runs the staged, proved sd-cli: the engine folder is proved
// again just before the conversion, and a changed sd-cli never runs.
func TestAChangedConverterNeverRuns(t *testing.T) {
	converter := fakeConverter(t)
	files := programfiles.NewVerifier(programfiles.Options{ACL: openACL{}})
	engine := stageEngine(t, files, converter, nil)
	program := filepath.Join(engine.Root, sdCLIName())
	changed := slices.Clone(converter)
	changed[len(changed)-1] ^= 0xff
	if err := os.WriteFile(program, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	models := t.TempDir()
	input := filepath.Join(models, "encoder-bf16.safetensors")
	if err := os.WriteFile(input, []byte("gemma weights"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := convertedFile("encoder-Q8_0.gguf", []byte("gemma weights"))
	dest := filepath.Join(models, f.name)
	err := convertAndKeep(files, engine, input, models, dest, f, convertMemoryLimit)
	if err == nil || !strings.Contains(err.Error(), "is not the pinned") || !errors.Is(err, programfiles.ErrMismatch) {
		t.Fatalf("a changed sd-cli was not refused: %v", err)
	}
	for _, left := range []string{dest, filepath.Join(models, "args.txt")} {
		if _, err := os.Stat(left); !os.IsNotExist(err) {
			t.Fatalf("the changed sd-cli ran: %s: %v", left, err)
		}
	}
}

// A derived file the download service's archive lists but cannot give is
// converted from its source file: the archive fetch carries no credential,
// the source file carries the person's, and a work folder a stopped command
// left is removed.
func TestAnArchiveCopyThatIsNotGivenIsConverted(t *testing.T) {
	converter := fakeConverter(t)
	files := programfiles.NewVerifier(programfiles.Options{ACL: openACL{}})
	engine := stageEngine(t, files, converter, nil)
	set := t.TempDir()
	stale := filepath.Join(set, convertWorkPrefix+"stopped")
	fresh := filepath.Join(set, convertWorkPrefix+"running")
	for _, dir := range []string{stale, fresh} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "encoder-bf16.safetensors"), []byte("left"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-3 * staleConvertWork)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	weights := []byte("gemma weights")
	sum := sha256.Sum256(weights)
	from := pinnedFile{name: "encoder-bf16.safetensors", url: "https://huggingface.co/Lightricks/LTX-2.5/resolve/r/encoder-bf16.safetensors",
		sha256: hex.EncodeToString(sum[:]), size: int64(len(weights))}
	f := convertedFile("encoder-Q8_0.gguf", weights)
	f.url = from.url
	type call struct{ sha256, credential string }
	var calls []call
	g := setGetter{
		fetch: func(p pinnedFile, dest, credential string) error {
			calls = append(calls, call{p.sha256, credential})
			if p.sha256 != from.sha256 {
				return errors.New("the archive copy is missing")
			}
			return os.WriteFile(dest, weights, 0o600)
		},
		holds: func(p pinnedFile) bool { return p.sha256 == f.sha256 },
		derived: func(p pinnedFile) (pinnedFile, bool) {
			return from, p.sha256 == f.sha256
		},
		files: files, command: "models host", output: io.Discard,
	}
	dest := filepath.Join(set, f.name)
	if err := g.place(f, dest, engine, "hf", convertMemoryLimit); err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(dest); !bytes.Equal(kept, bytes.ToUpper(weights)) {
		t.Fatal("the converted file was not kept")
	}
	if want := []call{{f.sha256, ""}, {from.sha256, "hf"}}; !slices.Equal(calls, want) {
		t.Fatalf("fetches %+v, want %+v", calls, want)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stopped command's work folder is left: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("a running command's work folder was removed: %v", err)
	}
	entries, _ := os.ReadDir(set)
	for _, entry := range entries {
		if name := entry.Name(); name != f.name && name != filepath.Base(fresh) {
			t.Fatalf("%s is left in the set folder", name)
		}
	}
}
