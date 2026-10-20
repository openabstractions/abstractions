package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/placetest"
)

func TestComfyFuturePathsExpandTheSameExistingShortAncestor(t *testing.T) {
	long := filepath.Join(t.TempDir(), "existing ancestor with spaces")
	if err := os.MkdirAll(long, 0700); err != nil {
		t.Fatal(err)
	}
	short, err := placetest.ShortName(long)
	if err != nil || strings.EqualFold(short, long) {
		t.Skipf("actual8.3 fixture unavailable: %q %v", short, err)
	}
	if err := UseRuntimeState(short); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	plan := mustPlanOf(t, fixtureCatalog(), ComfyUI, Lemonade)
	component, ok := planComponent(plan, "comfyui-app")
	if !ok || component.Artifact == nil {
		t.Fatal("missing fixture app pin")
	}
	a := *component.Artifact
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("fixture requires future root: %v", err)
	}
	paths, err := comfyPathsForArtifact(a)
	if err != nil {
		t.Fatal(err)
	}
	if paths.install != identity.CanonicalProgramPath(root) || !withinPath(paths.install, paths.caller) {
		t.Fatalf("future locators disagree: %+v", paths)
	}
	if _, err := os.Lstat(paths.install); !os.IsNotExist(err) {
		t.Fatalf("derivation created an installation: %v", err)
	}
	outside := filepath.Join(long, "outside", "python.exe")
	if _, err := comfyPathsAt(paths.install, outside, paths.user, paths.output); err == nil {
		t.Fatal("outside caller admitted")
	}
}
