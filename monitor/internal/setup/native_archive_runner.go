package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// nativeArchiveInstallPolicy describes optional operations for a compiled
// historical archive. Generic archive custody does not depend on them.
type nativeArchiveInstallPolicy struct {
	runtimeSite   func(Journal, State) (string, error)
	afterReadback func(context.Context, Journal)
}

func nativeResourceDownload(a Artifact, index int) (PinnedDownload, error) {
	if a.NativeResources == nil || index < 0 || index > len(a.NativeResources.Backend.Archive.Parts) {
		return PinnedDownload{}, errors.New("invalid catalog resource index")
	}
	if index == 0 {
		return a.DownloadPin(), nil
	}
	p := a.NativeResources.Backend.Archive.Parts[index-1]
	d := a.NativeResources.Backend.DataRelease
	return PinnedDownload{URL: "https://github.com/" + d.Repository + "/releases/download/" + url.PathEscape(d.Tag) + "/" + url.PathEscape(p.Name), Bytes: p.Bytes, SHA256: p.SHA256, Label: "Install " + a.InstallIdentity + " resource"}, nil
}

func nativeResourceWorkKey(component string, a Artifact, index int) string {
	p, err := nativeResourceDownload(a, index)
	if err != nil {
		return ""
	}
	return workKey(fmt.Sprintf("%s-resource-%d", component, index), Artifact{SHA256: p.SHA256})
}

func (r Runner) nativePartPath(a Artifact, index int) string {
	if index == 0 {
		return nativeHostStagePath(r.Journal, a)
	}
	return filepath.Join(filepath.Dir(r.Journal.Path), "downloads", a.SHA256+"-parts", a.NativeResources.Backend.Archive.Parts[index-1].Name)
}

func nativeHostStagePath(j Journal, a Artifact) string {
	return filepath.Join(filepath.Dir(j.Path), "downloads", a.SHA256+".zip")
}

func (r Runner) advanceNativeArchive(ctx context.Context, state State, id string, a Artifact, e Effect) (Status, error) {
	root, err := installPath(a)
	if err != nil {
		return r.status(ctx, state), err
	}
	index := e.ResourceIndex
	pin, err := nativeResourceDownload(a, index)
	if err != nil {
		return r.status(ctx, state), err
	}
	published := Installed
	if e.Retained != nil || firstInstallChecked(id) {
		published = Staged
	}
	save := func(next Effect) (Status, error) {
		updated, err := r.Journal.Update(ctx, state.Plan.ID, id, e, next)
		if err != nil {
			return r.status(ctx, state), err
		}
		return r.status(ctx, updated), nil
	}
	switch e.Phase {
	case Pending:
		sweepClearedNative(root)
		if err := nativeArchiveSpace(a, root, filepath.Dir(r.Journal.Path), r.nativeArchiveFreeBytes); err != nil {
			return r.status(ctx, state), err
		}
		installed, err := archiveInstalledContext(ctx, a, root)
		if err != nil {
			return r.status(ctx, state), err
		}
		if installed {
			return save(Effect{Phase: published, Reason: "complete native installation revalidated"})
		}
		if err := absent(root); err != nil {
			return r.status(ctx, state), err
		}
		work, err := r.Transfer.Reserve(ctx, pin, nativeResourceWorkKey(id, a, index))
		if err != nil {
			return r.status(ctx, state), err
		}
		state, err = r.Journal.Update(ctx, state.Plan.ID, id, e, Effect{Phase: Requested, Work: &work})
		if err != nil {
			return Status{}, err
		}
		err = r.Transfer.Submit(ctx, pin, work)
		return r.status(ctx, state), err
	case Requested:
		if e.Work == nil {
			return r.status(ctx, state), errors.New("native transfer lacks recorded public work")
		}
		if err := r.Transfer.Submit(ctx, pin, *e.Work); err != nil {
			return r.status(ctx, state), err
		}
		observation, err := r.Transfer.Observe(ctx, *e.Work)
		if err != nil {
			return r.status(ctx, state), err
		}
		if observation.State == "failed" || observation.State == "cancelled" {
			return save(Effect{Phase: Failed, Reason: observation.Failure})
		}
		if observation.State != "complete" {
			return r.status(ctx, state), nil
		}
		if index > 0 {
			dir := filepath.Dir(r.nativePartPath(a, index))
			if err := os.Mkdir(dir, 0700); err == nil {
				if err := privatedir.Tighten(dir); err != nil {
					return r.status(ctx, state), err
				}
			} else if !errors.Is(err, os.ErrExist) {
				return r.status(ctx, state), err
			}
			if err := privatedir.CheckDir(dir); err != nil {
				return r.status(ctx, state), err
			}
		}
		staged, err := r.stageArchivePart(ctx, Artifact{Bytes: pin.Bytes, SHA256: pin.SHA256}, *e.Work, r.nativePartPath(a, index))
		if err != nil {
			return r.status(ctx, state), err
		}
		host := e.StagedPath
		if index == 0 {
			host = staged
		}
		if index == len(a.NativeResources.Backend.Archive.Parts) {
			return save(Effect{Phase: Verified, StagedPath: host})
		}
		nextPin, err := nativeResourceDownload(a, index+1)
		if err != nil {
			return r.status(ctx, state), err
		}
		work, err := r.Transfer.Reserve(ctx, nextPin, nativeResourceWorkKey(id, a, index+1))
		if err != nil {
			return r.status(ctx, state), err
		}
		state, err = r.Journal.Update(ctx, state.Plan.ID, id, e, Effect{Phase: Requested, Work: &work, StagedPath: host, ResourceIndex: index + 1})
		if err != nil {
			return Status{}, err
		}
		err = r.Transfer.Submit(ctx, nextPin, work)
		return r.status(ctx, state), err
	case Verified:
		if err := verifyFile(e.StagedPath, a); err != nil {
			return r.status(ctx, state), err
		}
		_, hostFiles, _, err := nativeImagePin(ctx, a, root, e.StagedPath)
		if err != nil {
			return r.status(ctx, state), err
		}
		if err := nativeArchivePathBudget(ctx, root, append(hostFiles, archiveFileRecord{Path: archiveMarker})); err != nil {
			return r.status(ctx, state), err
		}
		parts := make([]sevenZipPin, 0, len(a.NativeResources.Backend.Archive.Parts))
		for i, p := range a.NativeResources.Backend.Archive.Parts {
			name := r.nativePartPath(a, i+1)
			if err := verifyFile(name, Artifact{Bytes: p.Bytes, SHA256: p.SHA256}); err != nil {
				return r.status(ctx, state), err
			}
			parts = append(parts, sevenZipPin{Path: name, Bytes: p.Bytes, SHA256: p.SHA256})
		}
		policy := nativeArchiveInstallPolicyFor(id)
		var site string
		if policy.runtimeSite != nil {
			site, err = policy.runtimeSite(r.Journal, state)
			if err != nil {
				return r.status(ctx, state), err
			}
		}
		state, err = r.Journal.Update(ctx, state.Plan.ID, id, e, Effect{Phase: Installing})
		if err != nil {
			return Status{}, err
		}
		err = InstallArchive(ctx, a, e.StagedPath, root)
		if err == nil {
			err = installNativeResourcesFor(ctx, a, root, e.StagedPath, parts, site)
		}
		if err == nil {
			var installed bool
			installed, err = archiveInstalledContext(ctx, a, root)
			if err == nil && !installed {
				err = errors.New("native installation did not pass full readback")
			}
		}
		if err == nil && policy.afterReadback != nil {
			policy.afterReadback(ctx, r.Journal)
		}
		phase := published
		reason := "complete native installation revalidated"
		if err != nil {
			phase = Uncertain
			reason = "partial native installation preserved: " + err.Error()
		}
		state, recordErr := r.Journal.Update(ctx, state.Plan.ID, id, state.Effects[id], Effect{Phase: phase, Reason: reason})
		return r.status(ctx, state), errors.Join(err, recordErr)
	case Installing:
		installed, err := archiveInstalledContext(ctx, a, root)
		if err != nil {
			return r.status(ctx, state), err
		}
		if installed {
			return save(Effect{Phase: published, Reason: "complete native installation recovered"})
		}
		return save(Effect{Phase: Uncertain, Reason: "partial native installation preserved; explicit recovery required"})
	}
	return r.status(ctx, state), errors.New("invalid native archive phase")
}

