package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type undoPackageRegistry struct {
	*nativeComfyService
	beforeReplace func()
	afterReplace  func() error
}

func (r undoPackageRegistry) replaceAndVerifyRevision(ctx context.Context, program string) (fwire.ApplicationChange, string, error) {
	if r.beforeReplace != nil {
		r.beforeReplace()
	}
	change, revision, err := r.nativeComfyService.replaceAndVerifyRevision(ctx, program)
	if err == nil && r.afterReplace != nil {
		err = r.afterReplace()
	}
	return change, revision, err
}

// The registry fixture advances the caller's deadline at the exact boundary
// between an applied Register and its expired readback.
type undoDeadlineContext struct {
	context.Context
	done chan struct{}
}

func (ctx undoDeadlineContext) Done() <-chan struct{} { return ctx.done }

func (ctx undoDeadlineContext) Err() error {
	select {
	case <-ctx.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func newUndoUpgradeFixture(t *testing.T) (*upgradeFixture, []byte) {
	t.Helper()
	f := newUpgradeFixture(t)
	current := make([]byte, 100000)
	digest := sha256.Sum256(current)
	pins := bytes.Replace(comfyExtensionFixturePins(), []byte(strings.Repeat("a", 64)), []byte(hex.EncodeToString(digest[:])), 1)
	journal, err := f.journal.WithAdopterAssets(pins)
	if err != nil {
		t.Fatal(err)
	}
	*f.journal = journal
	return f, current
}

func useUndoPackageState(t *testing.T) string {
	t.Helper()
	oldInstall := officialPicture.installSavedPackage
	officialPicture.installSavedPackage = func(ctx context.Context, packagePath, base string, beforeEffect func() error) error {
		if err := beforeEffect(); err != nil {
			return err
		}
		return officialPicture.installPackage(ctx, packagePath, base)
	}
	t.Cleanup(func() { officialPicture.installSavedPackage = oldInstall })
	oldClaim := officialPicture.claim
	officialPicture.claim = func() (func(), bool, error) { return func() {}, true, nil }
	t.Cleanup(func() { officialPicture.claim = oldClaim })
	if err := setup.UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.UseUserState)
	home, err := setup.CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home.Config, 0700); err != nil {
		t.Fatal(err)
	}

	return filepath.Join(home.Config, "comfyui-openabstractions.msi")
}

