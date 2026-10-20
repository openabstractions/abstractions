package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The shipped Panel is this package built with -tags panel; the presenter is
// the same package built with -tags presenter (installer/build.py). Only the
// presenter compiles the question dialogs, the credential form and the toast
// surface, so the Panel binary cannot link or draw them.
func TestPanelBuildLinksNoNativeQuestionOrNotificationSurface(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	files := func(tags, pkg string) []string {
		t.Helper()
		cmd := exec.Command("go", "list", "-tags", tags, "-f", `{{join .GoFiles "\n"}}`, pkg)
		cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -tags %s %s: %v", tags, pkg, err)
		}
		return strings.Fields(strings.ReplaceAll(string(out), "\r", ""))
	}
	surfaces := map[string][]string{
		"./win": {"permission_dialog_windows.go", "credential_form_windows.go", "taskdialog_windows.go",
			"permission_theme_windows.go", "permission_chime_windows.go", "toast_windows.go"},
		".": {"presenter_windows.go", "notification_windows.go", "presenter_main_windows.go"},
	}
	for pkg, names := range surfaces {
		panel, presenter := files("panel", pkg), files("presenter", pkg)
		for _, name := range names {
			if slices.Contains(panel, name) {
				t.Errorf("the Panel build compiles %s/%s", pkg, name)
			}
			if !slices.Contains(presenter, name) {
				t.Errorf("the presenter build lacks %s/%s", pkg, name)
			}
		}
	}
	if panel := files("panel", "."); !slices.Contains(panel, "main.go") || slices.Contains(files("presenter", "."), "main.go") {
		t.Error("each build must have its own main")
	}
}
