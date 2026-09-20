package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxBody = 16 << 10

type bridgeClient struct {
	endpoint string
	token    string
	client   *http.Client
}

type command struct {
	Kind      string `json:"kind"`
	Arguments any    `json:"arguments"`
}

func localBridge(endpoint, token string, client *http.Client) (*bridgeClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() == "" {
		return nil, errors.New("comfy fixture: endpoint must be a loopback HTTP URL")
	}
	host := parsed.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return nil, errors.New("comfy fixture: endpoint must be loopback")
	}
	return &bridgeClient{endpoint: strings.TrimRight(endpoint, "/"), token: token, client: client}, nil
}

func (b *bridgeClient) call(ctx context.Context, kind string, arguments any) (map[string]any, error) {
	body, err := json.Marshal(command{Kind: kind, Arguments: arguments})
	if err != nil || len(body) > maxBody {
		return nil, errors.New("comfy fixture: request exceeds bound")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.endpoint, "/")+"/oa/presentation/v1/command", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBody {
		return nil, errors.New("comfy fixture: response exceeds bound")
	}
	if response.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 256 {
			detail = detail[:256]
		}
		return nil, fmt.Errorf("comfy fixture: bridge refused (%s): %s", response.Status, detail)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
		return nil, errors.New("comfy fixture: bridge response lacks result object")
	}
	var result map[string]any
	if err := json.Unmarshal(envelope.Result, &result); err != nil || result == nil {
		return nil, errors.New("comfy fixture: bridge result must be an object")
	}
	return result, nil
}

type previewInput struct {
	Instance string  `json:"instance" jsonschema:"expected browser page instance from comfy_read_context"`
	Context  string  `json:"context" jsonschema:"expected workflow context from comfy_read_context"`
	Revision string  `json:"revision" jsonschema:"expected workflow revision from comfy_read_context"`
	NodeID   int64   `json:"node_id" jsonschema:"exact ComfyUI node id"`
	Widget   string  `json:"widget" jsonschema:"exact numeric widget name"`
	Value    float64 `json:"value" jsonschema:"proposed numeric value"`
}
type readInput struct {
	Operation string `json:"operation" jsonschema:"operation id returned by the visible panel"`
}

func newServer(bridge *bridgeClient) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "oa-comfy-presentation-fixture", Version: "0.1.0-dev"}, nil)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	mcp.AddTool(server, &mcp.Tool{Name: "comfy_read_context", Description: "Read the current browser instance, workflow context and revision required by preview.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
			out, err := bridge.call(ctx, "context", struct{}{})
			return nil, out, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "comfy_preview_parameter", Description: "Preview one exact ComfyUI numeric parameter. A person separately authorizes any apply in the visible panel.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input previewInput) (*mcp.CallToolResult, map[string]any, error) {
			if input.Instance == "" || input.Context == "" || input.Revision == "" || input.NodeID < 0 || input.Widget == "" {
				return nil, nil, errors.New("expected instance, context, revision, exact node_id and widget are required")
			}
			out, err := bridge.call(ctx, "preview", input)
			return nil, out, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "comfy_read_outcome", Description: "Read a bounded observed result by operation id.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input readInput) (*mcp.CallToolResult, map[string]any, error) {
			if input.Operation == "" {
				return nil, nil, errors.New("operation is required")
			}
			out, err := bridge.call(ctx, "read", input)
			return nil, out, err
		})
	return server
}

func main() {
	mode := flag.String("mode", "", "required: fixture or oa")
	endpoint := flag.String("comfy-url", "http://127.0.0.1:8188", "exact isolated ComfyUI origin")
	oaEndpoint := flag.String("oa-endpoint", "", "explicit local OA resolver endpoint; requires --oa-runtime-program")
	oaRuntimeProgram := flag.String("oa-runtime-program", "", "absolute trusted OA runtime executable; requires --oa-endpoint")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	token := os.Getenv("OA_COMFY_BRIDGE_TOKEN")
	if len(token) < 32 {
		fmt.Fprintln(os.Stderr, "OA_COMFY_BRIDGE_TOKEN must contain at least 32 characters")
		os.Exit(2)
	}
	bridge, err := localBridge(*endpoint, token, &http.Client{Timeout: 25 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var server *mcp.Server
	switch *mode {
	case "fixture":
		server = newServer(bridge)
	case "oa":
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		apps, decisions, log, resolveErr := resolveOAServices(ctx, *oaEndpoint, *oaRuntimeProgram)
		cancel()
		if resolveErr != nil {
			fmt.Fprintln(os.Stderr, "OA services unavailable:", resolveErr)
			os.Exit(1)
		}
		server = newOAServer(newOAAdapter(bridge, apps, decisions, log))
	default:
		fmt.Fprintln(os.Stderr, "--mode must be fixture or oa")
		os.Exit(2)
	}
	if err := server.Run(context.Background(), &mcp.StdioTransport{MaxLineLength: 32 << 10}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
