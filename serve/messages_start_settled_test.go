package main

import "context"

// progressMessageStartSettled is one progress pass followed by the claimed
// start task it handed off, for tests that observe the start's outcome.
// Production passes return at the claim (architect review 43).
func (h *applicationsHost) progressMessageStartSettled(ctx context.Context, key string, r *messageRecord) error {
	err := h.progressMessageStart(ctx, key, r)
	h.messageStarts.wait()
	return err
}
