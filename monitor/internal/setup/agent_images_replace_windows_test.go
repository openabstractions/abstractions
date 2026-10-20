package setup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

func replacementPin(t *testing.T, path string) agentReplacementPin {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := fileid.Open(context.Background(), fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	id := h.FileID()
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	return agentReplacementPin{ID: id, Hash: sha256.Sum256(b), Bytes: int64(len(b))}
}

func TestReplaceAgentImageTransaction(t *testing.T) {
	for _, mode := range []string{"publish", "cancel-before", "cancel-gap", "foreign", "parent", "custody", "bad-pin"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "parent")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			target, stage := filepath.Join(root, "helper.exe"), filepath.Join(root, "stage.exe")
			if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stage, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			oldPin, stagePin := replacementPin(t, target), replacementPin(t, stage)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel-before" {
				cancel()
			}
			if mode == "bad-pin" {
				oldPin.Hash[0] ^= 1
			}
			result, err := replaceAgentImage(ctx, target, oldPin, stage, stagePin, func(at string) {
				if at == "admitted" {
					if mode == "parent" {
						if err := os.Rename(root, root+"-moved"); err == nil {
							t.Fatal("held parent moved")
						}
					}
					if mode == "custody" {
						for _, path := range []string{target, stage} {
							if err := os.WriteFile(path, []byte("foreign"), 0600); err == nil {
								t.Fatal("held content changed")
							}
							if err := os.Rename(path, path+"-moved"); err == nil {
								t.Fatal("held source moved")
							}
						}
					}
				}
				if at == "backup" {
					if mode == "cancel-gap" {
						cancel()
					}
					if mode == "foreign" {
						if err := os.WriteFile(target, []byte("foreign"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			})
			failure := mode == "cancel-before" || mode == "cancel-gap" || mode == "foreign" || mode == "bad-pin"
			if (err != nil) != failure {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Published == failure {
				t.Fatalf("published=%v", result.Published)
			}
			if mode == "cancel-before" || mode == "bad-pin" {
				if result.Backup != "" {
					t.Fatal(result)
				}
				b, _ := os.ReadFile(target)
				if string(b) != "old" {
					t.Fatal(string(b))
				}
				return
			}
			if got := replacementPin(t, result.Backup); !got.ID.Equal(oldPin.ID) || got.Hash != oldPin.Hash {
				t.Fatal("backup lost identity/content")
			}
			if mode == "cancel-gap" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			} else {
				b, err := os.ReadFile(target)
				if err != nil {
					t.Fatal(err)
				}
				want := "new"
				if mode == "foreign" {
					want = "foreign"
				}
				if string(b) != want {
					t.Fatal(string(b))
				}
			}
			if failure {
				if got := replacementPin(t, stage); !got.ID.Equal(stagePin.ID) {
					t.Fatal("stage lost")
				}
			} else if got := replacementPin(t, target); !got.ID.Equal(stagePin.ID) {
				t.Fatal("publication lost stage identity")
			}
		})
	}
}

func TestReplaceAgentImageAdmitsRunningExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := fileid.Open(context.Background(), fileid.Self, filepath.Dir(exe), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	f, err := openReplacementFile(context.Background(), parent, filepath.Base(exe), replacementPin(t, exe))
	if err != nil {
		t.Fatal("running test executable not admitted for replacement:", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceAgentImageBusyChild(t *testing.T) {
	if os.Getenv("OA_TEST_AGENT_IMAGE_CHILD") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, "ready")
	_, _ = os.Stdin.Read(make([]byte, 1))
}

// startRunningAgentImage runs path as a copy of this test binary that waits
// on stdin, the way an agent app keeps its connector helper running. The
// returned stop ends it.
func startRunningAgentImage(t *testing.T, path string) func() error {
	t.Helper()
	cmd := exec.Command(path, "-test.run=^TestReplaceAgentImageBusyChild$")
	cmd.Env = append(os.Environ(), "OA_TEST_AGENT_IMAGE_CHILD=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		return errors.Join(input.Close(), cmd.Wait())
	}
	t.Cleanup(func() { _ = stop() })
	ready := make([]byte, 5)
	if _, err := io.ReadFull(output, ready); err != nil || string(ready) != "ready" {
		t.Fatal(string(ready), err)
	}
	return stop
}

func testExecutableBytes(t *testing.T) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An agent app that keeps its connector running must not hold the update
// back: the running image moves to the backup name and the staged image is
// published at once.
func TestReplaceAgentImagePublishesOverRunningHelper(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	target, stage := filepath.Join(root, "helper.exe"), filepath.Join(root, "stage.exe")
	if err := os.WriteFile(target, testExecutableBytes(t), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	oldPin, stagePin := replacementPin(t, target), replacementPin(t, stage)
	stop := startRunningAgentImage(t, target)
	result, err := replaceOwnedAgentImage(ctx, target, oldPin, stage, stagePin)
	if err != nil || !result.Published || result.Backup == "" {
		t.Fatal(result, err)
	}
	if got := replacementPin(t, target); !got.ID.Equal(stagePin.ID) || got.Hash != stagePin.Hash {
		t.Fatal("staged image not published over the running helper")
	}
	if got := replacementPin(t, result.Backup); !got.ID.Equal(oldPin.ID) || got.Hash != oldPin.Hash {
		t.Fatal("running helper image lost")
	}
	if err := stop(); err != nil {
		t.Fatal("running helper did not keep working on its moved image:", err)
	}
}

func TestReplaceAgentImageBusyBoundaryAndOwnedCloseRetry(t *testing.T) {
	for _, mode := range []string{"current-held", "stage-held"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			target, stage := filepath.Join(root, "helper.exe"), filepath.Join(root, "stage.exe")
			oldBytes := []byte("old")
			if err := os.WriteFile(target, oldBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stage, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			oldPin, stagePin := replacementPin(t, target), replacementPin(t, stage)
			var release func() error
			want := "current image admission"
			path := target
			if mode == "stage-held" {
				path, want = stage, "staged image admission"
			}
			held, _, err := openAgentImage(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			release = held.Close
			t.Cleanup(func() {
				if release != nil {
					_ = release()
				}
			})
			result, err := replaceOwnedAgentImage(ctx, target, oldPin, stage, stagePin)
			var at *agentReplacementError
			if !errors.As(err, &at) || at.Operation != want || !errors.Is(err, windows.STATUS_SHARING_VIOLATION) || result.Backup != "" || result.Published {
				t.Fatalf("result=%+v error=%v boundary=%+v", result, err, at)
			}
			if got := replacementPin(t, target); !got.ID.Equal(oldPin.ID) || got.Hash != oldPin.Hash {
				t.Fatal("old image changed on refusal")
			}
			if got := replacementPin(t, stage); !got.ID.Equal(stagePin.ID) || got.Hash != stagePin.Hash {
				t.Fatal("staged image changed on refusal")
			}
			if err := release(); err != nil {
				t.Fatal(err)
			}
			release = nil
			result, err = replaceOwnedAgentImage(ctx, target, oldPin, stage, stagePin)
			if err != nil || !result.Published || result.Backup == "" {
				t.Fatal(result, err)
			}
			if got := replacementPin(t, result.Backup); !got.ID.Equal(oldPin.ID) || got.Hash != oldPin.Hash {
				t.Fatal("old object lost after owned close")
			}
			if got := replacementPin(t, target); !got.ID.Equal(stagePin.ID) || got.Hash != stagePin.Hash {
				t.Fatal("fresh object lost after owned close")
			}
		})
	}
}

func TestReplaceAgentImageReparseOccupantAtPublication(t *testing.T) {
	root := t.TempDir()
	target, stage, foreign := filepath.Join(root, "helper.exe"), filepath.Join(root, "stage.exe"), filepath.Join(root, "foreign.exe")
	for path, data := range map[string]string{target: "old", stage: "new", foreign: "foreign"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Establish symlink support before entering the transaction.
	probe := filepath.Join(root, "link-probe")
	if err := os.Symlink(foreign, probe); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	oldPin, stagePin := replacementPin(t, target), replacementPin(t, stage)
	result, err := replaceAgentImage(context.Background(), target, oldPin, stage, stagePin, func(at string) {
		if at == "backup" {
			if err := os.Symlink(foreign, target); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err == nil || result.Published || result.Backup == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := replacementPin(t, result.Backup); !got.ID.Equal(oldPin.ID) {
		t.Fatal("backup changed")
	}
	if got := replacementPin(t, stage); !got.ID.Equal(stagePin.ID) {
		t.Fatal("stage changed")
	}
	if b, err := os.ReadFile(foreign); err != nil || string(b) != "foreign" {
		t.Fatal(string(b), err)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal(info, err)
	}
}
