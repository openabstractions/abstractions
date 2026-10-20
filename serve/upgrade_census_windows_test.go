package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestUpgradeCensusMetadataWaitRetainsIdentityAndBudget(t *testing.T) {
	for _, outcome := range []string{"image", "exit", "deadline", "cancel", "wait-error", "other-error", "partial-copy"} {
		t.Run(outcome, func(t *testing.T) {
			ops, events := inertUpgradeCensusOps()
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			reads := 0
			ops.image = func(h windows.Handle) (string, error) {
				if h != 1 {
					t.Fatal("metadata changed query handle", h)
				}
				reads++
				if outcome == "image" && reads == 2 {
					return `C:\owned\tools\openabstractionsw.exe`, nil
				}
				if outcome == "cancel" {
					cancel()
				}
				if outcome == "other-error" {
					return "", windows.ERROR_INVALID_PARAMETER
				}
				if outcome == "partial-copy" {
					return "", windows.ERROR_PARTIAL_COPY
				}
				return "", windows.ERROR_ACCESS_DENIED
			}
			ops.exited = func(h windows.Handle) (bool, error) {
				if h != 1 {
					t.Fatal("wait changed query handle", h)
				}
				if outcome == "wait-error" {
					return false, windows.ERROR_INVALID_HANDLE
				}
				return outcome == "exit" && reads >= 2, nil
			}
			p, row, err := admitUpgradeCandidateContext(ctx, upgradeCandidate{PID: 42, Name: "openabstractionsw.exe"}, nil, ops)
			switch outcome {
			case "image":
				if err != nil || p == nil || reads != 2 {
					t.Fatal(p, row, err, reads)
				}
				p.close()
			case "exit":
				if err != nil || p != nil || reads != 2 || row.Stage != "exited-during-query-image" {
					t.Fatal(p, row, err, reads)
				}
			default:
				if err == nil || p != nil || row.Stage != "query-image" {
					t.Fatal(p, row, err)
				}
				if outcome != "other-error" && outcome != "partial-copy" && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
					t.Fatal(err)
				}
				if outcome == "deadline" && (!errors.Is(err, context.DeadlineExceeded) || reads < 2) {
					t.Fatal(err, reads)
				}
				if outcome == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if (outcome == "other-error" || outcome == "partial-copy" || outcome == "wait-error") && reads != 1 {
					t.Fatal(reads)
				}
			}
			joined := strings.Join(*events, ",")
			if !strings.Contains(joined, "close:1") {
				t.Fatal("query leaked", joined)
			}
			if outcome != "image" && (strings.Contains(joined, "classify") || strings.Contains(joined, "open:101001")) {
				t.Fatal("unclassified candidate acquired stop rights", joined)
			}
		})
	}
}

func inertUpgradeCensusOps() (upgradeCensusOps, *[]string) {
	events := &[]string{}
	ops := upgradeCensusOps{
		list: func() ([]upgradeCandidate, error) {
			return []upgradeCandidate{{PID: 42, Name: "openabstractionsw.exe"}}, nil
		},
		open: func(pid, access uint32) (windows.Handle, error) {
			*events = append(*events, fmt.Sprintf("open:%x", access))
			if access&windows.PROCESS_TERMINATE != 0 {
				return 2, nil
			}
			return 1, nil
		},
		image: func(windows.Handle) (string, error) {
			*events = append(*events, "image")
			return `C:\owned\tools\openabstractionsw.exe`, nil
		},
		created: func(windows.Handle) (int64, error) { return 77, nil },
		exited:  func(windows.Handle) (bool, error) { return false, nil },
		sameObject: func(windows.Handle, windows.Handle) (bool, error) {
			*events = append(*events, "same-object")
			return true, nil
		},
		close:    func(h windows.Handle) error { *events = append(*events, fmt.Sprintf("close:%d", h)); return nil },
		classify: func(string, []string) (bool, error) { *events = append(*events, "classify"); return true, nil },
	}
	return ops, events
}

