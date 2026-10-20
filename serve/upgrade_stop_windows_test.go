package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type inertUpgradeProcess struct {
	cooperative, alive, unkillable bool
	closeError, waitError          error
	events                         []string
}

func (p *inertUpgradeProcess) requestClose() error {
	p.events = append(p.events, "request")
	if p.cooperative {
		p.alive = false
	}
	return p.closeError
}
func (p *inertUpgradeProcess) wait(time.Duration) (bool, error) {
	p.events = append(p.events, "wait")
	return !p.alive, p.waitError
}
func (p *inertUpgradeProcess) terminate() error {
	p.events = append(p.events, "fallback")
	if !p.unkillable {
		p.alive = false
	}
	return nil
}
func (p *inertUpgradeProcess) close() error { p.events = append(p.events, "release"); return nil }

func TestUpgradeStopCooperativeFallbackAndQuiescentRescan(t *testing.T) {
	for _, cooperative := range []bool{true, false} {
		p := &inertUpgradeProcess{cooperative: cooperative, alive: true}
		calls := 0
		err := stopUpgradeProcesses(context.Background(), nil, func([]string) ([]upgradeStopProcess, error) {
			calls++
			if calls == 1 {
				return []upgradeStopProcess{p}, nil
			}
			return nil, nil
		})
		if err != nil || calls != 2 {
			t.Fatal(err, calls)
		}
		want := []string{"request", "wait", "release"}
		if !cooperative {
			want = []string{"request", "wait", "fallback", "wait", "release"}
		}
		if !reflect.DeepEqual(p.events, want) {
			t.Fatal(p.events, want)
		}
	}
}

func TestUpgradeStopClosesLateInstanceBeforeEmptyCensus(t *testing.T) {
	first := &inertUpgradeProcess{alive: true, cooperative: true}
	late := &inertUpgradeProcess{alive: true, cooperative: true}
	calls := 0
	err := stopUpgradeProcesses(context.Background(), nil, func([]string) ([]upgradeStopProcess, error) {
		calls++
		if calls == 1 {
			return []upgradeStopProcess{first}, nil
		}
		if calls == 2 {
			return []upgradeStopProcess{late}, nil
		}
		return nil, nil
	})
	if err != nil || calls != 3 || first.alive || late.alive {
		t.Fatal(err, calls, first.alive, late.alive)
	}
}

func TestEarlyUpgradeStopEvidenceDistinguishesAdmissionAndHeldRescans(t *testing.T) {
	x := newTestExclusion(t)
	holder := holderFor(77)
	x.alive[holder.PID] = true
	folders := []string{testFolder}
	report := earlyUpgradeStop{folders: len(folders)}
	ops, events := inertUpgradeCensusOps()
	core := upgradeCandidate{PID: 42, Name: "OpenAbstractions.exe"}
	ops.list = func() ([]upgradeCandidate, error) { return []upgradeCandidate{core}, nil }
	admitted := &inertUpgradeProcess{alive: true, cooperative: true}
	if err := holdAndStopUpgradeAdmission("machine", folders, holder, x.env,
		func(ctx context.Context, folders []string) error {
			return readOnlyUpgradeCensus(ctx, folders, ops, report.admission.observe)
		}, func(ctx context.Context, folders []string) error {
			return stopUpgradeProcesses(ctx, folders, func([]string) ([]upgradeStopProcess, error) {
				report.scans++
				if report.scans == 1 {
					report.held.observe(upgradeCensusObservation{upgradeCandidate: core, Stage: "installed-stop-admitted"})
					return []upgradeStopProcess{admitted}, nil
				}
				report.held.observe(upgradeCensusObservation{upgradeCandidate: core, Stage: "unrelated-file"})
				return nil, nil
			})
		}); err != nil {
		t.Fatal(err)
	}
	report.guard, report.holderPID = earlyUpgradeGuard(x.env, "machine", folders)
	if admitted.alive || report.guard != "active" || report.holderPID != holder.PID || report.scans != 2 ||
		report.admission != (earlyUpgradeCensus{named: 1, admitted: 1}) ||
		report.held != (earlyUpgradeCensus{named: 2, admitted: 1, unrelated: 1}) {
		t.Fatalf("unexpected early-stop evidence %s", report.summary())
	}
	if !strings.Contains(strings.Join(*events, ","), "close:1,close:2") {
		t.Fatalf("read-only admission retained handles: %v", *events)
	}
	x.alive[holder.PID] = false
	if guard, pid := earlyUpgradeGuard(x.env, "machine", folders); guard != "inactive" || pid != holder.PID {
		t.Fatalf("dead holder reported %s pid %d", guard, pid)
	}
}

