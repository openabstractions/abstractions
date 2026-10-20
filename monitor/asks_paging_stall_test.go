package main

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	wire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-identity/listen"
)

// pageServer is a fake asks frame server: every call answers with page.
func pageServer(t *testing.T, page wire.OperatorPage, calls *int) listen.FrameClient {
	t.Helper()
	encoded := wire.EncodeOperatorPage(&page)
	return listen.FrameClient{Endpoint: "explicit-fixture", Dialer: func(context.Context, string) (net.Conn, error) {
		*calls++
		caller, server := net.Pipe()
		go func() {
			defer server.Close()
			frame, err := listen.ReadFrameFrom(server, 0)
			if err != nil {
				t.Error(err)
				return
			}
			var request struct {
				Service string `json:"service"`
				Method  string `json:"method"`
			}
			if err := json.Unmarshal(frame, &request); err != nil {
				t.Error(err)
				return
			}
			reply, err := json.Marshal(map[string]any{"version": 1, "service": request.Service, "method": request.Method, "ok": true, "payload": map[string]json.RawMessage{"value": encoded}})
			if err != nil {
				t.Error(err)
				return
			}
			if err := listen.WriteFrameTo(server, reply, 0); err != nil {
				t.Error(err)
			}
		}()
		return caller, nil
	}}
}

// An unavailable question book is unavailable to the tray after one call.
// The earlier tray reader re-read the first page 64 times and reported an
// available runtime with no pending questions.
func TestTrayPollReportsUnavailableBook(t *testing.T) {
	for _, outcome := range []wire.OperatorPageOutcome{wire.OperatorPageOutcomeUnavailable, wire.OperatorPageOutcomeForbidden} {
		calls := 0
		transport := pageServer(t, wire.OperatorPage{Outcome: outcome}, &calls)
		previous := resolveTrayOperator
		resolveTrayOperator = func(context.Context, *facade.Machine) (*askclient.Operator, error) {
			return askclient.NewOperatorWithTransport(transport), nil
		}
		fresh, operator, unavailable := pollTray(context.Background(), nil, newTrayAnnouncer())
		resolveTrayOperator = previous
		if !unavailable || fresh != nil || operator != nil || calls != 1 {
			t.Fatalf("%s: fresh=%v unavailable=%v calls=%d", outcome, fresh, unavailable, calls)
		}
	}
}
