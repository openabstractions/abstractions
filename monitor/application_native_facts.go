package main

import registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"

// A program record may carry several application facets. Presence stays with
// that facet's observed reading; a missing reading establishes no ownership.
func nativeApplicationFacet(row *registrationwire.Registration, name string) (*registrationwire.Manifest, *registrationwire.ApplicationReading, bool) {
	var manifest *registrationwire.Manifest
	for i := range row.Spec.Applications {
		facet := &row.Spec.Applications[i]
		if facet.Name != name {
			continue
		}
		if manifest != nil || facet.Manifest.Name != name {
			return nil, nil, false
		}
		manifest = &facet.Manifest
	}
	if manifest == nil {
		return nil, nil, false
	}
	var reading *registrationwire.ApplicationReading
	for i := range row.Applications {
		if row.Applications[i].Name != name {
			continue
		}
		if reading != nil {
			return nil, nil, false
		}
		reading = &row.Applications[i]
	}
	return manifest, reading, true
}
