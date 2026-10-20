package setup

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The launch helper's bootstrap reads its profile view; under the
// environment Setup builds for every helper operation it must find one.
// The child is this test binary, whose TestMain probes the profile view
// with exactly that environment.
func TestNativeArchiveEnvironmentGivesTheProfileView(t *testing.T) {
	work := t.TempDir()
	env, err := nativeArchiveEnvironment(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range nativeArchiveProfileNames {
		found := false
		for _, entry := range env {
			if strings.HasPrefix(strings.ToUpper(entry), name+"=") && len(entry) > len(name)+1 {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is absent from the helper environment", name)
		}
	}
	child := exec.Command(os.Args[0], "-test.run=^TestNativeArchiveEnvironmentGivesTheProfileView$", "-test.count=1")
	child.Env = append(env, "OA_SETUP_PROFILE_CHILD=1")
	child.Dir = work
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("a helper operation under Setup's environment has no profile view: %v\n%s", err, out)
	}
}
