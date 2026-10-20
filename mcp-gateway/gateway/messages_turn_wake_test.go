package gateway

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// W5 (AB-1s ruling 2026-11-09): the model never sees or passes a wake key or
// a turn signal. Only the adapter's watch tool carries turn.
func TestModelMessageToolsCarryNoTurnWake(t *testing.T) {
	_, s := connected(t, &messageFixture{Backend: &fixtureBackend{}}, "fixture", "2026-07-28")
	tools, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &receiverCatalogueFixture{messageReceiveFixture: messageReceiveFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewMessagesReceiverServer(b).Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "arbitrary-client", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	receiver, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range append(tools.Tools, receiver.Tools...) {
		if tool.Name == "oa_messages_watch" {
			continue
		}
		seen[tool.Name] = true
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"wake", "turn", "wake_key"} {
			if _, ok := schema.Properties[name]; ok {
				t.Fatal(tool.Name, "offers the model", name)
			}
		}
	}
	for _, name := range []string{"oa_messages_send", "oa_messages_reply", "oa_messages_prepare", "oa_messages_wait"} {
		if !seen[name] {
			t.Fatal("model message tool missing", name, seen)
		}
	}
}
