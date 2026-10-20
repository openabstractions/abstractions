package main

import (
	"reflect"
	"testing"
)

// The Context chooser offers Automatic plus only the sizes that fit this
// machine, each with the conversations it keeps (decision D, 2026-11-08).
func TestLibraryContextOffersOnlySizesThatFit(t *testing.T) {
	// A 128 GB gfx1151 machine whose card is carved from memory.
	large := planLibraryContext("qwen3.6-35b-a3b", libraryPool([]cardResourceState{{Resource: "card:0"}, {Resource: "memory", Capacity: 128 << 30}}))
	if large == nil || large.Automatic != (libraryContextOption{Context: 262144, Slots: 2}) {
		t.Fatalf("large machine chooser %+v", large)
	}
	want := []libraryContextOption{{32768, 2}, {65536, 2}, {131072, 2}, {262144, 2}}
	if !reflect.DeepEqual(large.Sizes, want) {
		t.Fatalf("large machine sizes %+v", large.Sizes)
	}
	// 36 GiB: 262144 fits in one slot only, and 131072 keeps two.
	small := planLibraryContext("qwen3.6-35b-a3b", libraryPool([]cardResourceState{{Resource: "card:0", Capacity: 36 << 30}, {Resource: "memory", Capacity: 128 << 30}}))
	want = []libraryContextOption{{32768, 2}, {65536, 2}, {131072, 2}, {262144, 1}}
	if !reflect.DeepEqual(small.Sizes, want) || small.Automatic != (libraryContextOption{262144, 1}) {
		t.Fatalf("36 GiB chooser %+v", small)
	}
	// Unknown memory: Automatic alone, the measured profile.
	unknown := planLibraryContext("qwen3.6-35b-a3b", libraryPool(nil))
	if len(unknown.Sizes) != 0 || unknown.Automatic != (libraryContextOption{32768, 2}) {
		t.Fatalf("unknown-memory chooser %+v", unknown)
	}
	if planLibraryContext("no-such-family", 128<<30) != nil {
		t.Fatal("an uncurated family got a chooser")
	}
}

func TestLibraryContextOnlyForHostedModels(t *testing.T) {
	models := []libraryModel{
		{Key: "qwen3.6-35b-a3b", Objects: []libraryObject{{Store: "oa-chat", ID: "w", Hosted: true}}},
		{Key: "qwen3.6-35b-a3b", Objects: []libraryObject{{Store: "lmstudio", ID: "w"}}},
	}
	attachLibraryContexts(models, []cardResourceState{{Resource: "memory", Capacity: 128 << 30}})
	if models[0].Context == nil || models[1].Context != nil {
		t.Fatalf("contexts %+v / %+v", models[0].Context, models[1].Context)
	}
}
