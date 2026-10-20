package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
)

const maintenanceFamily = "{5C2E44B1-6F0B-4D7E-9B1F-2E7A0C3D9A41}"
const maintenanceTitle = "OpenAbstractions Setup"
const maintenanceUnavailable = "Open the current OpenAbstractions Setup installer to add or remove AI apps. Its installed maintenance copy could not be verified."

// MaintenanceReview approves opening the existing interactive installer. It
// establishes no package-installation or application-registration outcome.
type MaintenanceReview struct {
	Available bool   `json:"available"`
	PlanID    string `json:"plan_id,omitempty"`
	Version   string `json:"version,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type maintenanceRegistration struct {
	BundleCode, Family, Version, Program, Title string
	Trusted, Installed                          bool
}

type maintenanceImage struct {
	BundleCode, FileKey string
	Code, PanelCode     identity.Code
	Proof, PanelProof   identity.Proof
	close               func() error
	launch              func() error
}

var maintenanceRegistrations = readMaintenanceRegistrations
var maintenanceOpenImage = openMaintenanceImage

func maintenanceVersion() (string, error) {
	pins, err := parseAdopterAssets(adopterAssetsJSON)
	return strings.TrimPrefix(pins.ReleaseTag, "v"), err
}

func reviewedMaintenance(ctx context.Context) (MaintenanceReview, maintenanceImage, error) {
	fail := func(err error) (MaintenanceReview, maintenanceImage, error) {
		return MaintenanceReview{Reason: maintenanceUnavailable}, maintenanceImage{}, err
	}
	home, err := CurrentHome()
	if err != nil {
		return fail(err)
	}
	if home.Isolated {
		return fail(errors.New("installed Setup maintenance belongs to the account's installed runtime"))
	}
	version, err := maintenanceVersion()
	if err != nil {
		return fail(err)
	}
	rows, err := maintenanceRegistrations(ctx)
	if err != nil {
		return fail(err)
	}
	var selected *maintenanceRegistration
	for _, row := range rows {
		if row.Family != maintenanceFamily {
			continue
		}
		if selected != nil || !row.Trusted || !row.Installed || row.Title != maintenanceTitle || row.Version != version {
			return fail(errors.New("installed Setup registration is ambiguous, untrusted or from another release"))
		}
		copy := row
		selected = &copy
	}
	if selected == nil {
		return fail(errors.New("no installed Setup in the selected bundle family"))
	}
	image, err := maintenanceOpenImage(ctx, *selected)
	if err != nil {
		return fail(err)
	}
	code, panel := image.Code, image.PanelCode
	if image.close == nil || image.launch == nil || image.FileKey == "" || image.BundleCode != selected.BundleCode ||
		image.Proof != identity.ProofBound || image.PanelProof != identity.ProofBound || !code.Trusted || !panel.Trusted ||
		code.Catalog || code.Subject == "" || code.Issuer == "" || code.Subject != panel.Subject || code.Issuer != panel.Issuer ||
		code.Description != maintenanceTitle || code.OriginalName != "OpenAbstractions-Setup-"+version+".exe" {
		if image.close != nil {
			_ = image.close()
		}
		return fail(errors.New("installed Setup image differs from its registration, release or trusted Panel publisher"))
	}
	// This is an approval digest over the observed registration and file identity,
	// never an artifact pin or a substitute for the native signature verification.
	raw, _ := json.Marshal(struct {
		Registration maintenanceRegistration
		FileKey      string
		Subject      string
		Issuer       string
	}{*selected, image.FileKey, code.Subject, code.Issuer})
	sum := sha256.Sum256(raw)
	return MaintenanceReview{Available: true, PlanID: hex.EncodeToString(sum[:]), Version: version}, image, nil
}

func ReviewMaintenance(ctx context.Context) MaintenanceReview {
	review, image, err := reviewedMaintenance(ctx)
	if err == nil {
		if err := image.close(); err != nil {
			return MaintenanceReview{Reason: maintenanceUnavailable}
		}
	}
	return review
}

func OpenMaintenance(ctx context.Context, planID string) (err error) {
	if planID == "" {
		return errors.New("review the installed Setup before opening it")
	}
	review, image, err := reviewedMaintenance(ctx)
	if err != nil {
		return errors.New(maintenanceUnavailable)
	}
	defer func() { err = errors.Join(err, image.close()) }()
	if planID != review.PlanID {
		return errors.New("installed Setup changed; review it again")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return image.launch()
}
