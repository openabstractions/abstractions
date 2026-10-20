package main

import (
	"context"
	"io"
	"net/http"
	"strings"

	facade "github.com/openabstractions/abstraction-facade/go"
	resourcewire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	resource "github.com/openabstractions/abstraction-resource/go/client"
)

// cardRoutes serves the card page and its calls. The table read goes through
// the resolved abstraction.resource/table@1, gated by
// abstraction.resource/table.read on account; the runtime narrows a refused
// caller to its own rows rather than refusing the call (CONTRACT.md RES-T4),
// so this handler forwards whatever the client returns unchanged.
func (p *servicePanel) cardRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/card", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("k") != key {
			http.Error(w, "panel key required", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		//unchecked: a failed write means the client is already gone; there is no response left to recover
		_, _ = io.WriteString(w, panelPage(r, key, "card", "Device", cardPage))
	})
	mux.HandleFunc("/card/table", guard(key, p.cardTable))
	mux.HandleFunc("/card/awake", guard(key, p.cardAwake))
	mux.HandleFunc("/card/storage", guard(key, p.storageInventory))
	mux.HandleFunc("/card/storage/logs", guard(key, p.storageLogs))
	mux.HandleFunc("/card/storage/remove", guard(key, p.storageRemove))
	p.deviceRoutes(mux, key)
}

// Native records remain with their generated codec; titles are page composition.
type cardHolder = resource.Holder
type cardResourceState = resource.ResourceState

const cardEvidenceVerified = resourcewire.EvidenceVerified
const cardEvidenceClaimed = resourcewire.EvidenceClaimed

type cardTableView struct {
	Resources []string          `json:"resources"`
	Titles    map[string]string `json:"titles,omitempty"`
}

// cardTable reads every resource the table reports: Resources() names them,
// fresh controls whether Holders re-reads the instrument or stands on its
// last sample within its age bound.
func (p *servicePanel) cardTable(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET required", 405)
		return
	}
	fresh := r.URL.Query().Get("fresh") == "1"
	ctx, cancel := panelCall(r)
	defer cancel()
	resources, err := readCardResources(ctx, fresh)
	if err != nil {
		panelError(w, err)
		return
	}
	view := cardTableView{Resources: []string{}, Titles: p.titleCardHolders(ctx, resources)}
	logForeignNamedByFile(resources, view.Titles)
	for i := range resources {
		view.Resources = append(view.Resources, string(resourcewire.EncodeResourceState(&resources[i])))
	}
	panelJSON(w, view)
}

// readCardResources reads every resource the table reports with its
// holders; the Device page and the Models page's Running now read it.
func readCardResources(ctx context.Context, fresh bool) ([]cardResourceState, error) {
	table, err := panelMachine().ResolveResourceTable(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	list, err := table.ResourcesContext(ctx)
	if err != nil {
		return nil, err
	}
	resources := []cardResourceState{}
	for _, name := range list.Resources {
		state, err := table.HoldersContext(ctx, name, fresh)
		if err != nil {
			return nil, err
		}
		resources = append(resources, state)
	}
	return resources, nil
}

// titleCardHolders names each holder the way the shared program titler
// does (architect ruling 2026-11-09, second block): a registered app by its
// title, a provider's program by its provider title, OpenAbstractions' own
// programs by the product names, and any other program by its signed file
// description. A holder no rule names stays out of the map; the page then
// reads its file stem in words.
func (p *servicePanel) titleCardHolders(ctx context.Context, resources []cardResourceState) map[string]string {
	return titleHolders(resources, p.programTitler(ctx))
}

// titleHolders asks the titler for each holder's name; the titler decides
// whether two spellings are one file (identity.SameSubjectProgram), so a
// junction or an 8.3 alias of the registered program matches, and a twin
// name the volume keeps apart does not.
func titleHolders(resources []cardResourceState, titler *programTitler) map[string]string {
	titles := map[string]string{}
	for i := range resources {
		for j := range resources[i].Holders {
			program := resources[i].Holders[j].Program
			if _, done := titles[program]; done || program == "" {
				continue
			}
			if name := titler.deviceName(program); name != "" {
				titles[program] = name
			}
		}
	}
	return titles
}

// cardAwakeHold is one program's hold of the machine's awake resource, as
// either source reports it. The table's lease rows (CONTRACT.md RES-A1) carry
// program, account, amount if any, since and lease id; the legacy rights
// fallback carries program, since and why, and a lease id once R4b's rights
// service is itself composed with the lease book.
type cardAwakeHold struct {
	Program string `json:"program"`
	Account string `json:"account,omitempty"`
	Amount  int64  `json:"amount,omitempty"`
	Lease   string `json:"lease,omitempty"`
	Since   string `json:"since,omitempty"`
	Why     string `json:"why,omitempty"`
}

// cardAwakeView reports the resource table's awake rows, or why that IPC read
// was unavailable.
type cardAwakeView struct {
	Outcome string          `json:"outcome"` // "page" or "unavailable"
	Source  string          `json:"source,omitempty"`
	Holds   []cardAwakeHold `json:"holds"`
	Reason  string          `json:"reason,omitempty"`
}

// awakeFromTable reads the awake resource through the same client the card
// rows use: a wake hold is a lease of resource awake, a claimed row carrying
// its lease, its grant and why it is held (CONTRACT.md RES-A1). Holders never
// errors on a resource the instrument does not offer directly; an unsupported
// name reads an empty reading, and the table's rows for it are whatever the
// lease book claims (abstraction-resource/go/instrument/instrument.go). An
// empty, error-free result means nobody holds awake through this table.
func awakeFromTable(ctx context.Context) ([]cardAwakeHold, error) {
	table, err := panelMachine().ResolveResourceTable(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	state, err := table.HoldersContext(ctx, resource.ResourceAwake, true)
	if err != nil {
		return nil, err
	}
	out := make([]cardAwakeHold, 0, len(state.Holders))
	for _, row := range state.Holders {
		out = append(out, cardAwakeHold{Program: row.Program, Account: row.Account, Amount: row.Amount,
			Lease: row.Lease, Since: row.Since, Why: row.Detail})
	}
	return out, nil
}

// cardAwake answers the card page's awake section from the resource table
// (RES-A1). An empty table result is authoritative; legacy rights state is
// not consulted.
func (p *servicePanel) cardAwake(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET required", 405)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()

	holds, tableErr := awakeFromTable(ctx)
	if tableErr != nil {
		// The table is the runtime's answer. A runtime that lacks it says so in
		// this reason (oaPlain reads the contract name).
		panelJSON(w, cardAwakeView{Outcome: "unavailable", Holds: []cardAwakeHold{}, Reason: tableErr.Error()})
		return
	}
	panelJSON(w, cardAwakeView{Outcome: "page", Source: "table", Holds: holds})
}

// logForeignNamedByFile logs, once per program (logNameFallback), each
// measured graphics card row no lease covers whose foreign-memory sentence
// on the Device page names it by its file: the page's foreignWords shows
// it for a program with a path and at least half a GB, and titles holds no
// registered title or signed description for it.
func logForeignNamedByFile(resources []cardResourceState, titles map[string]string) {
	for i := range resources {
		if !strings.HasPrefix(resources[i].Resource, "card:") {
			continue
		}
		for _, h := range resources[i].Holders {
			if h.Evidence != cardEvidenceVerified || h.Lease != "" || h.Amount < 1<<29 || !strings.ContainsAny(h.Program, `\/`) || titles[h.Program] != "" {
				continue
			}
			logNameFallback(h.Program)
		}
	}
}
