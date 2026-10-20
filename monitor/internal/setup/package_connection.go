package setup

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// PackageConnectionReceipt binds a connection to the exact release package
// and target. BeforeDispatch permits recovery before the MSI effect.
type PackageConnectionReceipt struct {
	CatalogID             string `json:"catalog_id"`
	Registration          string `json:"registration"`
	Program               string `json:"program"`
	ReleaseURL            string `json:"release_url"`
	PackageSHA256         string `json:"package_sha256"`
	ConnectorSHA256       string `json:"connector_sha256"`
	PackageBytes          int64  `json:"package_bytes"`
	ConnectorBytes        int64  `json:"connector_bytes"`
	LegacyOrder           string `json:"legacy_order,omitempty"`
	BeforeDispatch        bool   `json:"before_dispatch,omitempty"`
	InstallerAcknowledged bool   `json:"installed,omitempty"`
}

const (
	connectionProofTimeout = 10 * time.Minute
	publishFirst           = "publish_first"
	installFirst           = "install_first"
)

// PackageConnection supplies the native Registry and receipt operations for
// Setup's initial or unkept connection. Confirm also retains the caller's
// existing native Keep flow.
type PackageConnection struct {
	checkFreshProduct func(context.Context) error
	ExistingReceipt   *PackageConnectionReceipt
	Admit             func(ctx context.Context, registration, program string, installing bool) error
	Expect            func(ctx context.Context, registration, program, digest string)
	Confirm           func(ctx context.Context, registration, program string) error
	ConfirmLegacy     func(ctx context.Context, registration, program string) error
	Save              func(ctx context.Context, before, after *PackageConnectionReceipt) error
	Publish           func(ctx context.Context, prepared PackageConnectionReceipt, publish func(context.Context) error) error
	Commit            func(ctx context.Context, installed PackageConnectionReceipt, publish func(context.Context) error) error
}

