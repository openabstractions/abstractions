package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// selfProgramSites is every function of this package that reads the Panel's
// own program path (os.Executable), with why it cannot reach the installed
// runtime from a Panel that serves an explicit one. A launch passes the
// explicit runtime's flags, runs only where panelExplicit is nil, or runs
// only inside a mode that never binds an explicit runtime (--setup and
// --finish, main.go). A new site needs its own line here.
var selfProgramSites = map[string]string{
	"installedSetupTransport":                  "runs only when panelExplicit is nil and verifies the Panel beside the registered runtime before binding it",
	"siblingProgram":                           "names an OA program beside this one; panelProgramPath and presenterExecutable carry its callers' rules",
	"panelProgramPath":                         "the Panel for launchActivityPanel, launchQuestionPanel and launchProgramRepairPanel (questionPanelCommand passes -runtime-endpoint, -runtime-program and -runtime-state-dir), and for launchSetupStep and launchSetupStepFor, which run only where panelExplicit is nil (the presenter's resume and request watch, setupStepState's POST)",
	"presenterExecutable":                      "the presenter for launchPresenter and presentThroughPresenter, which pass explicitRuntimeArgs(panelExplicit)",
	"openAbstractionsCommand":                  "launches openabstractions; modelHostOpenAbstractions appends --endpoint and --runtime-program",
	"servicePanel.modelHostPrograms":           "locates the installed modelhostd and inventoryd; launches none",
	"servicePanel.reviewClaudeBackgroundScope": "names this Panel's program in a question; launches none",
	"exploreSelfProgram":                       "names this Panel's program; launches none",
	"oaOwnPrograms":                            "titles this Panel and its runtime programs; launches none",
	"loadPanelNativeModel":                     "loads the app-model library beside the Panel; launches none",
	"main":                                     "validates review examples against this Panel's program; launches none",
	"guardLoopbackPage":                        "checks whether this Panel is the installed one to guard its developer page; launches none",
}

func TestEverySelfProgramSiteIsListed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	set := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				recv := fn.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if ident, ok := recv.(*ast.Ident); ok {
					key = ident.Name + "." + key
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Executable" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" {
						found[key] = true
					}
				}
				return true
			})
		}
	}
	for key := range found {
		if _, ok := selfProgramSites[key]; !ok {
			t.Errorf("%s reads os.Executable and is not in selfProgramSites: say how it keeps an explicit runtime's Panel off the installed runtime", key)
		}
	}
	for key := range selfProgramSites {
		if !found[key] {
			t.Errorf("selfProgramSites lists %s, which no longer reads os.Executable", key)
		}
	}
}

// Review of ba49901ef, F1: a Panel serving an explicit runtime starts no
// --setup process, which would run against the installed runtime and this
// account's own step journal; the row and the click say Setup runs only on
// the installed OpenAbstractions. The installed Panel's click still claims
// the session's step and starts it.
func TestSetupStepRowStartsNothingForAnExplicitRuntime(t *testing.T) {
	launches, claims := 0, 0
	savedLaunch, savedClaim, savedExplicit := setupStepLaunch, setupStepClaim, panelExplicit
	t.Cleanup(func() { setupStepLaunch, setupStepClaim, panelExplicit = savedLaunch, savedClaim, savedExplicit })
	setupStepLaunch = func() error { launches++; return nil }
	setupStepClaim = func() (func(), bool, error) { claims++; return func() {}, true, nil }
	h := newTestPanel(t).handler("test-key")

	panelExplicit = &explicitRuntime{Endpoint: "oa-fixture-runtime", Program: `C:\fixture\openabstractions.exe`}
	got := panelRequest(t, h, "/setup/step", map[string]string{})
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), stepInstalledOnly) || launches != 0 || claims != 0 {
		t.Fatalf("explicit POST: %d %s launches %d claims %d", got.Code, got.Body.String(), launches, claims)
	}
	row := panelRequest(t, h, "/setup/step", nil)
	if row.Code != http.StatusOK || !strings.Contains(row.Body.String(), `"model":"`+stepInstalledOnly+`"`) || strings.Contains(row.Body.String(), `"ready":true`) {
		t.Fatalf("explicit row: %d %s", row.Code, row.Body.String())
	}

	// With the runtime's state folder Setup runs there (the Setup page), so
	// the row reads that folder's step and never says Setup cannot run; the
	// click still starts no --setup process.
	state := t.TempDir()
	if err := setup.UseRuntimeState(state); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.UseUserState)
	panelExplicit = &explicitRuntime{Endpoint: "oa-fixture-runtime", Program: `C:\fixture\openabstractions.exe`, StateDir: state}
	if row := panelRequest(t, h, "/setup/step", nil); row.Code != http.StatusOK || strings.Contains(row.Body.String(), stepInstalledOnly) {
		t.Fatalf("explicit row with a state folder: %d %s", row.Code, row.Body.String())
	}
	if got := panelRequest(t, h, "/setup/step", map[string]string{}); !strings.Contains(got.Body.String(), stepInstalledOnly) || launches != 0 || claims != 0 {
		t.Fatalf("explicit POST with a state folder: %s launches %d claims %d", got.Body.String(), launches, claims)
	}
	setup.UseUserState()

	panelExplicit = nil
	got = panelRequest(t, h, "/setup/step", map[string]string{})
	if got.Code != http.StatusOK || launches != 1 || claims != 1 || strings.Contains(got.Body.String(), stepInstalledOnly) {
		t.Fatalf("installed POST: %d %s launches %d claims %d", got.Code, got.Body.String(), launches, claims)
	}
	if row := panelRequest(t, h, "/setup/step", nil); strings.Contains(row.Body.String(), stepInstalledOnly) {
		t.Fatalf("installed row: %s", row.Body.String())
	}
}
