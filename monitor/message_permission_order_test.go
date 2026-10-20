package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/grants"
)

// Receivers read in title order whatever order the effect stores them in.
func TestMessagePermissionReceiversReadInTitleOrder(t *testing.T) {
	sender := applications.CapturedAgentApplication{Name: "oa-codex-integration"}
	captured := []applications.AgentMessageResource{
		{Sender: sender, Receiver: applications.CapturedAgentApplication{Name: "oa-opencode-integration"}},
		{Sender: sender, Receiver: applications.CapturedAgentApplication{Name: "oa-claude-integration"}},
	}
	name, receivers := messagePermissionTitles(nil, captured)
	if got := grants.MessagePermissionQuestion(name, receivers); got != "Allow Codex to send tasks to Claude and OpenCode?" {
		t.Fatal(got)
	}
	if captured[0].Receiver.Name != "oa-claude-integration" {
		t.Fatal("details do not follow the title order", captured)
	}
}

// OA's own connectors show no Publisher line on the registration question.
func TestConnectorRegistrationShowsNoPublisherLine(t *testing.T) {
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })
	image := filepath.Join(root, connectorImageLeaf(t, "oa-codex-integration"))
	if err := os.WriteFile(image, []byte("connector"), 0o600); err != nil {
		t.Fatal(err)
	}
	signedByPanelSigner(t, image)
	if line := selfRegisteredPublisherLine(trayPublisher{}, image); line != "" {
		t.Fatal("connector shows a Publisher line", line)
	}
	other := filepath.Join(t.TempDir(), "third-party.exe")
	if err := os.WriteFile(other, []byte("app"), 0o600); err != nil {
		t.Fatal(err)
	}
	if line := selfRegisteredPublisherLine(trayPublisher{}, other); !strings.HasPrefix(line, "Publisher: ") {
		t.Fatal("third-party program lost its Publisher line", line)
	}
}
