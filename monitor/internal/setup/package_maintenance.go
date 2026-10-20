package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// PackageMaintenanceRecord is the durable state of a connected extension.
// Its JSON keys also read receipts already kept by earlier releases.
type PackageMaintenanceRecord struct {
	// Seen and Recorded refer to the installer's connector pin.
	Seen              string                    `json:"seen,omitempty"`
	Kept              string                    `json:"kept,omitempty"`
	KeptPackageSHA256 string                    `json:"kept_package_sha256,omitempty"`
	History           []string                  `json:"history,omitempty"`
	PendingUpdate     *PackageUpdateExpectation `json:"pending,omitempty"`
	Connection        *PackageConnectionReceipt `json:"connection,omitempty"`
	Previous          *PreviousIntegration      `json:"previous,omitempty"`
	Undo              *PackageUndoProof         `json:"undo,omitempty"`
	Declined          string                    `json:"declined,omitempty"`
	Changed           bool                      `json:"changed,omitempty"`
	Recorded          string                    `json:"recorded,omitempty"`
}

// PackageUpdateExpectation names the connector selected for a kept update.
type PackageUpdateExpectation struct {
	Application string              `json:"application"`
	Program     string              `json:"program"`
	SHA256      string              `json:"sha256"`
	Version     string              `json:"version,omitempty"`
	At          int64               `json:"at"`
	Replacement *PackageReplacement `json:"replacement,omitempty"`
}

// MatchesProgram compares the saved connector locator with the selected host.
func (e *PackageUpdateExpectation) MatchesProgram(program string) bool {
	return e != nil && filepath.IsAbs(program) && filepath.Clean(e.Program) == program
}

// PackageUndoProof records the observed native effects of restoring a kept
// predecessor.
type PackageUndoProof struct {
	At                int64              `json:"at"`
	Replacement       PackageReplacement `json:"replacement"`
	RestoreRequired   bool               `json:"restore_required,omitempty"`
	RestoreDispatched bool               `json:"restore_dispatched,omitempty"`
	RestoreComplete   bool               `json:"restore_complete,omitempty"`
}

// PackageMaintenancePath selects the existing receipt path for a declared
// Catalog package independently of which installer payload is available.
func PackageMaintenancePath(catalogID string) (string, error) {
	spec, ok := product.PackageDeclaration(catalogID)
	if !ok {
		return "", errors.New("no declared extension package owns this maintenance record")
	}
	home, err := CurrentHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home.Config, spec.ID+"-integration.json"), nil
}

const packageMaintenanceRecordLimit = 16 << 10

// PackageMaintenanceStore serializes receipt edits for one selected package.
type PackageMaintenanceStore struct {
	Path string
}

func (s PackageMaintenanceStore) Load() (PackageMaintenanceRecord, error) {
	var record PackageMaintenanceRecord
	raw, err := cas.ReadLimit(s.Path, packageMaintenanceRecordLimit)
	if err == nil && raw != nil {
		err = json.Unmarshal(raw, &record)
	}
	if err != nil {
		return PackageMaintenanceRecord{}, err
	}
	return record, nil
}

func (s PackageMaintenanceStore) Edit(ctx context.Context, change func(*PackageMaintenanceRecord) error) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return cas.ChangeLimitContext(ctx, s.Path, packageMaintenanceRecordLimit, func(raw []byte) ([]byte, error) {
		var record PackageMaintenanceRecord
		if raw != nil && json.Unmarshal(raw, &record) != nil {
			record = PackageMaintenanceRecord{}
		}
		if err := change(&record); err != nil {
			return nil, err
		}
		return json.Marshal(record)
	})
}

// SaveConnection claims or advances the first-install receipt. A kept
// replacement or Undo already in progress owns the installer.
func (r *PackageMaintenanceRecord) SaveConnection(before, after *PackageConnectionReceipt) error {
	if (r.Connection == nil) != (before == nil) ||
		before != nil && *r.Connection != *before ||
		r.Undo != nil || r.PendingUpdate != nil && r.PendingUpdate.Replacement != nil {
		return errors.New("another integration change owns the installation receipt")
	}
	if after != nil && after.BeforeDispatch && (after.InstallerAcknowledged || after.LegacyOrder != publishFirst) {
		return errors.New("invalid integration publication receipt")
	}
	r.Connection = after
	return nil
}

// PublishConnection verifies the exact pre-dispatch receipt and publishes
// while the maintenance store edit excludes competing connections.
func (r *PackageMaintenanceRecord) PublishConnection(ctx context.Context, prepared PackageConnectionReceipt, publish func(context.Context) error) error {
	if !prepared.BeforeDispatch || prepared.InstallerAcknowledged || prepared.LegacyOrder != publishFirst {
		return errors.New("invalid integration publication receipt")
	}
	if err := r.SaveConnection(&prepared, &prepared); err != nil {
		return err
	}
	return publish(ctx)
}

// CommitConnection publishes only while the exact acknowledged receipt is
// held inside the store's edit.
func (r *PackageMaintenanceRecord) CommitConnection(ctx context.Context, installed PackageConnectionReceipt, publish func(context.Context) error) error {
	if r.Connection == nil || *r.Connection != installed || !installed.InstallerAcknowledged || installed.BeforeDispatch {
		return errors.New("the pending integration installation changed")
	}
	if err := publish(ctx); err != nil {
		return err
	}
	// The connector remains held through settlement. Legacy pins can prove
	// this installed connector without associating an MSI with a Keep grant.
	if installed.LegacyOrder == "" || installed.ConnectorSHA256 != "" {
		r.RecordKeptConnector(installed.ConnectorSHA256)
		r.Declined = ""
	}
	if installed.LegacyOrder == "" {
		r.KeptPackageSHA256 = installed.PackageSHA256
	}
	r.Connection = nil
	return nil
}

