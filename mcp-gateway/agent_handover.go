package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// A connector update publishes over a running helper (owner R16, 2026-11-09).
// Windows then names this process by its moved image, and the runtime denies
// it every admission, so its tools stay refused until the agent app starts a
// new helper. The person never restarts an agent app to fix messaging (owner,
// 2026-11-10). The helper therefore keeps the host's stdio and hands the
// connection to a successor: the published connector at the path this process
// started from, which the runtime reads enrolled at that path (and, on a
// signed build, signed by the moved image's publisher), run with the
// arguments and environment this process started with. The
// successor receives the host's recorded initialize and the latest host
// session binding the old helper observed, then every later line; the old
// process relays bytes and holds no admission of its own.

// agentHandoverID prefixes the JSON-RPC ids of the requests the old process
// replays to its successor. Their responses never reach the host.
const agentHandoverID = "oa-handover-"

// agentHandoverBudget bounds the successor's answer to each replayed request.
const agentHandoverBudget = 10 * time.Second

// agentHandoverReplayPause separates repeated binding starts to a successor
// that is still admitting its presence.
var agentHandoverReplayPause = 200 * time.Millisecond

// agentHandoverRetry is the delay before another handover after a failed one.
var agentHandoverRetry = 30 * time.Second

// agentHandoverCheck is how often a helper checks whether Setup moved its image.
const agentHandoverCheck = 2 * time.Second

// agentHandoverSentence answers a tool call while this moved helper hands the
// connection to its successor.
const agentHandoverSentence = "OpenAbstractions updated this connector and is moving this chat to it. Try again in a few seconds; nothing was done."

// agentHandoverSendSentence answers the retry of a Send whose outcome the old
// helper left uncertain. The successor cannot reconcile it.
const agentHandoverSendSentence = "OpenAbstractions reconnected since this send, so its outcome cannot be reconciled on this connection. Do not send again; tell the person the outcome is not known."

// agentSuccessor is one started successor process: its stdin and stdout, and
// stop, which ends it when the handover fails.
type agentSuccessor struct {
	in   io.WriteCloser
	out  io.ReadCloser
	stop func()
}

// agentBindingTools record the host's session binding, which a successor needs
// before it can serve the same chat, with the tool that ends it. Codex binds
// through each call's metadata and needs no replay.
var agentBindingTools = map[string]string{
	claudeSessionStartTool: claudeSessionEndTool,
	openCodeChatStartTool:  "",
}

// agentStdio is a dedicated helper's stdio for the whole host connection.
// Host lines go to the local MCP server until a handover, then to the
// successor. Whole lines from either reach the host's stdout one at a time.
type agentStdio struct {
	hostOut io.Writer
	outMu   sync.Mutex

	localIn io.ReadCloser

	// routeMu orders host lines with a handover.
	routeMu    sync.Mutex
	local      *io.PipeWriter
	successor  io.WriteCloser
	failedAt   time.Time
	switching  atomic.Bool
	ended      chan struct{}
	now        func() time.Time
	launch     func() (*agentSuccessor, error)
	imageMoved func() bool
	budget     time.Duration

	// recMu guards what the old helper observed of its own connection.
	recMu      sync.Mutex
	hostSeen   bool
	initialize json.RawMessage
	binding    json.RawMessage
	// starts and sends are host calls awaiting the local server's answer.
	starts map[string]json.RawMessage
	sends  map[string]string
	// uncertain are the sends whose local answer was unknown or unavailable.
	uncertain map[string]bool
	// noReplay latches after the host retired its session binding; fenced
	// latches after a final refusal of this process's current image.
	noReplay bool
	fenced   bool
}

