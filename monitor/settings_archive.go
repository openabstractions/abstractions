package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/openabstractions/abstraction-download/go/archive"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// settingsArchiveReply is the Download archive row's state, all read from
// Setup's stored setting and listing: the folder the person set ("" for Not
// set), whether it is a network folder, Keep a copy of new downloads here,
// and the listing's counts. Reachable is filled only by a probe (?probe=1),
// which waits at most the archive's probe bound; the page asks for it after
// it shows the stored state.
type settingsArchiveReply struct {
	Folder    string `json:"folder"`
	Network   bool   `json:"network"`
	Reachable *bool  `json:"reachable,omitempty"`
	Keep      bool   `json:"keep"`
	// Found and Other are the listing's models Setup can use and model
	// files it cannot; WalkedAt is empty before the first listing.
	Found      int    `json:"found"`
	Other      int    `json:"other"`
	WalkedAt   string `json:"walked_at,omitempty"`
	Incomplete bool   `json:"incomplete"`
	// Stopped names the bound an incomplete listing reached ("files" for
	// the 50,000-entry bound).
	Stopped string `json:"stopped,omitempty"`
	Looking bool   `json:"looking"`
	// Unanswered is whether the last look in this Panel run found the
	// folder silent.
	Unanswered bool `json:"unanswered,omitempty"`
}

func settingsArchiveState(probe bool) settingsArchiveReply {
	folder := setup.ArchiveFolder()
	state := setup.ArchiveStatus()
	reply := settingsArchiveReply{Folder: folder, Keep: state.Keep}
	if folder == "" {
		return reply
	}
	reply.Network = archive.IsNetwork(folder)
	reply.Found, reply.Other, reply.Incomplete, reply.Looking, reply.Unanswered = state.Found, state.Other, state.Incomplete, state.Looking, state.Unanswered
	if state.Listed {
		reply.WalkedAt = state.WalkedAt.UTC().Format(time.RFC3339)
		reply.Stopped = state.Limit
	}
	if probe {
		reachable := archive.Reachable(folder)
		reply.Reachable = &reachable
	}
	return reply
}

// settingsArchive reads (GET, ?probe=1 adds reachability) or changes the
// download archive Setup checks before a pinned download and fills after it
// (monitor/internal/setup/download_archive.go). POST takes one of
// {"folder": ...} ("" to stop; a new folder is listed in the background),
// {"look": true} to list the folder again, or {"keep": <bool>}.
func (p *servicePanel) settingsArchive(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		panelJSON(w, settingsArchiveState(r.URL.Query().Get("probe") == "1"))
	case http.MethodPost:
		var request struct {
			Folder *string `json:"folder"`
			Look   bool    `json:"look"`
			Keep   *bool   `json:"keep"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request); err != nil || request.Folder == nil && !request.Look && request.Keep == nil {
			http.Error(w, "a folder, look or keep is required", http.StatusBadRequest)
			return
		}
		ctx, cancel := panelCall(r)
		defer cancel()
		if request.Folder != nil {
			before := setup.ArchiveFolder()
			if err := setup.SetArchiveFolder(ctx, *request.Folder); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if after := setup.ArchiveFolder(); after != "" && after != before {
				setup.ArchiveLook()
			}
		}
		if request.Keep != nil {
			if err := setup.SetArchiveKeep(ctx, *request.Keep); err != nil {
				panelError(w, err)
				return
			}
		}
		if request.Look {
			setup.ArchiveLook()
		}
		panelJSON(w, settingsArchiveState(false))
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}
