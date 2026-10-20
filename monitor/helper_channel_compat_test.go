package main

import "testing"

// These successful legacy spellings are part of the current receiving surface.
// A closed generated transport codec cannot replace it without a compatibility
// disposition. This test reads inert bytes and changes no connection or marker.
func TestHelperChannelRetainsAcceptedCaseInsensitiveCodexView(t *testing.T) {
	raw := []byte(`{"Outcome":"page","Scope":"user","Registration":"saved","Name":"OpenAbstractions","Program":"/owned/helper","Owned":true,"Previous":{"Application":"oa-codex-integration","Program":"/owned/prior","File_Key":"native-key","Revision":"revision"}}`)
	view, err := decodeCodexView(raw)
	if err != nil || view.Outcome != "page" || view.Previous == nil || view.Previous.Program != "/owned/prior" || view.Previous.FileKey != "native-key" {
		t.Fatalf("previously accepted helper row changed: %+v, %v", view, err)
	}
}
