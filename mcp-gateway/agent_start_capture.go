package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
)

type agentStartExecution struct {
	ExitCode   int
	ExitKnown  bool
	DurationMS int64
}

// Vendor fields are program-reported metadata. Native inbox ownership supplies
// readiness independently; neither a vendor status nor an exit proves delivery.
type agentStartExitMetadata struct {
	Schema           string            `json:"schema"`
	Provenance       string            `json:"provenance"`
	VendorStatus     string            `json:"vendor_status"`
	VendorError      *bool             `json:"vendor_error,omitempty"`
	VendorDurationMS *uint64           `json:"vendor_duration_ms,omitempty"`
	EstimatedCostUSD string            `json:"estimated_cost_usd,omitempty"`
	Usage            map[string]uint64 `json:"usage,omitempty"`
	ExitCode         *int              `json:"exit_code,omitempty"`
	DurationMS       int64             `json:"duration_ms"`
	StdoutBytes      uint64            `json:"stdout_bytes"`
	StderrBytes      uint64            `json:"stderr_bytes"`
	Truncated        bool              `json:"truncated"`
}

func agentStartMetadata(raw []byte, execution *agentStartExecution, stdout, stderr uint64, truncated bool) agentStartExitMetadata {
	out := agentStartExitMetadata{Schema: "oa.agent.exit@1", Provenance: "program_reported", VendorStatus: "unrecognized", StdoutBytes: stdout, StderrBytes: stderr, Truncated: truncated}
	if execution != nil {
		out.DurationMS = execution.DurationMS
		if execution.ExitKnown {
			code := execution.ExitCode
			out.ExitCode = &code
		}
	}
	if len(raw) > 1<<20 {
		return out
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var fields map[string]any
	if dec.Decode(&fields) != nil || dec.Decode(new(any)) != io.EOF {
		return out
	}
	if fields["type"] != "result" {
		return out
	}
	if status, ok := fields["subtype"].(string); ok {
		switch status {
		case "success", "error", "error_during_execution", "error_max_budget_usd", "error_max_turns", "error_max_structured_output_retries":
			out.VendorStatus = status
		}
	}
	if failed, ok := fields["is_error"].(bool); ok {
		out.VendorError = &failed
	}
	integer := func(value any) (uint64, bool) {
		n, ok := value.(json.Number)
		if !ok {
			return 0, false
		}
		v, err := strconv.ParseUint(string(n), 10, 53)
		return v, err == nil
	}
	if ms, ok := integer(fields["duration_ms"]); ok {
		out.VendorDurationMS = &ms
	}
	if n, ok := fields["total_cost_usd"].(json.Number); ok {
		if v, err := strconv.ParseFloat(string(n), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1000000 {
			out.EstimatedCostUSD = strconv.FormatFloat(v, 'f', 6, 64)
		}
	}
	if usage, ok := fields["usage"].(map[string]any); ok {
		out.Usage = map[string]uint64{}
		for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
			if n, ok := integer(usage[key]); ok {
				out.Usage[key] = n
			}
		}
	}
	return out
}

func submitAgentStartMetadata(ctx context.Context, metadata agentStartExitMetadata) (string, error) {
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	machine := receiverStartMachine()
	sink, err := machine.ResolveLog(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return "incomplete", err
	}
	attrs, err := agentStartLogAttributes(metadata)
	if err != nil {
		return "incomplete", err
	}
	var correlation [16]byte
	if _, err := rand.Read(correlation[:]); err != nil {
		return "incomplete", err
	}
	attrs["capture_id"] = hex.EncodeToString(correlation[:]) // correlation only, never authority
	err = sink.LogContext(call, 0, "Program-reported headless exit", attrs)
	if err != nil {
		return "incomplete", err
	}
	program, pathErr := os.Executable()
	reader, readErr := machine.ResolveLogApplicationReader(call, client.Requirements{Scope: client.ScopeLocal})
	if pathErr == nil && readErr == nil && retainedAgentStartMetadata(call, reader, identity.NormalizeSubjectProgram(program), attrs) {
		return "retained", nil
	}
	// LOG-P5 Write is one-way. Denied/missing/mismatched readback leaves
	// submission incomplete; no new read grant or first-use question is raised.
	return "submitted_unconfirmed", nil
}

type agentStartHistory interface {
	ReadContext(context.Context, logclient.ApplicationLogTarget, string, int64, int64) (logclient.ApplicationLogPage, error)
}

func retainedAgentStartMetadata(ctx context.Context, reader agentStartHistory, program string, attrs map[string]string) bool {
	target := logclient.ApplicationLogTarget{Selection: logwire.ApplicationLogSelectionOneApp, Program: program}
	cursor := ""
	for pageNumber := 0; pageNumber < 16; pageNumber++ {
		page, err := reader.ReadContext(ctx, target, cursor, 64, 65536)
		if err != nil || page.Outcome != logwire.HistoryPageOutcomePage {
			return false
		}
		for _, record := range page.Records {
			if record.Attrs["capture_id"] == attrs["capture_id"] && record.Attrs["oa_event"] == attrs["oa_event"] && record.Attrs["exit_metadata"] == attrs["exit_metadata"] {
				return true
			}
		}
		cursor = page.Next
		if page.AtEnd {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return false
}

func agentStartLogAttributes(metadata agentStartExitMetadata) (map[string]string, error) {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return map[string]string{"oa_event": "agent.start.exit", "exit_metadata": string(raw)}, nil
}
