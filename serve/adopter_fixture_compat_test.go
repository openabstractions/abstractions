package main

// Generic compatibility for the remaining application/native-binding/causal
// fixtures. Adopter assertions live in adopters/opencode/test; shared support
// has one test-only owner in mcp-gateway/fixture.
import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

const openCodeProviderFixtureArg = fixture.OpenCodeProviderFixtureArg

func init() {
	if len(os.Args) > 1 && os.Args[1] == openCodeProviderFixtureArg {
		os.Exit(fixture.RunOpenCodeProviderFixtureArgs(os.Args))
	}
}

func copyTree(t *testing.T, source, target string) { fixture.CopyTree(t, source, target) }
func fileURL(path string) string                   { return fixture.FileURL(path) }

type openCodeProviderSources struct {
	provider, facade, inference, ipc, router, asks, rights, storage, logging, download string
}

func findOpenCodeProviderSources(root string) (openCodeProviderSources, error) {
	v, err := fixture.FindOpenCodeProviderSources(root)
	return openCodeProviderSources{provider: v.Provider, facade: v.Facade, inference: v.Inference, ipc: v.Ipc, router: v.Router, asks: v.Asks, rights: v.Rights, storage: v.Storage, logging: v.Logging, download: v.Download}, err
}

func stageOpenCodeProviderFrom(t *testing.T, root string) string {
	return fixture.StageOpenCodeProviderFrom(t, root)
}
func stageOpenCodeProvider(t *testing.T) string {
	return stageOpenCodeProviderFrom(t, filepath.Clean(".."))
}
func isolatedOpenCodeEnvironment(t *testing.T, root, addon, library string) []string {
	return fixture.IsolatedOpenCodeEnvironment(t, root, addon, library)
}
func environmentWithout(environment []string, names ...string) []string {
	return fixture.EnvironmentWithout(environment, names...)
}
