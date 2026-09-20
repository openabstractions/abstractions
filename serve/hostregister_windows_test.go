package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// This fake SCM grants exactly the requested handle rights. No services are
// created or configured on the test host.
func TestRegistrationHandleCanConfigureRestartRecovery(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprintf("repair=%t", repair), func(t *testing.T) {
			var granted uint32
			opens := 0
			h, existed, err := registrationHandle(func(access uint32) (windows.Handle, error) {
				if repair {
					return 0, windows.ERROR_SERVICE_EXISTS
				}
				granted = access
				return 41, nil
			}, func(access uint32) (windows.Handle, error) {
				opens++
				granted = access
				return 41, nil
			})
			if err != nil || h != 41 || existed != repair || (opens == 1) != repair {
				t.Fatalf("handle=%v existed=%v opens=%d err=%v", h, existed, opens, err)
			}
			if granted&windows.SERVICE_CHANGE_CONFIG == 0 || granted&windows.SERVICE_START == 0 {
				t.Fatalf("restart recovery needs change-config and start rights, granted %#x", granted)
			}
		})
	}
}

func TestRegistrationHandleDoesNotHideSCMFailures(t *testing.T) {
	for _, repair := range []bool{false, true} {
		opens := 0
		h, existed, err := registrationHandle(func(uint32) (windows.Handle, error) {
			if repair {
				return 0, windows.ERROR_SERVICE_EXISTS
			}
			return 0, windows.ERROR_ACCESS_DENIED
		}, func(uint32) (windows.Handle, error) {
			opens++
			return 0, windows.ERROR_ACCESS_DENIED
		})
		if h != 0 || existed != repair || !errors.Is(err, windows.ERROR_ACCESS_DENIED) || (opens == 1) != repair {
			t.Fatalf("repair=%v handle=%v existed=%v opens=%d err=%v", repair, h, existed, opens, err)
		}
	}
}

func testTemplateName() string {
	return fmt.Sprintf("OpenAbstractionsHostTest%d%d", os.Getpid(), time.Now().UnixNano())
}

// Registering a template under an unknown name reaches the real SCM with this
// token. An unelevated token is refused before anything is created, and the
// refusal is what installer-actions.txt records.
func TestHostRegistrationWithoutAdministratorIsRefusedAndRecorded(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("elevated: this would create a service")
	}
	dir := t.TempDir()
	twin := filepath.Join(dir, windowlessName)
	os.WriteFile(twin, nil, 0o600)
	var out bytes.Buffer
	registrar := systemHostRegistrar(&out)
	registrar.name = testTemplateName()
	registrar.image = func() (string, error) { return twin, nil }
	err := registrar.register()
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !strings.Contains(err.Error(), "administrator token") {
		t.Fatalf("unelevated registration: %v", err)
	}
	actions := filepath.Join(dir, "machine", "upgrade-v1", "installer-actions.txt")
	at := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	recordInstallerFailure(func(string) (string, error) { return actions, nil }, "user", "host register", err, at)
	data, readErr := os.ReadFile(actions)
	if readErr != nil || !strings.HasPrefix(string(data), "2026-09-16T12:00:00Z host register: registering the per-user service template needs an administrator token: Access is denied.") {
		t.Fatalf("recorded %q err=%v", data, readErr)
	}
}

func TestHostRegistrationRefusesAMissingTwinBeforeTheSCM(t *testing.T) {
	var out bytes.Buffer
	registrar := systemHostRegistrar(&out)
	registrar.name = testTemplateName()
	registrar.manager = func(uint32) (windows.Handle, error) { t.Fatal("SCM opened without a windowless image"); return 0, nil }
	if err := registrar.register(); err == nil || !strings.Contains(err.Error(), windowlessName) {
		t.Fatalf("missing twin: %v", err)
	}
}

// Unregistering a name nothing registered enumerates the real SCM and removes
// nothing.
func TestHostUnregisterOfAnUnknownNameRemovesNothing(t *testing.T) {
	var out bytes.Buffer
	registrar := systemHostRegistrar(&out)
	registrar.name = testTemplateName()
	registrar.budget = 20 * time.Second
	if err := registrar.unregister(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no "+registrar.name+" registration was present") {
		t.Fatalf("output %q", out.String())
	}
}

func TestHostCommandArguments(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if err := hostCommand([]string{"--help"}, &out, &diagnostics); err != nil || !strings.Contains(out.String(), "host register|unregister") {
		t.Fatalf("help: %v %q", err, out.String())
	}
	for _, args := range [][]string{nil, {"install"}, {"register", "--runtime"}} {
		var exit *exitError
		if err := hostCommand(args, &out, &diagnostics); !errors.As(err, &exit) || exit.code != 2 {
			t.Fatalf("%v accepted: %v", args, err)
		}
	}
}