func TestUpgradeCensusClassifiesBeforeStopRightsAndReadonlyCloses(t *testing.T) {
	for _, installed := range []bool{false, true} {
		ops, events := inertUpgradeCensusOps()
		ops.classify = func(string, []string) (bool, error) { *events = append(*events, "classify"); return installed, nil }
		var output bytes.Buffer
		if err := inspectUpgradeCensus([]string{`C:\owned`}, ops, &output); err != nil {
			t.Fatal(err)
		}
		want := []string{"open:101000", "image", "classify", "close:1"}
		if installed {
			want = []string{"open:101000", "image", "classify", "open:101001", "same-object", "close:1", "close:2"}
		}
		if !reflect.DeepEqual(*events, want) {
			t.Fatal(installed, *events, want)
		}
		if !strings.Contains(output.String(), map[bool]string{true: "installed-stop-admitted", false: "unrelated-file"}[installed]) {
			t.Fatal(output.String())
		}
	}
}

func TestUpgradeCensusNamedRefusalsPreserveErrorIdentityAndCloseEveryHandle(t *testing.T) {
	for _, stage := range []string{"open-query", "query-birth", "query-image", "classify-file", "open-stop-rights", "stop-birth", "stop-object", "wait-before-stop-rights", "close-query"} {
		t.Run(stage, func(t *testing.T) {
			ops, events := inertUpgradeCensusOps()
			denied := windows.ERROR_ACCESS_DENIED
			switch stage {
			case "open-query":
				ops.open = func(uint32, uint32) (windows.Handle, error) { return 0, denied }
			case "query-birth":
				ops.created = func(windows.Handle) (int64, error) { return 0, denied }
			case "query-image":
				ops.image = func(windows.Handle) (string, error) { return "", denied }
			case "classify-file":
				ops.classify = func(string, []string) (bool, error) { return false, denied }
			case "open-stop-rights":
				base := ops.open
				ops.open = func(pid, access uint32) (windows.Handle, error) {
					if access&windows.PROCESS_TERMINATE != 0 {
						return 0, denied
					}
					return base(pid, access)
				}
			case "stop-birth":
				ops.created = func(h windows.Handle) (int64, error) {
					if h == 2 {
						return 0, denied
					}
					return 77, nil
				}
			case "stop-object":
				ops.sameObject = func(windows.Handle, windows.Handle) (bool, error) { return false, denied }
			case "wait-before-stop-rights":
				ops.exited = func(windows.Handle) (bool, error) { return false, denied }
			case "close-query":
				base := ops.close
				ops.close = func(h windows.Handle) error {
					base(h)
					if h == 1 {
						return denied
					}
					return nil
				}
			}
			var output bytes.Buffer
			err := inspectUpgradeCensus(nil, ops, &output)
			if !errors.Is(err, denied) || !strings.Contains(err.Error(), "stage="+stage) || !strings.Contains(err.Error(), "pid=42") || !strings.Contains(err.Error(), "snapshot=\"openabstractionsw.exe\"") {
				t.Fatal(err)
			}
			if stage != "open-query" && !strings.Contains(strings.Join(*events, ","), "close:1") {
				t.Fatal(*events)
			}
			if stage == "close-query" && !strings.Contains(strings.Join(*events, ","), "close:2") {
				t.Fatal("admitted handle leaked after query close failure", *events)
			}
		})
	}
}