func TestManualUndoPromotesTheRestoredPackageAfterRegistrySuccess(t *testing.T) {
	f, current := newUndoUpgradeFixture(t)
	canonical := useUndoPackageState(t)
	f.panel.appActivity.now = func() time.Time { return time.UnixMilli(42) }
	f.panel.appActivity.add(applicationActivity{
		Program: officialPicture.extension.Connector(f.found.Base), Undo: []string{setup.ComfyApplicationName}, UndoRestores: true,
	})
	f.panel.appActivity.now = func() time.Time { return time.UnixMilli(43) }
	f.step(t)
	lines := f.panel.appActivity.lines()
	previous := officialPicture.readRecord().Previous
	if len(lines) != 2 || !lines[1].UndoRestores || previous == nil || previous.PackageSHA256 == "" ||
		previous.ActivityAt != lines[1].At {
		t.Fatalf("update did not bind Undo: activity %+v, previous %+v", lines, previous)
	}
	earlier, err := os.ReadFile(previous.Package)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, current, 0600); err != nil {
		t.Fatal(err)
	}

	claimed := false
	officialPicture.claim = func() (func(), bool, error) {
		if claimed {
			return nil, false, nil
		}
		claimed = true
		return func() { claimed = false }, true, nil
	}
	officialPicture.installPackage = func(_ context.Context, path, base string) error {
		connect := installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/setup/comfy/connect", `{"confirm":true}`)
		if connect.Code != http.StatusConflict || !strings.Contains(connect.Body.String(), "already in progress") {
			t.Fatalf("Connect bypassed the active Undo claim: HTTP %d %s", connect.Code, connect.Body.String())
		}
		actual, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, earlier) {
			t.Fatalf("Undo chose a different MSI: %q", actual)
		}
		f.restores++
		return f.writeConnector(base, comfyR33Connector)
	}
	f.panel.comfyApps = undoPackageRegistry{nativeComfyService: f.service, beforeReplace: func() {
		raw, err := os.ReadFile(canonical)
		if err != nil || !bytes.Equal(raw, current) {
			t.Fatalf("saved-current changed before registry confirmation: %q (%v)", raw, err)
		}
	}}

	handler := installHandlerFor(f.panel.handler("setup-test-key"))
	undo := func(line applicationActivity) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(setupKeepUndo{At: line.At, Apps: line.Undo})
		if err != nil {
			t.Fatal(err)
		}
		return handler("POST", "/activity/app-requests", string(body))
	}
	if got := undo(lines[0]); got.Code != http.StatusConflict || f.restores != 0 {
		t.Fatalf("superseded Activity line attempted a restore: HTTP %d %s", got.Code, got.Body.String())
	}
	if err := officialPicture.changeRecord(context.Background(), func(r *setup.PackageMaintenanceRecord) {
		r.Previous.ActivityAt, r.KeptPackageSHA256 = 0, "" // Legacy receipts.
	}); err != nil {
		t.Fatal(err)
	}
	if got := undo(lines[0]); got.Code != http.StatusConflict || f.restores != 0 {
		t.Fatalf("superseded legacy Activity line attempted a restore: HTTP %d %s", got.Code, got.Body.String())
	}
	if err := os.WriteFile(canonical, []byte("changed MSI"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := undo(lines[1]); got.Code != http.StatusConflict || f.restores != 0 || officialPicture.readRecord().Undo != nil {
		t.Fatalf("Undo accepted a changed saved-current package: HTTP %d %s", got.Code, got.Body.String())
	}
	if err := os.WriteFile(canonical, current, 0600); err != nil {
		t.Fatal(err)
	}
	got := undo(lines[1])
	if got.Code != http.StatusOK || f.restores != 1 || f.service.replaces != 2 ||
		f.service.entry.state != "enabled" || f.service.entry.pin != previous.SHA256 {
		t.Fatalf("Undo did not apply the earlier image: HTTP %d %s, restores %d, replaces %d, entry %+v",
			got.Code, got.Body.String(), f.restores, f.service.replaces, f.service.entry)
	}
	raw, err := os.ReadFile(canonical)
	sum := sha256.Sum256(raw)
	if err != nil || !bytes.Equal(raw, earlier) || hex.EncodeToString(sum[:]) != previous.PackageSHA256 {
		t.Fatalf("successful Undo left the wrong saved-current MSI: digest %x, want %s (%v)", sum, previous.PackageSHA256, err)
	}
}

func TestManualUndoRetainsBothPackagesWhenRegistryReplyIsLost(t *testing.T) {
	for _, lostReadback := range []bool{false, true} {
		name := "no applied revision"
		if lostReadback {
			name = "applied revision with no readback"
		}
		t.Run(name, func(t *testing.T) {
			f, current := newUndoUpgradeFixture(t)
			canonical := useUndoPackageState(t)
			if err := os.WriteFile(canonical, current, 0600); err != nil {
				t.Fatal(err)
			}
			f.step(t)
			previous := officialPicture.readRecord().Previous
			if previous == nil {
				t.Fatal("forward update did not record Undo")
			}
			earlier, err := os.ReadFile(previous.Package)
			if err != nil {
				t.Fatal(err)
			}
			line := f.panel.appActivity.lines()[0]
			valid := setupKeepUndo{At: line.At, Apps: line.Undo}
			request := func(undo setupKeepUndo) *httptest.ResponseRecorder {
				t.Helper()
				body, err := json.Marshal(undo)
				if err != nil {
					t.Fatal(err)
				}
				return installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/activity/app-requests", string(body))
			}
			f.service.loseReplaceReply, f.service.loseReplaceReadback = !lostReadback, lostReadback
			if code := request(valid).Code; code != http.StatusConflict {
				t.Fatalf("ambiguous Undo returned HTTP %d", code)
			}
			record := officialPicture.readRecord()
			if record.Undo == nil || !record.Undo.Replacement.InstallComplete ||
				(record.Undo.Replacement.After != "") != lostReadback || record.Previous == nil {
				t.Fatalf("pending Undo lost its evidence: %+v", record)
			}
			for path, want := range map[string][]byte{
				canonical: current, previous.Package: earlier, record.Undo.Replacement.Previous.Package: current,
			} {
				if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, want) {
					t.Fatalf("retained MSI %s = %q (%v), want %q", path, raw, err, want)
				}
			}

			f.service.loseReplaceReply, f.service.loseReplaceReadback = false, false
			if err := os.Remove(f.panel.appActivity.path); err != nil {
				t.Fatal(err)
			}
			for _, wrong := range []setupKeepUndo{
				{At: line.At, Apps: []string{"other-extension"}},
				{At: line.At + 1, Apps: line.Undo},
				{At: line.At, Apps: []string{line.Undo[0], "other-extension"}},
			} {
				reply := request(wrong)
				if reply.Code != http.StatusConflict || !strings.Contains(reply.Body.String(), "This Setup line can no longer be undone.") {
					t.Fatalf("unmatched aged request %v reached an Undo owner: HTTP %d %s", wrong, reply.Code, reply.Body)
				}
			}
			reply := request(valid)
			if lostReadback {
				if reply.Code != http.StatusOK {
					t.Fatalf("acknowledged aged Undo failed: HTTP %d %s", reply.Code, reply.Body)
				}
			} else if reply.Code != http.StatusConflict || !strings.Contains(reply.Body.String(), "could not restore") {
				t.Fatalf("ambiguous aged Undo: HTTP %d %s", reply.Code, reply.Body)
			}
			if f.restores != 1 || f.service.replaces != 2 {
				t.Fatalf("Undo recovery ran native effects: restores %d, replaces %d", f.restores, f.service.replaces)
			}
			if lostReadback {
				if raw, err := os.ReadFile(canonical); err != nil || !bytes.Equal(raw, earlier) {
					t.Fatalf("recovered saved-current = %q (%v), want %q", raw, err, earlier)
				}
				if r := officialPicture.readRecord(); r.Undo != nil || r.Previous != nil {
					t.Fatalf("Undo did not settle: %+v", r)
				}
			}
		})
	}
}

