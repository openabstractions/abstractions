package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type checkSession interface {
	ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
	Close() error
}

func connectionCheck(output io.Writer) int {
	// Reserve three seconds for CommandTransport's bounded stdin/terminate/kill
	// shutdown. The same work context covers selection, initialization and reads.
	ctx, cancel := context.WithTimeout(context.Background(), 17*time.Second)
	defer cancel()
	return checkConnection(ctx, output, func(ctx context.Context) (string, error) {
		selected, err := bootstrap.SelectInstalled(ctx)
		return selected.Endpoint, err
	}, connectCheckChild, checkTemporaryRoot)
}

func checkTemporaryRoot() (string, error) {
	platform := layout.Current()
	roots, err := layout.Resolve(platform)
	if err != nil {
		return "", err
	}
	return layout.EnsureTmp(roots, platform)
}

func checkEnvironment(environment []string, endpoint string) []string {
	out := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "ABSTRACTION_RUNTIME_ENDPOINT") {
			out = append(out, entry)
		}
	}
	return append(out, "ABSTRACTION_RUNTIME_ENDPOINT="+endpoint)
}

func connectCheckChild(ctx context.Context, endpoint, state string) (checkSession, error) {
	if endpoint == "" {
		return nil, errors.New("no selected endpoint")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := childproc.Hidden(exec.CommandContext(ctx, executable, "--state-dir", state))
	cmd.Env = checkEnvironment(os.Environ(), endpoint)
	return connectCheckCommand(ctx, cmd)
}

func connectCheckCommand(ctx context.Context, cmd *exec.Cmd) (checkSession, error) {
	// Diagnostics report fixed categories, never the child's potentially private
	// resolver paths, application names, model names or service error text.
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	return checkClient(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second})
}

// checkClient connects the check's MCP client over transport. Its name
// (gateway.CheckClientName) tells the gateway the reads hold for nobody.
func checkClient(ctx context.Context, transport mcp.Transport) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: gateway.CheckClientName, Version: gateway.Version}, nil)
	return client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
}

func checkConnection(ctx context.Context, output io.Writer, selectEndpoint func(context.Context) (string, error), connect func(context.Context, string, string) (checkSession, error), temporaryRoot func() (string, error)) (exit int) {
	// No capability calls or child launch occur without installed selection.
	endpoint, err := selectEndpoint(ctx)
	if err != nil || endpoint == "" {
		checkLine(output, "OA selection: unavailable; MCP not attempted")
		return 1
	}
	tmpRoot, err := temporaryRoot()
	if err != nil || tmpRoot == "" {
		checkLine(output, "MCP: not attempted (owned temporary root unavailable)")
		return 1
	}
	state, err := os.MkdirTemp(tmpRoot, "oa-mcp-check-")
	if err != nil {
		checkLine(output, "MCP: not attempted (temporary state unavailable)")
		return 1
	}
	closed := false
	defer func() {
		if !closed {
			checkLine(output, "Check cleanup: child shutdown not confirmed; temporary state retained")
			exit = 1
			return
		}
		if os.RemoveAll(state) != nil {
			checkLine(output, "Check cleanup: temporary state removal failed")
			exit = 1
		}
	}()
	session, err := connect(ctx, endpoint, state)
	if err != nil {
		checkLine(output, "MCP: initialization failed ("+checkError(err)+"); OA reads not attempted")
		return 1
	}
	defer func() {
		if session.Close() != nil {
			checkLine(output, "MCP cleanup: child shutdown failed")
			exit = 1
		} else {
			closed = true
		}
	}()
	checkLine(output, "MCP: connected")
	tools, err := session.ListTools(ctx, nil)
	if err != nil || !checkReadTools(tools) {
		checkLine(output, "MCP tools: unavailable or invalid read-only declarations; OA reads not attempted")
		return 1
	}
	checkLine(output, fmt.Sprintf("MCP tools: %d declared", len(tools.Tools)))
	// Each read states its own time, so a slow one is named.
	started := time.Now()
	apps, appErr := session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolApplications, Arguments: gateway.ApplicationsInput{}})
	appCount, appErr := checkApplications(apps, appErr)
	appTime := time.Since(started)
	started = time.Now()
	models, modelErr := session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolModels, Arguments: gateway.ModelsInput{Fresh: false}})
	modelCount, modelErr := checkModels(models, modelErr)
	modelTime := time.Since(started)
	for _, probe := range []struct {
		name  string
		count int
		err   error
		took  time.Duration
	}{{"applications", appCount, appErr, appTime}, {"models", modelCount, modelErr, modelTime}} {
		if probe.err != nil {
			checkLine(output, fmt.Sprintf("OA %s: unavailable (%s) after %d ms", probe.name, checkError(probe.err), probe.took.Milliseconds()))
			exit = 1
		} else {
			checkLine(output, fmt.Sprintf("OA %s: available (%d) in %d ms", probe.name, probe.count, probe.took.Milliseconds()))
		}
	}
	return exit
}

