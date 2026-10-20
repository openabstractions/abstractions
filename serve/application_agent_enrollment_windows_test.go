//go:build windows

package main

import (
	"os"
	"strings"
	"testing"
)

func TestApplicationAgentNativePeerUsesCurrentKeptProgramSubject(t *testing.T) {
	exerciseApplicationAgentNativePeer(t, "", "")
}

func TestApplicationAgentSharedGatewayLocationMatchesPackagingOwner(t *testing.T) {
	b, err := os.ReadFile(privateInput(t, "..", "installer", "build.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), applicationSharedGatewayComponent) || !strings.Contains(string(b), "McpGateway") {
		t.Fatal("shared gateway build component drift")
	}
	b, err = os.ReadFile(privateInput(t, "..", "installer", "abstraction.wxs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `Id="McpGatewayFiles" Directory="ToolsFolder"`) || !strings.Contains(string(b), `Source="payload/`+applicationSharedGatewayComponent+`"`) {
		t.Fatal("shared gateway MSI component drift")
	}
}