// PackageUpdateObservation carries the native and package facts already
// checked by the scheduled caller. An unknown observation defers the decision.
type PackageUpdateObservation struct {
	Known, Kept, Outdated bool
	Application, Program  string
}

// ScheduledUpdateSnapshot drops only a superseded expectation without an
// admitted replacement. The caller reconciles retained effects separately.
func (r PackageMaintenanceRecord) ScheduledUpdateSnapshot(pin string) PackageMaintenanceRecord {
	if pending := r.PendingUpdate; pending != nil && pending.SHA256 != pin && pending.Replacement == nil {
		r.PendingUpdate = nil
	}
	return r
}

// PlanScheduledUpdate advances a scheduled maintenance snapshot. The supplied
// stamp is evaluated only when a new expectation is necessary. The caller
// retains the native read, installation and presentation responsibilities.
func (r PackageMaintenanceRecord) PlanScheduledUpdate(pin string, observation PackageUpdateObservation, stamp func() (string, int64)) (next PackageMaintenanceRecord, done, downgrade bool) {
	if !observation.Known || r.Connection != nil || r.Undo != nil || r.PendingUpdate != nil && r.PendingUpdate.Replacement != nil {
		return r, false, false
	}
	r.Seen = pin
	switch {
	case !observation.Kept:
		r.PendingUpdate, r.Recorded = nil, pin
	case !observation.Outdated:
		r.PendingUpdate = nil
	case r.Declined == pin:
		r.PendingUpdate, r.Recorded = nil, pin
	case r.IsDowngrade(pin):
		r.PendingUpdate, r.Recorded = nil, pin
		downgrade = true
	case r.PendingUpdate == nil:
		version, at := stamp()
		r.PendingUpdate = &PackageUpdateExpectation{Application: observation.Application, Program: observation.Program,
			SHA256: pin, Version: version, At: at}
	}
	return r, true, downgrade
}

// AdmitScheduledUpdate compares the whole receipt before saving the planned
// decision, optionally with the exact first replacement proof.
func (r *PackageMaintenanceRecord) AdmitScheduledUpdate(before, next PackageMaintenanceRecord, pending *PackageUpdateExpectation) error {
	if !reflect.DeepEqual(*r, before) {
		return errors.New("the integration receipt changed")
	}
	*r = next
	r.PendingUpdate = pending
	return nil
}

// CheckUpdate verifies both the selected update and its replacement proof
// before a caller changes the receipt.
func (r *PackageMaintenanceRecord) CheckUpdate(pending *PackageUpdateExpectation) error {
	if r.PendingUpdate == nil || pending == nil {
		return errors.New("the pending integration update is gone")
	}
	want, have := *pending, *r.PendingUpdate
	if (want.Replacement == nil) != (have.Replacement == nil) ||
		want.Replacement != nil && *want.Replacement != *have.Replacement {
		return errors.New("the pending integration replacement changed")
	}
	want.Replacement, have.Replacement = nil, nil
	if want != have {
		return errors.New("the pending integration update changed")
	}
	return nil
}

// CommitReplacement settles the exact published update and retains its
// predecessor for Undo. The transaction calls it after package publication.
func (r *PackageMaintenanceRecord) CommitReplacement(pending *PackageUpdateExpectation, activityAt int64) (PreviousIntegration, error) {
	if err := r.CheckUpdate(pending); err != nil {
		return PreviousIntegration{}, err
	}
	step := pending.Replacement
	if step == nil || !step.InstallComplete || step.After == "" {
		return PreviousIntegration{}, errors.New("the published replacement has no completed receipt")
	}
	previous := step.Previous
	previous.ActivityAt = activityAt
	r.RecordKeptConnector(pending.SHA256)
	r.KeptPackageSHA256 = step.Candidate
	r.Previous, r.Declined, r.Recorded = &previous, "", pending.SHA256
	return previous, nil
}

// RecordKeptConnector settles the update and retains the bounded pin history.
func (r *PackageMaintenanceRecord) RecordKeptConnector(pin string) {
	if r.Kept != pin {
		r.KeptPackageSHA256 = ""
	}
	r.Kept, r.PendingUpdate, r.Changed = pin, nil, false
	r.History = append(slices.DeleteFunc(r.History, func(p string) bool { return p == pin }), pin)
	if len(r.History) > 16 {
		r.History = r.History[len(r.History)-16:]
	}
}

// IsDowngrade reports a pin that predates a later observed installation.
func (r *PackageMaintenanceRecord) IsDowngrade(pin string) bool {
	i := slices.Index(r.History, pin)
	return i >= 0 && i < len(r.History)-1
}

// SaveUndo records a new Undo proof against the previous receipt snapshot.
// Completion selects the verified predecessor and clears the finished work.
func (r *PackageMaintenanceRecord) SaveUndo(before PackageMaintenanceRecord, next *PackageUndoProof, complete bool) error {
	if before.Previous == nil || r.Previous == nil || *r.Previous != *before.Previous ||
		r.Kept != before.Kept || r.KeptPackageSHA256 != before.KeptPackageSHA256 ||
		(r.Undo == nil) != (before.Undo == nil) || r.Undo != nil && *r.Undo != *before.Undo {
		return errors.New("the pending Undo changed")
	}
	r.Undo = next
	if complete {
		r.Declined, r.Kept, r.Previous, r.PendingUpdate, r.Changed = r.Seen, before.Previous.SHA256, nil, nil, false
		r.KeptPackageSHA256 = before.Previous.PackageSHA256
	}
	return nil
}
