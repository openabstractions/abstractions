package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type archiveImageRecipe struct {
	Version         int    `json:"version"`
	SelectionPolicy string `json:"selection_policy"`
}

// writeArchiveImageRecipe publishes a complete fixed recipe without
// replacing a file created by a concurrent writer.
func writeArchiveImageRecipe(path, title, policy string, prepare func() error) error {
	if _, err := os.Lstat(path); err == nil {
		if err := verifyArchiveImageRecipe(path, title, policy); err == nil {
			return nil
		}
		return errors.New("an existing " + title + " image recipe differs from OA setup; its contents were preserved")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := prepare(); err != nil {
		return err
	}
	data, err := json.Marshal(archiveImageRecipe{Version: 1, SelectionPolicy: policy})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".image-recipe-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Linking publishes a complete file atomically and fails if another setup
	// action or the user created the destination after the initial check.
	if err := os.Link(temporaryName, path); err != nil {
		if errors.Is(err, os.ErrExist) && verifyArchiveImageRecipe(path, title, policy) == nil {
			return nil
		}
		return err
	}
	return os.Remove(temporaryName)
}

// verifyArchiveImageRecipe accepts the exact supported record and policy.
func verifyArchiveImageRecipe(path, title, policy string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 {
		return errors.New(title + " image recipe is not a small regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var recipe archiveImageRecipe
	if err := decoder.Decode(&recipe); err != nil {
		return fmt.Errorf("%s image recipe is invalid: %w", title, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New(title + " image recipe contains trailing data")
	}
	if recipe.Version != 1 || recipe.SelectionPolicy != policy {
		return errors.New(title + " image recipe does not select the configured OA image policy")
	}
	return nil
}
