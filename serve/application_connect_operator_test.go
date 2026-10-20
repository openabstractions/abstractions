package main

import (
	"context"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Architect ruling 2026-11-11: ExpectConnect lives on the operator-only
// abstraction.applications/operator@1. applications@3, which apps and the
// MCP gateway reach, does not carry it; the operator service reaches the
// Panel admission, which refuses a caller without a Program proof.
func TestExpectConnectIsOnlyOnTheOperatorService(t *testing.T) {
	request := applications.ConnectExpectation{Application: "comfyui", Program: `C:\ComfyUI\custom_nodes\comfyui-openabstractions\comfyui-connector.exe`, SHA256: strings.Repeat("a", 64)}
	onApplications := applications.NewApplicationsOperatorClient(&applications.ApplicationsDispatcher{Handler: &applicationV3Receiver{}})
	if change, err := onApplications.ExpectConnect(request); err == nil {
		t.Fatalf("applications@3 answered ExpectConnect: %+v", change)
	}
	d, err := openApplications(t.TempDir(), "account", func(context.Context, rwire.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	d.connectPanel = func(string) bool { return true }
	host := &applicationsHost{directory: d}
	onOperator := applications.NewApplicationsOperatorClient(&applications.ApplicationsOperatorDispatcher{Handler: &applicationsOperatorReceiver{host: host, ctx: context.Background()}})
	change, err := onOperator.ExpectConnect(request)
	if err != nil || change.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatalf("operator@1 without a Program proof: %+v %v", change, err)
	}
	if contracts := (&applicationsHost{v3: &applicationV3Gateway{}}).ApplicationContracts(); !strings.Contains(strings.Join(contracts, " "), applicationsOperatorContract) {
		t.Fatalf("contracts %v", contracts)
	}
}
