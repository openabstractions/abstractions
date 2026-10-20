package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func TestAgentStartExitLogOmitsPeerAnswerAndSecrets(t *testing.T) {
	raw := []byte(`{"type":"result","subtype":"success","is_error":false,"session_id":"11111111-1111-4111-8111-111111111111","configDirectory":"PRIVATE_CONFIG_DIRECTORY","result":"PRIVATE_PEER_ANSWER","prompt":"PRIVATE_PROMPT","access_token":"PRIVATE_AUTH_TOKEN","tool_result":"PRIVATE_TOOL_BODY","duration_ms":123,"total_cost_usd":0.15,"usage":{"input_tokens":3,"output_tokens":5,"api_key":"PRIVATE_API_KEY"}}`)
	metadata := agentStartMetadata(raw, &agentStartExecution{ExitCode: 0, ExitKnown: true, DurationMS: 140}, uint64(len(raw)), 12, false)
	attrs, err := agentStartLogAttributes(metadata)
	if err != nil {
		t.Fatal(err)
	}
	record := logwire.Record{Schema: 1, Time: "2026-10-29T00:00:00.000000Z", Level: 0, Msg: "Program-reported headless exit", Attrs: attrs}
	encoded := string(logwire.Encode(&record))
	for _, secret := range []string{"11111111-1111-4111-8111-111111111111", "PRIVATE_CONFIG_DIRECTORY", "PRIVATE_PEER_ANSWER", "PRIVATE_PROMPT", "PRIVATE_AUTH_TOKEN", "PRIVATE_TOOL_BODY", "PRIVATE_API_KEY"} {
		if strings.Contains(encoded, secret) {
			t.Fatal("private text entered LogEntry", secret)
		}
	}
	if metadata.VendorStatus != "success" || metadata.Usage["input_tokens"] != 3 || metadata.Usage["output_tokens"] != 5 || metadata.EstimatedCostUSD != "0.150000" || metadata.ExitCode == nil || metadata.StderrBytes != 12 {
		t.Fatal(metadata)
	}
	for _, bad := range []string{`{"type":"result","subtype":"PRIVATE_STATUS","usage":{"input_tokens":"PRIVATE_TOKEN"}}`, `{"type":"result","usage":{"input_tokens":-1},"total_cost_usd":1e300}`, `PRIVATE_RAW_BODY`} {
		projected := agentStartMetadata([]byte(bad), nil, 5, 6, true)
		data, _ := json.Marshal(projected)
		if strings.Contains(string(data), "PRIVATE_") || projected.VendorStatus != "unrecognized" {
			t.Fatal(string(data))
		}
	}
}

type agentStartHistoryFunc func(context.Context, logclient.ApplicationLogTarget, string, int64, int64) (logclient.ApplicationLogPage, error)

func (f agentStartHistoryFunc) ReadContext(ctx context.Context, target logclient.ApplicationLogTarget, cursor string, records, bytes int64) (logclient.ApplicationLogPage, error) {
	return f(ctx, target, cursor, records, bytes)
}

func TestAgentStartRetentionRequiresExactNativeTargetAndEvent(t *testing.T) {
	attrs := map[string]string{"capture_id": "non-authoritative-correlation", "oa_event": "agent.start.exit", "exit_metadata": "safe-metadata"}
	program := filepath.Join(t.TempDir(), "kept-helper.exe")
	for _, variant := range []string{"exact", "denied", "mismatch"} {
		t.Run(variant, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := agentStartHistoryFunc(func(_ context.Context, target logclient.ApplicationLogTarget, _ string, records, bytes int64) (logclient.ApplicationLogPage, error) {
				if target.Selection != logwire.ApplicationLogSelectionOneApp || target.Program != program || records != 64 || bytes != 65536 {
					t.Fatal("widened native log read", target)
				}
				if variant == "denied" {
					return logclient.ApplicationLogPage{Outcome: logwire.HistoryPageOutcomeForbidden}, nil
				}
				copyAttrs := map[string]string{}
				for k, v := range attrs {
					copyAttrs[k] = v
				}
				if variant == "mismatch" {
					copyAttrs["exit_metadata"] = "other-event"
					cancel()
				}
				return logclient.ApplicationLogPage{Outcome: logwire.HistoryPageOutcomePage, Records: []logwire.Record{{Attrs: copyAttrs}}, AtEnd: true, Next: "opaque-cursor"}, nil
			})
			if found := retainedAgentStartMetadata(ctx, reader, program, attrs); found != (variant == "exact") {
				t.Fatal(variant, found)
			}
		})
	}
}

func TestAgentStartCaptureSubmissionAndFailureRemainIncomplete(t *testing.T) {
	for _, failed := range []bool{false, true} {
		resolver := fixtureStartRecipe(func(context.Context, string, io.Writer, uint64) error { return nil })
		result := runAgentStart(context.Background(), "fixture", "queue-1", func(ctx context.Context, p, c string) (*agentStartRecipe, error) {
			r, e := resolver(ctx, p, c)
			r.capture = func(context.Context, agentStartExitMetadata) (string, error) {
				if failed {
					return "", errors.New("sink down")
				}
				return "submitted_unconfirmed", nil
			}
			return r, e
		})
		want := "submitted_unconfirmed"
		if failed {
			want = "incomplete"
		}
		if result.Capture != want {
			t.Fatal(result)
		}
	}
}

func TestAgentStartDirectInvocationHasNoRuntimeAdmission(t *testing.T) {
	folder := t.TempDir()
	p := startprofile.Profile{Name: "claude-v1", Context: "registered-inbox", Folder: folder, StateDir: filepath.Join(folder, "state"), CLIFileID: "registered-saved-image", Model: "registered-model", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	// Even the complete canonical operator recipe carries no invocation permit.
	resolve := registeredAgentStartResolver(p.Arguments())
	if r, err := resolve(context.Background(), p.Name, p.Context); r != nil || err == nil || err.Error() != "runtime_start_admission_unavailable" {
		t.Fatal(r, err)
	}
	if runtime.GOOS != "windows" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(folder, "oa-claude-integration.exe")
	if err := os.WriteFile(helper, raw, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helper, p.Arguments()...)
	cmd.Env = append(os.Environ(), "OA_RECEIVER_START_DIRECT_FIXTURE=1")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || len(output) != 0 {
		t.Fatal("direct unadmitted helper invocation", string(output), err)
	}
}
