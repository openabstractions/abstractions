package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
)

type joinedUpgrade struct {
	Version   int             `json:"version"`
	Scope     string          `json:"scope"`
	Record    string          `json:"record"`
	Installer processIdentity `json:"installer"`
}

func joinedUpgradePath(path string) string { return filepath.Join(filepath.Dir(path), "joined.json") }

// Begin verifies the Burn token under the scope lock before admitting its MSI
// occurrence. End and rollback use the occurrence of their own MSI parent.
func (e exclusionEnv) storeJoinedUpgrade(ctx context.Context, path string, record *upgradeExclusion, holder processIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(joinedUpgrade{Version: 1, Scope: record.Scope, Record: upgradeAsideName(record), Installer: holder})
	if err != nil {
		return err
	}
	return e.writeUpgradeSidecar(joinedUpgradePath(path), record.Scope, data)
}

func (e exclusionEnv) writeUpgradeSidecar(path, scope string, data []byte) error {
	if e.writeContext != nil {
		// The scope lock orders writers. The native writer syncs and renames
		// in one attempt without the v1 exclusion-schema collision retry.
		return e.writeContext(context.Background(), path, scope, data)
	}
	return e.write(path, scope, data)
}

func (e exclusionEnv) admittedUpgradeInstaller(path string, record *upgradeExclusion) (processIdentity, error) {
	path = joinedUpgradePath(path)
	data, err := e.read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return record.Installer, nil
	}
	if err != nil {
		return processIdentity{}, err
	}
	if len(data) > exclusionMaxBytes {
		return processIdentity{}, errors.New("joined upgrade admission is too large")
	}
	if err := e.trusted(path, record.Scope); err != nil {
		return processIdentity{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var joined joinedUpgrade
	if err := decoder.Decode(&joined); err != nil {
		return processIdentity{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return processIdentity{}, errors.New("joined upgrade admission has trailing data")
	}
	if joined.Version != 1 || joined.Scope != record.Scope || joined.Installer.PID == 0 || joined.Installer.Created < 0 {
		return processIdentity{}, errors.New("invalid joined upgrade admission")
	}
	if joined.Record != upgradeAsideName(record) {
		return record.Installer, nil
	}
	return joined.Installer, nil
}

// Burn can run its fallback rollback after the admitted MSI has exited.
// The caller, the joined MSI and the exclusion are checked under the scope lock.
func (e exclusionEnv) canRollbackJoinedUpgrade(record *upgradeExclusion, admitted, caller processIdentity, rollback bool) bool {
	return rollback && record.Scope == "machine" && caller.Created > 0 && admitted.Created > 0 &&
		sameUpgradeInstaller(record.Installer, caller) && !sameUpgradeInstaller(record.Installer, admitted) &&
		e.ended != nil && e.ended(admitted)
}
