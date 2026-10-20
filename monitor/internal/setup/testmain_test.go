package setup

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
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
func TestMain(m *testing.M) {
	if err := os.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", unservedRuntimeEndpoint); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	discoverRuntime = func() *client.Machine { return client.New(unservedRuntimeEndpoint) }
	os.Exit(m.Run())
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
