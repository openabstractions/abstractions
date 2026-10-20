package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// standInZIP is a ZIP of the named files, with the digest a pin names.
func standInZIP(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o600)
		member, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(member, files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(out.Bytes())
	return out.Bytes(), hex.EncodeToString(sum[:])
}

// standInApps builds a ComfyUI app ZIP with a source ZIP its provenance
// names, and the pins document for it in adopter_assets.json's schema. It
// returns the pins and the app's bytes.
func standInApps(t *testing.T) ([]byte, map[setup.AppID][]byte) {
	t.Helper()
	// The existing canonical producer exports this inert native-row fixture.
	raw, err := os.ReadFile("internal/setup/testdata/native_producer_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var exported struct {
		Row map[string]any `json:"row"`
	}
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	row := exported.Row
	delete(row, "release_tag")
	data, sha := standInZIP(t, map[string]string{"launch-helper.exe": "inert native image", "archive-tool/7zr.exe": "inert decoder", "oa-control/control.py": "inert control", "OA-PROVENANCE.json": "{}"})
	row["binary_sha256"], row["binary_bytes"] = sha, len(data)
	row["peak_bytes"] = 4 << 20
	// ComfyUI runs its torch from the PyTorch component, whose row the
	// native document carries beside it.
	inputs, inputsSHA := standInZIP(t, map[string]string{"python/python.exe": "inert interpreter"})
	pytorch := map[string]any{"id": "pytorch", "binary_zip": "pytorch-inputs.zip", "binary_sha256": inputsSHA, "binary_bytes": len(inputs),
		"source_zip": "pytorch-source.zip", "source_sha256": strings.Repeat("d", 64), "source_bytes": 10,
		"source_commit": strings.Repeat("e", 40), "source_url": "https://github.com/openabstractions/abstractions/commit/" + strings.Repeat("e", 40),
		"signer": "unsigned", "version": "2.11.0", "platform": "windows/amd64", "peak_bytes": 4 << 20, "license": "BSD-3-Clause",
		"publisher": "openabstractions", "install_identity": "pt", "action": "archive_per_user"}
	pins, err := json.Marshal(map[string]any{"schema": 2, "release_tag": "v0.8.0", "apps": []any{row, pytorch}})
	if err != nil {
		t.Fatal(err)
	}
	return pins, map[setup.AppID][]byte{setup.ComfyUI: data}
}

// standInJournal is a Setup journal in a temporary folder whose catalog
// carries the stand-in pins.
func standInJournal(t *testing.T, pins []byte) setup.Journal {
	t.Helper()
	journal, err := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}.WithAdopterAssets(pins)
	if err != nil {
		t.Fatalf("the stand-in pins document is not in the release schema: %v", err)
	}
	return journal
}

func requirePinnedPlatform(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("the OA app builds are pinned for windows/amd64")
	}
}

// Setup installs no ComfyUI (owner, 2026-11-11): a pins document's ComfyUI
// row offers no package, a plan naming ComfyUI is blocked, and the picture
// engine stays offered without it.
func TestSetupOffersNoComfyBuild(t *testing.T) {
	requirePinnedPlatform(t)
	withOtherCopies(t, nil)
	pins, _ := standInApps(t)
	journal := standInJournal(t, pins)
	call := installHandler(t, &journal)
	stubOverviewMachine(t)
	overview := call("GET", "/setup/overview", "")
	var offered setupOverview
	if overview.Code != 200 || json.Unmarshal(overview.Body.Bytes(), &offered) != nil || slices.Contains(offered.Offered, setup.ComfyUI) {
		t.Fatalf("the Setup page is offered the OA build of ComfyUI: %d %s", overview.Code, overview.Body.String())
	}
	if engine := journal.Catalog().PictureEngine(setup.ComfyUI); slices.Contains(offered.Offered, engine) && offered.PictureEngine != engine {
		t.Fatalf("picture engine %q, want %q", offered.PictureEngine, engine)
	}
	got := call("POST", "/setup/plan", `{"selected":["comfyui"]}`)
	var review setupPlanReview
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &review) != nil || review.Preparable || len(review.Blockers) == 0 {
		t.Fatalf("a plan naming ComfyUI is not blocked: %d %s", got.Code, got.Body.String())
	}
}

