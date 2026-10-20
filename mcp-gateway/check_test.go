package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type checkFixture struct {
	apps, models               *mcp.CallToolResult
	appErr, modelErr, closeErr error
	calls                      []string
	closed                     bool
}

func (f *checkFixture) ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: []*mcp.Tool{
		{Name: gateway.ToolApplications, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		{Name: gateway.ToolModels, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
	}}, nil
}
func (f *checkFixture) CallTool(_ context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	f.calls = append(f.calls, p.Name)
	switch p.Name {
	case gateway.ToolApplications:
		if _, ok := p.Arguments.(gateway.ApplicationsInput); !ok {
			panic("nonempty applications arguments")
		}
		return f.apps, f.appErr
	case gateway.ToolModels:
		if in, ok := p.Arguments.(gateway.ModelsInput); !ok || in.Fresh {
			panic("fresh model survey")
		}
		return f.models, f.modelErr
	default:
		panic("effectful check call")
	}
}
func (f *checkFixture) Close() error { f.closed = true; return f.closeErr }
func checkGood() *checkFixture {
	return &checkFixture{
		apps:   &mcp.CallToolResult{StructuredContent: gateway.ApplicationsOutput{Outcome: "page", ObservedUnixMS: 1, Applications: []gateway.Application{}}},
		models: &mcp.CallToolResult{StructuredContent: gateway.ModelsOutput{ObservedUnixMS: 1, Models: []gateway.Model{}}},
	}
}

func checkTestRoot(t *testing.T) func() (string, error) {
	t.Helper()
	root := t.TempDir()
	return func() (string, error) { return root, nil }
}

func TestCheckDefaultModeAndEndpointEnvironment(t *testing.T) {
	flags, state, check, _ := commandFlags(&bytes.Buffer{}, "kept-state")
	if err := flags.Parse(nil); err != nil || *check || *state != "kept-state" {
		t.Fatalf("default changed: %v", err)
	}
	if err := flags.Parse([]string{"--check"}); err != nil || !*check {
		t.Fatalf("check flag: %v", err)
	}
	env := checkEnvironment([]string{"OTHER=kept", "abstraction_runtime_endpoint=old", "ABSTRACTION_RUNTIME_ENDPOINT="}, "selected")
	if strings.Join(env, ";") != "OTHER=kept;ABSTRACTION_RUNTIME_ENDPOINT=selected" {
		t.Fatal(env)
	}
}

func TestCheckBothReadProbesAndTemporaryCleanup(t *testing.T) {
	for _, which := range []string{"none", "applications", "models", "both", "close"} {
		t.Run(which, func(t *testing.T) {
			f := checkGood()
			secret := errors.New("private-app model credential password")
			if which == "applications" || which == "both" {
				f.appErr = secret
			}
			if which == "models" || which == "both" {
				f.modelErr = secret
			}
			if which == "close" {
				f.closeErr = secret
			}
			var state string
			var output bytes.Buffer
			exit := checkConnection(context.Background(), &output, func(context.Context) (string, error) { return "selected", nil }, func(_ context.Context, endpoint, dir string) (checkSession, error) {
				if endpoint != "selected" {
					t.Fatal(endpoint)
				}
				state = dir
				if _, err := os.Stat(dir); err != nil {
					t.Fatal(err)
				}
				return f, nil
			}, checkTestRoot(t))
			if (exit == 0) != (which == "none") || !f.closed || strings.Join(f.calls, ",") != gateway.ToolApplications+","+gateway.ToolModels {
				t.Fatalf("exit=%d closed=%v calls=%v", exit, f.closed, f.calls)
			}
			_, statErr := os.Stat(state)
			if which == "close" {
				if statErr != nil || !strings.Contains(output.String(), "temporary state retained") {
					t.Fatalf("uncertain child state removed: %v %s", statErr, output.String())
				}
				t.Cleanup(func() {
					if err := os.RemoveAll(state); err != nil {
						t.Error(err)
					}
				})
			} else if !os.IsNotExist(statErr) {
				t.Fatalf("positively closed child state retained: %v", statErr)
			}
			if strings.Contains(output.String(), "password") || !strings.Contains(output.String(), "MCP: connected") {
				t.Fatal(output.String())
			}
		})
	}
}

