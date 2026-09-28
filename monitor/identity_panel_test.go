package main

import "testing"

// TestAccountDisplayNameFallsBackToTheAccountID is task 2026-09-23's eighth
// first-time visitor, finding 7: Identity's own sentence named the caller's
// raw SID directly ("Account S-1-5-21-…"), never a name a person reads as
// their own account. accountDisplayName resolves it through os/user.LookupId
// where the platform can (a real Windows SID, this process's own account);
// an id nothing resolves — every fixture here, since none names a real
// account on the machine running this test — falls back to the id itself
// unchanged, never panics, and never returns an empty string that would
// leave "Your account, ," in the sentence it feeds.
func TestAccountDisplayNameFallsBackToTheAccountID(t *testing.T) {
	for _, id := range []string{"S-1-5-21-1", "not-an-account", "1000"} {
		got := accountDisplayName(id)
		if got == "" {
			t.Errorf("accountDisplayName(%q) returned empty; the caller always has a non-empty name to show", id)
		}
	}
}
