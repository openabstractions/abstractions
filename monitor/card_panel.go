package main

import (
	"context"
	"io"
	"net/http"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
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
}

// cardHolder is one row of a resource, projected for the page the same way
// serve/resources_command.go projects one for the command line.
type cardHolder struct {
	Program  string `json:"program"`
	Account  string `json:"account,omitempty"`
	Amount   int64  `json:"amount"`
	Evidence string `json:"evidence"`
	Lease    string `json:"lease,omitempty"`
	Grant    string `json:"grant,omitempty"`
	Since    string `json:"since,omitempty"`
	Detail   string `json:"detail,omitempty"`
	// Title is the title of the registered local application whose
	// descriptor names this holder's program (titleCardHolders).
	Title string `json:"title,omitempty"`
}

// cardResourceState is one resource's held state. capacity, held and observed
// describe the machine; held sums verified rows only, and a reader never adds
// two resources' held figures together (CONTRACT.md, card:<n> and memory on an
// APU report the same bytes).
type cardResourceState struct {
	Resource   string       `json:"resource"`
	Capacity   int64        `json:"capacity"`
	Held       int64        `json:"held"`
	Observed   string       `json:"observed"`
	Instrument string       `json:"instrument"`
	Holders    []cardHolder `json:"holders"`
}

type cardTableView struct {
	Resources []cardResourceState `json:"resources"`
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
	table, err := panelMachine().ResolveResourceTable(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	list, err := table.ResourcesContext(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	view := cardTableView{Resources: []cardResourceState{}}
	for _, name := range list.Resources {
		state, err := table.HoldersContext(ctx, name, fresh)
		if err != nil {
			panelError(w, err)
			return
		}
		view.Resources = append(view.Resources, projectCardResource(state))
	}
	p.titleCardHolders(ctx, view.Resources)
	panelJSON(w, view)
}

// titleCardHolders gives each holder the title of the registered local
// application whose descriptor names its program, read from the application
// directory the Apps page reads. An unreadable directory leaves every holder
// untitled; the page then names it by its file name.
func (p *servicePanel) titleCardHolders(ctx context.Context, resources []cardResourceState) {
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil || page.Outcome != fwire.ApplicationOutcomePage && page.Outcome != fwire.ApplicationOutcomeStale {
		return
	}
	titleHolders(resources, page.Applications)
}

// titleHolders asks the filesystem whether a holder's program and a
// descriptor's program are one file (identity.SameSubjectProgram): a junction
// or an 8.3 alias of the registered program matches, and a twin name the
// volume keeps apart does not.
func titleHolders(resources []cardResourceState, apps []fwire.ApplicationEntry) {
	for i := range resources {
		for j := range resources[i].Holders {
			holder := &resources[i].Holders[j]
			for _, app := range apps {
				d := app.Descriptor
				if app.Scope == fwire.ScopeLocal && d.Title != "" && identity.SameSubjectProgram(holder.Program, d.Program) {
					holder.Title = appEntryTitle(app)
					break
				}
			}
		}
	}
}

func projectCardResource(state resource.ResourceState) cardResourceState {
	out := cardResourceState{Resource: state.Resource, Capacity: state.Capacity, Held: state.Held,
		Observed: state.Observed, Instrument: state.Instrument, Holders: []cardHolder{}}
	for _, row := range state.Holders {
		out.Holders = append(out.Holders, cardHolder{Program: row.Program, Account: row.Account, Amount: row.Amount,
			Evidence: row.Evidence.String(), Lease: row.Lease, Grant: row.Grant, Since: row.Since, Detail: row.Detail})
	}
	return out
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
