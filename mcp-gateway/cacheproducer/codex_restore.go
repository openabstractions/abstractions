package cacheproducer

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// The native snapshot already matched the existing receipt. Admit every
// surviving source member against the compiled recipe before creating any
// absent member. The existing O_EXCL creator preserves changed/racing files.
func restoreCodexSource(ctx context.Context, root, plugin, catalogPath, receipt string, base []byte, files map[string][]byte, catalog []byte, handles []setupIdentityRef) error {
	if err := codexHookInventory(plugin, true); err != nil {
		return err
	}
	if _, err := codexHookMembers(ctx, plugin, files, false, true); err != nil {
		return err
	}
	oldCatalog, err := cas.ReadLimitContext(ctx, catalogPath, claudeMarkerLimit)
	if err != nil || oldCatalog != nil && !bytes.Equal(oldCatalog, catalog) {
		return errors.Join(err, codexHookPreserved("source catalog changed; preserved"))
	}
	for _, directory := range []string{filepath.Join(root, "plugins"),
		filepath.Join(plugin, "hooks"), filepath.Join(plugin, ".codex-plugin"), filepath.Dir(catalogPath)} {
		if !setupIdentitiesCurrent(ctx, handles) {
			return codexHookPreserved("source identity changed before restoration")
		}
		if err := privatedir.Ensure(directory); err != nil {
			return err
		}
	}
	var held []setupIdentityRef
	for _, directory := range []string{root, plugin, filepath.Join(plugin, "hooks"), filepath.Join(plugin, ".codex-plugin"), filepath.Dir(catalogPath)} {
		h, _, err := setupIdentity(ctx, directory, true)
		if err != nil {
			return err
		}
		defer h.Close()
		held = append(held, h)
	}
	create := func(path string, expected []byte) error {
		if !setupIdentitiesCurrent(ctx, handles) || !setupIdentitiesCurrent(ctx, held) {
			return codexHookPreserved("source identity changed before restoration")
		}
		current, err := cas.ReadLimitContext(ctx, receipt, claudeMarkerLimit)
		if err != nil || !bytes.Equal(current, base) {
			return codexHookPreserved("source receipt changed before restoration")
		}
		return codexHookCreate(path, expected)
	}
	for name, expected := range files {
		if err := create(filepath.Join(plugin, filepath.FromSlash(name)), expected); err != nil {
			return err
		}
	}
	return create(catalogPath, catalog)
}
