package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

func (p *servicePanel) activityStorage(w http.ResponseWriter, r *http.Request) {
	var occurrence string
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if r.Header.Get("X-Panel-Key") == "" {
			http.Error(w, "panel key header required", http.StatusForbidden)
			return
		}
		var request struct {
			Occurrence string `json:"occurrence"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) || len(request.Occurrence) != 97 {
			http.Error(w, "one removal occurrence is required", http.StatusBadRequest)
			return
		}
		occurrence = request.Occurrence
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	transport, err := storageRemovalInventoryTransport(ctx)
	if err != nil {
		panelJSON(w, activityPage{Outcome: "unavailable", Events: []activityEvent{}})
		return
	}
	reply, err := storageservice.ArchiveRestore(ctx, transport.WithDefaults(6*time.Second, 16<<10), occurrence)
	if err != nil {
		panelJSON(w, activityPage{Outcome: "unavailable", Events: []activityEvent{}})
		return
	}
	out := activityPage{Outcome: reply.Outcome, Events: []activityEvent{}, Complete: true}
	for _, row := range reply.Entries {
		event := activityEvent{At: row.At, App: "Storage", What: "Removed " + row.Digest, Result: "Removed"}
		switch row.Status {
		case "restoring":
			event.Result = "Restoring from the download archive"
		case "restored":
			event.Result = "Restored from the download archive"
		case "unavailable":
			event.Result = "Archive restore unavailable"
		}
		if row.Available {
			event.Restore = row.Occurrence
		}
		out.Events = append(out.Events, event)
	}
	panelJSON(w, out)
}
