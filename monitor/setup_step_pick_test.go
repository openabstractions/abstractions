package main

import (
	"testing"

	model "github.com/openabstractions/abstraction-model/go/client"
)

// Review 25: the step reads Fit through the one pick rule. A paired
// runtime serving the entry ranked first is no pick and no need; the pick
// is the first entry this computer fits.
func TestSetupStepPickSkipsAPairedEntryRankedFirst(t *testing.T) {
	fit, err := stepFitOf(model.FitResult{Outcome: model.FitOutcomeFitted, PoolBytes: 30e9, Entries: []model.FitEntry{
		{Title: "Paired", Family: "big", Fit: model.FitNo, Availability: model.AvailabilityServed, Machine: "studio", NeedBytes: 1},
		{Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", Fit: model.FitFits, Availability: model.AvailabilityDownload, NeedBytes: 25e9, Digest: "sha256:aa"},
	}})
	if err != nil || fit.Pick == nil || fit.Pick.Family != "qwen3.6-35b-a3b" || fit.Pick.Machine != "" || fit.SmallestTitle != "Qwen3.6 35B" || fit.SmallestNeed != 25e9 {
		t.Fatalf("fit = %+v, %v", fit, err)
	}
}
