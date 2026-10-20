package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

func TestModelHostChatRootIsGenericAndStable(t *testing.T) {
	base := t.TempDir()
	modelRoot := filepath.Join(base, "Models I chose")
	if err := os.Mkdir(modelRoot, 0700); err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(base, "profile")
	first, err := NewModelHostChatRoot(modelRoot, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewModelHostChatRoot(modelRoot, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Store != "oa-chat-"+first.ID || !strings.HasPrefix(first.Provider, "oa-setup-chat-") {
		t.Fatalf("root names: first=%+v second=%+v", first, second)
	}
	if strings.Contains(first.Path, "OA-models") && !strings.Contains(modelRoot, "OA-models") {
		t.Fatalf("root was hardcoded to an owner folder: %+v", first)
	}
	if !filepath.IsAbs(first.ConfigPath) || !filepath.IsAbs(first.ReceiptPath) {
		t.Fatalf("Setup paths are not absolute: %+v", first)
	}
}

func TestModelHostChatRootUsesDirectoryFileIdentity(t *testing.T) {
	base := t.TempDir()
	configRoot := filepath.Join(base, "profile")
	for _, names := range [][2]string{{"link", "linK"}, {"xs", "xſ"}} {
		t.Run(names[0]+"-"+names[1], func(t *testing.T) {
			a, b := filepath.Join(base, names[0]), filepath.Join(base, names[1])
			if err := os.Mkdir(a, 0700); err != nil {
				t.Fatal(err)
			}
			mkdirBErr := os.Mkdir(b, 0700)
			if mkdirBErr != nil && !os.IsExist(mkdirBErr) {
				t.Fatal(mkdirBErr)
			}
			same, err := identity.SameFile(a, b)
			if err != nil {
				t.Fatal(err)
			}
			if mkdirBErr != nil && !same {
				t.Fatalf("creating twin %q failed, but it does not name %q: %v", b, a, mkdirBErr)
			}
			rootA, err := NewModelHostChatRoot(a, configRoot)
			if err != nil {
				t.Fatal(err)
			}
			rootB, err := NewModelHostChatRoot(b, configRoot)
			if err != nil {
				t.Fatal(err)
			}
			if same && rootA.ID != rootB.ID {
				t.Fatalf("aliases of one directory received different IDs: %q and %q", rootA.ID, rootB.ID)
			}
			if !same && rootA.ID == rootB.ID {
				t.Fatalf("distinct directories received the same Setup ID %q", rootA.ID)
			}
		})
	}
}

func TestModelHostChatConfigNamesOnlyTheApprovedRoot(t *testing.T) {
	base := t.TempDir()
	modelRoot := filepath.Join(base, "Chosen local models")
	if err := os.Mkdir(modelRoot, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := NewModelHostChatRoot(modelRoot, filepath.Join(base, "profile"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := ModelHostChatInventoryConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || len(decoded["extra_roots"]) == 0 || strings.Contains(string(data), "ABSTRACTION_MODEL_ROOTS") {
		t.Fatalf("config includes anything beyond the explicit root: %s", data)
	}
	var extras []struct {
		Store           string `json:"store"`
		Path            string `json:"path"`
		ExpectedFileKey string `json:"expected_file_key"`
	}
	if err := json.Unmarshal(decoded["extra_roots"], &extras); err != nil || len(extras) != 1 || extras[0].Store != root.Store || extras[0].Path != root.Path {
		t.Fatalf("config roots %+v %v, want only %+v", extras, err, root)
	}
	fileKey, err := identity.FileKey(root.Path)
	if err != nil || extras[0].ExpectedFileKey != fileKey {
		t.Fatalf("config did not pin the selected directory identity: %+v %v", extras, err)
	}
	sentence := ModelHostChatApproval(root)
	if !strings.Contains(sentence, root.Path) || !strings.Contains(sentence, root.ConfigPath) || !strings.Contains(sentence, root.Store) || ModelHostChatApprovalDigest(sentence) == "" {
		t.Fatalf("approval does not identify the exact effect: %q", sentence)
	}
}

func TestModelHostChatConfigRequiresTheSameDirectoryIdentity(t *testing.T) {
	base := t.TempDir()
	selected := filepath.Join(base, "chosen models")
	if err := os.Mkdir(selected, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := NewModelHostChatRoot(selected, filepath.Join(base, "profile"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ModelHostChatInventoryConfig(root); err != nil {
		t.Fatalf("selected folder config: %v", err)
	}
	if err := os.Rename(selected, selected+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(selected, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ModelHostChatInventoryConfig(root); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("replaced folder kept its old Setup identity: %v", err)
	}
}

func TestModelHostChatRootRefusesNonDirectoryAndRelativePaths(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "model.gguf")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(base, "profile")
	for _, path := range []string{"relative/models", file, filepath.Join(base, "missing")} {
		if _, err := NewModelHostChatRoot(path, configRoot); err == nil {
			t.Errorf("NewModelHostChatRoot(%q) succeeded", path)
		}
	}
	if runtime.GOOS == "windows" {
		if _, err := NewModelHostChatRoot(`\\server\share\models`, configRoot); err == nil {
			t.Fatal("network UNC root succeeded")
		}
	}
}

func TestModelHostChatRootResolvesDirectoryLinksBeforeApproval(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real models")
	link := filepath.Join(base, "selected alias")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	root, err := NewModelHostChatRoot(link, filepath.Join(base, "profile"))
	if err != nil {
		t.Fatal(err)
	}
	// The temporary folder may itself sit below a link (macOS /tmp and /var
	// are links into /private), so the resolved path is the target's own
	// resolution, not its spelling.
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if root.Path != want {
		t.Fatalf("approval would name link spelling %q, want resolved path %q", root.Path, want)
	}
}