func TestUpgradeStopRetainsExclusionAndRefusesReactivation(t *testing.T) {
	x := newTestExclusion(t)
	holder := holderFor(77)
	x.alive[holder.PID] = true
	var stopped bool
	err := holdAndStopUpgrade("user", []string{testFolder}, holder, x.env, func(ctx context.Context, folders []string) error {
		stopped = true
		if x.env.refusal(filepath.Join(testFolder, "tools", "openabstractionsw.exe")) == nil {
			t.Fatal("activation admitted during stop")
		}
		if !reflect.DeepEqual(folders, []string{testFolder}) {
			t.Fatal(folders)
		}
		return errors.New("inert survivor")
	})
	if !stopped || err == nil {
		t.Fatal(stopped, err)
	}
	if x.env.refusal(filepath.Join(testFolder, "tools", "openabstractionsw.exe")) == nil {
		t.Fatal("failed stop released exclusion")
	}
	stopCalls := 0
	x.env.write = func(string, string, []byte) error { return errors.New("inert exclusion failure") }
	if err := holdAndStopUpgrade("user", []string{testFolder}, holder, x.env, func(context.Context, []string) error { stopCalls++; return nil }); err == nil || stopCalls != 0 {
		t.Fatal(err, stopCalls)
	}
}

// A begin-upgrade whose stop fails after it took the exclusion leaves it
// held; the rollback start the installer scheduled before begin
// (abstraction.wxs, test_shutdown_gate.py
// test_each_rollback_start_is_scheduled_before_its_begin) releases it, so
// activation works again before msiexec ends.
func TestUpgradeStopFailureIsReleasedByTheRollbackStart(t *testing.T) {
	x := newTestExclusion(t)
	holder := holderFor(78)
	x.alive[holder.PID] = true
	image := filepath.Join(testFolder, "tools", "openabstractionsw.exe")
	err := holdAndStopUpgrade("user", []string{testFolder}, holder, x.env, func(context.Context, []string) error {
		return errors.New("installed OA process did not exit before file replacement")
	})
	if err == nil || x.env.refusal(image) == nil {
		t.Fatalf("a failed stop must fail begin and keep the exclusion: %v", err)
	}
	// What `service start --related` / `--machine` does first.
	x.env.release("user")
	if err := x.env.refusal(image); err != nil {
		t.Fatalf("the rollback start left activation refused: %v", err)
	}
}

func TestUpgradeStopCollectsFailuresAndRefusesUnknownOrRespawningProcesses(t *testing.T) {
	first := &inertUpgradeProcess{alive: true, unkillable: true}
	second := &inertUpgradeProcess{alive: true, cooperative: true}
	if err := stopUpgradeProcesses(context.Background(), nil, func([]string) ([]upgradeStopProcess, error) { return []upgradeStopProcess{first, second}, nil }); err == nil {
		t.Fatal("survivor accepted")
	}
	if first.events[len(first.events)-1] != "release" || second.events[len(second.events)-1] != "release" {
		t.Fatal(first.events, second.events)
	}
	p := &inertUpgradeProcess{alive: true, waitError: errors.New("inert unknown wait")}
	if err := stopUpgradeProcesses(context.Background(), nil, func([]string) ([]upgradeStopProcess, error) { return []upgradeStopProcess{p}, nil }); err == nil {
		t.Fatal("unknown wait accepted")
	}
	if reflect.DeepEqual(p.events, []string{"request", "wait", "fallback", "wait", "release"}) {
		t.Fatal("unknown wait fell back")
	}
	count := 0
	if err := stopUpgradeProcesses(context.Background(), nil, func([]string) ([]upgradeStopProcess, error) {
		count++
		return []upgradeStopProcess{&inertUpgradeProcess{alive: true, cooperative: true}}, nil
	}); err == nil || count != 3 {
		t.Fatal(err, count)
	}
}

func TestUpgradeStopAdmitsInstalledFileIdentityAndRetainsForeignImages(t *testing.T) {
	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(tools, "openabstractionsw.exe")
	if err := os.WriteFile(image, []byte("inert image"), 0600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "openabstractionsw.exe")
	if err := os.WriteFile(foreign, []byte("inert image"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		image   string
		want    bool
		missing bool
	}{{image, true, false}, {foreign, false, false}, {filepath.Join(t.TempDir(), "Codex.exe"), false, true}, {filepath.Join(t.TempDir(), "openabstractions-mcp.exe"), false, true}} {
		got, err := installedUpgradeImage(row.image, []string{root})
		if (row.missing && !errors.Is(err, os.ErrNotExist)) || (!row.missing && err != nil) || got != row.want {
			t.Fatal(row.image, got, err)
		}
	}
}
