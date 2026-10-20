package main

import (
	"context"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

// One product, one Apps entry (facade one-owner row AD-02a: one custom-node
// pack and one host program). An app's own applications@3 registration, made
// by its native host program, owns its name. A manifest request under that
// name, such as the same app's plug-in pack, is the same app: it is
// connected through that registration, and no Not connected row offers to
// replace it. A manifest never replaces an applications@3 entry in any case;
// that entry changes only through its own revision.

// nativeApplicationNamesRead is nativeApplicationNames; TestMain keeps tests
// away from this account's runtime.
var nativeApplicationNamesRead = nativeApplicationNames

// nativeApplicationNames are the application names an applications@3
// registration holds now.
func nativeApplicationNames(ctx context.Context) (map[string]bool, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	rows, err := selectionRegistrationRows(applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).Observe)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for i := range rows {
		for _, facet := range rows[i].Spec.Applications {
			if _, _, ok := nativeApplicationFacet(&rows[i], facet.Name); ok {
				names[facet.Name] = true
			}
		}
	}
	return names, nil
}

// nativeOwns reports that an applications@3 registration holds name. A read
// that fails owns nothing: the request then waits in Apps as before.
func nativeOwns(ctx context.Context, name string) bool {
	names, err := nativeApplicationNamesRead(ctx)
	return err == nil && names[name]
}
