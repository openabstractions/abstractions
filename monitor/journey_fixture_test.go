package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// journeyComfy is an installed stand-in ComfyUI package.
type journeyComfy struct {
	journal setup.Journal
	// root is the installed version's folder, and program its launch
	// helper, the program a registration of this installation names.
	root    string
	program string
	files   int
	// userDirectory is the private data folder the helper's
	// check-native-install names; selfRegistered is the file its
	// register-self writes its own path to, as the runtime records the
	// program that registered itself.
	userDirectory  string
	selfRegistered string
}

// journeyServiceProgram is the native package's relay program, a member of
// its engine folder (the catalogue row's service_program).
const journeyServiceProgram = "ComfyUI/custom_nodes/abstraction_downloads/helper/python.exe"

// installJourneyComfy installs the stand-in ComfyUI app package
// (standInApps) with its launch helper and an engine folder of resources
// small files, and records it installed in an approved plan. The host ZIP
// goes through the real archive installer, which writes the genuine
// receipt; the engine folder is laid down as the launch helper's own install
// plan lists it, since its real decoder is the pinned 7-Zip backend archive.
func installJourneyComfy(t *testing.T, ctx context.Context, stateDir string, resources int) journeyComfy {
	t.Helper()
	work := t.TempDir()
	directory := "oa-backend"

	// The engine folder: resources files of 1-4 KB in 300 folders, with
	// the manifest the catalog pins and the relay program.
	type file struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	manifest := []byte(fmt.Sprintf("{\"journey\":\"J-SETUP\",\"files\":%d}\n", resources))
	manifestSum := sha256.Sum256(manifest)
	content := func(i int) []byte {
		if i == 0 {
			return manifest
		}
		line := fmt.Sprintf("# J-SETUP stand-in engine module %05d\n", i)
		size := 1024 + (i*977)%3072
		return bytes.Repeat([]byte(line), size/len(line)+1)[:size]
	}
	name := func(i int) string {
		switch i {
		case 0:
			return "manifest.json"
		case 1:
			return journeyServiceProgram
		}
		return fmt.Sprintf("ComfyUI/lib/pkg%03d/module_%05d.py", i%300, i)
	}
	files := make([]file, resources)
	dirSet := map[string]bool{}
	engineRecords := int64(121)
	for i := range files {
		data := content(i)
		sum := sha256.Sum256(data)
		files[i] = file{Path: name(i), Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
		engineRecords += int64(len(files[i].Path)) + 96
		for d := path.Dir(files[i].Path); d != "."; d = path.Dir(d) {
			dirSet[d] = true
		}
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	plan, err := json.Marshal(map[string]any{"directory": directory,
		"manifest": map[string]any{"bytes": len(manifest), "sha256": hex.EncodeToString(manifestSum[:])}, "files": files, "dirs": dirs})
	if err != nil {
		t.Fatal(err)
	}
	// The data folder ComfyUI keeps beside its installation, private to
	// the account as the native helper makes it.
	user := filepath.Join(stateDir, "setup", "local", "OA", "comfyui-user")
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := privatedir.Tighten(user); err != nil {
		t.Fatal(err)
	}
	registered := filepath.Join(work, "register-self.txt")
	helper := buildJourneyLaunchHelper(t, ctx, work, plan, user, registered)

	// The app package: standInApps's ComfyUI row with this launch helper and
	// budgets for the engine folder.
	control := "inert control"
	pins, _ := standInApps(t)
	data, sha := standInZIP(t, map[string]string{"launch-helper.exe": string(helper), "archive-tool/7zr.exe": "inert decoder",
		"oa-control/control.py": control, "OA-PROVENANCE.json": "{}"})
	var document map[string]any
	if err := json.Unmarshal(pins, &document); err != nil {
		t.Fatal(err)
	}
	row := document["apps"].([]any)[0].(map[string]any)
	if row["id"] != string(setup.ComfyUI) {
		t.Fatalf("the stand-in pins start with %v, not ComfyUI", row["id"])
	}
	// The record budget is exactly the two record roots' inventories, and
	// the peak exactly the package, the backend archive, the records, the
	// receipt, 64 KiB of metadata and both trees (validateNativeRecordBudget).
	records := 2*engineRecords + 2*(121+int64(len("control.py"))+96)
	const marker = 1 << 20
	backend := int64(row["backend"].(map[string]any)["archive"].(map[string]any)["bytes"].(float64))
	peak := int64(len(data)) + backend + records + marker + 65536 + int64(len(helper)+len("inert decoder")+len(control)+len("{}"))
	for _, f := range files {
		peak += f.Size
	}
	row["binary_sha256"], row["binary_bytes"] = sha, len(data)
	row["peak_bytes"], row["marker_bytes"], row["record_bytes"] = peak, marker, records
	pin := row["backend"].(map[string]any)["provenance"].(map[string]any)["manifest"].(map[string]any)
	pin["bytes"], pin["sha256"] = len(manifest), hex.EncodeToString(manifestSum[:])
	if pins, err = json.Marshal(document); err != nil {
		t.Fatal(err)
	}
	journal, err := setup.Journal{Path: filepath.Join(stateDir, "setup", "plan.json")}.WithAdopterAssets(pins)
	if err != nil {
		t.Fatalf("the journey's pins document is not in the release schema: %v", err)
	}
	var artifact *setup.Artifact
	for _, c := range journal.Catalog().Components {
		if c.ID == "comfyui-app" {
			artifact = c.Artifact
		}
	}
	if artifact == nil || artifact.NativeResources == nil {
		t.Fatalf("the journey's catalog carries no native ComfyUI package: %+v", artifact)
	}

	// The real archive installer publishes the host ZIP with its receipt
	// where Setup installs native ComfyUI (installPath).
	source := filepath.Join(work, "ComfyUI-OA-windows-x64.zip")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(stateDir, "setup", "local", "OA", "a", "cu", artifact.Version)
	if err := setup.InstallArchive(ctx, *artifact, source, root); err != nil {
		t.Fatalf("install the app package: %v", err)
	}
	engine := filepath.Join(root, directory)
	eachJourneyFile(t, len(files), func(i int) error {
		target := filepath.Join(engine, filepath.FromSlash(files[i].Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, content(i), 0o600)
	})
	// The installer reads back every file it extracted, and an installed
	// package's files are long settled. A file written moments ago waits for
	// the real-time scanner when it is next opened: 30,000 fresh files are
	// removed at about 80 a second, settled ones at about 1,500. One
	// parallel read settles them as the install's own verification does.
	settleAt := time.Now()
	eachJourneyFile(t, len(files), func(i int) error {
		f, err := os.Open(filepath.Join(engine, filepath.FromSlash(files[i].Path)))
		if err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, f)
		return errors.Join(err, f.Close())
	})
	t.Logf("journey: read back the engine folder in %s", time.Since(settleAt).Round(time.Millisecond))

	// An approved plan records the package installed.
	selected, err := journal.Catalog().Plan([]setup.AppID{setup.ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	state, err := journal.SavePlan(ctx, selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ApproveInstall(ctx, state.Plan.ID, "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	state.ApprovedPlanID = state.Plan.ID
	state.Effects["comfyui-app"] = setup.Effect{Phase: setup.Installed, Revision: 1}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return journeyComfy{journal: journal, root: root, program: filepath.Join(root, "launch-helper.exe"), files: len(files) + 4,
		userDirectory: user, selfRegistered: registered}
}

// eachJourneyFile calls do for 0..count-1 with sixteen workers.
func eachJourneyFile(t *testing.T, count int, do func(int) error) {
	t.Helper()
	var wg sync.WaitGroup
	var once sync.Once
	var failure error
	next := atomic.Int64{}
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= count {
					return
				}
				if err := do(i); err != nil {
					once.Do(func() { failure = err })
					return
				}
			}
		}()
	}
	wg.Wait()
	if failure != nil {
		t.Fatal(failure)
	}
}

// buildJourneyLaunchHelper builds the stand-in launch helper, an unsigned
// program with the four operations Setup runs from ComfyUI's helper:
// native-install-plan (with or without --installed-root) prints plan, the
// engine folder's inventory; check-native-install names user, its data
// folder; register-self writes the helper's own path to registered, where
// the journey's applications service reads the program that registered
// itself.
func buildJourneyLaunchHelper(t *testing.T, ctx context.Context, work string, plan []byte, user, registered string) []byte {
	t.Helper()
	src := filepath.Join(work, "helper-src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	program := `package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

//go:embed plan.json
var plan []byte

const userDirectory = ` + fmt.Sprintf("%q", user) + `
const registered = ` + fmt.Sprintf("%q", registered) + `

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	args := os.Args[1:]
	switch {
	case len(args) >= 1 && args[0] == "native-install-plan" && (len(args) == 1 || len(args) == 3 && args[1] == "--installed-root"):
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(plan, &fields); err != nil {
			fail(err)
		}
		if len(args) == 3 {
			root, _ := json.Marshal(args[2])
			fields["installed_root"] = root
		}
		out, _ := json.Marshal(fields)
		os.Stdout.Write(out)
	case len(args) == 1 && args[0] == "check-native-install":
		out, _ := json.Marshal(map[string]string{"user_directory": userDirectory})
		os.Stdout.Write(out)
	case len(args) == 1 && args[0] == "register-self":
		self, err := os.Executable()
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(registered, []byte(self), 0o600); err != nil {
			fail(err)
		}
		os.Stdout.Write([]byte("{\"outcome\":\"applied\"}"))
	default:
		fmt.Fprintln(os.Stderr, "the journey's stand-in launch helper does not answer", args)
		os.Exit(2)
	}
}
`
	for name, data := range map[string][]byte{"go.mod": []byte("module journeyhelper\n\ngo 1.22\n"), "main.go": []byte(program), "plan.json": plan} {
		if err := os.WriteFile(filepath.Join(src, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goTool += ".exe"
	}
	out := filepath.Join(work, "launch-helper.exe")
	build := exec.CommandContext(ctx, goTool, "build", "-trimpath", "-ldflags=-s -w", "-o", out, ".")
	build.Dir = src
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOOS=windows", "GOARCH=amd64")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the stand-in launch helper: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	helper, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return helper
}
