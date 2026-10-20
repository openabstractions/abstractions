package nativecli

import (
	"context"
	"errors"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// Saved FileIDs locate images. Fresh native holds provide admission evidence.
func selectSavedImage(ctx context.Context, stored []string, hold func(context.Context, string) (nativeProductImage, error)) (nativeProductImage, error) {
	if len(stored) == 0 || len(stored) > 64 || hold == nil {
		return nil, errors.New("saved Code command unavailable")
	}
	var selected nativeProductImage
	fail := func(err error) (nativeProductImage, error) {
		if selected != nil {
			err = errors.Join(err, selected.Close())
		}
		return nil, err
	}
	for _, raw := range stored {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		id, err := fileid.ParseStoredFileID(raw)
		if err != nil {
			return fail(errors.New("saved Code image locator unavailable"))
		}
		// The owned marker locates a configured installation; the hold may
		// follow a vendor update. Its old FileID supplies no admission authority.
		candidate, err := hold(ctx, id.Path())
		if err != nil {
			if candidate != nil {
				err = errors.Join(err, candidate.Close())
			}
			return fail(err)
		}
		if candidate == nil {
			return fail(errors.New("saved Code image hold unavailable"))
		}
		if err := candidate.Check(ctx); err != nil {
			return fail(errors.Join(err, candidate.Close()))
		}
		if candidate.FileKey() == "" {
			return fail(errors.Join(errors.New("saved Code current image identity unavailable"), candidate.Close()))
		}
		if selected != nil {
			same := selected.FileKey() == candidate.FileKey()
			if err := candidate.Close(); err != nil {
				return fail(err)
			}
			if !same {
				return fail(errors.New("saved Code commands are ambiguous"))
			}
			continue
		}
		selected = candidate
	}
	if err := selected.Check(ctx); err != nil {
		return fail(err)
	}
	return selected, nil
}
