package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOrdinaryMCPPreviewAndReadUseBoundedAuthenticatedBridge(t *testing.T) {
	token := "fixture-secret-outside-model-input"
	var kinds []string
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var in command
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&in); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		kinds = append(kinds, in.Kind)
		if in.Kind == "context" {
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"instance": "page-1", "context": "workflow-1", "revision": "7"}})
			return
		}
		if in.Kind == "preview" {
			arguments := in.Arguments.(map[string]any)
			if arguments["instance"] != "page-1" || arguments["context"] != "workflow-1" || arguments["revision"] != "7" {
				http.Error(w, "missing binding", http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"outcome": "proposed", "token": "opaque", "instance": "page-1", "context": "workflow-1", "revision": "7"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"outcome": "applied", "actual": 12}})
	}))
	defer h.Close()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := newServer(&bridgeClient{endpoint: h.URL, token: token, client: h.Client()}).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "ordinary-mcp-fixture", Version: "1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	for _, call := range []mcp.CallToolParams{
		{Name: "comfy_read_context", Arguments: map[string]any{}},
		{Name: "comfy_preview_parameter", Arguments: map[string]any{"instance": "page-1", "context": "workflow-1", "revision": "7", "node_id": 70, "widget": "steps", "value": 12}},
		{Name: "comfy_read_outcome", Arguments: map[string]any{"operation": "operation-fixture"}},
	} {
		result, err := clientSession.CallTool(context.Background(), &call)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("tool returned error: %#v", result)
		}
	}
	if len(kinds) != 3 || kinds[0] != "context" || kinds[1] != "preview" || kinds[2] != "read" {
		t.Fatalf("bridge calls = %v", kinds)
	}
}

func TestBridgeRefusalRemainsToolError(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no browser", http.StatusConflict) }))
	defer h.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, _ := newServer(&bridgeClient{endpoint: h.URL, token: "x", client: h.Client()}).Connect(context.Background(), serverTransport, nil)
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "comfy_preview_parameter", Arguments: map[string]any{"instance": "page-1", "context": "workflow-1", "revision": "7", "node_id": 70, "widget": "steps", "value": 12}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("refusal was presented as success: %#v", result)
	}
}

func TestBridgeEndpointIsLoopback(t *testing.T) {
	if _, err := localBridge("https://example.com", "secret", http.DefaultClient); err == nil {
		t.Fatal("external endpoint accepted")
	}
	if _, err := localBridge("http://127.0.0.1:8188", "secret", http.DefaultClient); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeSuccessRequiresResultObject(t *testing.T) {
	for _, body := range []string{`{}`, `{"result":null}`, `{"result":4}`} {
		t.Run(body, func(t *testing.T) {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer h.Close()
			bridge := &bridgeClient{endpoint: h.URL, token: "x", client: h.Client()}
			if _, err := bridge.call(context.Background(), "read", map[string]any{"operation": "x"}); err == nil {
				t.Fatal("invalid success envelope accepted")
			}
		})
	}
}
