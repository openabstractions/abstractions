package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	resource "github.com/openabstractions/abstraction-resource/go/client"
	rights "github.com/openabstractions/abstraction-rights/go"
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
	panelJSON(w, view)
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

// cardAwakeView says which source answered: "table" for the resource table's
// awake rows, "rights fallback" for the legacy connection-owned rights
// service read used only when the table reports no awake resource, or empty
// when neither answered.
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
// lease book claims (abstraction-resource/go/instrument/instrument.go). So an
// empty, error-free result here means nobody holds awake through this table,
// which is this function's signal to fall back.
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

// awakeHolds dials the legacy rights service exactly as `rights holds` does:
// same endpoint, same account, the admin secret it wrote beside its state.
// Used only as the fallback awakeFromTable calls for when the table reports
// no awake resource.
func awakeHolds() ([]rights.Hold, error) {
	admin, err := os.ReadFile(filepath.Join(rights.DefaultStateDir(), "admin.secret"))
	if err != nil {
		return nil, err
	}
	c := &rights.Client{Endpoint: rights.DefaultEndpoint(), Admin: string(admin)}
	hs, err := c.Holds()
	if err != nil {
		return nil, err
	}
	out := make([]rights.Hold, 0, len(hs))
	for _, h := range hs {
		if h.Right == rights.RightAwake {
			out = append(out, h)
		}
	}
	return out, nil
}

// cardAwake answers the card page's awake section. The table's awake rows are
// the primary source (RES-A1); the legacy rights service is read only when
// the table reports no awake resource, and the reply says which one answered.
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
		// this reason (oaPlain reads the contract name), and the legacy read
		// beside the installed runtime's state is not consulted: its own
		// failure, a missing admin secret at a path, said nothing a person
		// could act on (review round three, 2026-09-23).
		panelJSON(w, cardAwakeView{Outcome: "unavailable", Holds: []cardAwakeHold{}, Reason: tableErr.Error()})
		return
	}
	if len(holds) > 0 {
		panelJSON(w, cardAwakeView{Outcome: "page", Source: "table", Holds: holds})
		return
	}

	// The table answered and reports no awake resource: a legacy runtime may
	// still record holds in the rights service, which wrote an admin secret
	// beside its state. No secret means no legacy service to ask, and the
	// table's answer stands: nobody holds awake. Any other failure of that
	// read is reported as its own.
	hs, err := awakeHolds()
	if errors.Is(err, os.ErrNotExist) {
		panelJSON(w, cardAwakeView{Outcome: "page", Source: "table", Holds: []cardAwakeHold{}})
		return
	}
	if err != nil {
		panelJSON(w, cardAwakeView{Outcome: "unavailable", Holds: []cardAwakeHold{}, Reason: "legacy rights read: " + err.Error()})
		return
	}
	view := cardAwakeView{Outcome: "page", Source: "rights fallback", Holds: []cardAwakeHold{}}
	for _, h := range hs {
		view.Holds = append(view.Holds, cardAwakeHold{Program: h.App, Since: h.Since.UTC().Format(time.RFC3339),
			Lease: h.Lease, Why: h.Why})
	}
	panelJSON(w, view)
}
