package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	providerSetup "github.com/openabstractions/abstraction-provider-lemonade/go/setup"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// ST-2 ruling 2a: on macOS and Linux the bridge accepts a Lemonade provider
// root under AccountData, Setup's own data root, and refuses one under the
// old cache folder.
func TestLemonadeProviderRootFollowsAccountData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps %LOCALAPPDATA% as both roots")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	data, err := layout.AccountData()
	if err != nil {
		t.Fatal(err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	parse := func(root string) error {
		info := providerSetup.SetupInfoReply{Version: 2, ProviderID: lemonadeProviderID, ProviderRoot: root, DownloadDir: filepath.Join(root, "engine-downloads"),
			KnownPackages: []providerSetup.KnownPackageInfo{{Version: "v1", ArchiveSHA256: "a", ProgramSHA256: "b", BackendArchiveSHA256: "c", ModelRevision: "d"}}}
		raw, _ := json.Marshal(info)
		_, err := parseLemonadeInfo(raw, nil)
		return err
	}
	if err := parse(filepath.Join(data, "OpenAbstractions", "providers", "lemonade")); err != nil {
		t.Fatalf("a provider root under the account data folder: %v", err)
	}
	if err := parse(filepath.Join(cache, "OpenAbstractions", "providers", "lemonade")); err == nil {
		t.Fatal("a provider root under the old cache folder was accepted")
	}
	if home, err := CurrentHome(); err != nil || home.Data != data {
		t.Fatalf("Setup data root %q %v, want %q", home.Data, err, data)
	}
}
