package setup

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	archive "github.com/openabstractions/abstraction-download/go/archive"
)

// The download archive is a folder the person names, often on a NAS share,
// that the download service checks before the internet and keeps a copy of
// every download in (abstraction-download archive package, DL-R44). Setup
// owns its Settings row and the walks its pages ask for; its downloads use
// the archive through the download service like every other download.
//
// The archive keeps its setting, listing and write-back queue in Setup's
// state folder, which is also the runtime's archive state folder
// (archive.StateFor): the Panel and the runtime it serves read one setting.

// useArchiveState points the archive at this process's Setup state, or
// turns it off when Setup has none.
func useArchiveState() {
	home, err := CurrentHome()
	if err != nil {
		archive.UseState("")
		return
	}
	archive.UseState(home.Config)
}

// ArchiveFolder is the download archive folder the person set, or "".
func ArchiveFolder() string {
	useArchiveState()
	return archive.Folder()
}

// ArchiveKeep is Keep a copy of new downloads here: on unless the person
// turned it off.
func ArchiveKeep() bool {
	useArchiveState()
	return archive.KeepCopies()
}

// SetArchiveFolder saves the download archive folder; "" stops using one.
func SetArchiveFolder(ctx context.Context, folder string) error {
	useArchiveState()
	return archive.SetFolder(ctx, folder)
}

// SetArchiveKeep saves Keep a copy of new downloads here.
func SetArchiveKeep(ctx context.Context, keep bool) error {
	useArchiveState()
	return archive.SetKeep(ctx, keep)
}

// ArchiveLook lists the archive folder again: the person chose it or
// pressed Look again. The channel closes when the walk ends; it is nil with
// no folder set.
func ArchiveLook() <-chan struct{} {
	useArchiveState()
	return archive.Look()
}

func archiveWants(downloads []PinnedDownload) []archive.Want {
	wants := make([]archive.Want, 0, len(downloads))
	for _, d := range downloads {
		wants = append(wants, archive.Want{SHA256: d.SHA256, Bytes: d.Bytes})
	}
	return wants
}

// ArchiveLookFor lists the archive folder when one of downloads has no
// listed candidate and the last walk began more than ten minutes ago. The
// channel closes when the walk ends; it is nil when no walk runs.
func ArchiveLookFor(downloads []PinnedDownload) <-chan struct{} {
	useArchiveState()
	return archive.LookFor(archiveWants(downloads))
}

// ArchiveHolds is whether the download archive's stored listing has a
// candidate for every one of downloads, under any name, so the download
// service copies them from the archive. It neither walks nor reads the
// folder; none is false.
func ArchiveHolds(downloads []PinnedDownload) bool {
	useArchiveState()
	return archive.Holds(archiveWants(downloads))
}

// reviewedModelPlans are the model plans Setup offers: the coding model and
// every engine's picture models.
func reviewedModelPlans() []ModelPlan {
	plans := []ModelPlan{CodingModelPlan()}
	for _, recipe := range starterPictureRecipes {
		for _, offer := range PictureModelOffers(recipe.engine) {
			plans = append(plans, offer.Plan)
		}
	}
	plans = append(plans, PictureModelChoices()...)
	seen := map[string]bool{}
	return slices.DeleteFunc(plans, func(plan ModelPlan) bool {
		if seen[plan.ID] {
			return true
		}
		seen[plan.ID] = true
		return false
	})
}

// ArchiveState is the Settings row's view of the archive, from the stored
// setting and listing; reading it touches no folder.
type ArchiveState struct {
	Keep bool
	// Listed is whether a stored listing describes the folder.
	Listed     bool
	WalkedAt   time.Time
	Incomplete bool
	Limit      string
	// Found counts the reviewed model plans whose every file has a listed
	// candidate; Other counts listed .gguf and .safetensors files that
	// match no pin.
	Found, Other int
	Looking      bool
	// Unanswered is whether this process's last walk of the folder found it
	// silent.
	Unanswered bool
}

// ArchiveStatus is the archive's state for the Settings row.
func ArchiveStatus() ArchiveState {
	useArchiveState()
	state := ArchiveState{Keep: archive.KeepCopies()}
	if archive.Folder() == "" {
		return state
	}
	state.Looking, state.Unanswered = archive.Walking()
	index, ok := archive.ReadListing()
	if !ok {
		return state
	}
	state.Listed, state.WalkedAt, state.Incomplete, state.Limit = true, index.WalkedAt, index.Incomplete, index.Limit
	matched := map[string]bool{}
	for _, plan := range reviewedModelPlans() {
		held := len(plan.Assets) > 0
		for _, asset := range plan.Assets {
			listed := index.Candidates(archive.Want{SHA256: asset.Download.SHA256, Bytes: asset.Download.Bytes})
			for _, file := range listed {
				matched[file.Path] = true
			}
			held = held && len(listed) > 0
		}
		if held {
			state.Found++
		}
	}
	for _, file := range index.Files {
		ext := strings.ToLower(path.Ext(file.Path))
		if (ext == ".gguf" || ext == ".safetensors") && !matched[file.Path] {
			state.Other++
		}
	}
	return state
}

// RemoveArchiveSpool removes the local copies earlier builds made from the
// archive in Setup's downloads\archive folder; the download service copies
// archive files into its own work now. The Panel calls it when it starts.
func RemoveArchiveSpool() {
	home, err := CurrentHome()
	if err != nil {
		return
	}
	spool := filepath.Join(home.Config, "downloads", "archive")
	entries, err := os.ReadDir(spool)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			os.Remove(filepath.Join(spool, entry.Name()))
		}
	}
	os.Remove(spool)
}

// ArchiveUsage is the archive's size from the stored listing and the bytes
// the download service wrote there for each app, for the Panel's storage
// page. Reading it touches no folder.
func ArchiveUsage() (listed int64, written map[string]int64) {
	useArchiveState()
	return archive.Usage()
}
