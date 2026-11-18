package main

import (
	"context"

	watch "github.com/openabstractions/abstraction-watch/go"
)

// Rights and directory edits wake the existing gate after publication. Each
// wait uses its own current authority and storage checks before answering.
func (h *applicationsHost) followMessageChanges(ctx context.Context) func() {
	d, store := h.directory, h.messages
	recheck := watch.NewRecheck(ctx, 0, func(context.Context) { store.advance() })
	d.mu.Lock()
	d.messageRecheck = recheck
	d.unlock()
	remove := h.messageRights.policy.OnChange(recheck.Request)
	return func() {
		remove()
		d.mu.Lock()
		d.messageRecheck = nil
		d.unlock()
		recheck.Stop()
	}
}
