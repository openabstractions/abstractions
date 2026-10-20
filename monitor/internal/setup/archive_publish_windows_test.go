package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

func stageRenameFixture(t *testing.T) (stage, destination, member, original string) {
	t.Helper()
	root := t.TempDir()
	stage, destination = filepath.Join(root, ".stage"), filepath.Join(root, "installed")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	member = filepath.Join(stage, "fixture.txt")
	if err := os.WriteFile(member, []byte("verified"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(stage)
	if err != nil {
		t.Fatal(err)
	}
	original, err = fileid.FileKeyOf(dir)
	if err := errors.Join(err, dir.Close()); err != nil {
		t.Fatal(err)
	}
	return stage, destination, member, original
}

func TestWindowsStagePublicationWaitsForNonDeletingMember(t *testing.T) {
	stage, destination, member, original := stageRenameFixture(t)
	path, err := windows.UTF16PtrFromString(member)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
	})
	calls := 0
	err = publishWindowsArchiveDirectory(context.Background(), stage, destination, original, func(file *os.File, parent fileid.Handle, name string) error {
		calls++
		err := renameReplacementNoReplace(file, parent, name)
		if calls == 1 {
			var status windows.NTStatus
			if !errors.As(err, &status) || status.Errno() != windows.ERROR_ACCESS_DENIED {
				t.Fatalf("the held member must reproduce native error 5, got %v", err)
			}
			if err := windows.CloseHandle(h); err != nil {
				t.Fatal(err)
			}
			h = 0
		}
		return err
	})
	if err != nil || calls != 2 {
		t.Fatalf("publication after the reader closed: calls %d, error %v", calls, err)
	}
	if raw, err := os.ReadFile(filepath.Join(destination, filepath.Base(member))); err != nil || string(raw) != "verified" {
		t.Fatalf("published content: %q, %v", raw, err)
	}
}

func TestWindowsStagePublicationRefusesForeignDestination(t *testing.T) {
	stage, destination, member, original := stageRenameFixture(t)
	calls := 0
	err := publishWindowsArchiveDirectory(context.Background(), stage, destination, original, func(file *os.File, parent fileid.Handle, name string) error {
		calls++
		// Race the actual native move with an empty directory. A separate
		// precheck alone cannot protect it.
		if err := os.Mkdir(destination, 0700); err != nil {
			t.Fatal(err)
		}
		return renameReplacementNoReplace(file, parent, name)
	})
	if err == nil || calls != 1 {
		t.Fatalf("foreign destination: calls %d, error %v", calls, err)
	}
	if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
		t.Fatalf("foreign destination changed: %v, %v", entries, err)
	}
	if raw, err := os.ReadFile(member); err != nil || string(raw) != "verified" {
		t.Fatalf("stage changed: %q, %v", raw, err)
	}
}

func TestWindowsStagePublicationDeniesSwapAtCommitBoundary(t *testing.T) {
	stage, destination, member, original := stageRenameFixture(t)
	err := publishWindowsArchiveDirectory(context.Background(), stage, destination, original, func(file *os.File, parent fileid.Handle, name string) error {
		if err := os.Rename(stage, stage+"-displaced"); err == nil {
			t.Fatal("held stage could be displaced at commit")
		}
		if err := os.Mkdir(stage, 0700); !errors.Is(err, os.ErrExist) {
			t.Fatalf("replacing the occupied stage name: %v", err)
		}
		return renameReplacementNoReplace(file, parent, name)
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(destination, filepath.Base(member))); err != nil || string(raw) != "verified" {
		t.Fatalf("published content: %q, %v", raw, err)
	}
}

func TestWindowsStagePublicationRefusesCancellationAndStageReplacement(t *testing.T) {
	for _, scenario := range []string{"cancelled", "stage replaced"} {
		t.Run(scenario, func(t *testing.T) {
			stage, destination, _, original := stageRenameFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantCalls := 1
			if scenario == "stage replaced" {
				wantCalls = 0
				if err := os.Rename(stage, stage+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(stage, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stage, "foreign.txt"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			err := publishWindowsArchiveDirectory(ctx, stage, destination, original, func(*os.File, fileid.Handle, string) error {
				calls++
				cancel()
				return windows.ERROR_ACCESS_DENIED
			})
			cleanArchiveStage(stage, original)
			if err == nil || calls != wantCalls || (scenario == "cancelled" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("publication: calls %d, error %v", calls, err)
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination exists: %v", err)
			}
			_, statErr := os.Lstat(stage)
			if scenario == "cancelled" && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("owned failed stage remains: %v", statErr)
			}
			if scenario == "stage replaced" {
				if raw, err := os.ReadFile(filepath.Join(stage, "foreign.txt")); err != nil || string(raw) != "foreign" {
					t.Fatalf("foreign stage was changed: %q, %v", raw, err)
				}
			}
		})
	}
}

func TestWindowsStagePublicationRemainsCommittedWhenClosingFails(t *testing.T) {
	stage, destination, member, original := stageRenameFixture(t)
	err := publishWindowsArchiveDirectory(context.Background(), stage, destination, original, func(file *os.File, parent fileid.Handle, name string) error {
		if err := renameReplacementNoReplace(file, parent, name); err != nil {
			return err
		}
		// Closing here makes the production release fail after the move.
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("committed publication reported failure: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(destination, filepath.Base(member))); err != nil || string(raw) != "verified" {
		t.Fatalf("published content: %q, %v", raw, err)
	}
}
