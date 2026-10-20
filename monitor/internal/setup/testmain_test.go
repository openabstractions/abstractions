package setup

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/profile"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/monitor/internal/personfolders"
)

// unservedRuntimeEndpoint is an endpoint no process serves.
var unservedRuntimeEndpoint = listen.Endpoint(fmt.Sprintf("oa-setup-test-unserved-%d", os.Getpid()))

// TestMain keeps every test away from the runtime installed on the machine
// running the tests: discoverRuntime (client.Discover in production) binds an
// endpoint nobody serves, which also covers macOS, whose installed selection
// ignores the environment, and ABSTRACTION_RUNTIME_ENDPOINT names it for
// Windows and Linux selection and the unverified default. Set, it also stops
// Windows discovery from starting an installed runtime. A test that needs a
// runtime passes its own Machine (PublicTransfer.Machine) or replaces the seam.
//
// TestMain also keeps every test away from the person's own folders: the
// process's per-user folders lie in testPerson, a fresh temporary folder
// removed afterwards (personfolders.Enter). A test that installs takes its
// own (standInHome).
func TestMain(m *testing.M) {
	// A launch helper stand-in: probe the profile view with exactly the
	// environment the parent test built, before anything here changes it.
	if os.Getenv("OA_SETUP_PROFILE_CHILD") == "1" {
		if _, err := profile.Current(); err != nil {
			fmt.Println("profile view:", err)
			os.Exit(3)
		}
		os.Exit(0)
	}
	if err := os.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", unservedRuntimeEndpoint); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	discoverRuntime = func() *client.Machine { return client.New(unservedRuntimeEndpoint) }
	person, err := personfolders.Enter()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	testPerson = person
	code := m.Run()
	personfolders.Leave(person)
	os.Exit(code)
}

// testPerson holds the per-user folders of a test without its own stand-in.
var testPerson string

// A test without its own stand-in home resolves Setup's per-user folders
// inside a temporary folder. The person's real folders can hold an earlier
// install at a fixture's identity: on the Mac a 2026-10-07 run left
// Library/Caches/OpenAbstractions/apps/oa-setup-lemonade-engine/1.2.3, and
// four runner tests then met "destination already exists".
func TestATestWithoutAFixtureReachesNoPersonFolder(t *testing.T) {
	home, err := CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	for what, path := range map[string]string{"data": home.Data, "config": home.Config} {
		if testPerson == "" || !identity.Inside(path, testPerson) {
			t.Errorf("Setup's %s folder %s is the person's own, outside the stand-in %q", what, path, testPerson)
		}
	}
}

func TestATestWithoutAFixtureReachesNoRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := setupEngineRegistry(ctx); err == nil {
		t.Fatal("the default engine registry reached a runtime")
	}
	if _, err := (PublicTransfer{}).machine().ObserveCaller(ctx); err == nil {
		t.Fatal("the default transfer machine reached a runtime")
	}
}
