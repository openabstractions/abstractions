package cacheproducer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func installedCopyFixture(t *testing.T, predecessor bool) (string, ClaudeOptions, string, *cachedClaudePluginFixture) {
	t.Helper()
	o, self, vendor := cachedClaudePlugin(t)
	config := t.TempDir()
	o.State = filepath.Join(config, "claude-connections")
	o.Scope = "local"
	if err := privatedir.Make(o.State); err != nil {
		t.Fatal(err)
	}
	original := claudePluginModule
	t.Cleanup(func() { claudePluginModule = original })
	if predecessor {
		claudePluginModule = append(append([]byte(nil), original...), []byte("\n// predecessor\n")...)
	}
	if err := ApplyClaude(context.Background(), o, self, vendor.run); err != nil {
		t.Fatal(err)
	}
	claudePluginModule = original
	return config, o, self, vendor
}

func discoverFixture(t *testing.T, config string, vendor *cachedClaudePluginFixture) []Copy {
	t.Helper()
	copies, err := copiesInConfig(context.Background(), config, vendor.run,
		func(context.Context, string, string, []string) (CodexResult, error) {
			t.Fatal("unrelated producer invoked")
			return CodexResult{}, nil
		})
	if err != nil || len(copies) != 2 {
		t.Fatalf("discovery: %v, %v", copies, err)
	}
	return copies
}

func TestCopyReceiptDiscoveryUsesRealProducerRepairAndReadback(t *testing.T) {
	for _, kind := range []CopyKind{SourceCopy, VendorCopy} {
		t.Run(string(kind), func(t *testing.T) {
			config, _, _, vendor := installedCopyFixture(t, true)
			copies := discoverFixture(t, config, vendor)
			for _, copy := range copies {
				if copy.Kind != kind {
					continue
				}
				before, err := copy.Observe(context.Background())
				if err != nil || before.State != CopyStale || !copy.CanReconcile() {
					t.Fatalf("old owned copy: %+v, %v", before, err)
				}
				if err := copy.Reconcile(context.Background()); err != nil {
					t.Fatal(err)
				}
				after, err := copy.Observe(context.Background())
				if err != nil || after.State != CopyCurrent {
					t.Fatalf("producer readback: %+v, %v", after, err)
				}
			}
		})
	}
}

func TestCopyProducerAuthorizedRepairRechecksKeepAfterReadback(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "current-Keep", true: "revoked-during-readback"}[revoke], func(t *testing.T) {
			config, _, _, vendor := installedCopyFixture(t, true)
			copies := discoverFixture(t, config, vendor)
			before, _, err := ReadClaudeMarker(context.Background(), copies[0].Receipt)
			if err != nil {
				t.Fatal(err)
			}
			revoked := false
			run := func(ctx context.Context, cli, project string, args []string) ([]byte, error) {
				raw, err := vendor.run(ctx, cli, project, args)
				if revoke && len(args) == 1 && args[0] == "--version" {
					revoked = true
				}
				return raw, err
			}
			captured, err := copiesInConfig(context.Background(), config, run, nil)
			if err != nil || len(captured) != 2 {
				t.Fatal(captured, err)
			}
			checks := 0
			refusal := errors.New("current native Keep revoked")
			err = captured[0].ReconcileAuthorized(context.Background(), func(context.Context) error {
				checks++
				if revoked {
					return refusal
				}
				return nil
			})
			if revoke {
				if !errors.Is(err, refusal) || checks < 2 {
					t.Fatal("readback hid revoked Keep", checks, err)
				}
				after, _, err := ReadClaudeMarker(context.Background(), copies[0].Receipt)
				if err != nil || string(after) != string(before) {
					t.Fatal("revoked repair changed ownership", err)
				}
			} else {
				if err != nil || checks < 2 {
					t.Fatal(checks, err)
				}
				if read, err := captured[0].Observe(context.Background()); err != nil || read.State != CopyCurrent {
					t.Fatal(read, err)
				}
			}
		})
	}
}

