package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

// waitingRegistry applies every typed write and keeps it for the person's
// Keep, as the runtime does for a changed model host registration.
type waitingRegistry struct{ written int }

func (r *waitingRegistry) DeclarePinned(context.Context, string, wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	return wire.DeclarationChange{}, nil
}
func (r *waitingRegistry) ReplacePinned(context.Context, string, wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	return wire.DeclarationChange{}, nil
}
func (r *waitingRegistry) Withdraw(context.Context, string, string) (wire.DeclarationChange, error) {
	return wire.DeclarationChange{}, nil
}
func (r *waitingRegistry) WriteConfigured(context.Context, registration.RegistrationSpec, bool) (wire.DeclarationChange, error) {
	r.written++
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: "after", Reason: approvalPendingReason}, nil
}

// `models host --video-model ltx-2.5` whose change waits for the person's
// Keep says LTX-2.5 is set up and starts once allowed; it never says
// serving (owner's R31 PC, 2026-11-12).
func TestModelsHostSaysAWaitingChangeWaits(t *testing.T) {
	w := newWaiting(30 * time.Second)
	defer w.stop()
	dir := t.TempDir()
	program := filepath.Join(dir, "modelhostd.exe")
	engine := filepath.Join(dir, "sd-server.exe")
	for _, p := range []string{program, engine} {
		if err := os.WriteFile(p, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	settings := hostSettings{program: program, endpoint: "modelhost-video", resource: "leases",
		media: hostMedia{videoModel: "ltx-2.5", videoKind: videoKindSdCpp, videoEngine: engine}}
	registry := &waitingRegistry{}
	var out bytes.Buffer
	if err := declareHost(w, registry, wire.DeclarationList{Revision: "before"}, settings, nil, "models host", &out, false, settings.media.describe()); err != nil {
		t.Fatal(err)
	}
	said := out.String()
	if registry.written == 0 || !strings.Contains(said, "LTX-2.5 is set up. It starts once you allow the model host change in the OpenAbstractions question.") ||
		strings.Contains(said, "serving") {
		t.Fatalf("said %q", said)
	}
}
