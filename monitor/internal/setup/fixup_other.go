//go:build !windows

package setup

import (
	"context"
	"errors"
)

// Fix-ups repair a Windows Installer package; elsewhere none is installed.
func installedPackage(string) (bool, error) { return false, nil }

func repairPackage(context.Context, string) error {
	return errors.New("repair needs Windows Installer")
}

func folderProgramRunning(string) (bool, error) { return false, nil }

func folderProcesses([]string) ([]uint32, error) { return nil, nil }