func TestUpgradeCensusExitNeedsRetainedSignalOrFreshPIDAbsence(t *testing.T) {
	for _, gone := range []bool{false, true} {
		ops, _ := inertUpgradeCensusOps()
		calls := 0
		ops.open = func(uint32, uint32) (windows.Handle, error) { return 0, windows.ERROR_INVALID_PARAMETER }
		ops.list = func() ([]upgradeCandidate, error) {
			calls++
			if calls > 1 && gone {
				return nil, nil
			}
			return []upgradeCandidate{{PID: 42, Name: "openabstractionsw.exe"}}, nil
		}
		_, err := censusUpgradeProcesses(nil, ops, nil)
		if (err == nil) != gone || calls != 2 {
			t.Fatal(gone, err, calls)
		}
	}
	for _, exited := range []bool{false, true} {
		ops, _ := inertUpgradeCensusOps()
		ops.image = func(windows.Handle) (string, error) { return "", windows.ERROR_ACCESS_DENIED }
		ops.exited = func(windows.Handle) (bool, error) { return exited, nil }
		_, row, err := admitUpgradeCandidate(upgradeCandidate{PID: 42, Name: "openabstractionsw.exe"}, nil, ops)
		if (err == nil) != exited {
			t.Fatal(exited, err)
		}
		if exited && row.Stage != "exited-during-query-image" {
			t.Fatal(row)
		}
	}
}

func TestUpgradeCensusRejectsChangedBirthOrObjectAndCollectsAllRefusals(t *testing.T) {
	for _, birth := range []bool{false, true} {
		ops, _ := inertUpgradeCensusOps()
		if birth {
			ops.created = func(h windows.Handle) (int64, error) { return int64(h), nil }
		} else {
			ops.sameObject = func(windows.Handle, windows.Handle) (bool, error) { return false, nil }
		}
		if processes, err := censusUpgradeProcesses(nil, ops, nil); err == nil || len(processes) != 0 {
			t.Fatal(birth, processes, err)
		}
	}
	ops, _ := inertUpgradeCensusOps()
	ops.list = func() ([]upgradeCandidate, error) {
		return []upgradeCandidate{{PID: 42, Name: "openabstractionsw.exe"}, {PID: 43, Name: "jobd.exe"}}, nil
	}
	ops.open = func(uint32, uint32) (windows.Handle, error) { return 0, windows.ERROR_ACCESS_DENIED }
	_, err := censusUpgradeProcesses(nil, ops, nil)
	if err == nil || !strings.Contains(err.Error(), "pid=42") || !strings.Contains(err.Error(), "pid=43") {
		t.Fatal(err)
	}
}

func TestUpgradeExclusionErrorsNameStorageAndHolderStages(t *testing.T) {
	x := newTestExclusion(t)
	x.env.write = func(string, string, []byte) error { return windows.ERROR_ACCESS_DENIED }
	err := holdAndStopUpgrade("machine", []string{testFolder}, holderFor(77), x.env, func(context.Context, []string) error { t.Fatal("stop dispatched after failed write"); return nil })
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !strings.Contains(err.Error(), "stage=hold") || !strings.Contains(err.Error(), "holder_pid=77") {
		t.Fatal(err)
	}
}

type upgradeInspectionBrokenWriter struct{ err error }

func (w upgradeInspectionBrokenWriter) Write([]byte) (int, error) { return 0, w.err }

func TestUpgradeInspectionArgumentsAndOutputFailureRetainNoHandles(t *testing.T) {
	for _, scope := range []string{"user", "machine"} {
		request, err := serviceArguments([]string{"inspect-upgrade", "--" + scope, `C:\owned`})
		if err != nil || request.command != "inspect-upgrade" || request.scope != scope {
			t.Fatal(request, err)
		}
	}
	for _, args := range [][]string{{"inspect-upgrade", "--system", `C:\owned`}, {"inspect-upgrade", "--machine", ""}, {"inspect-upgrade", "--machine", `C:\owned`, "extra"}} {
		if _, err := serviceArguments(args); err == nil {
			t.Fatal(args)
		}
	}
	ops, events := inertUpgradeCensusOps()
	lost := errors.New("inert output unavailable")
	err := inspectUpgradeCensus(nil, ops, upgradeInspectionBrokenWriter{lost})
	if !errors.Is(err, lost) || !strings.Contains(strings.Join(*events, ","), "close:2") {
		t.Fatal(err, *events)
	}
}
