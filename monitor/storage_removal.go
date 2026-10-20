package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/openabstractions/abstraction-identity/listen"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	contentdigest "github.com/openabstractions/abstraction-storage/go/digest"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

type storageRemovalRow struct {
	Name               string `json:"name"`
	Bytes              int64  `json:"bytes"`
	Target             string `json:"target,omitempty"`
	Ready              bool   `json:"ready"`
	NeedsAdministrator bool   `json:"needs_administrator,omitempty"`
}

const removalRowLimit = 6
const nativeRemovalStore = "runtime-content"

var storageRemovalInventoryTransport = func(ctx context.Context) (listen.FrameClient, error) {
	return panelMachine().ResolveStorageRemovalInventory(ctx)
}
var storageRemovalTransport = func(ctx context.Context) (listen.FrameClient, error) {
	return panelMachine().ResolveStorageRemover(ctx)
}

func readStorageRemovalRows(ctx context.Context, cursor string) ([]storageRemovalRow, string) {
	transport, err := storageRemovalInventoryTransport(ctx)
	if err != nil {
		return nil, ""
	}
	catalogue := content.NewInventorySourceClient(transport.WithContext(ctx))
	page, err := catalogue.Snapshot(cursor, removalRowLimit)
	if err != nil || page.Outcome != content.SourcePageOutcomePage || (!page.Complete && page.Continuation == "") ||
		len(page.Stores) != 1 || page.Stores[0].Name != nativeRemovalStore {
		return nil, ""
	}
	var rows []storageRemovalRow
	for _, item := range page.Objects {
		object := item.Object
		if item.Store != nativeRemovalStore || object.Locator != object.Digest || !contentdigest.Valid(object.Digest) ||
			object.Partial || object.Size < 0 {
			continue
		}
		row := storageRemovalRow{Name: object.Digest, Bytes: object.Size}
		eligible, err := catalogue.Verify(object.Digest)
		if err == nil && len(eligible.Stores) == 1 && eligible.Stores[0].Name == item.Store {
			row.NeedsAdministrator = eligible.Outcome == content.SourcePageOutcomeForbidden &&
				eligible.Stores[0].Rule == storageservice.RemovalNeedsAdministrator
			if eligible.Outcome == content.SourcePageOutcomePage && eligible.Complete && len(eligible.Objects) == 1 {
				picked := eligible.Objects[0]
				digest, ok := storageservice.RemovalTicketDigest(picked.Object.Locator)
				if ok && digest == object.Digest && picked.Store == item.Store && picked.Object.Digest == object.Digest &&
					picked.Object.Size == object.Size && picked.Object.Modified == object.Modified {
					row.Ready, row.Target = true, picked.Object.Locator
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, page.Continuation
}

func (p *servicePanel) storageRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	// Destructive calls require the in-window header rather than a key in
	// the URL, which can be sent by a navigation or form.
	if r.Header.Get("X-Panel-Key") == "" {
		http.Error(w, "panel key header required", http.StatusForbidden)
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		http.Error(w, "one committed content selection is required", http.StatusBadRequest)
		return
	}
	if _, ok := storageservice.RemovalTicketDigest(body.Target); !ok {
		http.Error(w, "one committed content selection is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	transport, err := storageRemovalTransport(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	out, err := content.NewContentRemoverClient(transport.WithContext(ctx)).Remove(body.Target)
	if err != nil {
		panelError(w, err)
		return
	}
	var holders []string
	for _, holder := range out.Holders {
		if holder.Program != "" {
			holders = append(holders, holder.Program)
		}
	}
	panelJSON(w, struct {
		Outcome string   `json:"outcome"`
		Holders []string `json:"holders,omitempty"`
	}{out.Outcome.String(), holders})
}