// newAgentStdio starts reading host lines from in. launch starts a successor;
// nil disables handover.
func newAgentStdio(in io.Reader, out io.Writer, report func(string), launch func() (*agentSuccessor, error)) *agentStdio {
	reader, writer := io.Pipe()
	s := &agentStdio{hostOut: out, localIn: reader, local: writer, ended: make(chan struct{}), now: time.Now, launch: launch, imageMoved: ownImageMoved, budget: agentHandoverBudget,
		starts: map[string]json.RawMessage{}, sends: map[string]string{}, uncertain: map[string]bool{}}
	go func() {
		err := copyJSONRPCLines(in, hostLineWriter{s}, report)
		s.hostEnded(err)
	}()
	return s
}

// transport is the local MCP server's stdio.
func (s *agentStdio) transport() *mcp.IOTransport {
	return &mcp.IOTransport{Reader: s.localIn, Writer: &agentLineWriter{stdio: s}, MaxLineLength: agentStdioLineLimit + 1}
}

// agentRPC is the part of one JSON-RPC line the relay reads.
type agentRPC struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type agentCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// agentRequestKey is a request id's canonical JSON, the same for a host's
// request and the server's response to it.
func agentRequestKey(id json.RawMessage) string {
	var value any
	if len(id) == 0 || json.Unmarshal(id, &value) != nil || value == nil {
		return ""
	}
	key, _ := json.Marshal(value)
	return string(key)
}

// agentSendIdentity names a Send the way the gateway reconciles it: a
// one-call Send by receiver and content, an explicit Send by its key.
func agentSendIdentity(arguments json.RawMessage) string {
	var in gateway.MessagesSendInput
	if json.Unmarshal(arguments, &in) != nil {
		return ""
	}
	if in.Receiver != "" {
		key, _ := json.Marshal([]string{gateway.MessageReceiverName(in.Receiver), in.Context, in.Kind, in.Subject, in.Body})
		return "receiver:" + string(key)
	}
	if in.IdempotencyKey != "" {
		return "key:" + in.IdempotencyKey
	}
	return ""
}

type hostLineWriter struct{ s *agentStdio }

// Write receives exactly one validated host line from copyJSONRPCLines.
func (w hostLineWriter) Write(line []byte) (int, error) {
	s := w.s
	var message agentRPC
	_ = json.Unmarshal(line, &message)
	call := message.Method == "tools/call" && agentRequestKey(message.ID) != ""
	if call && s.switching.Load() {
		// A call during the switch answers at once instead of waiting for it.
		s.answerHost(message.ID, &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: agentHandoverSentence}}})
		return len(line), nil
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if s.successor != nil {
		if call && s.uncertainSend(message.Params) {
			out := wire.SendResult{Outcome: wire.OutcomeUnknown, Reason: "reconnected"}
			s.answerHost(message.ID, &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: string(wire.EncodeSendResult(&out))}, &mcp.TextContent{Text: agentHandoverSendSentence, Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}}}},
				StructuredContent: json.RawMessage(wire.EncodeSendResult(&out)),
			})
			return len(line), nil
		}
		// A successor that stopped reading has ended; its copier ends the
		// process. The host's line is lost with it.
		_, _ = s.successor.Write(line)
		return len(line), nil
	}
	s.recordHost(message)
	return s.local.Write(line)
}

// uncertainSend reports a Send call the old helper left uncertain.
func (s *agentStdio) uncertainSend(params json.RawMessage) bool {
	var call agentCallParams
	if json.Unmarshal(params, &call) != nil || call.Name != gateway.ToolMessagesSend {
		return false
	}
	identity := agentSendIdentity(call.Arguments)
	s.recMu.Lock()
	defer s.recMu.Unlock()
	return identity != "" && s.uncertain[identity]
}

// keepPendingSendsUncertainLocked marks each Send still unanswered at the
// switch uncertain, like an answered-uncertain one: it may have been
// accepted. Its entry stays, so a late local answer still settles it. Call
// with recMu held.
func (s *agentStdio) keepPendingSendsUncertainLocked() {
	for _, identity := range s.sends {
		s.uncertain[identity] = true
	}
}

