package setup

import (
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-model/go/releasefiles"
)

// The runtime lets operators read exactly releasefiles.ModelDigests through
// the storage inventory; Setup looks for a copy of each model file its plans
// name. A plan file missing there would be hidden from Setup's lookup on
// every installed machine, and a release file no plan uses would grant a
// read Setup never needs.
func TestEveryPlanModelDigestIsAReleaseFile(t *testing.T) {
	if got, want := modelDigests(), releasefiles.ModelDigests(); !slices.Equal(got, want) {
		t.Fatalf("Setup's model digests %q differ from the release model files %q", got, want)
	}
}
