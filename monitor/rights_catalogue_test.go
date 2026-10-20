package main

import (
	"testing"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	credentialswire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	inferencewire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	jobwire "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// servePackageActions are the rights actions serve/runtime_*.go registers
// into o.RightsActions that name their own Go constant only in serve, a
// main package the monitor module cannot import (task 2026-09-23, sixth
// first-time visitor, finding 2). Kept in sync by hand with rightsActionPlain
// for the same reason those two facade actions already were:
//   - applications.go: ActionApplicationManage, ActionApplicationRead,
//     ActionApplicationAnnounce, ActionApplicationActivate
//   - registry.go: ActionProviderManage
//   - provider_inventory.go: ActionInventoryProvide
//   - runtime_inventory.go: ActionInventoryRead
//   - runtime_lending.go: ActionLend
//
// abstraction.credentials/apply is not listed here: abstraction-credentials's
// own ResourceActions (credentialswire) already names it, even though serve
// also holds its own copy of the string (credentials.go's setApplyRule).
var servePackageActions = []string{
	"abstraction.facade/application.manage",
	"abstraction.facade/application.read",
	"abstraction.facade/application.announce",
	"abstraction.facade/application.activate",
	// applications.go's ActionApplicationRegisterSelf (rights RIGHTS-R22).
	"abstraction.facade/application.register_self",
	"abstraction.facade/provider.manage",
	"abstraction.storage/inventory.provide",
	"abstraction.storage/inventory.read",
	"abstraction.storage/lend",
}

// rightsCatalogueActions is every action id this runtime can list, gathered
// from the two sources task 2026-09-23's sixth first-time visitor, finding 2
// names: the generic catalogue abstraction-rights/go declares
// (rightswire.ResourceActions, the same list composeRights loads the policy
// with in serve/runtime_rights.go), each capability's own ResourceActions
// wire list, and servePackageActions above for what only serve itself names.
func rightsCatalogueActions() []string {
	var all []string
	all = append(all, rightswire.ResourceActions...)
	all = append(all, inferencewire.ResourceActions...)
	all = append(all, credentialswire.ResourceActions...)
	all = append(all, askswire.ResourceActions...)
	all = append(all, jobwire.ResourceActions...)
	all = append(all, servePackageActions...)
	return all
}

// TestRightsActionPlainCoversCatalogue is task 2026-09-23's sixth
// first-time visitor, finding 2: the Questions and rights page's action
// dropdown and its "Actions with no rule names yet" list mixed plain names
// with raw ids for actions the Panel's own pages never happen to emit
// themselves, but that this runtime can still list, e.g.
// abstraction.storage/content.observe. rightsActionPlain (rights_panel.go)
// must name every action in the runtime's actual catalogue, not only the
// ones a Panel page's own markup happens to mention; a new action added to
// any wire package's ResourceActions, or to servePackageActions above, that
// has no rightsActionPlain entry fails this test.
func TestRightsActionPlainCoversCatalogue(t *testing.T) {
	seen := map[string]bool{}
	for _, action := range rightsCatalogueActions() {
		if seen[action] {
			continue
		}
		seen[action] = true
		if rightsActionPlain[action] == "" {
			t.Errorf("action %q is in the runtime's catalogue with no rightsActionPlain entry", action)
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no actions in the runtime's catalogue; a wire package's ResourceActions may have moved")
	}
}
