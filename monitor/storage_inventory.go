package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"

	facade "github.com/openabstractions/abstraction-facade/go"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

var storageReadLogUsage = func(ctx context.Context) (logwire.HistoryUsageResult, error) {
	reader, err := panelMachine().ResolveLogReader(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return logwire.HistoryUsageResult{}, err
	}
	return reader.UsageContext(ctx)
}

func (p *servicePanel) storageLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	usage, err := storageReadLogUsage(ctx)
	if err != nil || usage.Outcome != logwire.HistoryUsageOutcomeAvailable {
		log.Print("panel: storage log usage unavailable")
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, string(logwire.EncodeHistoryUsageResult(&usage)))
}

type storageInventory interface {
	List(context.Context, string, int64) (content.InventoryPage, error)
	Unheld(context.Context, string, int64) (content.InventoryPage, error)
}

var storageResolveInventory = func(ctx context.Context) (storageInventory, error) {
	return panelMachine().ResolveStorageInventory(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// storageHoldClass implements STO-T8 for the holds the service actually reports.
// A missing hold is classified only after Unheld establishes the grace proof.
func storageHoldClass(holds []content.Hold) string {
	class := ""
	for _, hold := range holds {
		next := "data"
		if hold.Purpose == "cache" {
			next = "cache"
		} else if hold.Lifetime == content.LifetimeLease {
			next = "tmp"
		}
		if storageClassStrength(next) > storageClassStrength(class) {
			class = next
		}
	}
	return class
}

func storageClassStrength(class string) int {
	switch class {
	case "data":
		return 3
	case "tmp":
		return 2
	case "cache":
		return 1
	}
	return 0
}

type storageContentBytes struct {
	Class string `json:"class"`
	Bytes int64  `json:"bytes"`
}

type storageInventoryView struct {
	Classes []storageContentBytes `json:"classes"`
	Disk    *setupDisk            `json:"disk,omitempty"`
	// Unclassified is visible content for which this composition cannot prove
	// a hold class. It is never silently counted as an unheld cleanup candidate.
	Unclassified  int64                  `json:"unclassified"`
	Large         []storageInventoryItem `json:"large"`
	Matches       []storageInventoryItem `json:"matches"`
	Committed     []storageRemovalRow    `json:"committed,omitempty"`
	CommittedNext string                 `json:"committed_next,omitempty"`
}

type storageInventoryItem struct {
	Name      string   `json:"name"`
	Store     string   `json:"store"`
	Bytes     int64    `json:"bytes"`
	Locations int      `json:"locations,omitempty"`
	Places    []string `json:"places,omitempty"`
	ListedBy  []string `json:"listed_by,omitempty"`
}

type storageCountedObject struct {
	store, name, digest string
	listedBy            []string
	class               string
	bytes               int64
}

func (p *servicePanel) storageInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	inventory, err := storageResolveInventory(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	view, err := readStorageInventory(ctx, inventory)
	if err != nil {
		panelError(w, err)
		return
	}
	if volume, free, err := setupInstallDisk(); err == nil && len(volume) == 2 && volume[1] == ':' && (volume[0] >= 'A' && volume[0] <= 'Z' || volume[0] >= 'a' && volume[0] <= 'z') {
		view.Disk = &setupDisk{Volume: volume, FreeBytes: free}
	}
	cursor := r.URL.Query().Get("committed")
	if len(cursor) > 160 {
		http.Error(w, "invalid Storage continuation", http.StatusBadRequest)
		return
	}
	view.Committed, view.CommittedNext = readStorageRemovalRows(ctx, cursor)
	panelJSON(w, view)
}

// readStorageInventory counts each reported object once per store. Shared
// manifest entries use their strongest hold, and inventories remain filtered
// by the receiving service's existing content-read decisions.
func readStorageInventory(ctx context.Context, inventory storageInventory) (storageInventoryView, error) {
	objects := map[string]storageCountedObject{}
	add := func(store, digest, locator, class, name string, bytes int64, verified bool, holds []content.Hold) error {
		if bytes < 0 {
			return errors.New("storage inventory reported a negative size")
		}
		keyPart := "digest:" + digest
		if locator != "" {
			keyPart = "locator:" + locator
		} else if digest == "" {
			return errors.New("storage inventory object has no locator or digest")
		}
		key := store + "\x00" + keyPart
		old, exists := objects[key]
		if exists && old.bytes != bytes {
			return errors.New("storage inventory reported conflicting object sizes")
		}
		if storageClassStrength(old.class) > storageClassStrength(class) {
			class = old.class
		}
		if old.name != "" && (name == "" || old.name < name) {
			name = old.name
		}
		if !verified || locator == "" {
			digest = ""
		}
		if old.digest != "" {
			if digest != "" && old.digest != digest {
				return errors.New("storage inventory reported conflicting verified digests")
			}
			digest = old.digest
		}
		listedBy := old.listedBy
		for _, hold := range holds {
			name := hold.Holder.Program
			if hold.Basis != nil && name != "" && (hold.Attestation == content.AttestationObserved || hold.Attestation == content.AttestationVerified) && !slices.Contains(listedBy, name) {
				listedBy = append(listedBy, name)
			}
		}
		slices.Sort(listedBy)
		objects[key] = storageCountedObject{store: store, name: name, digest: digest, class: class, bytes: bytes, listedBy: listedBy}
		return nil
	}
	continuation := ""
	seen := map[string]bool{}
	for {
		page, err := inventory.List(ctx, continuation, 256)
		if err != nil {
			return storageInventoryView{}, err
		}
		if page.Outcome != content.InventoryOutcomePage {
			return storageInventoryView{}, fmt.Errorf("storage inventory %s", page.Outcome)
		}
		for _, item := range page.Manifests {
			class := storageHoldClass(item.Holds)
			name := ""
			if len(item.Manifest.Names) > 0 {
				name = item.Manifest.Names[0].Name
			}
			for _, entry := range item.Manifest.Entries {
				if entry.Manifest != "" {
					continue // an index reference is not another stored byte copy
				}
				if err := add(item.Manifest.Store, entry.Digest, entry.Locator, class, name, entry.Size, entry.Evidence == content.EvidenceHashed, item.Holds); err != nil {
					return storageInventoryView{}, err
				}
			}
		}
		for _, item := range page.Objects {
			if err := add(item.Store, item.Object.Digest, item.Object.Locator, storageHoldClass(item.Holds), "", item.Object.Size, !item.Object.Partial && item.Object.Evidence == content.EvidenceHashed, item.Holds); err != nil {
				return storageInventoryView{}, err
			}
		}
		if page.Complete {
			break
		}
		if page.Continuation == "" || seen[page.Continuation] {
			return storageInventoryView{}, errors.New("storage inventory did not advance")
		}
		seen[page.Continuation] = true
		continuation = page.Continuation
	}
	view := storageInventoryView{Classes: []storageContentBytes{{Class: "data"}, {Class: "tmp"}, {Class: "cache"}}}
	type contentMatch struct {
		digest string
		bytes  int64
	}
	matches := map[contentMatch]storageInventoryItem{}
	for _, object := range objects {
		name := object.name
		if name == "" {
			name = "Stored content"
		}
		if object.bytes >= 1_000_000_000 {
			view.Large = append(view.Large, storageInventoryItem{Name: name, Store: object.store, Bytes: object.bytes, ListedBy: object.listedBy})
		}
		if object.digest != "" {
			key := contentMatch{object.digest, object.bytes}
			item := matches[key]
			item.Bytes = object.bytes
			item.Locations++
			if object.store != "" && !slices.Contains(item.Places, object.store) {
				item.Places = append(item.Places, object.store)
			}
			if object.name != "" && (item.Name == "" || object.name < item.Name) {
				item.Name = object.name
			}
			matches[key] = item
		}
		i := slices.IndexFunc(view.Classes, func(row storageContentBytes) bool { return row.Class == object.class })
		total := &view.Unclassified
		if i >= 0 {
			total = &view.Classes[i].Bytes
		}
		if *total > (1<<63-1)-object.bytes {
			return storageInventoryView{}, errors.New("storage inventory total exceeds its range")
		}
		*total += object.bytes
	}
	for _, match := range matches {
		if match.Locations > 1 {
			if match.Name == "" {
				match.Name = "Stored content"
			}
			slices.Sort(match.Places)
			view.Matches = append(view.Matches, match)
		}
	}
	for _, items := range []*[]storageInventoryItem{&view.Large, &view.Matches} {
		slices.SortFunc(*items, func(a, b storageInventoryItem) int {
			if a.Bytes != b.Bytes {
				if a.Bytes > b.Bytes {
					return -1
				}
				return 1
			}
			if result := strings.Compare(a.Name, b.Name); result != 0 {
				return result
			}
			if result := strings.Compare(a.Store, b.Store); result != 0 {
				return result
			}
			return a.Locations - b.Locations
		})

	}
	return view, nil
}
