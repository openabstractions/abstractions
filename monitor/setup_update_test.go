package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// A plan approved under the build before the Lemonade pin recorded its tested
// pairing: the same bytes, an earlier exact pin carried by install_pins.json.
func earlierLemonadePlan(t *testing.T) setup.Plan {
	t.Helper()
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	var earlier *setup.Artifact
	for _, pin := range setup.NewCatalog().PreviousInstallPins {
		if pin.ComponentID == "lemonade-engine" && pin.Artifact.TestedBackendSHA256 == "" {
			artifact := pin.Artifact
			earlier = &artifact
			break
		}
	}
	if earlier == nil {
		t.Fatal("the release no longer records the historical Lemonade installation")
	}
	plan.Components = append([]setup.PlannedComponent(nil), plan.Components...)
	plan.Components[0].Artifact = earlier
	plan.CatalogSHA256 = strings.Repeat("a", 64)
	key, _ := json.Marshal(struct {
		Catalog  string        `json:"catalog"`
		Selected []setup.AppID `json:"selected"`
	}{plan.CatalogSHA256, plan.Selected})
	id := sha256.Sum256(key)
	plan.ID = hex.EncodeToString(id[:])
	return plan
}

func TestSetupPlanReviewNamesTheInstalledVersionItReplaces(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("the recorded Lemonade pins are Windows x64 only")
	}
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	plan := earlierLemonadePlan(t)
	older := *plan.Components[0].Artifact
	older.Version, older.SHA256 = "v2026.38.0", strings.Repeat("b", 64)
	plan.Components[0].Artifact = &older
	state := setup.State{Plan: plan, ApprovedPlanID: plan.ID, Configuration: map[string]bool{},
		Effects: map[string]setup.Effect{"lemonade-engine": {Revision: 1, Phase: setup.Installed}}}
	data, _ := json.Marshal(state)
	if err := os.MkdirAll(filepath.Dir(journal.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	handler := (&servicePanel{setupJournal: &journal}).handler("setup-test-key")
	req := httptest.NewRequest("POST", "/setup/plan", strings.NewReader(`{"selected":["lemonade"]}`))
	req.Host = "127.0.0.1"
	req.Header.Set("X-Panel-Key", "setup-test-key")
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, req)
	var review setupPlanReview
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &review) != nil {
		t.Fatalf("plan review: %d %s", got.Code, got.Body.String())
	}
	if len(review.Replaces) != 1 || review.Replaces[0].From != "v2026.38.0" || review.Replaces[0].To != "v2026.39.1" ||
		!review.Preparable || len(review.Components) != 1 || review.Components[0].Artifact == nil {
		t.Fatalf("the review does not name the installed Lemonade version it replaces: %+v", review)
	}
	// The replacement projection itself still names both exact versions
	// when an explicitly supplied plan has an artifact.
	known := earlierLemonadePlan(t)
	replacements := setupReplacements(known, map[string]setup.Artifact{"lemonade-engine": older})
	if len(replacements) != 1 || replacements[0].From != "v2026.38.0" || replacements[0].To != "v2026.39.1" {
		t.Fatalf("the artifact projection lost the replaced version: %+v", replacements)
	}
	if setupReplacements(known, map[string]setup.Artifact{"lemonade-engine": *known.Components[0].Artifact}) != nil {
		t.Fatal("an unchanged installed version was named as replaced")
	}
}

func TestSetupRecoverSettlesAnEarlierPlansInterruptedInstall(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("the recorded Lemonade pins are Windows x64 only")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	plan := earlierLemonadePlan(t)
	state := setup.State{Plan: plan, ApprovedPlanID: plan.ID, Configuration: map[string]bool{"lemonade-model": false},
		Effects: map[string]setup.Effect{"lemonade-engine": {Revision: 3, Phase: setup.Installing, StagedPath: "old.zip"}}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(journal.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	handler := (&servicePanel{setupJournal: &journal}).handler("setup-test-key")
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		return got
	}
	current, err := setup.NewCatalog().Plan([]setup.AppID{setup.Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	approve := `{"selected":["lemonade"],"plan_id":"` + current.ID + `","confirm":true}`
	if got := post("/setup/intent", approve); got.Code != http.StatusConflict {
		t.Fatalf("an interrupted earlier install was dropped by a new approval: %d %s", got.Code, got.Body.String())
	}
	if got := post("/setup/advance", `{"plan_id":"`+plan.ID+`","component_id":"lemonade-engine"}`); got.Code != http.StatusConflict {
		t.Fatalf("an earlier plan advanced: %d %s", got.Code, got.Body.String())
	}
	got := post("/setup/recover", `{"plan_id":"`+plan.ID+`","component_id":"lemonade-engine"}`)
	if got.Code != http.StatusOK {
		t.Fatalf("explicit reconciliation refused: %d %s", got.Code, got.Body.String())
	}
	var reply setupPanelStatus
	if err := json.Unmarshal(got.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.CurrentCatalog || len(reply.Components) != 1 || reply.Components[0].Effect.Phase != setup.Failed {
		t.Fatalf("an install that left nothing was not settled: %+v", reply)
	}
	if got := post("/setup/intent", approve); got.Code != http.StatusOK {
		t.Fatalf("the settled earlier plan still blocks the current one: %d %s", got.Code, got.Body.String())
	}
}

// R19 render check, 2026-11-09: the update that interrupted an install also
// changed the catalogue. GET /setup/install keeps the earlier release's
// approval of the journal's plan, marked interrupted, so the page can show
// it stopped; an approval of another plan stays hidden.
func TestInstallStateKeepsAnEarlierCatalogsInterruptedApproval(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("the recorded Lemonade pins are Windows x64 only")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	plan := earlierLemonadePlan(t)
	state := setup.State{Plan: plan, ApprovedPlanID: plan.ID, Configuration: map[string]bool{"lemonade-model": false},
		Effects: map[string]setup.Effect{"lemonade-engine": {Revision: 3, Phase: setup.Requested}}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(journal.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := setup.PanelBuild
	setup.PanelBuild = func() string { return "r19" }
	t.Cleanup(func() { setup.PanelBuild = previous })
	record := `{"plan_id":"` + plan.ID + `","rules_digest":"","approved_at":"2026-11-09T10:29:05Z","build":"r18"}`
	installPath := filepath.Join(filepath.Dir(journal.Path), "install.json")
	if err := os.WriteFile(installPath, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	call := installHandler(t, &journal)
	read := func() setupInstallReply {
		t.Helper()
		got := call("GET", "/setup/install", "")
		var reply setupInstallReply
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &reply) != nil {
			t.Fatalf("read: %d %s", got.Code, got.Body.String())
		}
		return reply
	}
	reply := read()
	if reply.CurrentCatalog || reply.Approved || reply.Plan.ID != plan.ID || reply.Install.PlanID != plan.ID || reply.Install.Interrupted != setup.InterruptedByUpdate {
		t.Fatalf("the earlier approval is not shown stopped: current %v approved %v plan %s install %+v", reply.CurrentCatalog, reply.Approved, reply.Plan.ID, reply.Install)
	}
	if phase := reply.Components[0].Effect.Phase; phase != setup.Requested {
		t.Fatalf("reading the stopped install changed its row: %s", phase)
	}
	if err := os.WriteFile(installPath, []byte(`{"plan_id":"another-plan","approved_at":"2026-11-09T10:29:05Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if reply := read(); reply.Install.PlanID != "" {
		t.Fatalf("an approval of another plan was shown: %+v", reply.Install)
	}
}
