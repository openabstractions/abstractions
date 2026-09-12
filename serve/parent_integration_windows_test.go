package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-download/go/serve/runtimehost"
)

// Set the probe to a separately built central executable. This exercises its
// actual entry point under the installed parent's native containment mechanism.
func TestContainedCentralRuntime(t *testing.T) {
	executable := os.Getenv("OA_RUNTIME_HOST_PROBE")
	if executable == "" {
		t.Skip("set OA_RUNTIME_HOST_PROBE to a built central executable")
	}
	options, _ := isolatedRuntime(t)
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		host, err := runtimehost.Start(ctx, runtimehost.Options{
			Executable:     executable,
			Args:           []string{"--endpoint", options.endpoint, "--log-endpoint", options.logEndpoint, "--config-endpoint", options.configEndpoint, "--out", options.out},
			StartupTimeout: 10 * time.Second, ShutdownTimeout: 5 * time.Second, ForceTimeout: 2 * time.Second,
		})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = runtimeStatus([]string{"--json", "--endpoint", options.endpoint}, &out, io.Discard)
		if err != nil {
			cancel()
			host.Close()
			t.Fatalf("ready child: %v %s", err, out.String())
		}
		closeErr := host.Close()
		cancel()
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if err := host.Wait(); err != nil {
			t.Fatal(err)
		}
		assertListenersReleased(t, options)
	}
}
