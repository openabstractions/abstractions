package main

import (
	"context"
	"errors"
	"os"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWindowsActivationUsesOnlyOwnedInstalledPaths(t *testing.T) {
	absent := exitForStartTest(t, 1060)
	running := exitForStartTest(t, 1056)
	denied := exitForStartTest(t, 5)
	root := t.TempDir()
	twin := filepath.Join(root, "openabstractionsw.exe")
	if err := os.WriteFile(twin, nil, 0600); err != nil {
		t.Fatal(err)
	}
	sc := `C:\Windows\System32\sc.exe`
	for _, mode := range []string{"service", "running", "user", "denied", "missing", "upgrading", "virtualized", "noshell"} {
		t.Run(mode, func(t *testing.T) {
			var commands [][]string
			run := func(ctx context.Context, path string, args ...string) error {
				commands = append(commands, append([]string{path}, args...))
				switch len(commands) {
				case 1:
					if mode == "user" || mode == "virtualized" || mode == "noshell" {
						return absent
					}
					if mode == "denied" {
						return denied
					}
				case 2:
					if mode == "running" {
						return running
					}
				}
				return nil
			}
			var launched, shelled [][]string
			stat := os.Stat
			if mode == "missing" {
				stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			}
			refusal := func() error { return nil }
			if mode == "upgrading" {
				refusal = func() error { return upgradeRefusal(errUpgradeInProgress) }
			}
			err := activateWindows(context.Background(), windowsActivation{
				instance: "OpenAbstractionsSupervisor_123", sc: sc, run: run,
				launch: func(image string, args ...string) error {
					launched = append(launched, append([]string{image}, args...))
					return nil
				},
				executable: func() (string, error) { return filepath.Join(root, "openabstractions.exe"), nil },
				stat:       stat, refusal: refusal,
				profile: func() (bootstrap.ProfileView, error) {
					return bootstrap.ProfileView{Virtualized: mode == "virtualized" || mode == "noshell", Family: "Claude_pzs8sxrjxfjjc"}, nil
				},
				shellLaunch: func(image, dir string, args ...string) error {
					if mode == "noshell" {
						return errNoShell
					}
					shelled = append(shelled, append([]string{image, dir}, args...))
					return nil
				},
			})
			switch mode {
			case "missing":
				if err == nil || !strings.Contains(err.Error(), "openabstractionsw.exe") || len(commands)+len(launched) != 0 {
					t.Fatal(err, commands, launched)
				}
				return
			case "upgrading":
				// The exclusion refuses before the service manager or the host is asked.
				var exit *exitError
				if !errors.As(err, &exit) || exit.code != 3 || len(commands)+len(launched) != 0 {
					t.Fatal(err, commands, launched)
				}
				return
			case "denied":
				if err == nil || len(commands) != 1 || len(launched) != 0 {
					t.Fatal(err, commands, launched)
				}
				return
			case "virtualized":
				// A contained caller's host starts through the desktop shell, never directly.
				if err != nil || len(commands) != 1 || len(launched) != 0 || !reflect.DeepEqual(shelled, [][]string{{twin, root, "serve", "host"}}) {
					t.Fatal(err, commands, launched, shelled)
				}
				return
			case "noshell":
				var exit *exitError
				if !errors.As(err, &exit) || exit.code != exitVirtualizedProfile || !strings.Contains(err.Error(), "virtualized_caller_no_shell") || len(launched)+len(shelled) != 0 {
					t.Fatal(err, commands, launched, shelled)
				}
				return
			case "user":
				if err != nil || len(commands) != 1 || !reflect.DeepEqual(launched, [][]string{{twin, "serve", "host"}}) {
					t.Fatal(err, commands, launched)
				}
				return
			}
			want := [][]string{{sc, "query", "OpenAbstractionsSupervisor_123"}, {sc, "start", "OpenAbstractionsSupervisor_123"}}
			if err != nil || !reflect.DeepEqual(commands, want) || len(launched) != 0 {
				t.Fatal(err, commands, launched)
			}
		})
	}
}

// launchDetached returns while the launched program keeps running.
func TestLaunchDetachedDoesNotWait(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("OA_START_HELPER", "sleep")
	began := time.Now()
	if err := launchDetached(os.Args[0], "-test.run=^TestDetachedHelperProcess$", marker); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(began); elapsed > 5*time.Second {
		t.Fatalf("launch waited %s", elapsed)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && len(data) > 0 {
			pid := strings.TrimSpace(string(data))
			exec.Command("taskkill", "/F", "/PID", pid).Run()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("detached program never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDetachedHelperProcess(t *testing.T) {
	if os.Getenv("OA_START_HELPER") != "sleep" || len(os.Args) < 3 {
		return
	}
	marker := os.Args[len(os.Args)-1]
	if !strings.HasSuffix(marker, "started") {
		return
	}
	os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600)
	time.Sleep(time.Minute)
}
