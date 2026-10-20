package gateway

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	model "github.com/openabstractions/abstraction-model/go/client"
)

// SetupBackend reads abstraction.model/catalog@1 Fit for one use under this
// gateway's OA principal. The catalogue service gates Fit by the router's
// inventory.read right. Fit ranks and reports; it downloads, selects and
// hosts nothing.
type SetupBackend interface {
	SetupFit(context.Context, SetupFitInput) (SetupFitOutput, error)
}

// SetupFitInput names the use to rank the catalogue for.
type SetupFitInput struct {
	Use string `json:"use" jsonschema:"chat, code, vision, embed, image or video"`
}

// SetupFitEntry is one ranked catalogue entry as an assistant may say it to
// the person: the title, the fit word and the sizes. Repository ids, file
// names and digests stay hidden (first-run DECISION §7).
type SetupFitEntry struct {
	Title  string `json:"title"`
	Family string `json:"family"`
	// Fit is fits, slow or no against this computer's pool.
	Fit string `json:"fit"`
	// Ready is an entry a runtime already serves: nothing to download.
	Ready bool `json:"ready"`
	// Machine names the paired machine that serves the entry; empty is this
	// computer.
	Machine       string `json:"machine,omitempty"`
	DownloadBytes int64  `json:"download_bytes"`
	NeedBytes     int64  `json:"need_bytes"`
	Context       int64  `json:"context"`
	License       string `json:"license,omitempty"`
	// Pick marks this computer's pick: the first entry it serves or that fits
	// here; an entry a paired machine serves is never the pick.
	Pick bool `json:"pick"`
}

// SetupFitOutput is Fit's answer: fitted carries the catalogue version, the
// pool the rule compared against and every entry in rank order.
type SetupFitOutput struct {
	Outcome        string          `json:"outcome"`
	CatalogVersion string          `json:"catalog_version,omitempty"`
	PoolBytes      int64           `json:"pool_bytes"`
	Entries        []SetupFitEntry `json:"entries"`
}

func setupUse(word string) (model.CatalogUse, bool) {
	for _, use := range model.CatalogUseValues() {
		if use.String() == word {
			return use, true
		}
	}
	return 0, false
}

// SetupFit reads catalog@1 Fit for the use. It is read-only.
func (o *OA) SetupFit(ctx context.Context, in SetupFitInput) (SetupFitOutput, error) {
	use, ok := setupUse(in.Use)
	if !ok {
		return SetupFitOutput{}, errors.New("gateway: use must be chat, code, vision, embed, image or video")
	}
	catalog, err := o.machine.ResolveModelCatalog(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return SetupFitOutput{}, err
	}
	result, err := catalog.FitContext(ctx, use)
	if err != nil {
		return SetupFitOutput{}, err
	}
	return setupFitFrom(result), nil
}

// setupFitFrom keeps Fit's rank order and marks its pick: the first entry
// that fits or is served.
func setupFitFrom(result model.FitResult) SetupFitOutput {
	out := SetupFitOutput{Outcome: result.Outcome.String(), CatalogVersion: result.CatalogVersion, PoolBytes: result.PoolBytes, Entries: []SetupFitEntry{}}
	// The one pick rule the setup step and the runtime use: a paired
	// machine's entry is never this computer's pick.
	pick, _ := model.LocalPick(result)
	for i, e := range result.Entries {
		out.Entries = append(out.Entries, SetupFitEntry{Title: e.Title, Family: e.Family, Fit: string(e.Fit), Ready: e.Availability == model.AvailabilityServed,
			Machine: e.Machine, DownloadBytes: e.DownloadBytes, NeedBytes: e.NeedBytes, Context: e.Context, License: e.License, Pick: i == pick})
	}
	return out
}

func addSetupTools(s *mcp.Server, backend SetupBackend, bounded func(context.Context, func(context.Context) error) error, read *mcp.ToolAnnotations) {
	mcp.AddTool(s, &mcp.Tool{Name: ToolSetupFit, Description: "Rank OA's model catalogue for one use against this computer and what paired runtimes already serve. Read-only: it downloads, selects and hosts nothing. The person approves any setup in OA's own question.", Annotations: read},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SetupFitInput) (*mcp.CallToolResult, SetupFitOutput, error) {
			var out SetupFitOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.SetupFit(ctx, in); return err })
			return nil, out, err
		})
}
