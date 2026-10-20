package main

import (
	"slices"
	"testing"

	router "github.com/openabstractions/abstraction-router/go"
	storage "github.com/openabstractions/abstraction-storage/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

func heldNames(held []router.HeldObject) []string {
	var out []string
	for _, o := range held {
		out = append(out, o.Store+":"+o.Descriptor["family"])
	}
	slices.Sort(out)
	return out
}

func unreadStore(name, detail string) content.Store {
	return content.Store{Name: name, Present: true, Placement: content.PlacementLocal,
		Errors: []content.StoreError{{Kind: content.StoreErrorKindOther, Detail: detail}}}
}

// A store whose source did not answer keeps what the router was last told
// of it, and a store answered from an earlier observation keeps that
// observation's models; both read as incomplete to the model host's
// reconciler, which keeps their entries. A store read now is as read.
func TestAnUnreadStoreKeepsWhatWasLastRead(t *testing.T) {
	last := []router.HeldObject{
		{Store: "huggingface", Names: []string{"test/Seed-GGUF:Seed-1B-Q4_K_M.gguf"}, Descriptor: map[string]string{"family": "seed-1b"}},
		{Store: "lmstudio", Names: []string{"test/Old-GGUF/Old-Q4_K_M.gguf"}, Descriptor: map[string]string{"family": "old"}},
	}
	composed := storageservice.ComposedInventory{
		Incomplete: []string{"inventoryd"},
		Stores: []content.Store{
			unreadStore("huggingface", storage.UnansweredSourceError),
			{Name: "lmstudio", Present: true, Placement: content.PlacementLocal},
			unreadStore("jan", storage.StaleObservationError+"2026-11-01T14:00:00Z"),
		},
		Manifests: []content.ManifestHolders{
			heldGGUF("lmstudio", "lms-tiny", "sha256:aa", map[string]string{"family": "tiny-135m"}),
			heldGGUF("jan", "jan-fresh", "sha256:bb", map[string]string{"family": "fresh-0.5b"}),
		},
	}
	missed := map[string]int{}
	held, current, gone := boundUnread(missed, last, composed)
	if want := []string{"huggingface:seed-1b", "jan:fresh-0.5b", "lmstudio:tiny-135m"}; !slices.Equal(heldNames(held), want) {
		t.Fatalf("held %v, want %v", heldNames(held), want)
	}
	if len(gone) != 0 || missed["huggingface"] != 1 || missed["jan"] != 1 || missed["lmstudio"] != 0 {
		t.Fatalf("gone %v, missed %v", gone, missed)
	}
	complete, _ := discoveredModels(current)
	if complete["huggingface"] || complete["jan"] || !complete["lmstudio"] {
		t.Fatalf("complete stores %v, want lmstudio alone", complete)
	}
}

// A store unread for missedLimit refreshes in a row counts as read and empty:
// the router is told nothing of it and the reconciler drops its entries. A
// store read again in between starts its count over.
func TestAStoreUnreadPastTheBoundCountsAsGone(t *testing.T) {
	last := []router.HeldObject{
		{Store: "huggingface", Names: []string{"test/Seed-GGUF:Seed-1B-Q4_K_M.gguf"}, Descriptor: map[string]string{"family": "seed-1b"}},
	}
	unanswered := storageservice.ComposedInventory{Incomplete: []string{"inventoryd"},
		Stores: []content.Store{unreadStore("huggingface", storage.UnansweredSourceError)}}
	stale := storageservice.ComposedInventory{
		Stores:    []content.Store{unreadStore("huggingface", storage.StaleObservationError+"2026-11-01T14:00:00Z")},
		Manifests: []content.ManifestHolders{heldGGUF("huggingface", "sha256:aa", "sha256:aa", map[string]string{"family": "seed-1b"})},
	}
	read := storageservice.ComposedInventory{Stores: []content.Store{{Name: "huggingface", Present: true, Placement: content.PlacementLocal}},
		Manifests: stale.Manifests}
	missed := map[string]int{}
	held := last
	// Unanswered and stale refreshes alternate: both count.
	for i := 1; i < missedLimit; i++ {
		composed := unanswered
		if i%2 == 0 {
			composed = stale
		}
		var gone []string
		held, _, gone = boundUnread(missed, held, composed)
		if len(gone) != 0 || len(held) != 1 {
			t.Fatalf("refresh %d: gone %v, held %v", i, gone, heldNames(held))
		}
	}
	held, current, gone := boundUnread(missed, held, unanswered)
	if !slices.Equal(gone, []string{"huggingface"}) || len(held) != 0 {
		t.Fatalf("after %d unread refreshes: gone %v, held %v", missedLimit, gone, heldNames(held))
	}
	complete, found := discoveredModels(current)
	if !complete["huggingface"] || len(found) != 0 {
		t.Fatalf("a store past the bound reads complete %v with %v, want complete and empty", complete, found)
	}
	entries := []hostedModel{{Name: "seed-1b", Store: "huggingface", Object: "sha256:aa"}}
	if next := reconcileHosted(entries, complete, found, nil, listedObjects(current)); len(next) != 0 {
		t.Fatalf("the reconciler kept %v from a store past the bound", names(next))
	}

	// Read again, the store is as read and its count starts over.
	held, current, gone = boundUnread(missed, held, read)
	if len(gone) != 0 || missed["huggingface"] != 0 || !slices.Equal(heldNames(held), []string{"huggingface:seed-1b"}) {
		t.Fatalf("read again: gone %v, missed %v, held %v", gone, missed, heldNames(held))
	}
	if complete, found := discoveredModels(current); !complete["huggingface"] || len(found) != 1 {
		t.Fatalf("read again: complete %v, found %v", complete, found)
	}
}
