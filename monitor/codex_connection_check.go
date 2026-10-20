package main

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// This uses the UI's read-only selection and bounded inventory path. It never
// calls prepare/enroll/change, and deliberately omits vendor records and paths.
func reportCodexConnectionAvailability(parent context.Context, out io.Writer, read func(context.Context) (codexConnectionView, error)) int {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	view, err := read(ctx)
	switch view.Reason {
	case "", "unsupported_platform", "window_closed", "cli_selection", "installed_gateway", "setup_state", "configuration_check", "command_layout", "command_tmp_root", "command_temp", "command_identity", "command_environment", "command_handles", "command_start", "command_wait", "command_exit", "command_cleanup", "command_virtualized_profile", "command_unknown_profile", "command_profile_folder", "command_sandboxed":
	default:
		view.Reason = "unknown"
	}
	if view.Outcome != "page" && view.Outcome != "unavailable" {
		view.Outcome = "unknown"
	}
	safe := struct {
		Supported bool   `json:"supported"`
		Available bool   `json:"available"`
		Outcome   string `json:"outcome"`
		Reason    string `json:"reason,omitempty"`
	}{view.Supported, view.Available, view.Outcome, view.Reason}
	if encodeErr := json.NewEncoder(out).Encode(safe); encodeErr != nil {
		return 1
	}
	if err != nil || !view.Supported || !view.Available || view.Outcome != "page" {
		return 1
	}
	return 0
}
