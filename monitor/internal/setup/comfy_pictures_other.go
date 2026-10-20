//go:build !windows

package setup

import (
	"os"
	"path/filepath"
)

func comfyPicturesDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Pictures"), nil
}
