package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// This private installer diagnostic shares classification and right admission
// with the real census. It never invokes stop, requestClose, or exclusion write.
func inspectUpgradeCensus(folders []string, ops upgradeCensusOps, output io.Writer) error {
	return inspectUpgradeCensusContext(context.Background(), folders, ops, output)
}

func inspectUpgradeCensusContext(ctx context.Context, folders []string, ops upgradeCensusOps, output io.Writer) error {
	encoder := json.NewEncoder(output)
	var writeErr error
	err := readOnlyUpgradeCensus(ctx, folders, ops, func(row upgradeCensusObservation) {
		writeErr = errors.Join(writeErr, encoder.Encode(row))
	})
	return errors.Join(err, writeErr)
}

func readOnlyUpgradeCensus(ctx context.Context, folders []string, ops upgradeCensusOps, observe func(upgradeCensusObservation)) error {
	processes, err := censusUpgradeProcessesContext(ctx, folders, ops, observe)
	for _, process := range processes {
		err = errors.Join(err, process.close())
	}
	return err
}

func serviceInspectUpgrade(request serviceRequest, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	folders, _, err := upgradeFolders(request.folder, nil)
	if err != nil {
		return fmt.Errorf("upgrade inspection stage=folders: %w", err)
	}
	path, err := exclusionPath(request.scope)
	if err != nil {
		return fmt.Errorf("upgrade inspection stage=storage-path: %w", err)
	}
	encoder := json.NewEncoder(output)
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("upgrade inspection stage=caller-token: %w", err)
	}
	if err := encoder.Encode(map[string]any{
		"scope": request.scope, "pid": os.Getpid(), "folders": folders,
		"caller_sid": sid, "elevated": windows.GetCurrentProcessToken().IsElevated(),
		"context": "current caller only; not SYSTEM proof", "exclusion_path": path,
		"storage_write_tested": false, "process_stop_requested": false,
	}); err != nil {
		return err
	}
	var failure error
	if info, err := os.Lstat(path); err == nil {
		failure = errors.Join(failure, encoder.Encode(map[string]any{"stage": "record-metadata", "present": true, "regular": info.Mode().IsRegular(), "readonly": info.Mode().Perm()&0200 == 0}))
	} else if errors.Is(err, os.ErrNotExist) {
		failure = errors.Join(failure, encoder.Encode(map[string]any{"stage": "record-metadata", "present": false}))
	} else {
		failure = errors.Join(failure, fmt.Errorf("upgrade inspection stage=record-metadata path=%q: %w", path, err))
	}
	if request.scope == "machine" {
		for _, directory := range []string{filepath.Dir(filepath.Dir(path)), filepath.Dir(path)} {
			err := requireAdministrativeOwner(directory)
			row := map[string]any{"stage": "read-directory-owner", "path": directory}
			if err != nil {
				row["error"] = err.Error()
				failure = errors.Join(failure, fmt.Errorf("upgrade inspection stage=read-directory-owner path=%q: %w", directory, err))
			}
			failure = errors.Join(failure, encoder.Encode(row))
		}
	}
	return errors.Join(failure, inspectUpgradeCensusContext(ctx, folders, systemUpgradeCensusOps(), output))
}