// sweepClearedNative removes folders an interrupted clearPartialNative set
// aside beside root (".<version>.clear-*"). Each held a partial package tree
// only; a failure leaves it for the next retry or install.
func sweepClearedNative(root string) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+".clear-*"))
	if err != nil {
		return
	}
	for _, aside := range matches {
		info, err := os.Lstat(aside)
		if err != nil || !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			continue
		}
		linked := false
		_ = filepath.WalkDir(aside, func(current string, _ fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				linked = true // an unreadable entry fails closed
				return fs.SkipAll
			}
			if info, err := os.Lstat(current); err != nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				linked = true
				return fs.SkipAll
			}
			return nil
		})
		if linked || programfiles.UnlockWithin(aside) != nil {
			continue
		}
		_ = os.RemoveAll(aside)
	}
}

// clearPartialNative removes a native installation Setup left partial, so a
// retry installs the pinned packages again. The folder holds package files
// only (the user directory is separate). It must carry Setup's own receipt
// for this exact package, and no link or reparse point anywhere inside.
func clearPartialNative(ctx context.Context, a Artifact, root string) error {
	raw, err := os.ReadFile(filepath.Join(root, archiveMarker))
	if err != nil {
		return fmt.Errorf("the unfinished %s folder has no Setup receipt, so it was preserved: %w", nativeArchiveAppTitle, err)
	}
	var receipt archiveReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.ArtifactSHA256 != a.SHA256 ||
		receipt.Version != a.Version || receipt.InstallIdentity != a.InstallIdentity {
		return errors.New("the unfinished " + nativeArchiveAppTitle + " folder is not this package's, so it was preserved")
	}
	if err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return errors.New("the unfinished " + nativeArchiveAppTitle + " folder contains a link, so it was preserved")
		}
		return ctx.Err()
	}); err != nil {
		return err
	}
	if err := programfiles.UnlockWithin(root); err != nil {
		return err
	}
	// The folder moves aside whole first, so its receipt never names a
	// half-deleted tree at the install path.
	aside, err := os.MkdirTemp(filepath.Dir(root), "."+filepath.Base(root)+".clear-")
	if err != nil {
		return err
	}
	target := filepath.Join(aside, filepath.Base(root))
	if err := os.Rename(root, target); err != nil {
		return errors.Join(err, os.Remove(aside))
	}
	return os.RemoveAll(aside)
}

// verifiedNativeDownloads checks the staged host ZIP and every backend part
// against their pins before a retry discards the partial installation.
func (r Runner) verifiedNativeDownloads(a Artifact, host string) error {
	if err := verifyFile(host, a); err != nil {
		return err
	}
	for i, p := range a.NativeResources.Backend.Archive.Parts {
		if err := verifyFile(r.nativePartPath(a, i+1), Artifact{Bytes: p.Bytes, SHA256: p.SHA256}); err != nil {
			return err
		}
	}
	return nil
}