// panelTreeDigest names every file and folder under root with its digest.
func panelTreeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() {
			tree[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		tree[rel] = hex.EncodeToString(sum[:])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return tree
}

func writePanelFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Setup reads fixture folders for an unmarked ComfyUI portable, with an
// uninstall entry, through its real finder. The page reports each with its
// location, every route that would install OA's build refuses without the
// person's choice, the choice lets it through, and no file of the other
// copies changes. An OpenCode install is no other copy of Setup's.
func TestSetupReportsFixtureOtherCopiesAndProceedsOnlyBeside(t *testing.T) {
	root := t.TempDir()
	finder := setup.CopyFinder{UserProfile: filepath.Join(root, "home"), LocalAppData: filepath.Join(root, "local"),
		AppData: filepath.Join(root, "roaming")}
	portable := filepath.Join(finder.UserProfile, "Desktop", "ComfyUI_windows_portable")
	writePanelFixture(t, filepath.Join(portable, "ComfyUI", "main.py"))
	writePanelFixture(t, filepath.Join(portable, "python_embeded", "python.exe"))
	writePanelFixture(t, filepath.Join(portable, "ComfyUI", "models", "checkpoints", "person.safetensors"))
	openCode := filepath.Join(finder.LocalAppData, "Programs", "opencode")
	writePanelFixture(t, filepath.Join(openCode, "opencode.exe"))
	registered := filepath.Join(root, "Program Files", "ComfyUI Desktop")
	writePanelFixture(t, filepath.Join(registered, "ComfyUI.exe"))
	finder.Uninstall = func() ([]setup.UninstallEntry, error) {
		return []setup.UninstallEntry{{DisplayName: "ComfyUI Desktop 0.4", DisplayIcon: filepath.Join(registered, "ComfyUI.exe") + ",0"}}, nil
	}
	prior := findOtherCopies
	findOtherCopies = finder.Find
	t.Cleanup(func() { findOtherCopies = prior })
	before := panelTreeDigest(t, root)

	pins, _ := standInApps(t)
	journal := standInJournal(t, pins)
	call := installHandler(t, &journal)
	listed := call("GET", "/setup/other-copies", "")
	var reply setupOtherCopiesReply
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &reply) != nil || reply.Incomplete {
		t.Fatalf("other copies: %d %s", listed.Code, listed.Body.String())
	}
	locations := map[string]setup.AppID{}
	for _, c := range reply.Copies {
		locations[c.Location] = c.App
	}
	if len(reply.Copies) != 2 || locations[portable] != setup.ComfyUI || locations[registered] != setup.ComfyUI || locations[openCode] != "" {
		t.Fatalf("the page does not name each copy and its location: %+v", reply.Copies)
	}
	request := func(app setup.AppID, beside bool) string {
		plan, err := journal.Catalog().Plan([]setup.AppID{app})
		if err != nil {
			t.Fatal(err)
		}
		rules, _ := setupPictureRules(plan)
		choice := setupChoiceRequest{Selected: []setup.AppID{app}, PlanID: plan.ID, ModelPlanID: setup.PyTorchModelPlan().ID, PermissionsID: setup.RulesDigest(rules), Confirm: true}
		// ComfyUI alone adds the starter model of the PyTorch engine it
		// installs and its picture rules on that engine.
		if beside {
			choice.Beside = []setup.AppID{app}
		}
		raw, _ := json.Marshal(choice)
		return string(raw)
	}
	for app, location := range map[setup.AppID]string{setup.ComfyUI: portable} {
		for _, path := range []string{"/setup/plan", "/setup/intent", "/setup/install"} {
			if got := call("POST", path, request(app, false)); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), location) {
				t.Fatalf("%s for %s without the choice: %d %s", path, app, got.Code, got.Body.String())
			}
		}
	}
	if _, err := os.Stat(journal.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused plan wrote the journal: %v", err)
	}
	for _, app := range []setup.AppID{setup.ComfyUI} {
		chosen := call("POST", "/setup/plan", request(app, true))
		var review setupPlanReview
		if chosen.Code != 200 || json.Unmarshal(chosen.Body.Bytes(), &review) != nil || len(review.OtherCopies) != 2 {
			t.Fatalf("review of %s with the choice: %d %s", app, chosen.Code, chosen.Body.String())
		}
		if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
			if review.Preparable || len(review.Blockers) == 0 || review.DownloadBytes != 0 {
				t.Fatalf("the Windows build should be blocked: preparable=%v blockers=%v download_bytes=%d", review.Preparable, review.Blockers, review.DownloadBytes)
			}
			continue
		}
		if review.PictureModel == nil || review.PictureModel.ID != setup.PyTorchModelPlan().ID || len(review.Permissions) != 1 {
			t.Fatalf("ComfyUI alone lacks its engine's starter model: %+v", review)
		}
		if got := call("POST", "/setup/install", request(app, true)); got.Code != 200 {
			t.Fatalf("Install of %s with the choice: %d %s", app, got.Code, got.Body.String())
		}
	}
	if after := panelTreeDigest(t, root); !maps2Equal(after, before) {
		t.Fatalf("the other copies changed\nbefore %v\nafter  %v", before, after)
	}
}

func maps2Equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}

// panelTransfer hands Setup's runner the stand-in bytes as a completed
// public download.
type panelTransfer struct {
	data []byte
	work setup.WorkIdentity
}

func (f *panelTransfer) Reserve(_ context.Context, _ setup.PinnedDownload, key string) (setup.WorkIdentity, error) {
	f.work = setup.WorkIdentity{Key: key, HistoryEpoch: "epoch",
		Binding:           client.JobsBinding{Endpoint: "accept", LogicalOwner: "owner"},
		OperationsBinding: client.JobsBinding{Endpoint: "operations", LogicalOwner: "owner"}}
	return f.work, nil
}
func (f *panelTransfer) Submit(context.Context, setup.PinnedDownload, setup.WorkIdentity) error {
	return nil
}
func (f *panelTransfer) Observe(context.Context, setup.WorkIdentity) (setup.TransferObservation, error) {
	return setup.TransferObservation{State: "complete", Done: int64(len(f.data)), Total: int64(len(f.data))}, nil
}
func (f *panelTransfer) Copy(_ context.Context, _ setup.WorkIdentity, dst io.Writer) (int64, error) {
	return io.Copy(dst, bytes.NewReader(f.data))
}

func installHandlerFor(handler http.Handler) func(method, path, body string) *httptest.ResponseRecorder {
	return func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		return got
	}
}