func TestManualUndoRecordsAppliedRevisionAfterReadbackDeadline(t *testing.T) {
	f, current := newUndoUpgradeFixture(t)
	canonical := useUndoPackageState(t)
	if err := os.WriteFile(canonical, current, 0600); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	record := officialPicture.readRecord()
	line := f.panel.appActivity.lines()[0]
	ctx := undoDeadlineContext{Context: context.Background(), done: make(chan struct{})}
	f.panel.comfyApps = undoPackageRegistry{nativeComfyService: f.service, afterReplace: func() error {
		close(ctx.done)
		return ctx.Err()
	}}
	if err := officialPicture.restorePrevious(f.panel, ctx, f.found, record, line.At); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected expired registry readback, got %v", err)
	}
	pending := officialPicture.readRecord().Undo
	if pending == nil || pending.Replacement.After != f.service.revision() {
		t.Fatalf("readback deadline lost the acknowledged revision: %+v", pending)
	}
	if raw, err := os.ReadFile(canonical); err != nil || !bytes.Equal(raw, current) {
		t.Fatalf("readback deadline changed saved-current bytes: %v", err)
	}
	f.panel.comfyApps = f.service
	f.step(t)
	after := officialPicture.readRecord()
	if f.restores != 1 || f.service.replaces != 2 || after.Undo != nil || after.Kept != record.Previous.SHA256 {
		t.Fatalf("recovery repeated a native effect: restores %d, replaces %d, record %+v", f.restores, f.service.replaces, after)
	}
}

