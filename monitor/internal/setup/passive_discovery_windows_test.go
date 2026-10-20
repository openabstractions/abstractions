package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestDiscoveryRefusesLinkedRecord(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "installations.json"), []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(root, "ordinary")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(actual, "installations.json"), []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	if raw, err := readDiscoveryFile(context.Background(), root, "ordinary/installations.json"); err != nil || string(raw) != "[]" {
		t.Fatalf("ordinary record: %q, %v", raw, err)
	}
	link := filepath.Join(root, "linked")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("junction: %v %s", err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
	})
	for _, test := range []struct{ root, path string }{
		{root, "linked/installations.json"},
		{link, "installations.json"},
	} {
		if raw, err := readDiscoveryFile(context.Background(), test.root, test.path); err == nil || len(raw) != 0 {
			t.Fatalf("linked record from %s: %s, %v", strconv.Quote(test.root), raw, err)
		}
	}
}
