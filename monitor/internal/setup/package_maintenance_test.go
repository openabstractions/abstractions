package setup

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageRepairComparesBareSHA256(t *testing.T) {
	prior := fmt.Sprintf("%x", sha256.Sum256([]byte("earlier connector")))
	next := fmt.Sprintf("%x", sha256.Sum256([]byte("installed connector")))
	for _, check := range []struct {
		name, recorded, observed, pin string
		want                          bool
	}{
		{"uppercase receipt", strings.ToUpper(prior), prior, next, true},
		{"current image", prior, prior, strings.ToUpper(prior), false},
		{"malformed", strings.Repeat("z", 64), strings.Repeat("Z", 64), next, false},
		{"labelled receipt", "sha256:" + prior, prior, next, false},
	} {
		t.Run(check.name, func(t *testing.T) {
			r := PackageMaintenanceRecord{Kept: check.recorded, KeptPackageSHA256: next}
			validated := false
			pending, resume, err := r.PlanExplicitUpdate(t.Context(), ExplicitPackageUpdate{
				Application: "comfyui", Program: "connector.exe", Pin: check.pin,
				Available: func() bool { return true },
				Image:     func() (string, int64, error) { return check.observed, 17, nil },
				EntryKept: func(context.Context) (bool, bool) { return true, true },
				CheckMissingCompanion: func(_ context.Context, digest string, size int64) error {
					if digest != check.recorded || size != 17 {
						t.Fatal("repair did not pass the recorded digest and observed size")
					}
					validated = true
					return nil
				},
				Stamp: func() (string, int64) { return "0.8.0", 42 },
			})
			if (pending != nil) != check.want || resume || (err == nil) != check.want || validated != check.want {
				t.Fatalf("planning result %v, resume %t, err %v, checked companion %t", pending, resume, err, validated)
			}
			if check.want {
				pending.Replacement = &PackageReplacement{Previous: PreviousIntegration{SHA256: check.observed}}
				if err := r.AdmitExplicitUpdate(r, pending, true); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPackageMaintenanceStoreReadsAndSettlesAnEarlierConnection(t *testing.T) {
	ctx := context.Background()
	store := PackageMaintenanceStore{Path: filepath.Join(t.TempDir(), "integration.json")}
	const previous = `{"kept":"old","kept_package_sha256":"prior-msi","history":["old"],"pending":{"application":"example-app","program":"connector.exe","sha256":"new","at":1},"connection":{"catalog_id":"example-app","package_sha256":"new-msi","connector_sha256":"new","installed":true}}`
	if err := os.WriteFile(store.Path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	initial, err := store.Load()
	if err != nil || initial.Kept != "old" || initial.KeptPackageSHA256 != "prior-msi" ||
		initial.PendingUpdate == nil || initial.PendingUpdate.Application != "example-app" ||
		initial.Connection == nil || !initial.Connection.InstallerAcknowledged {
		t.Fatalf("read earlier receipt %+v: %v", initial, err)
	}
	published := 0
	publish := func(context.Context) error { published++; return nil }
	wrong := *initial.Connection
	wrong.PackageSHA256 = "another-msi"
	if err := store.Edit(ctx, func(r *PackageMaintenanceRecord) error {
		return r.CommitConnection(ctx, wrong, publish)
	}); err == nil || published != 0 {
		t.Fatalf("a competing receipt published: %d, error %v", published, err)
	}
	if err := store.Edit(ctx, func(r *PackageMaintenanceRecord) error {
		return r.CommitConnection(ctx, *initial.Connection, publish)
	}); err != nil {
		t.Fatal(err)
	}
	final, err := store.Load()
	if err != nil || published != 1 || final.Connection != nil || final.PendingUpdate != nil || final.Kept != "new" ||
		final.KeptPackageSHA256 != "new-msi" || !final.IsDowngrade("old") {
		t.Fatalf("settled receipt %+v, publications %d: %v", final, published, err)
	}
}

func TestPackageMaintenanceStorePreservesChangedPendingReplacement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := PackageMaintenanceStore{Path: filepath.Join(root, "integration.json")}
	save := func(name, text string) (string, string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		return path, fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	}
	program, connectorDigest := save("connector.exe", "earlier connector")
	pkg, packageDigest := save("previous.msi", "earlier package")
	replacement := PackageReplacement{
		Previous:  PreviousIntegration{Program: program, SHA256: connectorDigest, Bytes: int64(len("earlier connector")), Package: pkg, PackageSHA256: packageDigest},
		Candidate: "candidate", Before: "original-revision", BeforeDispatch: true,
	}
	pending := PackageUpdateExpectation{Application: "example-app", Program: program, SHA256: "old-release", Replacement: &replacement}
	if err := store.Edit(ctx, func(r *PackageMaintenanceRecord) error {
		r.PendingUpdate = &pending
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	observed := false
	err := store.ForgetUntouchedReplacement(ctx, &pending, program, func(ctx context.Context) (string, error) {
		observed = true
		// Another writer changes the receipt after the two files were held.
		err := store.Edit(ctx, func(r *PackageMaintenanceRecord) error {
			r.PendingUpdate.Replacement.BeforeDispatch = false
			return nil
		})
		return replacement.Before, err
	})
	if !observed || err == nil {
		t.Fatalf("changed replacement accepted: observed %v, err %v", observed, err)
	}
	got, err := store.Load()
	if err != nil || got.PendingUpdate == nil || got.PendingUpdate.Replacement == nil || got.PendingUpdate.Replacement.BeforeDispatch ||
		got.PendingUpdate.Replacement.Previous != replacement.Previous {
		t.Fatalf("the live receipt or predecessor changed: %+v, err %v", got, err)
	}
}