func TestManualUndoInstallerFailuresPreserveCurrentIntegration(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		removed      bool
		lostRollback bool
	}{
		{name: "preflight failure"},
		{name: "install failure after removal", removed: true},
		{name: "lost rollback acknowledgement", removed: true, lostRollback: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, current := newUndoUpgradeFixture(t)
			canonical := useUndoPackageState(t)
			if err := os.WriteFile(canonical, current, 0600); err != nil {
				t.Fatal(err)
			}
			f.step(t)
			previous := officialPicture.readRecord().Previous
			line := f.panel.appActivity.lines()[0]
			body, err := json.Marshal(setupKeepUndo{At: line.At, Apps: line.Undo})
			if err != nil {
				t.Fatal(err)
			}
			request := func() int {
				return installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/activity/app-requests", string(body)).Code
			}
			currentPin := f.service.entry.pin
			connector := officialPicture.extension.Connector(f.found.Base)
			fakeInstall := officialPicture.installSavedPackage
			rollbackDeadline := undoDeadlineContext{Context: context.Background(), done: make(chan struct{})}
			effects := 0
			officialPicture.installSavedPackage = func(ctx context.Context, path, base string, beforeEffect func() error) error {
				if path == previous.Package {
					if scenario.removed {
						if err := beforeEffect(); err != nil {
							return err
						}
						effects++
						if err := os.Remove(connector); err != nil {
							return err
						}
					}
					return errors.New("the fixture installer refused")
				}
				if !scenario.removed {
					t.Fatal("preflight failure attempted a restore")
				}
				effects++
				if effects == 2 && !scenario.lostRollback {
					return errors.New("first rollback failed before dispatch")
				}
				if err := beforeEffect(); err != nil {
					return err
				}
				raw, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(raw, current) {
					t.Fatalf("rollback did not use the verified saved-current MSI: %v", err)
				}
				if scenario.lostRollback {
					return errors.New("rollback did not acknowledge dispatch")
				}
				if err := f.writeConnector(base, connectorBytes); err != nil {
					return err
				}
				close(rollbackDeadline.done)
				return nil
			}
			if code := request(); code != http.StatusConflict {
				t.Fatalf("failed Undo returned HTTP %d", code)
			}
			if scenario.removed {
				proof := officialPicture.readRecord().Undo
				if proof == nil || !proof.RestoreRequired || proof.Replacement.BeforeDispatch {
					t.Fatalf("post-removal failure lost its retained proof: %+v", proof)
				}
				if _, err := os.Stat(connector); !os.IsNotExist(err) {
					t.Fatalf("fixture did not remove the connector: %v", err)
				}
				officialPicture.discover = func() []setup.ExtensionHost {
					found := f.found
					_, err := os.Stat(connector)
					found.Connected = err == nil
					return []setup.ExtensionHost{found}
				}
				if scenario.lostRollback {
					if code := request(); code != http.StatusConflict || effects != 2 {
						t.Fatalf("ambiguous rollback repeated an effect: HTTP %d, attempts %d", code, effects)
					}
					proof = officialPicture.readRecord().Undo
					if proof == nil || !proof.RestoreDispatched || proof.RestoreComplete {
						t.Fatalf("ambiguous rollback lost its dispatch evidence: %+v", proof)
					}
					if raw, err := os.ReadFile(canonical); err != nil || !bytes.Equal(raw, current) {
						t.Fatalf("ambiguous rollback changed saved-current bytes: %v", err)
					}
					return
				}
				found := officialPicture.discover()[0]
				if err := officialPicture.restorePrevious(f.panel, rollbackDeadline, found, officialPicture.readRecord(), line.At); !errors.Is(err, context.DeadlineExceeded) || effects != 3 {
					t.Fatalf("expected timeout after successful rollback: effects %d, error %v", effects, err)
				}
				proof = officialPicture.readRecord().Undo
				if proof == nil || !proof.RestoreDispatched || !proof.RestoreComplete {
					t.Fatalf("rollback deadline lost its acknowledgement: %+v", proof)
				}
				if code := request(); code != http.StatusConflict || effects != 3 {
					t.Fatalf("rollback recovery repeated the acknowledged install: HTTP %d, effects %d", code, effects)
				}
			}
			if record := officialPicture.readRecord(); record.Undo != nil || record.Previous == nil {
				t.Fatalf("known no-effect/restored Undo did not become retryable: %+v", record)
			}
			if raw, err := os.ReadFile(connector); err != nil || string(raw) != connectorBytes || f.service.entry.pin != currentPin {
				t.Fatalf("current integration was not preserved: connector %q (%v), pin %s", raw, err, f.service.entry.pin)
			}
			if raw, err := os.ReadFile(canonical); err != nil || !bytes.Equal(raw, current) {
				t.Fatalf("saved-current MSI changed before a successful Undo: %v", err)
			}
			officialPicture.installSavedPackage = fakeInstall
			if code := request(); code != http.StatusOK {
				t.Fatalf("fresh retry did not complete: HTTP %d", code)
			}
		})
	}
}
