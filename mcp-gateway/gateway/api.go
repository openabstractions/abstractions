package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
)

const ToolAPIDescribe = "oa_api_describe"
const ToolAPIRead = "oa_api_read"

type APIReadBackend interface {
	APIRead(context.Context, APIReadInput) (APIReadResult, error)
}

type APIReadInput struct {
	Contract  string          `json:"contract"`
	Method    string          `json:"method"`
	Arguments json.RawMessage `json:"arguments"`
}

type APIReadResult struct {
	JSON    []byte
	IsError bool
}

type apiTransport interface {
	ExchangeFrame([]byte) ([]byte, error)
	WriteFrame([]byte) error
}
type apiMethod struct {
	Contract        string          `json:"contract"`
	Method          string          `json:"method"`
	Class           string          `json:"class"`
	Trait           string          `json:"trait"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	Documentation   string          `json:"documentation,omitempty"`
	ArgumentsSchema json.RawMessage `json:"arguments_schema,omitempty"`
	ResultSchema    json.RawMessage `json:"result_schema,omitempty"`
	invoke          func(apiTransport, string, string, []byte) ([]byte, error)
	serviceError    func(error) ([]byte, bool)
}

func findAPIRead(contract, method string) (apiMethod, bool) {
	for _, entry := range apiMethods() {
		if entry.Contract == contract && entry.Method == method && entry.Class == "read" {
			return entry, true
		}
	}
	return apiMethod{}, false
}

func apiReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// Logging Observe and job observation admit a 30-second service wait.
	// Leave time for native resolution and response transport around that wait.
	return context.WithTimeout(ctx, 35*time.Second)
}

func (o *OA) APIRead(ctx context.Context, in APIReadInput) (APIReadResult, error) {
	ctx, cancel := apiReadContext(ctx)
	defer cancel()
	entry, ok := findAPIRead(in.Contract, in.Method)
	if !ok {
		return APIReadResult{}, errors.New("api: unknown projected readonly method")
	}
	if len(in.Arguments) > 1<<20 {
		return APIReadResult{}, errors.New("api: arguments exceed 1048576 bytes")
	}
	binding, err := o.machine.ResolveService(ctx, in.Contract, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return APIReadResult{}, err
	}
	raw, err := entry.invoke(binding.Transport().WithContext(ctx), in.Contract, in.Method, in.Arguments)
	if err != nil {
		if refusal, ok := entry.serviceError(err); ok {
			return APIReadResult{JSON: refusal, IsError: true}, nil
		}
		return APIReadResult{}, err
	}
	return APIReadResult{JSON: raw}, nil
}

func apiResult(out APIReadResult) (*mcp.CallToolResult, error) {
	if len(out.JSON) > 1<<20 {
		return nil, errors.New("api: answer exceeds 1048576 bytes")
	}
	var structured any
	decoder := json.NewDecoder(bytes.NewReader(out.JSON))
	decoder.UseNumber()
	if err := decoder.Decode(&structured); err != nil {
		return nil, err
	}
	if record, ok := structured.(map[string]any); ok {
		word := ""
		if out.IsError {
			word, _ = record["code"].(string)
		}
		if value, ok := record["value"].(map[string]any); ok {
			if outcome, ok := value["outcome"].(string); ok && apiRefusalOutcomes[outcome] {
				word = outcome
				out.IsError = true
			}
		}
		if word != "" {
			record["outcome"] = word
		}
	}
	return &mcp.CallToolResult{IsError: out.IsError, StructuredContent: structured, Content: []mcp.Content{&mcp.TextContent{Text: string(out.JSON), Meta: mcp.Meta{"org.openabstractions/untrusted": true}, Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}}}}}, nil
}

// The explicit tool-result refusal rows of abstraction-ipc CONTRACT IPC-E2.
// Native payloads and words outside this table retain their own meaning.
var apiRefusalOutcomes = map[string]bool{
	"invalid": true, "forbidden": true, "not_permitted": true, "needs_person": true,
	"unknown_action": true, "unavailable": true, "unsupported": true, "conflict": true,
	"exhausted": true, "not_found": true, "unknown": true, "expired": true, "gap": true,
}

type apiDescribeInput struct {
	Contract string `json:"contract,omitempty"`
	Method   string `json:"method,omitempty"`
}

func addAPITools(server *mcp.Server, backend APIReadBackend, bounded func(context.Context, func(context.Context) error) error, read *mcp.ToolAnnotations) {
	mcp.AddTool(server, &mcp.Tool{Name: ToolAPIDescribe, Description: "Describe OA's public methods. Omit method for a list; name contract and method for its argument and result schemas.", Annotations: read}, func(ctx context.Context, _ *mcp.CallToolRequest, in apiDescribeInput) (*mcp.CallToolResult, any, error) {
		entries := []apiMethod{}
		for _, entry := range apiMethods() {
			if in.Contract != "" && in.Contract != entry.Contract || in.Method != "" && in.Method != entry.Method {
				continue
			}
			if in.Method == "" {
				entry.ArgumentsSchema = nil
				entry.ResultSchema = nil
				entry.Documentation = ""
			}
			entries = append(entries, entry)
		}
		raw, err := json.Marshal(map[string]any{"methods": entries})
		if err != nil {
			return nil, nil, err
		}
		out, err := apiResult(APIReadResult{JSON: raw})
		return out, nil, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: ToolAPIRead, Description: "Call one read-only public OA method as this connection, with contract, method and arguments from oa_api_describe.", Annotations: read, InputSchema: map[string]any{"type": "object", "required": []string{"contract", "method", "arguments"}, "additionalProperties": false, "properties": map[string]any{"contract": map[string]any{"type": "string", "maxLength": 256}, "method": map[string]any{"type": "string", "maxLength": 128}, "arguments": map[string]any{"type": "object"}}}}, func(ctx context.Context, _ *mcp.CallToolRequest, in APIReadInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(string(in.Arguments)) == "" || len(in.Arguments) > 1<<20 {
			return nil, nil, errors.New("api: bounded arguments object required")
		}
		if _, ok := findAPIRead(in.Contract, in.Method); !ok {
			return nil, nil, errors.New("api: unknown projected readonly method")
		}
		var out APIReadResult
		err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.APIRead(ctx, in); return err })
		if err != nil {
			return nil, nil, err
		}
		result, err := apiResult(out)
		return result, nil, err
	})
}
