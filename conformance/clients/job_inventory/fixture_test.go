package inventory_test

import (
	"context"
	"errors"
	"fmt"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestInstalledInventory(t *testing.T) {
	probe := os.Getenv("OA_CPP_INVENTORY_PROBE")
	if probe == "" {
		t.Fatal("OA_CPP_INVENTORY_PROBE required")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable on current Darwin implementation")
	}
	for _, mode := range []string{"pages", "denied"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			endpoint := func(name string) string {
				if runtime.GOOS == "windows" {
					return fmt.Sprintf(`\\.\pipe\oa-inventory-%d-%s`, time.Now().UnixNano(), name)
				}
				return filepath.Join(dir, name+".sock")
			}
			o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), JobEndpoint: endpoint("jobs"), JobRoot: filepath.Join(dir, "private"), JobOwner: "inventory-fixture"}
			if mode == "denied" {
				o.JobMethodPolicy = func(_ context.Context, _ *identity.Peer, service, method string) error {
					if service == "abstraction.job/inventory@1" {
						return errors.New("denied inventory")
					}
					return nil
				}
			}
			h, err := host.Listen(o)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			done := make(chan error, 1)
			go func() { done <- h.Serve(ctx) }()
			defer func() {
				cancel()
				h.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("host did not drain")
				}
			}()
			command := exec.CommandContext(ctx, probe, o.Endpoint, mode)
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v", out, err)
			}
			t.Log(string(out))
		})
	}
}
