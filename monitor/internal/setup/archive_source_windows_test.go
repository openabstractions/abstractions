package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveSourceRefusesAnExistingWriterBeforeInstallation(t *testing.T) {
	source, pin := archiveFixture(t, archiveEntry{name: "program.exe", data: "catalog executable"})
	writer, err := os.OpenFile(source, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	dest := filepath.Join(t.TempDir(), "installed")
	if err := InstallArchive(context.Background(), pin, source, dest); err == nil {
		t.Fatal("source writer remained live during archive admission")
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("refused input published an installation: %v", err)
	}
}

func TestNativeRemovalRefusesAHandleHeldTree(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	source, a, _, plan := nativeArchiveFixture(t)
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(context.Background(), a, source, root); err != nil {
		t.Fatal(err)
	}
	old := nativeArchiveInstalledCommand
	t.Cleanup(func() { nativeArchiveInstalledCommand = old })
	nativeArchiveInstalledCommand = func(_ context.Context, _ sevenZipPin, original string) ([]byte, error) {
		plan.InstalledRoot = original
		return json.Marshal(plan)
	}
	hold, err := openArchiveSource(context.Background(), filepath.Join(root, a.ApplicationProgram))
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	if _, err := removePackageTree(a, root); err == nil {
		t.Fatal("held version moved or was removed")
	}
	if got, err := os.ReadFile(filepath.Join(root, a.ApplicationProgram)); err != nil || string(got) != "inert native image" {
		t.Fatal("held version changed")
	}
}