func TestCopyProducerRestoresOnlyReceiptOwnedMissingCurrentMember(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := "owned-partial"
		if foreign {
			name = "foreign-member"
		}
		t.Run(name, func(t *testing.T) {
			config, _, _, vendor := installedCopyFixture(t, false)
			missing := filepath.Join(vendor.v.root, "plugin", "hooks", "register.js")
			if err := os.Remove(missing); err != nil {
				t.Fatal(err)
			}
			foreignPath := filepath.Join(vendor.v.root, "plugin", "README.md")
			if foreign {
				if err := os.WriteFile(foreignPath, []byte("person-owned changes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			copy := discoverFixture(t, config, vendor)[0]
			before, err := copy.Observe(context.Background())
			if err != nil || (!foreign && before.State != CopyAbsent) || (foreign && before.State != CopyForeign) {
				t.Fatalf("partial classification: %+v, %v", before, err)
			}
			err = copy.ReconcileAuthorized(context.Background(), func(context.Context) error { return nil })
			if foreign {
				if err == nil {
					t.Fatal("foreign source accepted")
				}
				if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("restoration began before all existing members were admitted")
				}
				if actual, _ := os.ReadFile(foreignPath); string(actual) != "person-owned changes" {
					t.Fatal("foreign source overwritten")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if after, err := copy.Observe(context.Background()); err != nil || after.State != CopyCurrent {
				t.Fatalf("restored source readback: %+v, %v", after, err)
			}
		})
	}
}

func TestCopyProducerPendingOwnershipAndUnreadableReceiptCannotRepair(t *testing.T) {
	config, _, _, vendor := installedCopyFixture(t, false)
	copies := discoverFixture(t, config, vendor)
	path := copies[0].Receipt
	raw, marker, err := ReadClaudeMarker(context.Background(), path)
	if err != nil || raw == nil {
		t.Fatal(err)
	}
	marker.PluginOwned = false
	if err := os.WriteFile(path, PluginJSON(marker), 0600); err != nil {
		t.Fatal(err)
	}
	for _, copy := range discoverFixture(t, config, vendor) {
		reading, err := copy.Observe(context.Background())
		if err != nil || reading.State != CopyHumanNeeded || copy.CanReconcile() || copy.Reconcile(context.Background()) == nil {
			t.Fatalf("pending ownership: %+v, %v", reading, err)
		}
	}
	// A previously captured producer also re-admits the current ownership.
	if err := copies[0].Reconcile(context.Background()); err == nil {
		t.Fatal("captured producer bypassed revoked ownership")
	}
	if err := os.WriteFile(path, []byte("{unreadable receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	unreadable, err := copiesInConfig(context.Background(), config, vendor.run, nil)
	if err != nil || len(unreadable) != 1 || unreadable[0].CanReconcile() {
		t.Fatalf("unreadable receipt: %+v, %v", unreadable, err)
	}
	if reading, err := unreadable[0].Observe(context.Background()); err != nil || reading.State != CopyUnreadable {
		t.Fatalf("unreadable result: %+v, %v", reading, err)
	}
}

func TestCopyProducerCapturedRepairRefusesChangedSelectedCLI(t *testing.T) {
	config, options, _, vendor := installedCopyFixture(t, true)
	copies := discoverFixture(t, config, vendor)
	_, marker, err := ReadClaudeMarker(context.Background(), copies[0].Receipt)
	if err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(t.TempDir(), "different-selected-cli.exe")
	if err := os.WriteFile(selected, []byte("inert CLI fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	_, stored, err := setupIdentityClosed(context.Background(), selected, false)
	if err != nil {
		t.Fatal(err)
	}
	marker.CLI = stored
	if err := os.WriteFile(copies[0].Receipt, PluginJSON(marker), 0600); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(vendor.v.root, "plugin", "hooks", "register.js")
	before, err := os.ReadFile(module)
	if err != nil {
		t.Fatal(err)
	}
	if err := copies[0].Reconcile(context.Background()); err == nil {
		t.Fatal("captured producer used a superseded CLI selection", options.CLI)
	}
	if after, err := os.ReadFile(module); err != nil || string(after) != string(before) {
		t.Fatal("source changed after CLI selection changed", err)
	}
}

func TestCopyReceiptDiscoveryReportsUnrecordedSourceWithoutAdoptingIt(t *testing.T) {
	config, _, _, vendor := installedCopyFixture(t, false)
	copies := discoverFixture(t, config, vendor)
	if err := os.Remove(copies[0].Receipt); err != nil {
		t.Fatal(err)
	}
	rows, err := copiesInConfig(context.Background(), config, vendor.run, nil)
	if err != nil || len(rows) != 1 || rows[0].CanReconcile() || rows[0].Location != vendor.v.root {
		t.Fatalf("unrecorded source: %+v, %v", rows, err)
	}
	if reading, err := rows[0].Observe(context.Background()); err != nil || reading.State != CopyForeign {
		t.Fatalf("unrecorded source classification: %+v, %v", reading, err)
	}
	if err := rows[0].Reconcile(context.Background()); err == nil {
		t.Fatal("unrecorded source was adopted")
	}
}