func checkLine(output io.Writer, text string) {
	//unchecked: bounded command diagnostics; a failed output write has no fallback destination
	fmt.Fprintln(output, text)
}

var errCheckShape = errors.New("invalid structured result")
var errCheckRefusal = errors.New("tool refused")
var errCheckPermission = errors.New("permission question pending")

func checkError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "deadline or cancellation"
	case errors.Is(err, errCheckShape):
		return "invalid structured result"
	case errors.Is(err, errCheckRefusal):
		return "read refused"
	case errors.Is(err, errCheckPermission):
		return "waiting for the person's permission"
	default:
		return "transport or service failure"
	}
}

func checkReadTools(result *mcp.ListToolsResult) bool {
	if result == nil || result.NextCursor != "" {
		return false
	}
	seen := map[string]int{}
	for _, tool := range result.Tools {
		if tool == nil {
			return false
		}
		if tool.Name == gateway.ToolApplications || tool.Name == gateway.ToolModels {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				return false
			}
			seen[tool.Name]++
		}
	}
	return seen[gateway.ToolApplications] == 1 && seen[gateway.ToolModels] == 1
}

func checkStructured(result *mcp.CallToolResult, callErr error, fields []string, target any) error {
	if callErr != nil {
		return callErr
	}
	if result == nil || result.IsError {
		return errCheckRefusal
	}
	if result.StructuredContent == nil {
		return errCheckShape
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil || len(raw) > 1<<20 {
		return errCheckShape
	}
	// A read that needs the person's Allow answers with the permission
	// result (gateway permission_result.go); the check names it.
	var permission struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(raw, &permission) == nil {
		switch permission.Outcome {
		case "awaiting_permission", "permission_refused", "permission_blocked":
			return errCheckPermission
		}
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil || len(members) != len(fields) {
		return errCheckShape
	}
	for _, field := range fields {
		value, ok := members[field]
		if !ok || bytes.Equal(value, []byte("null")) {
			return errCheckShape
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return errCheckShape
	}
	return nil
}

func checkApplications(result *mcp.CallToolResult, err error) (int, error) {
	var out gateway.ApplicationsOutput
	if err := checkStructured(result, err, []string{"outcome", "observed_unix_ms", "applications"}, &out); err != nil {
		return 0, err
	}
	if out.ObservedUnixMS <= 0 {
		return 0, errCheckShape
	}
	if out.Outcome != "page" {
		switch out.Outcome {
		case "applied", "unknown", "stale", "conflict", "invalid", "forbidden", "unavailable":
			return 0, errCheckRefusal
		default:
			return 0, errCheckShape
		}
	}
	return len(out.Applications), nil
}

func checkModels(result *mcp.CallToolResult, err error) (int, error) {
	var out gateway.ModelsOutput
	if err := checkStructured(result, err, []string{"observed_unix_ms", "models"}, &out); err != nil {
		return 0, err
	}
	if out.ObservedUnixMS <= 0 {
		return 0, errCheckShape
	}
	return len(out.Models), nil
}
