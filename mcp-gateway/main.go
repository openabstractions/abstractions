package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

func commandFlags(output io.Writer, defaultState string) (*flag.FlagSet, *string, *bool, *bool) {
	flags := flag.NewFlagSet("openabstractions-mcp", flag.ContinueOnError)
	flags.SetOutput(output)
	state := flags.String("state-dir", defaultState, "absolute directory for durable opaque job handles")
	check := flags.Bool("check", false, "check MCP and read-only OA applications/models access, then exit (at most 20 seconds)")
	messagesOnly := flags.Bool("messages-only", false, "expose only the six messaging tools; requires an admitted native inbox and explicit inbox-context; grants no rights")
	flags.Usage = func() {
		//unchecked: usage text on a help/error path; a failed write to the flag package's own output writer has nothing further to report
		fmt.Fprintln(output, `usage: openabstractions-mcp [--check] [--state-dir ABSOLUTE_PATH]
       openabstractions-mcp connect claude --state-dir ABS [--claude ABS_EXE] [--project ABS]
       openabstractions-mcp disconnect claude --state-dir ABS [--claude ABS_EXE] [--project ABS]
       openabstractions-mcp connections claude --state-dir ABS
        openabstractions-mcp plugin claude install|update|remove --project ABS --state-dir ABS --claude ABS_EXE
       openabstractions-mcp connect codex --state-dir ABS --codex ABS_EXE
       openabstractions-mcp disconnect codex --state-dir ABS --codex ABS_EXE
       openabstractions-mcp start --profile NAME --context NAME

Local stdio MCP server. It exposes this fixed tool inventory:
  oa_applications_list       read visible application instances and contexts
  oa_applications_describe   describe permitted AppModel applications and contexts
  oa_applications_enrollment_read read native enrollment under caller permissions
  oa_api_describe       describe projected public OA methods and generated schemas
  oa_api_read           read a public OA method under this native caller's authority
  oa_applications_commands_list list admitted commands for a live instance
  oa_applications_command_call call one command through native OA mediation
  oa_applications_open       open a kept application through its declared verb
  oa_models_list             read servable model families
  oa_models_connect          ask the person for named model setup and read back readiness
  oa_asks_request_answering  request this native program's person-approved enrollment
  oa_asks_list_answerable    read currently answerable routine questions
  oa_asks_list_choices       read shared ordinary choices
  oa_asks_answer             answer one listed routine question
  oa_asks_answer_choice      answer one shared ordinary choice
  oa_setup_fit               rank the model catalogue for one use on this computer
  oa_logs_follow             follow bounded application or own-request OA log pages
  oa_setup_prepare           ask OA to set up a use; the person answers OA's question
  oa_inference_complete      run one bounded text inference call
  oa_inference_requests      read retained inference selection and placement records
  oa_image_generate          make one picture and save it in the Pictures folder
  oa_video_generate          make one video and save it in the Videos folder
  oa_inference_job_submit    start a video job (advanced; unlisted)
  oa_inference_job_status    read a video job's state and result by its handle
  oa_inference_job_cancel    stop a video job by its handle
  oa_inference_job_diagnose  say why a video job failed, by its handle
  oa_jobs_follow        observe one accepted job with an opaque cursor and bounded wait

oa_image_generate is listed while the person's picture policy names a local
model when the session starts, and oa_video_generate while the video policy
names one; listing tools asks nothing.

Authority belongs to the OS-bound OpenAbstractions identity of this gateway
executable. MCP clientInfo, request metadata and parent process IDs never grant
or select OA rights. Sessions using the same executable share that configured
integration principal. Application discovery and models are read-only;
application commands, completion, submission and cancellation can cause effects.

For kept agent connections, oa_messages_inboxes_list shows your own chat, the
chats you have exchanged messages with, a count of other chats per program, and
local agents that need permission.
When inboxes, other_chats and needs_permission are all empty, no agent can be messaged right now. To send, call oa_messages_send with receiver and body. oa_messages_prepare checks reach or permission first and sends nothing; its outcome gives the next step.

Supported MCP protocol versions: 2026-07-28, 2025-11-25.

--check reports MCP connection separately from OA read availability. It uses
temporary state, reads only applications and cached models, and does not activate
the runtime, grant rights or submit work. It exits within a twenty-second budget;
zero requires both reads and confirmed child cleanup. Counts and fixed error
categories omit application/model names and raw service errors.

connect/disconnect claude use the installed Claude Code CLI for a user-wide
OpenAbstractions connection. Explicit --project retains project scope. They do not grant OA rights or assert an active
MCP connection. Use connect claude --help for setup options.

connect/disconnect codex use the account's shared Codex configuration for
desktop, CLI and IDE. They grant no OA rights or live-connection proof.
They also install/remove the owned oa-codex-session-start plugin through Codex.
The person reviews and trusts it once in /hooks; hook status is separate from
MCP setup. Startup/resume delivery is best effort. The native Codex composition
alone adds the internal empty-input hook tool (oa_codex_session_start); it
observes host _meta.threadId, grants nothing and needs no ordinary model call.

options:`)
		flags.PrintDefaults()
	}
	return flags, state, check, messagesOnly
}

