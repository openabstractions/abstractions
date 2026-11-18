package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// An integration is an app or agent OpenAbstractions can add or connect:
// the picture app, OpenCode, Claude and Codex. Catalog lists every one and
// Apps lists the ones on this computer and connected, each as one row of the
// same shape: name, caption and a state button (owner report 2026-11-22,
// "only OpenCode has an entry in Catalog"). Each button runs the flow its
// integration already has: the picture app's Catalog card, OpenCode's
// integration package step, the agent connection pages, and the publisher's
// download page where OpenAbstractions installs nothing itself.
type setupIntegration struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Caption string `json:"caption"`
	// State names the button: "get" when it is not on this computer,
	// "connect" when it is and is not connected, "open" when connected and
	// OpenAbstractions can open it, else "manage".
	State     string `json:"state"`
	Installed bool   `json:"installed"`
	Connected bool   `json:"connected"`
	// Page is the Panel page that connects and manages the integration.
	Page string `json:"page"`
	// Download is the publisher's download page, the Get fallback for a
	// program OpenAbstractions does not install itself.
	Download string `json:"download,omitempty"`
	// Open is the Panel route that opens the app, for "open".
	Open string `json:"open,omitempty"`
	// Registrations are the application names the integration's writers
	// register; Apps shows the integration's row in their place.
	Registrations []string `json:"registrations"`
	// Attention is the one sentence that says why an agent that is not
	// connected needs attention: the last failure its connection page met,
	// or OpenAbstractions' own setup that stops connecting it. The row then
	// reads Needs attention in place of Not connected.
	Attention string `json:"attention,omitempty"`
}

// integrationDownloads are publishers' download pages for a program the
// Panel can find on this computer and OpenAbstractions does not install.
// Claude and Codex have none: the Panel finds them only once they run, and
// their connection pages say how.
var integrationDownloads = map[setup.AgentParty]string{setup.AgentOpenCode: "https://opencode.ai/download"}

// integrationEnrolledImage holds the party's enrolled OpenAbstractions
// connector, the connection the agent pages save; the caller closes it. A
// read that fails counts as not connected and writes a log line.
var integrationEnrolledImage = func(ctx context.Context, party setup.AgentParty) (claudePanelImage, bool) {
	image, err := setup.HoldEnrolledAgentImage(ctx, panelMachine(), party)
	if err != nil {
		if ctx.Err() == nil {
			agentReadBlocked.Store(string(party), agentSetupBlocker(err, product.AgentConnectionTitle(string(party))))
		}
		if !errors.Is(err, os.ErrNotExist) && ctx.Err() == nil {
			log.Printf("setup: %s connection could not be read: %v", product.AgentConnectionTitle(string(party)), err)
		}
		return nil, false
	}
	agentReadBlocked.Delete(string(party))
	return image, true
}

// agentReadBlocked keeps, by party, the setup sentence of the last failed
// read of its enrolled connector: an isolated home or a missing helper.
var agentReadBlocked sync.Map

// setupIntegrations lists the integrations in their one order: the apps,
// then the agents in title order.
func setupIntegrations(ctx context.Context, picture setupPictureApplication, desktop setupDesktopIntegrationFacts) []setupIntegration {
	rows := []setupIntegration{pictureIntegration(picture)}
	for _, party := range []setup.AgentParty{setup.AgentOpenCode, setup.AgentClaude, setup.AgentCodex} {
		rows = append(rows, agentIntegration(ctx, party, desktop))
	}
	return rows
}

func pictureIntegration(picture setupPictureApplication) setupIntegration {
	registration, _ := picturePackageIdentity()
	row := setupIntegration{ID: string(picture.ID), Title: picture.Title, Caption: "Create pictures", Page: appPagePrefix + registration,
		Download: picture.DownloadPage, Registrations: []string{registration}}
	switch picture.State {
	case "connected":
		row.Installed, row.Connected, row.State = true, true, "manage"
		if picture.Open && picture.Actions["open"] != "" {
			row.State, row.Open = "open", picture.Actions["open"]
		}
	case "connect", "finish":
		row.Installed, row.State = true, "connect"
	default:
		row.State = "get"
	}
	return row
}

// agentIntegration is an agent's row. Its registrations are the names the
// connection writers use: agentConnectionSetup registers the enrolled
// connector under agentImageApplication, and the installed OpenCode's
// manifest registers its stock program under the party's name
// (holdInstalledConnectionHost refuses any other).
func agentIntegration(ctx context.Context, party setup.AgentParty, desktop setupDesktopIntegrationFacts) setupIntegration {
	base := nativecli.AgentApplicationForParty(string(party))
	row := setupIntegration{ID: string(party), Title: product.AgentConnectionTitle(string(party)), Caption: "Coding assistant", Page: "/connections/" + string(party),
		Registrations: []string{base}}
	if image, ok := integrationEnrolledImage(ctx, party); ok {
		row.Connected = true
		if name := agentImageApplication(image, base); name != base {
			row.Registrations = append(row.Registrations, name)
		}
		//unchecked: the held image was only read; closing it changes nothing
		_ = image.Close()
	}
	if party == installedConnection().party {
		row.Registrations = append(row.Registrations, string(installedConnection().party))
	}
	row.Installed = row.Connected
	if download, ok := integrationDownloads[party]; ok {
		row.Download = download
		row.Installed = row.Connected || (desktop.App == setup.AppID(party) && desktop.Installed) || desktopProgramPresent(setup.AppID(party))
	}
	if !row.Connected {
		row.Attention = agentAttentionFor(string(party))
		if row.Attention == "" {
			if value, ok := agentReadBlocked.Load(string(party)); ok {
				row.Attention, _ = value.(string)
			}
		}
		row.Attention = firstSentence(row.Attention)
	}
	switch {
	case row.Connected:
		row.State = "manage"
	case !row.Installed && row.Download != "":
		row.State = "get"
	default:
		row.State = "connect"
	}
	return row
}

// setupIntegrationsView is GET /setup/integrations, the Apps page's read.
// It reads the picture app's card and the agents' enrollments only.
func (p *servicePanel) setupIntegrationsView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	journal, err := p.setupJournalForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	catalog := journal.Catalog()
	desktop := setupDesktopIntegrationFacts{App: setup.OpenCode, Installed: desktopProgramPresent(setup.OpenCode)}
	panelJSON(w, struct {
		Integrations []setupIntegration `json:"integrations"`
	}{setupIntegrations(ctx, p.readPictureApplication(ctx, catalog, journal), desktop)})
}