func runPackageConnection(ctx context.Context, spec product.PackageSpec, specTarget packageTarget, pin PinnedDownload, image sevenZipPin,
	current string, alreadyInstalled bool, install func(context.Context, string, func() error) error, ops PackageConnection, legacyOrder string) (err error) {
	if err := specTarget.match(spec); err != nil {
		return err
	}
	if image.Path != specTarget.connector {
		return errors.New("the verified connector does not name the registration target")
	}
	legacy := legacyOrder != ""
	if legacy && legacyOrder != publishFirst && legacyOrder != installFirst {
		return errors.New("this integration package has an unknown operation order")
	}
	if legacy && ops.ConfirmLegacy == nil {
		return errors.New("this integration package needs its native registration proof")
	}
	if legacyOrder == publishFirst && ops.Publish == nil {
		return errors.New("this integration package needs its publication receipt")
	}
	dispatch := PackageConnectionReceipt{
		CatalogID: specTarget.catalogID, Registration: specTarget.registration, Program: specTarget.connector,
		ReleaseURL: pin.URL, PackageSHA256: pin.SHA256, PackageBytes: pin.Bytes,
		ConnectorSHA256: image.SHA256, ConnectorBytes: image.Bytes, LegacyOrder: legacyOrder,
	}
	beforePublish, acknowledged := dispatch, dispatch
	beforePublish.BeforeDispatch = true
	acknowledged.InstallerAcknowledged = true
	existing := ops.ExistingReceipt
	replay := existing != nil && *existing == acknowledged
	resumePublication := existing != nil && legacyOrder == publishFirst && *existing == beforePublish
	if existing != nil && !replay && !resumePublication {
		return errors.New("an unacknowledged or different integration installation is pending")
	}
	installing := !replay && (resumePublication || !alreadyInstalled)
	if !legacy {
		if err := ops.Admit(ctx, specTarget.registration, specTarget.connector, installing); err != nil {
			return err
		}
		if installing && ops.checkFreshProduct != nil {
			if err := ops.checkFreshProduct(ctx); err != nil {
				return err
			}
		}
	}
	var held HeldProgram
	defer func() { err = errors.Join(err, held.Close()) }()
	proveAndConfirm := func(expect bool) error {
		program := image.Path
		// Historical pins did not always describe the installed connector.
		// That path retains the legacy native registration and Keep proof.
		if !legacy || image.SHA256 != "" {
			var err error
			held, err = holdPinnedProgram(ctx, image)
			if err != nil {
				return err
			}
			program = held.Path
		}
		if expect {
			ops.Expect(ctx, specTarget.registration, program, image.SHA256)
		}
		if legacy {
			return ops.ConfirmLegacy(ctx, specTarget.registration, program)
		}
		return ops.Confirm(ctx, specTarget.registration, program)
	}
	if existing == nil && alreadyInstalled {
		// Reconnect can prove the connector image. Only a successful MSI
		// call in this transaction identifies bytes for saved-current.
		return proveAndConfirm(true)
	}
	var publish func(context.Context) error
	stagedPath := packageStagePath(current, pin.SHA256)
	if existing != nil {
		var raw bytes.Buffer
		release, holdErr := holdSavedPackage(ctx, stagedPath, pin.SHA256, &raw)
		if holdErr != nil {
			return holdErr
		}
		defer func() { err = errors.Join(err, release()) }()
		if int64(raw.Len()) != pin.Bytes {
			return errors.New("the staged integration package differs from its recorded size")
		}
		publish = func(ctx context.Context) error { return writePinnedPackage(ctx, current, raw.Bytes(), 64<<20) }
	}
	var releaseCurrent func() error
	defer func() {
		if releaseCurrent != nil {
			err = errors.Join(err, releaseCurrent())
		}
	}()
	holdPublication := func(ctx context.Context) error {
		var err error
		releaseCurrent, err = HoldSavedPackage(ctx, current, pin.SHA256)
		return err
	}
	if replay && legacyOrder == publishFirst {
		if err := holdPublication(ctx); err != nil {
			return err
		}
	}
	if !replay {
		if legacy {
			ops.Expect(ctx, specTarget.registration, specTarget.connector, image.SHA256)
		}
		installStaged := func(ctx context.Context, staged string, publish func(context.Context) error) error {
			var claimed *PackageConnectionReceipt
			var dispatched bool
			beforeEffect := func() error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !legacy {
					if err := ops.Admit(ctx, specTarget.registration, specTarget.connector, true); err != nil {
						return err
					}
				}
				if dispatched {
					return nil
				}
				if err := ops.Save(ctx, claimed, &dispatch); err != nil {
					return err
				}
				if !legacy {
					ops.Expect(ctx, specTarget.registration, specTarget.connector, image.SHA256)
				}
				dispatched = true
				return nil
			}
			retireBeforeDispatch := func(cause error) error {
				if claimed == nil || dispatched {
					return cause
				}
				proof, cancel := context.WithTimeout(context.WithoutCancel(ctx), connectionProofTimeout)
				defer cancel()
				return errors.Join(cause, ops.Save(proof, claimed, nil))
			}
			if legacyOrder == publishFirst {
				if err := ops.Save(ctx, existing, &beforePublish); err != nil {
					return err
				}
				claimed = &beforePublish
				if err := ops.Publish(ctx, beforePublish, func(ctx context.Context) error {
					if err := publish(ctx); err != nil {
						return err
					}
					return holdPublication(ctx)
				}); err != nil {
					return retireBeforeDispatch(err)
				}
			}
			if err := install(ctx, staged, beforeEffect); err != nil {
				return retireBeforeDispatch(err)
			}
			if !dispatched {
				return retireBeforeDispatch(errors.New("the integration installer returned success without acknowledging dispatch"))
			}
			proof, cancel := context.WithTimeout(context.WithoutCancel(ctx), connectionProofTimeout)
			defer cancel()
			return ops.Save(proof, &dispatch, &acknowledged)
		}
		if resumePublication {
			err = installStaged(ctx, stagedPath, publish)
		} else {
			publish, err = runStagedPackage(ctx, pin, current, installStaged)
		}
		if err != nil {
			return err
		}
	}
	if err := proveAndConfirm(replay); err != nil {
		return err
	}
	if legacyOrder == publishFirst {
		// The installer could only be acknowledged after publication. Do
		// not republish after native registration or during receipt replay.
		publish = func(context.Context) error { return nil }
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return ops.Commit(ctx, acknowledged, publish)
}
