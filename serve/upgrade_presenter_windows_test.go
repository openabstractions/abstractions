package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Only the child test process uses this private fixture mutex. It does not
// claim the installed presenter's endpoint, read a profile, or start OA.
func TestUpgradePresenterOwnedChild(t *testing.T) {
	root := os.Getenv("OA_UPGRADE_PRESENTER_FIXTURE")
	if root == "" {
		return
	}
	marker, mode := os.Getenv("OA_UPGRADE_PRESENTER_MARKER"), os.Getenv("OA_UPGRADE_PRESENTER_MODE")
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Local\OAUpgradePresenterFixture.%x`, sha256.Sum256([]byte(root))))
	if err != nil {
		t.Fatal(err)
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(mutex)
		if err := os.WriteFile(filepath.Join(root, marker+".duplicate"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(mutex)
	ready := func() {
		if err := os.WriteFile(filepath.Join(root, marker+".ready"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "probe" {
		ready()
		return
	}
	if mode == "cooperative" {
		// Reuse the host's hidden top-level window: the production stop
		// owner's WM_CLOSE must reach this owned process and finish it.
		err := withSessionShutdown(context.Background(), func(ctx context.Context) error {
			ready()
			<-ctx.Done()
			return os.WriteFile(filepath.Join(root, marker+".closed"), nil, 0600)
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	ready()
	for {
		time.Sleep(time.Second) // No close window/IPC: exercise retained-handle fallback.
	}
}

type ownedUpgradePresenter struct {
	command *exec.Cmd
	done    chan struct{}
	err     error // published by done
}

func startOwnedUpgradePresenter(t *testing.T, image, root, mode, marker string) *ownedUpgradePresenter {
	t.Helper()
	p := &ownedUpgradePresenter{command: exec.Command(image, "-test.run=^TestUpgradePresenterOwnedChild$", "-test.timeout=40s"), done: make(chan struct{})}
	p.command.Env = append(os.Environ(), "OA_UPGRADE_PRESENTER_FIXTURE="+root, "OA_UPGRADE_PRESENTER_MODE="+mode, "OA_UPGRADE_PRESENTER_MARKER="+marker)
	p.command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := p.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = p.command.Wait(); close(p.done) }()
	t.Cleanup(func() {
		// Only the process this fixture created; Process.Kill retains its
		// original Windows process handle, never a basename search.
		_ = p.command.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			t.Error("owned presenter fixture did not join")
		}
	})
	return p
}

func awaitUpgradePresenterMarker(t *testing.T, root, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned presenter marker timeout: " + marker)
}

type observedUpgradePresenter struct {
	*nativeUpgradeProcess
	fallbacks *int
}

func (p *observedUpgradePresenter) terminate() error {
	*p.fallbacks++
	return p.nativeUpgradeProcess.terminate()
}

// Enumerate only PIDs created by this test. This exercises the production file
// admission, native close/wait/terminate handles and stop/rescan owner without
// querying or opening the person's installed processes via the global census.
func ownedUpgradePresenterCensus(t *testing.T, owned []*ownedUpgradePresenter, folders []string, fallbacks *int) ([]upgradeStopProcess, error) {
	t.Helper()
	var admitted []upgradeStopProcess
	for _, child := range owned {
		select {
		case <-child.done:
			continue
		default:
		}
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(child.command.Process.Pid))
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			return nil, err
		}
		process := &nativeUpgradeProcess{handle: handle, pid: uint32(child.command.Process.Pid)}
		if exited, err := process.wait(0); err != nil || exited {
			process.close()
			if err != nil {
				return nil, err
			}
			continue
		}
		buffer := make([]uint16, windows.MAX_LONG_PATH)
		size := uint32(len(buffer))
		if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
			process.close()
			return nil, err
		}
		match, err := installedUpgradeImage(windows.UTF16ToString(buffer[:size]), folders)
		if err != nil || !match {
			process.close()
			if err != nil {
				return nil, err
			}
			continue
		}
		admitted = append(admitted, &observedUpgradePresenter{process, fallbacks})
	}
	return admitted, nil
}

func TestUpgradeStopOwnedPresenterReleasesMutexBeforeFreshActivation(t *testing.T) {
	for _, mode := range []string{"cooperative", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			root, staged := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "tools"), 0700); err != nil {
				t.Fatal(err)
			}
			image := filepath.Join(root, "tools", "Abstraction Panel.exe")
			foreignImage := filepath.Join(staged, "Abstraction Panel.exe")
			copyTestImage(t, image)
			copyTestImage(t, foreignImage)
			first := startOwnedUpgradePresenter(t, image, root, mode, "first")
			foreign := startOwnedUpgradePresenter(t, foreignImage, staged, "fallback", "foreign")
			awaitUpgradePresenterMarker(t, root, "first.ready")
			awaitUpgradePresenterMarker(t, staged, "foreign.ready")
			duplicate := startOwnedUpgradePresenter(t, image, root, "probe", "blocked")
			awaitUpgradePresenterMarker(t, root, "blocked.duplicate")
			<-duplicate.done
			if _, err := os.Stat(filepath.Join(root, "blocked.ready")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("duplicate presenter performed a fresh startup")
			}
			x := newTestExclusion(t)
			holder := holderFor(77)
			x.alive[holder.PID] = true
			owned := []*ownedUpgradePresenter{first, foreign}
			calls, fallbacks := 0, 0
			var late *ownedUpgradePresenter
			err := holdAndStopUpgrade("user", []string{root}, holder, x.env, func(ctx context.Context, folders []string) error {
				return stopUpgradeProcesses(ctx, folders, func(folders []string) ([]upgradeStopProcess, error) {
					calls++
					if calls == 2 {
						// A late owned activation must also leave before the
						// exclusion owner can report quiescence.
						late = startOwnedUpgradePresenter(t, image, root, "cooperative", "late")
						awaitUpgradePresenterMarker(t, root, "late.ready")
						owned = append(owned, late)
					}
					return ownedUpgradePresenterCensus(t, owned, folders, &fallbacks)
				})
			})
			if err != nil || calls != 3 {
				t.Fatalf("exclusion did not require quiescence: calls=%d error=%v", calls, err)
			}
			for _, child := range []*ownedUpgradePresenter{first, late} {
				select {
				case <-child.done:
				case <-time.After(3 * time.Second):
					t.Fatal("old presenter survived successful begin-upgrade")
				}
			}
			if want := map[string]int{"cooperative": 0, "fallback": 1}[mode]; fallbacks != want {
				t.Fatalf("fallbacks=%d want=%d", fallbacks, want)
			}
			select {
			case <-foreign.done:
				t.Fatal("same-name staged image was stopped")
			default:
			}
			if x.env.refusal(image) == nil {
				t.Fatal("exclusion released before installer commit")
			}
			x.env.release("user")
			fresh := startOwnedUpgradePresenter(t, image, root, "probe", "fresh")
			awaitUpgradePresenterMarker(t, root, "fresh.ready")
			<-fresh.done
			if fresh.err != nil {
				t.Fatal(fresh.err)
			}
		})
	}
}
