package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// claudeRunning reports a running Claude desktop app or Claude Code CLI. A
// missing OA inbox proves no host absence (AB-1s C2), so while one runs the
// original conversation may be open and is never resumed in place.
var claudeRunning = nativecli.ClaudeRunning

// claudeClosed refuses with claude_open unless no Claude process runs. An
// unreadable inventory counts as running.
func claudeClosed(ctx context.Context) error {
	if running, err := claudeRunning(ctx); err != nil || running {
		return sessionRefusal("claude_open")
	}
	return nil
}

// resolveSelectedColdClaudeStart answers a question in a retained Claude
// conversation the runtime selected on the owned channel. With no Claude
// process running, the original is resumed in place. While one runs the
// original may be open, so the turn runs in a copy (`--fork-session`) that OA
// holds under the chat's context; later questions resume that copy while the
// original's change time is unchanged, and a changed original is copied
// again. The copy's helper reads the original chat's inbox.
func resolveSelectedColdClaudeStart(ctx context.Context, p startprofile.Profile, helper, configuration string, selected agentStartCLI, environment []string, custody agentStartCustody, version string, check func(context.Context) error, closeAll func() error, held *[]setupIdentityRef) (*agentStartRecipe, error) {
	fail := func(err error) (*agentStartRecipe, error) { return nil, errors.Join(err, closeAll()) }
	launch, err := custody.cold.Launch()
	if err != nil {
		return fail(err)
	}
	inPlace := claudeClosed(ctx) == nil
	sourceChange := int64(0)
	if !inPlace {
		// The original's change time decides whether its copy is current.
		if !filepath.IsAbs(configuration) {
			return fail(sessionRefusal("native_configuration_unavailable"))
		}
		if !agentSessionUUID.MatchString(launch.Session) {
			return fail(sessionRefusal("cold_selection_invalid"))
		}
		info, err := os.Stat(filepath.Join(configuration, "projects", claudeProjectName(launch.Cwd), launch.Session+".jsonl"))
		if err != nil || !info.Mode().IsRegular() {
			return fail(sessionRefusal("session_unknown"))
		}
		sourceChange = info.ModTime().UnixMilli()
	}
	args, folder, err := coldClaudeResumeArguments(launch, custody.cold.CwdKey, version, helper, p.StateDir, custody.cold.Context, p.BudgetCents)
	if err != nil {
		return fail(err)
	}
	directory, _, err := setupIdentity(ctx, folder, true)
	if err != nil {
		return fail(err)
	}
	if directory.FileID().Key() != custody.cold.CwdKey {
		directory.Close()
		return fail(sessionRefusal("cold_cwd_changed"))
	}
	*held = append(*held, &directory)
	r := &agentStartRecipe{profile: p.Name, context: p.Context, folder: folder, timeout: time.Duration(p.TimeoutMS) * time.Millisecond, outputBytes: int(p.OutputBytes), memoryBytes: p.MemoryBytes, approved: true, check: check, close: closeAll, execution: &agentStartExecution{}, capture: submitAgentStartMetadata}
	r.run = func(ctx context.Context, _ string, out io.Writer, memory uint64) error {
		if err := check(ctx); err != nil {
			return err
		}
		capture, ok := out.(*agentStartOutput)
		if !ok {
			return errors.New("capture_unavailable")
		}
		if !inPlace {
			lease, err := beginAgentFork(ctx, p.StateDir, custody.binding, custody.cold.Context, "copy:"+launch.Session+"@"+strconv.FormatInt(sourceChange, 10))
			if err != nil {
				return err
			}
			copyArgs, _, err := coldClaudeResumeArguments(launch, custody.cold.CwdKey, version, helper, p.StateDir, custody.cold.Context, p.BudgetCents, lease.record.Session)
			if err != nil {
				return err
			}
			if copyArgs, err = coldClaudeCopySession(copyArgs, agentAttachArguments(lease, launch.Session)); err != nil {
				return err
			}
			if err := check(ctx); err != nil {
				return err
			}
			r.delivery = agentDeliveryCopy
			if err := runOwnedAgentStartStreams(ctx, selected.Program(), folder, copyArgs, coldClaudeResumeEnvironment(environment), out, agentStartStderr{capture}, memory, r.execution); err != nil {
				return err
			}
			if !r.execution.ExitKnown || r.execution.ExitCode != 0 {
				return sessionRefusal("session_result_unconfirmed")
			}
			if err := lease.observe(capture.data); err != nil {
				return err
			}
			return lease.commit(ctx, check)
		}
		if _, _, err := coldClaudeResumeArguments(launch, custody.cold.CwdKey, version, helper, p.StateDir, custody.cold.Context, p.BudgetCents); err != nil {
			return err
		}
		// Rechecked immediately before the spawn: a Claude started since the
		// choice may hold the original open.
		if err := claudeClosed(ctx); err != nil {
			return err
		}
		r.delivery = agentDeliveryResumed
		if err := runOwnedAgentStartStreams(ctx, selected.Program(), folder, args, coldClaudeResumeEnvironment(environment), out, agentStartStderr{capture}, memory, r.execution); err != nil {
			return err
		}
		if !r.execution.ExitKnown || r.execution.ExitCode != 0 {
			return sessionRefusal("session_result_unconfirmed")
		}
		confirmed, err := nativeAgentSessionEnvelope(capture.data, launch.Session)
		if err != nil {
			return err
		}
		if !confirmed {
			return sessionRefusal("session_result_unconfirmed")
		}
		return nil
	}
	return r, nil
}

// coldClaudeCopySession replaces the in-place `--resume <original>` with the
// copy's session flags: the first run forks the original under OA's session
// ID, later runs resume that copy.
func coldClaudeCopySession(args, session []string) ([]string, error) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--resume" {
			return append(append(append([]string(nil), args[:i]...), session...), args[i+2:]...), nil
		}
	}
	return nil, sessionRefusal("cold_selection_invalid")
}

// The resumed vendor supplies its own session and messaging state. Enclosing
// host/session controls and credentials must not reach its probes or execution.
func coldClaudeResumeEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if strings.HasPrefix(name, "CLAUDE_AGENT_SDK_") {
			continue
		}
		switch name {
		case "CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CLAUDE_CODE_HOST_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_PID",
			"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH",
			"CLAUDE_CODE_ACCOUNT_UUID", "CLAUDE_CODE_ORGANIZATION_UUID", "CLAUDE_CODE_USER_EMAIL",
			"CLAUDE_CODE_SUBSCRIPTION_TYPE", "CLAUDE_CODE_RATE_LIMIT_TIER", "CLAUDE_CODE_OAUTH_SCOPES":
			continue
		default:
			clean = append(clean, entry)
		}
	}
	return clean
}