func newGatewayServer(backend gateway.Backend, messagesOnly bool, narrow ...bool) (*mcp.Server, error) {
	server, err := newGatewayServerFor(backend, messagesOnly, narrow...)
	if err != nil {
		return nil, err
	}
	server.AddReceivingMiddleware(movedImageMiddleware(ownMovedImageSentence))
	return server, nil
}

func newGatewayServerFor(backend gateway.Backend, messagesOnly bool, narrow ...bool) (*mcp.Server, error) {
	replyOnly := len(narrow) != 0 && narrow[0]
	if !messagesOnly && !replyOnly {
		return gateway.NewServer(backend), nil
	}
	receiver, ok := backend.(gateway.MessagesReceiverBackend)
	if !ok {
		return nil, errors.New("messages-only requires an admitted native inbox")
	}
	if replyOnly {
		return gateway.NewMessagesReplyServer(receiver), nil
	}
	return gateway.NewMessagesReceiverServer(receiver), nil
}

func messagesReplyOnlyFlag(flags *flag.FlagSet) *bool {
	return flags.Bool("messages-reply-only", false, "expose only Messages wait/reply for an admitted native inbox; grants no rights")
}

func validateMessagesReplyOnlyOptions(replyOnly, messagesOnly, check bool, inboxContext string) error {
	if replyOnly && messagesOnly {
		return errors.New("choose one Messages subset")
	}
	return validateMessagesReceiverOptions(replyOnly || messagesOnly, check, inboxContext)
}

func validateMessagesReceiverOptions(messagesOnly, check bool, inboxContext string) error {
	if messagesOnly && (check || inboxContext == "") {
		return errors.New("messages-only requires explicit inbox-context and cannot be used with check")
	}
	return nil
}

// gatewayMachine selects the runtime the gateway serves. The shipped binary
// discovers the installed runtime; only the journey fixture compiled into this
// package's test binary replaces it with an isolated runtime it names
// (release/testing/journeys.py).
var gatewayMachine = facade.Discover