func TestCheckInitializationUncertaintyRetainsTemporaryState(t *testing.T) {
	var state string
	var output bytes.Buffer
	exit := checkConnection(context.Background(), &output, func(context.Context) (string, error) { return "selected", nil },
		func(_ context.Context, _, dir string) (checkSession, error) {
			state = dir
			return nil, errors.New("uncertain child")
		}, checkTestRoot(t))
	if exit == 0 || !strings.Contains(output.String(), "temporary state retained") {
		t.Fatal(exit, output.String())
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatal(err)
	}
	// This inert connector launched no child; the fixture may release its state.
	if err := os.RemoveAll(state); err != nil {
		t.Fatal(err)
	}
}

func TestCheckTemporaryOwnerRefusalNeverFallsBackOrLaunches(t *testing.T) {
	for _, failure := range []error{nil, errors.New("owner refusal")} {
		var out bytes.Buffer
		exit := checkConnection(context.Background(), &out,
			func(context.Context) (string, error) { return "selected", nil },
			func(context.Context, string, string) (checkSession, error) {
				t.Fatal("child launched without owned tmp root")
				return nil, nil
			},
			func() (string, error) { return "", failure })
		if exit == 0 || !strings.Contains(out.String(), "owned temporary root unavailable") {
			t.Fatal(exit, out.String())
		}
	}
}

func TestCheckMissingBootstrapNeverLaunchesOrCalls(t *testing.T) {
	for _, endpoint := range []string{"", "ignored"} {
		var out bytes.Buffer
		exit := checkConnection(context.Background(), &out, func(context.Context) (string, error) {
			if endpoint == "ignored" {
				return endpoint, errors.New("missing")
			}
			return endpoint, nil
		}, func(context.Context, string, string) (checkSession, error) {
			t.Fatal("child launched without selection")
			return nil, nil
		}, func() (string, error) { t.Fatal("temporary owner called without selection"); return "", nil })
		if exit == 0 || !strings.Contains(out.String(), "MCP not attempted") {
			t.Fatal(exit, out.String())
		}
	}
	if _, err := connectCheckChild(context.Background(), "", t.TempDir()); err == nil {
		t.Fatal("empty endpoint accepted")
	}
}

func TestCheckStrictResultsAndReadOnlyDeclarations(t *testing.T) {
	good := checkGood()
	if n, err := checkApplications(good.apps, nil); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	for _, data := range []any{nil, map[string]any{}, map[string]any{"outcome": "page", "observed_unix_ms": 1, "applications": nil}, map[string]any{"outcome": "page", "observed_unix_ms": 1, "applications": []any{}, "extra": true}, map[string]any{"outcome": "page", "observed_unix_ms": 1, "applications": "bad"}, map[string]any{"outcome": "page", "observed_unix_ms": 1, "applications": []any{map[string]any{"unexpected": true}}}} {
		if _, err := checkApplications(&mcp.CallToolResult{StructuredContent: data}, nil); err == nil {
			t.Fatalf("accepted %#v", data)
		}
	}
	if _, err := checkModels(&mcp.CallToolResult{IsError: true}, nil); !errors.Is(err, errCheckRefusal) {
		t.Fatal(err)
	}
	if _, err := checkModels(&mcp.CallToolResult{StructuredContent: map[string]any{"observed_unix_ms": 1}}, nil); err == nil {
		t.Fatal("missing models accepted")
	}
	tools, _ := good.ListTools(context.Background(), nil)
	if !checkReadTools(tools) {
		t.Fatal("valid tools refused")
	}
	tools.Tools[0].Annotations.ReadOnlyHint = false
	if checkReadTools(tools) {
		t.Fatal("effectful declaration accepted")
	}
	tools.Tools[0].Annotations.ReadOnlyHint = true
	tools.Tools = append(tools.Tools, tools.Tools[0])
	if checkReadTools(tools) {
		t.Fatal("duplicate read tool accepted")
	}
}

func TestCheckSDKChild(t *testing.T) {
	if os.Getenv("OA_MCP_CHECK_TEST_CHILD") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "inert-check-fixture", Version: "test"}, nil)
	err := s.Run(context.Background(), &mcp.StdioTransport{})
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestCheckSDKInitializationAndEOFCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCheckSDKChild$")
	// The race runtime's default one-second exit pause matches the SDK's EOF
	// grace period. Keep race detection enabled and omit only the fixture pause.
	cmd.Env = append(os.Environ(), "OA_MCP_CHECK_TEST_CHILD=1",
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	session, err := connectCheckCommand(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Error(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() || !cmd.ProcessState.Success() {
		t.Fatalf("child not joined: %v", cmd.ProcessState)
	}
}
