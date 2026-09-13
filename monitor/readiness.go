package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
)

type readinessRow struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Capability string `json:"capability"`
	Contract   string `json:"contract"`
	Status     string `json:"status"`
	Provider   string `json:"provider,omitempty"`
}

type readinessView struct {
	BootstrapLabel string         `json:"bootstrapLabel"`
	Bootstrap      string         `json:"bootstrap"`
	Detail         string         `json:"detail,omitempty"`
	Capabilities   []readinessRow `json:"capabilities"`
	Error          string         `json:"error,omitempty"`
	CheckedAt      string         `json:"checkedAt"`
}

func readinessPresentation(observation wire.RuntimeObservation, err error) readinessView {
	view := readinessView{Bootstrap: observation.Bootstrap.State, Detail: observation.Bootstrap.Detail,
		BootstrapLabel: readinessLabel(observation.Bootstrap.State),
		Capabilities:   []readinessRow{}, CheckedAt: time.Now().Format(time.RFC3339)}
	if err != nil {
		view.Error = err.Error()
	}
	for _, item := range observation.Capabilities {
		row := readinessRow{Capability: item.Request.Capability, Contract: strings.Join(item.Request.Contracts, ", "), Status: "unobserved"}
		if item.Result != nil {
			row.Status = item.Result.Status
			if item.Result.Reference != nil {
				row.Provider = item.Result.Reference.Provider
			}
		}
		row.Name = readinessName(row.Contract)
		row.Label = readinessLabel(row.Status)
		view.Capabilities = append(view.Capabilities, row)
	}
	return view
}

func readinessName(contract string) string {
	names := map[string]string{
		"abstraction.logging/sink@1":   "Logging",
		"abstraction.config/reader@1":  "Configuration reads",
		"abstraction.config/editor@1":  "Configuration changes",
		"abstraction.job/acceptance@1": "Work submission",
		"abstraction.job/operations@1": "Work status and results",
	}
	if name := names[contract]; name != "" {
		return name
	}
	return contract
}

func readinessLabel(status string) string {
	labels := map[string]string{"unknown": "Unknown", "installed": "Installed", "starting": "Starting", "running": "Running",
		"resolved": "Ready", "unavailable": "Unavailable", "forbidden": "Access denied", "incompatible": "Contract not supported",
		"unmet_requirements": "Required guarantee unavailable", "not_ready": "Not ready", "invalid_request": "Invalid request", "unobserved": "Not checked"}
	if label := labels[status]; label != "" {
		return label
	}
	return status
}

// Installation observations and resolution share one caller budget. This is a
// read-only snapshot requested by the user; it never activates or repairs hosts.
func observeReadiness(ctx context.Context) readinessView {
	return collectReadiness(ctx, panelMachine(), bootstrap.ObserveInstalled)
}

func collectReadiness(ctx context.Context, machine *facade.Machine, installed func(context.Context) wire.BootstrapObservation) readinessView {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	evidence := installed(ctx)
	observation, err := machine.Observe(ctx, facade.DefaultStatusRequests(), evidence)
	return readinessPresentation(observation, err)
}

func (w *window) serveReadiness(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		rw.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(observeReadiness(r.Context()))
}

func readinessText(view readinessView) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Supervision: %s\n", view.BootstrapLabel)
	if view.Detail != "" {
		fmt.Fprintln(&text, view.Detail)
	}
	for _, row := range view.Capabilities {
		fmt.Fprintf(&text, "\n%s: %s", row.Name, row.Label)
		if row.Provider != "" {
			fmt.Fprintf(&text, " (%s)", row.Provider)
		}
	}
	if view.Error != "" {
		fmt.Fprintf(&text, "\n\nObservation: %s", view.Error)
	}
	fmt.Fprintf(&text, "\n\nChecked at %s", view.CheckedAt)
	return text.String()
}
