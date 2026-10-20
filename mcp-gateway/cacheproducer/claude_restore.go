package cacheproducer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// Restore only an absent member whose receipt already names the current
// producer's exact bytes. An older missing member cannot be reconstructed from
// its hash, and changed/foreign files never enter a write. All existing members
// are admitted before the first restoration. Source directories must survive.
func restoreOwnedClaudeMembers(ctx context.Context, root, markerPath string, base []byte, marker *ClaudeMarker, files map[string][]byte, handles []setupIdentityRef, authority ...func() error) error {
	if marker == nil || !marker.MarketplaceOwned || !marker.PluginOwned || len(marker.Files) != len(files) {
		return errors.New("source restoration needs confirmed plugin ownership")
	}
	var missing []string
	for name, expected := range files {
		want, known := marker.Files[name]
		if !known {
			return errors.New("unlisted source member; preserved")
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := privatedir.CheckFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		actual, err := cas.ReadLimitContext(ctx, path, claudePayloadLimit)
		if err != nil {
			return err
		}
		if actual != nil {
			if PluginHash(actual) != want {
				return errors.New("source payload changed; preserved")
			}
			continue
		}
		if want != PluginHash(expected) {
			return errors.New("missing predecessor source needs Setup; preserved")
		}
		if err := privatedir.CheckDir(filepath.Dir(path)); err != nil {
			return err
		}
		missing = append(missing, name)
	}
	store := casapi.BoundedFileStore{MaxBytes: claudePayloadLimit}
	for _, name := range missing {
		for _, authorize := range authority {
			if authorize == nil {
				return errors.New("repair authority unavailable")
			}
			if err := authorize(); err != nil {
				return err
			}
		}
		if !setupIdentitiesCurrent(ctx, handles) {
			return errors.New("source identity changed before restoration")
		}
		current, err := cas.ReadLimitContext(ctx, markerPath, claudeMarkerLimit)
		if err != nil || !bytes.Equal(current, base) {
			return errors.New("source receipt changed before restoration")
		}
		if err := store.WriteContext(ctx, filepath.Join(root, filepath.FromSlash(name)), casapi.Value{}, files[name]); err != nil {
			return err
		}
	}
	return nil
}
