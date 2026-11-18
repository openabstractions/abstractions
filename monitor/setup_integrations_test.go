package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func readIntegrations(t *testing.T, p *servicePanel) map[string]setupIntegration {
	t.Helper()
	got := installHandlerFor(p.handler("setup-test-key"))("GET", "/setup/integrations", "")
	if got.Code != http.StatusOK {
		t.Fatalf("GET /setup/integrations: %d %s", got.Code, got.Body.String())
	}
	var view struct {
		Integrations []setupIntegration `json:"integrations"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	var order []string
	rows := map[string]setupIntegration{}
	for _, row := range view.Integrations {
		order = append(order, row.ID)
		rows[row.ID] = row
	}
	if want := []string{"comfyui", "opencode", "claude", "codex"}; len(order) != len(want) || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] || order[3] != want[3] {
		t.Fatalf("integration order %v, want %v", order, want)
	}
	return rows
}

// integrationFixtureImage is an enrolled connector at the helper file the
// connection writers install.
type integrationFixtureImage struct{ program string }

func (i integrationFixtureImage) Program() string             { return i.program }
func (i integrationFixtureImage) FileKey() string             { return "fixture" }
func (i integrationFixtureImage) Check(context.Context) error { return nil }
func (i integrationFixtureImage) Close() error                { return nil }

// helperProgram is the connector file the agent-integrations installer
// writes for party on this platform.
func helperProgram(party setup.AgentParty) string {
	leaf := nativecli.AgentApplicationForParty(string(party))
	if runtime.GOOS == "windows" {
		leaf += ".exe"
	} else if runtime.GOOS == "darwin" {
		leaf += "-" + strings.Repeat("ab", 32)
	}
	return filepath.Join(os.TempDir(), "agent-integrations", leaf)
}

func stubEnrolled(t *testing.T, enrolled ...setup.AgentParty) {
	t.Helper()
	old := integrationEnrolledImage
	t.Cleanup(func() { integrationEnrolledImage = old })
	integrationEnrolledImage = func(_ context.Context, party setup.AgentParty) (claudePanelImage, bool) {
		for _, e := range enrolled {
			if e == party {
				return integrationFixtureImage{helperProgram(party)}, true
			}
		}
		return nil, false
	}
}

// Catalog and Apps read every integration from the same list, through the
// served routes: the picture app from its card, OpenCode from its program
// and enrollment, Claude and Codex from their enrollment. Each row names
// the flow its button runs.
func TestSetupIntegrationsListEveryIntegrationInEachState(t *testing.T) {
	stubOverviewMachine(t)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal}
	stubOfficialComfy(t)
	stubEnrolled(t)

	rows := readIntegrations(t, p)
	for id, want := range map[string]setupIntegration{
		"comfyui":  {Title: "ComfyUI", State: "get", Page: "/app/comfyui", Download: "https://www.comfy.org/download"},
		"opencode": {Title: "OpenCode", State: "get", Page: "/connections/opencode", Download: "https://opencode.ai/download"},
		"claude":   {Title: "Claude", State: "connect", Page: "/connections/claude"},
		"codex":    {Title: "Codex", State: "connect", Page: "/connections/codex"},
	} {
		got := rows[id]
		if got.Title != want.Title || got.State != want.State || got.Page != want.Page || got.Download != want.Download || got.Caption == "" || got.Connected || got.Open != "" {
			t.Errorf("%s on a fresh computer: %+v", id, got)
		}
	}

	stubOfficialComfy(t, setup.ExtensionHost{Kind: setup.ExtensionDesktop, Base: `C:\ComfyUI`})
	desktopProgramPresent = func(app setup.AppID) bool { return app == setup.OpenCode }
	rows = readIntegrations(t, p)
	if rows["comfyui"].State != "connect" || !rows["comfyui"].Installed || rows["opencode"].State != "connect" || !rows["opencode"].Installed {
		t.Fatalf("installed, not connected: %+v %+v", rows["comfyui"], rows["opencode"])
	}

	stubOfficialComfy(t, setup.ExtensionHost{Kind: setup.ExtensionDesktop, Base: `C:\ComfyUI`, Connected: true, Program: `C:\ComfyUI.exe`})
	stubEnrolled(t, setup.AgentOpenCode, setup.AgentClaude, setup.AgentCodex)
	rows = readIntegrations(t, p)
	if got := rows["comfyui"]; got.State != "open" || !got.Connected || got.Open != "/setup/comfy/open" {
		t.Fatalf("connected ComfyUI: %+v", got)
	}
	for _, id := range []string{"opencode", "claude", "codex"} {
		if got := rows[id]; got.State != "manage" || !got.Installed || !got.Connected {
			t.Errorf("connected %s: %+v", id, got)
		}
	}

	// The Catalog's overview carries the same list.
	overview := readOverview(t, p)
	if len(overview.Integrations) != 4 || overview.Integrations[2].ID != "claude" || overview.Integrations[2].State != "manage" {
		t.Fatalf("overview integrations: %+v", overview.Integrations)
	}
}

// The names each integration's writers register reach both pages: Apps shows
// one row per connected integration and none of its registered entries
// beside it, and Catalog one row per integration
// (integrations_names_page_test.js).
func TestIntegrationWriterNamesShowEachAppOnce(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel pages")
	}
	stubOverviewMachine(t)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal}
	stubOfficialComfy(t, setup.ExtensionHost{Kind: setup.ExtensionDesktop, Base: `C:\ComfyUI`, Connected: true, Program: `C:\ComfyUI.exe`})
	stubEnrolled(t, setup.AgentOpenCode, setup.AgentClaude, setup.AgentCodex)
	desktopProgramPresent = func(app setup.AppID) bool { return app == setup.OpenCode }
	got := installHandlerFor(p.handler("setup-test-key"))("GET", "/setup/integrations", "")
	if got.Code != http.StatusOK {
		t.Fatalf("GET /setup/integrations: %d %s", got.Code, got.Body.String())
	}
	// The names the writers register: the picture package's registration,
	// agentConnectionSetup's agentImageApplication for each enrolled
	// connector, and the installed OpenCode manifest's party name.
	picture, _ := picturePackageIdentity()
	registered := []map[string]string{{"name": picture, "title": "ComfyUI"}, {"name": string(installedConnection().party), "title": "OpenCode"}}
	for _, party := range []setup.AgentParty{setup.AgentOpenCode, setup.AgentClaude, setup.AgentCodex} {
		base := nativecli.AgentApplicationForParty(string(party))
		registered = append(registered, map[string]string{"name": agentImageApplication(integrationFixtureImage{helperProgram(party)}, base), "title": appPageTitleForParty(party)})
	}
	registered = append(registered, map[string]string{"name": "notes", "title": "Notes"})
	fixture := map[string]any{"integrations": json.RawMessage(got.Body.Bytes()), "registered": registered}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "names.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "integrations_names_page_test.js", file).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
}

// appPageTitleForParty is the title a registered connector reads under.
func appPageTitleForParty(party setup.AgentParty) string {
	title, _ := nativecli.AgentTitle(nativecli.AgentApplicationForParty(string(party)))
	return title
}

// Both pages render all four integrations in each state in one row shape
// (integrations_page_test.js).
func TestCatalogAndAppsRenderEveryIntegration(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel pages")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "integrations_page_test.js").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
}

// TestIntegrationRenderFixtures writes the Catalog and Apps documents with a
// fixture.js beside them that answers the pages' reads, for screenshots
// (OA_INTEGRATIONS_FIXTURE_OUTPUT names the folder; serve it with
// monitor/static under static/).
func TestIntegrationRenderFixtures(t *testing.T) {
	out := os.Getenv("OA_INTEGRATIONS_FIXTURE_OUTPUT")
	if out == "" {
		t.Skip("inert screenshot output is opt-in")
	}
	for name, page := range map[string]struct{ active, title, markup string }{
		"setup.html": {"setup", "Catalog", setupPage},
		"apps.html":  {"status", "Apps", servicePage},
	} {
		r := httptest.NewRequest("GET", "http://127.0.0.1/?platform=windows", nil)
		doc := panelPage(r, "fixture", page.active, page.title, page.markup)
		doc = strings.Replace(doc, "<head>", `<head><script src="fixture.js"></script>`, 1)
		if err := os.WriteFile(filepath.Join(out, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