// recordHost keeps what a successor needs from the host: its initialize
// request, when its protocol version has one, and the calls whose local
// answers decide the binding and the uncertain Sends. A host on protocol
// 2026-07-28 or later carries its client facts in each request's metadata
// and sends no initialize.
func (s *agentStdio) recordHost(message agentRPC) {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	s.hostSeen = true
	id := agentRequestKey(message.ID)
	switch message.Method {
	case "initialize":
		if id != "" {
			s.initialize = append(json.RawMessage(nil), message.Params...)
		}
	case "tools/call":
		var call agentCallParams
		if id == "" || json.Unmarshal(message.Params, &call) != nil {
			return
		}
		if _, ok := agentBindingTools[call.Name]; ok {
			s.starts[id] = append(json.RawMessage(nil), message.Params...)
			return
		}
		if call.Name == gateway.ToolMessagesSend {
			if identity := agentSendIdentity(call.Arguments); identity != "" {
				s.sends[id] = identity
			}
			return
		}
		for _, end := range agentBindingTools {
			if end == "" || call.Name != end {
				continue
			}
			// The host ended its association: a later observed start binds
			// again. A retired association is never replayed.
			s.binding = nil
			var arguments struct {
				Retire bool `json:"retire"`
			}
			if json.Unmarshal(call.Arguments, &arguments) == nil && arguments.Retire {
				s.noReplay = true
			}
		}
	}
}

type agentStartOutcome struct {
	Outcome string `json:"outcome"`
}

