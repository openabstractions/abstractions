package main

import registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"

// Inert received facts for consumer tests, never native image evidence.
func nativeRegistrationFixture(manifest registrationwire.Manifest, program, revision, digest string, owned []string) registrationwire.Registration {
	return registrationwire.Registration{
		Key: "path:" + program, Revision: revision, Epoch: "fixture-epoch",
		Spec:   registrationwire.RegistrationSpec{Program: program, Applications: []registrationwire.ApplicationFacet{{Name: manifest.Name, Manifest: manifest}}},
		Source: registrationwire.SourceOperator, State: registrationwire.StateEnabled,
		IdentityEvidence: registrationwire.IdentityEvidenceNone,
		Applications:     []registrationwire.ApplicationReading{{Name: manifest.Name, ManifestDigest: digest, OwnedInstances: owned}},
		Accepted:         "2000-01-01T00:00:00.000000Z", Verification: registrationwire.VerificationUnverified,
		VerificationReason: "identity:unverified", Verified: "2000-01-01T00:00:00.000000Z",
	}
}
