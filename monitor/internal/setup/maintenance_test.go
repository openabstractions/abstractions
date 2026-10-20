package setup

import (
	"context"
	"errors"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

func TestMaintenanceReviewAndHandoffRequireCurrentOwnedVerifiedImage(t *testing.T) {
	for _, fault := range []string{"valid", "absent", "untrusted-registration", "not-installed", "wrong-family", "wrong-version", "wrong-title", "ambiguous", "missing-cache", "tampered", "wrong-signer", "wrong-issuer", "wrong-guid", "other-oa-program", "old-image", "no-file-identity", "stale-review", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			UseUserState()
			beforeRead, beforeOpen := maintenanceRegistrations, maintenanceOpenImage
			t.Cleanup(func() { maintenanceRegistrations, maintenanceOpenImage = beforeRead, beforeOpen })
			version, err := maintenanceVersion()
			if err != nil {
				t.Fatal(err)
			}
			row := maintenanceRegistration{BundleCode: "fixture-bundle", Family: maintenanceFamily, Version: version, Title: maintenanceTitle, Program: "fixture-cache.exe", Trusted: true, Installed: true}
			code := identity.Code{Subject: "fixture publisher", Issuer: "fixture authority", Trusted: true, Description: maintenanceTitle, OriginalName: "OpenAbstractions-Setup-" + version + ".exe"}
			closed, launched, opens := 0, 0, 0
			maintenanceRegistrations = func(context.Context) ([]maintenanceRegistration, error) {
				current := row
				switch fault {
				case "absent":
					return nil, nil
				case "untrusted-registration":
					current.Trusted = false
				case "not-installed":
					current.Installed = false
				case "wrong-family":
					current.Family = "different-family"
				case "wrong-version":
					current.Version = "0.4.0"
				case "wrong-title":
					current.Title = "another installer"
				case "ambiguous":
					return []maintenanceRegistration{row, row}, nil
				}
				return []maintenanceRegistration{current}, nil
			}
			maintenanceOpenImage = func(_ context.Context, got maintenanceRegistration) (maintenanceImage, error) {
				opens++
				if got != row {
					t.Fatal("unselected registration reached image admission")
				}
				if fault == "missing-cache" {
					return maintenanceImage{}, errors.New("fixture cache absent")
				}
				image := maintenanceImage{BundleCode: row.BundleCode, FileKey: "held-object", Code: code, PanelCode: code, Proof: identity.ProofBound, PanelProof: identity.ProofBound,
					close: func() error { closed++; return nil }, launch: func() error {
						if closed != opens-1 {
							t.Fatal("image hold released before launch")
						}
						launched++
						return nil
					}}
				switch fault {
				case "tampered":
					image.Proof = identity.ProofUnmet
				case "wrong-signer":
					image.Code.Subject = "another publisher"
				case "wrong-issuer":
					image.Code.Issuer = "another authority"
				case "wrong-guid":
					image.BundleCode = "different-bundle"
				case "other-oa-program":
					image.Code.Description = "OpenAbstractions runtime"
				case "old-image":
					image.Code.OriginalName = "OpenAbstractions-Setup-0.4.0.exe"
				case "no-file-identity":
					image.FileKey = ""
				}
				return image, nil
			}
			review := ReviewMaintenance(context.Background())
			eligible := fault == "valid" || fault == "stale-review" || fault == "cancelled"
			if review.Available != eligible || launched != 0 {
				t.Fatalf("review=%+v launches=%d", review, launched)
			}
			if !eligible {
				if review.PlanID != "" || review.Reason == "" || OpenMaintenance(context.Background(), "unreviewed") == nil || launched != 0 {
					t.Fatal("unavailable maintenance accepted")
				}
				return
			}
			planID := review.PlanID
			if fault == "stale-review" {
				row.Program = "changed-cache.exe"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if fault == "cancelled" {
				cancel()
			}
			err = OpenMaintenance(ctx, planID)
			if (err == nil) != (fault == "valid") || launched != map[bool]int{true: 1}[fault == "valid"] || closed != opens {
				t.Fatalf("handoff error=%v launches=%d closed=%d opened=%d", err, launched, closed, opens)
			}
		})
	}
}

func TestMaintenanceRefusesPrivateRuntimeBeforeRegistry(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	before := maintenanceRegistrations
	maintenanceRegistrations = func(context.Context) ([]maintenanceRegistration, error) {
		t.Fatal("account registry read from private runtime")
		return nil, nil
	}
	t.Cleanup(func() { maintenanceRegistrations = before })
	if review := ReviewMaintenance(context.Background()); review.Available || review.PlanID != "" {
		t.Fatal(review)
	}
}