// agentResultOutcome reads a tool result's outcome word: its structured
// content's, else its first text's.
func agentResultOutcome(result json.RawMessage) (string, bool) {
	var answer struct {
		IsError           bool              `json:"isError"`
		StructuredContent agentStartOutcome `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(result, &answer) != nil {
		return "", false
	}
	if answer.StructuredContent.Outcome != "" {
		return answer.StructuredContent.Outcome, answer.IsError
	}
	if len(answer.Content) != 0 {
		var text agentStartOutcome
		if json.Unmarshal([]byte(answer.Content[0].Text), &text) == nil {
			return text.Outcome, answer.IsError
		}
	}
	return "", answer.IsError
}

// observeLocal reads the local server's answers to the recorded calls: an
// observed start becomes the binding to replay, and a Send's answer settles
// or keeps its uncertainty, as the gateway's own reconciliation does.
func (s *agentStdio) observeLocal(line []byte) {
	var message agentRPC
	if json.Unmarshal(line, &message) != nil || message.Method != "" {
		return
	}
	id := agentRequestKey(message.ID)
	if id == "" {
		return
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if params, ok := s.starts[id]; ok {
		delete(s.starts, id)
		if outcome, failed := agentResultOutcome(message.Result); !failed && outcome == "observed" && !s.noReplay {
			s.binding = params
		}
		return
	}
	if identity, ok := s.sends[id]; ok {
		delete(s.sends, id)
		if len(message.Result) == 0 {
			return
		}
		if agentSendUncertain(message.Result) {
			s.uncertain[identity] = true
		} else {
			delete(s.uncertain, identity)
		}
	}
}

func agentSendUncertain(result json.RawMessage) bool {
	var answer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(result, &answer) != nil {
		return false
	}
	for _, content := range answer.Content {
		if len(content.Text) >= len(gateway.SendUncertainLead) && content.Text[:len(gateway.SendUncertainLead)] == gateway.SendUncertainLead {
			return true
		}
	}
	return false
}

// noteFinalRefusal records a final refusal of this process's admission. While
// its image is current the refusal is the runtime's word on this program (a
// revoked enrollment, or the chat another process took), so no successor may
// take the connection over.
func (s *agentStdio) noteFinalRefusal(moved func() bool) {
	if s == nil || moved() {
		return
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	s.fenced = true
}

// canHandOver reports whether a handover may be tried now.
func (s *agentStdio) canHandOver() bool {
	if s == nil || s.launch == nil || s.switching.Load() {
		return false
	}
	s.recMu.Lock()
	ready := s.hostSeen && !s.fenced
	s.recMu.Unlock()
	if !ready {
		return false
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	return s.successor == nil && (s.failedAt.IsZero() || s.now().Sub(s.failedAt) >= agentHandoverRetry)
}

// handedOver reports whether a successor serves the host.
func (s *agentStdio) handedOver() bool {
	if s == nil {
		return false
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	return s.successor != nil
}

// handOver starts a successor, replays the host's initialize and binding to
// it, and routes every later host line to it. Host calls meanwhile answer at
// once that the chat is moving. On failure the successor is stopped and the
// local server keeps the host.
func (s *agentStdio) handOver() error {
	if s == nil || s.launch == nil {
		return errors.New("handover unavailable")
	}
	if !s.switching.CompareAndSwap(false, true) {
		return nil
	}
	defer s.switching.Store(false)
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if s.successor != nil {
		return nil
	}
	s.recMu.Lock()
	hostSeen, fenced, noReplay := s.hostSeen, s.fenced, s.noReplay
	s.keepPendingSendsUncertainLocked()
	initialize, binding := s.initialize, s.binding
	s.recMu.Unlock()
	if !hostSeen || fenced {
		return errors.New("handover refused for this connection")
	}
	if noReplay {
		binding = nil
	}
	successor, err := s.launch()
	if err != nil {
		s.failedAt = s.now()
		return err
	}
	lines := bufio.NewReaderSize(successor.out, 64<<10)
	replay := func(n int, method string, params json.RawMessage) (agentRPC, error) {
		id := fmt.Sprintf("%s%d", agentHandoverID, n)
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err != nil {
			return agentRPC{}, err
		}
		if _, err := successor.in.Write(append(request, '\n')); err != nil {
			return agentRPC{}, err
		}
		return awaitAgentResponse(lines, id, s.budget)
	}
	var answer agentRPC
	if initialize != nil {
		answer, err = replay(1, "initialize", initialize)
		if err == nil && len(answer.Error) != 0 && string(answer.Error) != "null" {
			err = errors.New("successor refused the host's initialize")
		}
		if err == nil {
			_, err = successor.in.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
		}
	}
	if err == nil && binding != nil {
		// The start is idempotent. A successor still admitting its own
		// presence answers it unavailable, so it is repeated within the
		// budget. A binding the successor does not observe would leave the
		// chat unbound behind a live-looking connection.
		until := s.now().Add(s.budget)
		for n := 2; ; n++ {
			answer, err = replay(n, "tools/call", binding)
			if err != nil {
				break
			}
			outcome, failed := agentResultOutcome(answer.Result)
			if !failed && outcome == "observed" {
				break
			}
			if !s.now().Before(until) {
				err = fmt.Errorf("successor did not bind the chat (%s)", outcome)
				break
			}
			time.Sleep(agentHandoverReplayPause)
		}
	}
	if err != nil {
		successor.stop()
		s.failedAt = s.now()
		return fmt.Errorf("handover: %w", err)
	}
	s.successor = successor.in
	go s.relaySuccessor(lines, successor)
	// The successor is the updated connector, and its tools and schemas may
	// differ from the ones the host listed. The host lists them again.
	s.writeHost([]byte(agentToolsChanged + "\n"))
	return nil
}

// agentToolsChanged tells the host to list the tools again (MCP
// notifications/tools/list_changed; the server declares tools.listChanged).
const agentToolsChanged = `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`

// readAgentLine reads one successor line of at most agentStdioLineLimit
// bytes; a longer line is dropped whole and reported as nil.
func readAgentLine(lines *bufio.Reader) ([]byte, error) {
	var line []byte
	overlong := false
	for {
		chunk, err := lines.ReadSlice('\n')
		if !overlong && len(line)+len(chunk) <= agentStdioLineLimit+1 {
			line = append(line, chunk...)
		} else {
			overlong, line = true, nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if overlong {
			return nil, err
		}
		return line, err
	}
}

// awaitAgentResponse reads successor lines until the response to id. Other
// lines during the replay are the successor's own and are dropped.
func awaitAgentResponse(lines *bufio.Reader, id string, budget time.Duration) (agentRPC, error) {
	type found struct {
		answer agentRPC
		err    error
	}
	result := make(chan found, 1)
	go func() {
		for {
			line, err := readAgentLine(lines)
			var message agentRPC
			if len(line) != 0 && json.Unmarshal(line, &message) == nil && message.Method == "" && agentRequestKey(message.ID) == agentRequestKey(json.RawMessage(`"`+id+`"`)) {
				result <- found{answer: message}
				return
			}
			if err != nil {
				result <- found{err: err}
				return
			}
		}
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case r := <-result:
		return r.answer, r.err
	case <-timer.C:
		return agentRPC{}, errors.New("successor did not answer in time")
	}
}

// agentReplayAnswer reports a successor line answering one of the replayed
// requests.
func agentReplayAnswer(line []byte) bool {
	var message agentRPC
	if json.Unmarshal(line, &message) != nil || message.Method != "" {
		return false
	}
	var id string
	return json.Unmarshal(message.ID, &id) == nil && len(id) > len(agentHandoverID) && id[:len(agentHandoverID)] == agentHandoverID
}

// relaySuccessor copies successor lines to the host until the successor
// ends, then ends the local server, which ends this process.
func (s *agentStdio) relaySuccessor(lines *bufio.Reader, successor *agentSuccessor) {
	for {
		line, err := readAgentLine(lines)
		if len(bytes.TrimSpace(line)) != 0 && !agentReplayAnswer(line) {
			if line[len(line)-1] != '\n' {
				line = append(line, '\n')
			}
			s.writeHost(line)
		}
		if err != nil {
			break
		}
	}
	successor.stop()
	s.local.Close()
	close(s.ended)
}

// hostEnded closes the successor's stdin and waits briefly for it to end,
// then ends the local server.
func (s *agentStdio) hostEnded(err error) {
	s.routeMu.Lock()
	successor := s.successor
	s.routeMu.Unlock()
	if successor != nil {
		successor.Close()
		select {
		case <-s.ended:
		case <-time.After(5 * time.Second):
		}
	}
	s.local.CloseWithError(err)
}

// answerHost writes the relay's own answer to one host call.
func (s *agentStdio) answerHost(id json.RawMessage, result *mcp.CallToolResult) {
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err == nil {
		s.writeHost(append(raw, '\n'))
	}
}

// writeHost writes one whole line to the host's stdout.
func (s *agentStdio) writeHost(line []byte) error {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, err := s.hostOut.Write(line)
	return err
}

// agentLineWriter passes the local server's output to the host one whole line
// at a time, so a successor's lines never interleave with it, and lets the
// relay read the answers to the calls it recorded.
type agentLineWriter struct {
	stdio   *agentStdio
	pending []byte
}

func (w *agentLineWriter) Write(data []byte) (int, error) {
	w.pending = append(w.pending, data...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			return len(data), nil
		}
		line := append([]byte(nil), w.pending[:end+1]...)
		w.pending = w.pending[end+1:]
		w.stdio.observeLocal(line)
		if err := w.stdio.writeHost(line); err != nil {
			return 0, err
		}
	}
}

func (w *agentLineWriter) Close() error { return nil }

// agentEnrollmentRead reads an application's agent enrollment from the
// runtime, as Applications ReadAgentEnrollment answers this process.
type agentEnrollmentRead func(context.Context, string) (applications.AgentEnrollmentResult, error)

// agentEnrollmentOverride replaces the runtime's read in fixtures that run
// without a runtime.
var agentEnrollmentOverride agentEnrollmentRead

// agentEnrollmentHolds requires the runtime to read application enrolled at
// exactly the held candidate's path (CONNECTOR-HANDOVER-AUTH step 2). Any
// other answer, or none, refuses the handover.
func agentEnrollmentHolds(ctx context.Context, read agentEnrollmentRead, application, candidate string) error {
	result, err := read(ctx, application)
	if err != nil {
		return fmt.Errorf("handover: the runtime did not read this connector's enrollment: %w", err)
	}
	if result.Outcome != applications.OutcomePage || result.Enrollment == nil {
		return fmt.Errorf("handover: the runtime answered this connector's enrollment read %s", result.Outcome.String())
	}
	if result.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
		return fmt.Errorf("handover: this connector's enrollment is %s", result.Enrollment.State.String())
	}
	if !identity.SameSubjectProgram(result.Enrollment.Program, candidate) {
		return errors.New("handover: the runtime keeps this connector at another path")
	}
	return nil
}

// startAgentStdio makes this process's stdio a dedicated helper's: program is
// the path it started from, args and environment as it started, before any
// owner removed a variable, and read the runtime's enrollment read. It
// watches for Setup moving the image aside.
func startAgentStdio(ctx context.Context, program string, args, environment []string, read agentEnrollmentRead) *agentStdio {
	if agentEnrollmentOverride != nil {
		read = agentEnrollmentOverride
	}
	folder, _ := os.Getwd()
	stdio := newAgentStdio(os.Stdin, stdoutCloser{os.Stdout}, reportStdioLine, agentSuccessorLauncher(program, folder, args, environment, read))
	setProcessAgentStdio(stdio)
	go watchAgentHandover(ctx, stdio, ownImageMoved, agentHandoverCheck)
	return stdio
}

// processAgentStdio is this process's dedicated helper stdio, when it has one.
var processAgentStdio struct {
	mu    sync.Mutex
	stdio *agentStdio
}

func setProcessAgentStdio(s *agentStdio) {
	processAgentStdio.mu.Lock()
	defer processAgentStdio.mu.Unlock()
	processAgentStdio.stdio = s
}

func currentProcessAgentStdio() *agentStdio {
	processAgentStdio.mu.Lock()
	defer processAgentStdio.mu.Unlock()
	return processAgentStdio.stdio
}

// observeAgentFinalRefusal is called for every final admission refusal in this
// process (agentToolAdmission.refuse).
func observeAgentFinalRefusal() {
	if stdio := currentProcessAgentStdio(); stdio != nil {
		stdio.noteFinalRefusal(stdio.imageMoved)
	}
}

// handOverNow answers a tool call that reached this moved helper: it hands
// the connection over now and says to try again, or, when the handover is
// refused or fails, returns "" so the caller names the restart.
func handOverNow(s *agentStdio) string {
	if s == nil {
		return ""
	}
	if s.switching.Load() || s.handedOver() {
		return agentHandoverSentence
	}
	if !s.canHandOver() {
		return ""
	}
	if err := s.handOver(); err != nil {
		//unchecked: diagnostic for a failed handover; stderr has no fallback destination
		fmt.Fprintf(os.Stderr, "openabstractions-mcp pid=%d: %v\n", os.Getpid(), err)
		return ""
	}
	return agentHandoverSentence
}

// requestAgentHandover starts a handover once, in the background, and reports
// whether one is running or done.
func requestAgentHandover(s *agentStdio) bool {
	if s == nil {
		return false
	}
	if s.switching.Load() || s.handedOver() {
		return true
	}
	if !s.canHandOver() {
		return false
	}
	go func() {
		if err := s.handOver(); err != nil {
			//unchecked: diagnostic for a failed handover; stderr has no fallback destination
			fmt.Fprintf(os.Stderr, "openabstractions-mcp pid=%d: %v\n", os.Getpid(), err)
		}
	}()
	return true
}

// watchAgentHandover hands the connection over once moved reports this
// process's image moved aside. It runs until ctx ends.
func watchAgentHandover(ctx context.Context, s *agentStdio, moved func() bool, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.handedOver() {
			return
		}
		if moved() {
			requestAgentHandover(s)
		}
	}
}