type shutdownProcess struct {
	alive  bool
	closed bool
}

func (p *shutdownProcess) exited() (bool, error) { return !p.alive, nil }
func (p *shutdownProcess) close() error          { p.closed = true; return nil }

func TestCheckedServiceRemoval(t *testing.T) {
	for _, mode := range []string{"denied", "pending", "timeout", "alive-after-stopped", "changed-pid", "shared", "already-stopped", "stop-only", "unobserved-pending"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
			defer cancel()
			process := &shutdownProcess{alive: mode == "alive-after-stopped"}
			queries, stops, deletes, pins := 0, 0, 0, 0
			ops := removalOps{
				query: func() (windows.SERVICE_STATUS_PROCESS, error) {
					queries++
					state := uint32(windows.SERVICE_RUNNING)
					pid := uint32(17)
					kind := uint32(windows.SERVICE_WIN32_OWN_PROCESS)
					if mode == "shared" {
						kind = windows.SERVICE_WIN32_SHARE_PROCESS
					}
					if mode == "changed-pid" && queries > 1 {
						pid = 18
					}
					if mode == "already-stopped" {
						state = windows.SERVICE_STOPPED
						pid = 0
					}
					if mode == "unobserved-pending" {
						state = windows.SERVICE_STOP_PENDING
						if queries > 1 {
							state = windows.SERVICE_STOPPED
							pid = 0
						}
					}
					if stops > 0 && mode != "timeout" {
						state = windows.SERVICE_STOPPED
						pid = 0
						if mode == "pending" && queries == 4 {
							state = windows.SERVICE_STOP_PENDING
							pid = 17
						}
					}
					return windows.SERVICE_STATUS_PROCESS{CurrentState: state, ProcessId: pid, ServiceType: kind}, nil
				},
				stop: func() error {
					stops++
					if mode == "denied" {
						return windows.ERROR_ACCESS_DENIED
					}
					return nil
				},
				pin: func(pid uint32) (removalProcess, error) {
					pins++
					if pid != 17 {
						t.Fatal(pid)
					}
					return process, nil
				},
				remove: func() error { deletes++; return nil },
			}
			if mode == "stop-only" {
				ops.remove = nil
			}
			removed, err := stopAndDeleteService(ctx, ops)
			success := mode == "pending" || mode == "already-stopped" || mode == "stop-only"
			if success != (err == nil) || removed != success {
				t.Fatalf("removed=%v err=%v queries=%d", removed, err, queries)
			}
			if (!success || mode == "stop-only") && deletes != 0 {
				t.Fatal("deleted before confirmed shutdown")
			}
			if success && mode != "stop-only" && deletes != 1 {
				t.Fatal("no deletion")
			}
			if pins > 0 && !process.closed {
				t.Fatal("retained handle leaked")
			}
			if mode == "already-stopped" && (stops != 0 || pins != 0) {
				t.Fatal("stopped service controlled")
			}
		})
	}
}

func TestServiceRemovalSharedBudgetAndEnumeration(t *testing.T) {
	denied := errors.New("enumeration denied")
	if _, err := removeServices(context.Background(), hostServiceName, func() ([]string, error) { return nil, denied }, func(context.Context, string) (bool, error) {
		t.Fatal("deleted after enumeration failure")
		return false, nil
	}); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	_, err := removeServices(ctx, hostServiceName, func() ([]string, error) { return []string{"first", "second"}, nil }, func(call context.Context, name string) (bool, error) {
		calls++
		if call != ctx {
			t.Fatal("new per-service budget")
		}
		if name != "first" {
			t.Fatal("continued after deadline", name)
		}
		<-call.Done()
		return false, call.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal(calls, err)
	}
	var order []string
	gone, err := removeServices(context.Background(), hostServiceName, func() ([]string, error) { return []string{hostServiceName + "_1a"}, nil }, func(_ context.Context, name string) (bool, error) {
		order = append(order, name)
		return true, nil
	})
	if err != nil || gone != 2 || strings.Join(order, ",") != hostServiceName+"_1a,"+hostServiceName {
		t.Fatalf("instances before template: %v gone=%d err=%v", order, gone, err)
	}
}

func TestServiceRemovalRefusesNewClone(t *testing.T) {
	lists := 0
	_, err := removeServices(context.Background(), hostServiceName, func() ([]string, error) {
		lists++
		if lists == 1 {
			return []string{"existing"}, nil
		}
		return []string{"existing", "new-login"}, nil
	}, func(context.Context, string) (bool, error) { return true, nil })
	if err == nil || lists != 2 {
		t.Fatal("new clone was not detected", lists, err)
	}
}
