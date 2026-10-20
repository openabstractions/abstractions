package main

import (
	"encoding/json"
	"testing"
)

// discoveredSessionFixture states a faked discovery row the way the
// launcher refresh does: s passes through discoveredSessions, which reads its
// folder's Project and CwdKey. A fixture without both fails, so a project
// scope check cannot pass on two empty projects.
func discoveredSessionFixture(t *testing.T, s applicationAttachSession) applicationAttachSession {
	t.Helper()
	entry := map[string]any{"chat": s.Chat, "cwd": s.Cwd, "title": s.Title, "updated": s.Updated, "origin": s.Origin, "mode": s.Mode}
	raw, err := json.Marshal(map[string]any{"sessions": []any{entry}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := discoveredSessions(raw, func(string) string { return s.Context })
	if err != nil || len(got) != 1 || got[0].Project == "" || got[0].CwdKey == "" {
		t.Fatalf("discovered session %q in %q has no project or folder identity: %+v %v", s.Chat, s.Cwd, got, err)
	}
	return got[0]
}
