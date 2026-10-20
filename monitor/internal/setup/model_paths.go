package setup

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

type modelPathsPolicy struct {
	root       string
	file       string
	group      string
	categories []string
}

// preparedModelFolders returns every declared folder only after the journal
// records a configured model and its staged files still have their pinned sizes.
func preparedModelFolders(ctx context.Context, journal Journal, stage string, plan ModelPlan, categories []string) (map[string]string, error) {
	configured, err := PictureModelConfigured(ctx, journal, plan)
	if err != nil || !configured {
		return nil, err
	}
	folders := map[string]string{}
	for _, asset := range plan.Assets {
		parts := strings.Split(asset.Path, "/")
		if len(parts) < 2 {
			continue
		}
		category := parts[len(parts)-2]
		if !slices.Contains(categories, category) {
			continue
		}
		path := asset.reviewedStagePath(stage)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != asset.Download.Bytes {
			return nil, nil
		}
		folders[category] = filepath.Dir(path)
	}
	if len(folders) != len(categories) {
		return nil, nil
	}
	return folders, nil
}

// syncModelPaths publishes the first prepared offer in the adopter's ordered
// model list. Only the account's own journal may change the shared record.
func syncModelPaths(ctx context.Context, journal Journal, policy modelPathsPolicy, offers []PictureOffer, stage func(Home) string) error {
	home, err := CurrentHome()
	if err != nil {
		return err
	}
	own, err := DefaultJournal()
	if err != nil {
		return err
	}
	if home.Isolated || filepath.Clean(journal.Path) != filepath.Clean(own.Path) {
		return nil
	}
	name := filepath.Join(home.Data, filepath.FromSlash(policy.root), policy.file)
	var folders map[string]string
	for _, offer := range offers {
		if folders, err = preparedModelFolders(ctx, journal, stage(home), offer.Plan, policy.categories); err != nil {
			return err
		}
		if folders != nil {
			break
		}
	}
	return writeModelPaths(name, policy.group, folders)
}

// writeModelPaths writes folders as a single named group, in
// a folder private to this account, or removes name for none.
func writeModelPaths(name, group string, folders map[string]string) error {
	if len(folders) == 0 {
		if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := json.Marshal(map[string]map[string]string{group: folders})
	if err != nil {
		return err
	}
	if current, err := os.ReadFile(name); err == nil && string(current) == string(raw) {
		return nil
	}
	dir := filepath.Dir(name)
	if err := privatedir.Ensure(dir); err != nil {
		return err
	}
	return cas.Change(name, func([]byte) ([]byte, error) { return raw, nil })
}
