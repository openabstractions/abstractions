package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

func TestOwnedApplicationImageRequiresLiveBoundTrustedHold(t *testing.T) {
	checked := 0
	image := ownedApplicationImage{program: filepath.Join(t.TempDir(), "fixture.exe"), code: identity.Code{Trusted: true}, proof: identity.ProofBound, recheck: func(context.Context) error { checked++; return nil }, close: func() error { return nil }}
	if err := image.admit(context.Background()); err != nil || checked != 1 {
		t.Fatalf("inert hold %v checks%d", err, checked)
	}
	for _, kind := range []string{"untrusted", "unbound", "interpreter", "named-interpreter", "signed-interpreter-name", "relative", "missing-recheck", "missing-close", "retired"} {
		t.Run(kind, func(t *testing.T) {
			candidate := image
			switch kind {
			case "untrusted":
				candidate.code.Trusted = false
			case "unbound":
				candidate.proof = identity.ProofUnmet
			case "interpreter":
				candidate.code.SharedInterpreter = true
			case "named-interpreter":
				candidate.program = filepath.Join(filepath.Dir(image.program), "python.exe")
			case "signed-interpreter-name":
				candidate.code.OriginalName = "python.exe"
			case "relative":
				candidate.program = "fixture.exe"
			case "missing-recheck":
				candidate.recheck = nil
			case "missing-close":
				candidate.close = nil
			case "retired":
				candidate.recheck = func(context.Context) error { return errors.New("hold closed") }
			}
			if err := candidate.admit(context.Background()); err == nil {
				t.Fatal("invalid hold admitted")
			}
		})
	}
}