func main() {
	// A successor after a connector update starts as this process started:
	// runOpenCodeChatServer later removes the OpenCode adapter key from the
	// environment (agent_handover.go).
	startArgs, startEnvironment := append([]string(nil), os.Args[1:]...), os.Environ()
	if len(os.Args) > 1 && os.Args[1] == "declare-companions" {
		os.Exit(declareCompanionsCommand(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "start" {
		if len(os.Args) == 3 && (os.Args[2] == "--help" || os.Args[2] == "-h") {
			os.Exit(agentStartCommand(os.Args[2:], os.Stdout, os.Stderr, nil))
		}
		os.Exit(ownedReceiverStartCommand(os.Args[1:], os.Stdin, os.Stdout, receiverStartConfirmation))
	}
	if len(os.Args) > 1 && os.Args[1] == "watch-permission" {
		os.Exit(claudeWatchCommand(os.Args[1:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "update" {
		os.Exit(agentTransitionCommand(os.Args[1:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "plugin" {
		os.Exit(claudePluginCommand(os.Args[1:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 2 && os.Args[2] == "codex" && (os.Args[1] == "connect" || os.Args[1] == "disconnect" || os.Args[1] == "connections") {
		os.Exit(codexSetupCommand(os.Args[1:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "connections" {
		os.Exit(claudeConnectionsCommand(os.Args[1:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && (os.Args[1] == "connect" || os.Args[1] == "disconnect") {
		os.Exit(claudeSetupCommand(os.Args[1:], os.Stdout, os.Stderr))
	}
	config, err := os.UserConfigDir()
	if err != nil {
		//unchecked: last diagnostic before os.Exit; a failed write to stderr has no fallback destination
		fmt.Fprintln(os.Stderr, "openabstractions-mcp:", err)
		os.Exit(1)
	}
	flags, state, check, messagesOnly := commandFlags(os.Stderr, filepath.Join(config, "OpenAbstractions", "mcp-gateway"))
	replyOnly := messagesReplyOnlyFlag(flags)
	inboxContext := flags.String("inbox-context", "", "explicit recipe routing context; native inbox admission remains required")
	claudeResume := flags.String("claude-resume-binding", "", "fixed recorded Claude resume correlation; reply-only startup, grants no authority")
	claudeResumeCwd := flags.String("claude-resume-cwd-key", "", "recorded native cwd identity for fixed Claude resume correlation")
	claudeResumeCopy := flags.String("claude-resume-copy", "", "with -claude-resume-binding: OA's session ID of a copy of that conversation, which reads its inbox")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return
		}
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		//unchecked: last diagnostic before os.Exit; a failed write to stderr has no fallback destination
		fmt.Fprintln(os.Stderr, "openabstractions-mcp: unexpected arguments")
		os.Exit(2)
	}
	if *inboxContext != "" && !agentRoutingContext.MatchString(*inboxContext) {
		//unchecked: final error diagnostic before exit
		fmt.Fprintln(os.Stderr, "openabstractions-mcp: invalid inbox context")
		os.Exit(2)
	}
	if err := validateMessagesReplyOnlyOptions(*replyOnly, *messagesOnly, *check, *inboxContext); err != nil {
		fmt.Fprintln(os.Stderr, "openabstractions-mcp:", err)
		os.Exit(2)
	}
	cold, coldErr := parseClaudeColdBinding(*claudeResume, *claudeResumeCwd, *inboxContext, *replyOnly)
	if coldErr == nil && *claudeResumeCopy != "" {
		cold, coldErr = cold.withCopy(*claudeResumeCopy)
	}
	if coldErr != nil {
		fmt.Fprintln(os.Stderr, "openabstractions-mcp:", coldErr)
		os.Exit(2)
	}
	program, programErr := os.Executable()
	if cold != nil && (programErr != nil || nativecli.AgentApplicationBase(dedicatedAgentApplication(program)) != "oa-claude-integration") {
		fmt.Fprintln(os.Stderr, "openabstractions-mcp: Claude resume requires its native integration")
		os.Exit(2)
	}
	if *check {
		os.Exit(connectionCheck(os.Stdout))
	}
	machine := gatewayMachine()
	backend, err := gateway.NewOA(machine, *state)
	if err != nil {
		//unchecked: last diagnostic before os.Exit; a failed write to stderr has no fallback destination
		fmt.Fprintln(os.Stderr, "openabstractions-mcp:", err)
		os.Exit(1)
	}
	if !*messagesOnly && !*replyOnly {
		// A video job that ended while no gateway ran gets its clip saved.
		backend.WatchUnsavedVideos()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if cold != nil {
		ctx = context.WithValue(ctx, claudeColdBindingKey{}, cold)
	}
	serve := func(ctx context.Context, backend gateway.Backend) error {
		server, err := newGatewayServer(backend, *messagesOnly, *replyOnly)
		if err != nil {
			return err
		}
		return runNativeMCPServer(ctx, server, backend, *state)
	}
	if programErr != nil {
		err = programErr
	} else if application := dedicatedAgentApplication(program); application != "" {
		diagnostic := newAgentDiagnostic(ctx, *state, program)
		if diagnostic != nil {
			diagnostic.parent = ctx
		}
		defer diagnostic.close()
		ctx = context.WithValue(ctx, agentDiagnosticKey{}, diagnostic)
		// The helper keeps the host's stdio across a connector update: once
		// Setup moves this image aside, the published connector takes over.
		startAgentStdio(ctx, program, startArgs, startEnvironment, backend.ApplicationsEnrollmentRead)
		sourceContext := *inboxContext
		if cold != nil {
			sourceContext = ""
		}
		contextName, contextErr := agentContext(sourceContext)
		if contextErr != nil {
			err = contextErr
		} else {
			status := &messagesInboxStatus{admission: new(agentToolAdmission)}
			var messages gateway.Backend
			messages, err = newMessagesBackendWithStatus(backend, machine, application, "", contextName, status)
			if err == nil {
				err = serveAgentConnection(ctx, func(ctx context.Context) {
					superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), readmit bool, stop func()) error {
						return runNativeAgentPresenceAttempt(ctx, machine, application, contextName, ready, status, readmit, stop)
					}, status, time.After, time.Now)
				}, messages, serve)
			}
			status.admission.retire()
		}
		observeAgentDiagnostic(ctx, "connection_end", err)
	} else {
		if cold != nil {
			err = errors.New("Claude resume requires its native integration")
		} else {
			err = serve(ctx, backend)
		}
	}
	if err != nil && ctx.Err() == nil {
		//unchecked: last diagnostic before os.Exit; a failed write to stderr has no fallback destination
		fmt.Fprintf(os.Stderr, "openabstractions-mcp pid=%d: %v\n", os.Getpid(), err)
		os.Exit(1)
	}
}
