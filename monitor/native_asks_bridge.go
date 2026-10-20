package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// This is a private presentation channel between the launcher and its own Go
// child, not an operator endpoint or identity proof. Only Go resolves the
// runtime and sends answers through its public operator contract.
const nativeAsksPrefix = "oa-native-asks: "
const maxNativeAsksLine = 1 << 20

type nativeAsksFrame struct {
	Kind        string                    `json:"kind"`
	ID          string                    `json:"id,omitempty"`
	Prompt      *trayQuestionPresentation `json:"prompt,omitempty"`
	Title       string                    `json:"title,omitempty"`
	Instruction string                    `json:"instruction,omitempty"`
	Content     string                    `json:"content,omitempty"`
	// Level is a report's header level (promptLevelFor); a question carries
	// its own in Prompt.
	Level promptLevel `json:"level,omitempty"`
	// A panel-response (native_panel_bridge.go): Seq numbers its chunks,
	// the first carries Status and Headers, Body is base64, and More says
	// another chunk follows. Aborted, alone in chunk 0, fails the request.
	Seq     int               `json:"seq,omitempty"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	More    bool              `json:"more,omitempty"`
	Aborted bool              `json:"aborted,omitempty"`
	// Notification is one application notification (ASK-N7).
	Notification *nativeNotification `json:"notification,omitempty"`
}

// nativeNotification is one application notification for the launcher's
// UserNotifications center (ASK-N7). Each click carries prepared arguments
// the launcher returns unchanged; the launcher decides nothing.
type nativeNotification struct {
	Title    string                     `json:"title"`
	Body     string                     `json:"body"`
	Sender   string                     `json:"sender"`
	Open     string                     `json:"open"`
	Actions  []nativeNotificationAction `json:"actions"`
	Silently bool                       `json:"silently"`
}

type nativeNotificationAction struct {
	Title string `json:"title"`
	Args  string `json:"args"`
}

type nativeAsksReply struct {
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
	Option   string `json:"option,omitempty"`
	Error    string `json:"error,omitempty"`
	Unlocked *bool  `json:"unlocked,omitempty"`
	Page     string `json:"page,omitempty"`
	// A notification click's prepared arguments (ASK-N7).
	Args string `json:"args,omitempty"`
	// A panel-request: method, absolute oa-panel URL, the Panel's request
	// headers and a base64 body.
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// panelFields reports that a reply carries a panel-request's fields.
func (r nativeAsksReply) panelFields() bool {
	return r.Method != "" || r.URL != "" || r.Headers != nil || r.Body != ""
}

type nativeAsksResponse struct {
	option string
	err    error
}

// nativeAsksBridge implements the existing session and surface interfaces.
// Correlation is child-local dialog lifetime, never authority. A session loss
// invalidates the current dialog even if an unlock arrives before its answer.
type nativeAsksBridge struct {
	ctx        context.Context
	out        io.Writer
	outMu      sync.Mutex
	mu         sync.Mutex
	unlocked   bool
	revision   uint64
	next       uint64
	activeID   string
	active     chan nativeAsksResponse
	activeKind string
	closed     error
	// panel answers the window's requests once the launcher opened it.
	panel *nativePanelServer
	// onNotification receives a notification click's prepared arguments and
	// onNotificationClosed a closed notification's record ID (ASK-N7).
	onNotification       func(args string)
	onNotificationClosed func(id string)
}

// servePanel routes the launcher's panel requests to server.
func (b *nativeAsksBridge) servePanel(server *nativePanelServer) {
	b.mu.Lock()
	b.panel = server
	b.mu.Unlock()
}

func newNativeAsksBridge(ctx context.Context, out io.Writer) *nativeAsksBridge {
	return &nativeAsksBridge{ctx: ctx, out: out}
}

func (b *nativeAsksBridge) Revision() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.revision
}

func (b *nativeAsksBridge) Unlocked() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.unlocked && b.closed == nil && b.ctx.Err() == nil
}

func (b *nativeAsksBridge) write(frame nativeAsksFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data)+len(nativeAsksPrefix) >= maxNativeAsksLine {
		return errors.New("native question presentation exceeds the bounded launcher channel")
	}
	b.outMu.Lock()
	defer b.outMu.Unlock()
	_, err = fmt.Fprintf(b.out, "%s%s\n", nativeAsksPrefix, data)
	return err
}

func (b *nativeAsksBridge) display(frame nativeAsksFrame) (string, error) {
	b.mu.Lock()
	if !b.unlocked {
		b.mu.Unlock()
		return "", errPromptNotShown
	}
	if b.closed != nil {
		err := b.closed
		b.mu.Unlock()
		return "", err
	}
	if b.active != nil {
		b.mu.Unlock()
		return "", errors.New("a native question dialog is already active")
	}
	b.next++
	frame.ID = strconv.FormatUint(b.next, 10)
	response := make(chan nativeAsksResponse, 1)
	b.activeID, b.activeKind, b.active = frame.ID, frame.Kind, response
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.activeID == frame.ID {
			b.activeID, b.activeKind, b.active = "", "", nil
		}
		b.mu.Unlock()
	}()
	if err := b.write(frame); err != nil {
		return "", err
	}
	select {
	case reply := <-response:
		return reply.option, reply.err
	case <-b.ctx.Done():
		_ = b.write(nativeAsksFrame{Kind: "dismiss", ID: frame.ID})
		return "", b.ctx.Err()
	}
}

func (b *nativeAsksBridge) Ask(prompt trayQuestionPresentation) (string, error) {
	prompt.Title = trayDialogText(prompt.Title)
	prompt.Instruction = trayDialogText(prompt.Instruction)
	prompt.Content = trayDialogText(prompt.Content)
	prompt.Details = trayDialogText(prompt.Details)
	prompt.Footer = trayDialogText(prompt.Footer)
	prompt.Warning = trayDialogText(prompt.Warning)
	prompt.PermissionContent = trayDialogText(prompt.PermissionContent)
	choices := make([]trayQuestionChoice, len(prompt.Choices))
	for index, choice := range prompt.Choices {
		choices[index] = trayQuestionChoice{Label: trayDialogText(choice.Label), Option: choice.Option}
	}
	prompt.Choices = choices
	prompt.PermissionPrompt, prompt.IconProgram = false, ""
	if prompt.Permission != nil && !prompt.AppChoice {
		prompt.PermissionPrompt, prompt.IconProgram = true, prompt.Permission.RequesterProgram
	}
	return b.display(nativeAsksFrame{Kind: "question", Prompt: &prompt})
}

func (b *nativeAsksBridge) showReport(report presenterReport) bool {
	_, err := b.display(nativeAsksFrame{Kind: "report", Title: trayDialogText(report.Title), Instruction: trayDialogText(report.Instruction), Content: trayDialogText(report.Content),
		Level: reportPromptLevel()})
	return b.ctx.Err() == nil && (err == nil || errors.Is(err, errPromptNotShown))
}

func (b *nativeAsksBridge) Dismiss() {
	b.dismiss(nativeAsksResponse{})
}

func (b *nativeAsksBridge) dismiss(reply nativeAsksResponse) {
	b.mu.Lock()
	id, active := b.activeID, b.active
	if active != nil {
		select {
		case active <- reply:
		default:
		}
	}
	b.mu.Unlock()
	if id != "" {
		_ = b.write(nativeAsksFrame{Kind: "dismiss", ID: id})
	}
}

func (b *nativeAsksBridge) session(unlocked bool) {
	b.mu.Lock()
	if !unlocked {
		b.revision++
	}
	b.unlocked = unlocked
	b.mu.Unlock()
	if !unlocked {
		b.dismiss(nativeAsksResponse{err: errPromptNotShown})
	}
}

func (b *nativeAsksBridge) close(err error) {
	b.mu.Lock()
	b.closed, b.unlocked = err, false
	b.revision++
	b.mu.Unlock()
	b.dismiss(nativeAsksResponse{err: err})
}

// read accepts one bounded JSON object per line. The only commands are session
// evidence, a reply to the current dialog, and an explicit on-demand Panel open.
// EOF ends the child; malformed input cannot leave an old prompt answerable.
func (b *nativeAsksBridge) read(in io.Reader, openPanel func(string) error) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), maxNativeAsksLine)
	for scanner.Scan() {
		var reply nativeAsksReply
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&reply); err != nil {
			return fmt.Errorf("native launcher reply: %w", err)
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return errors.New("native launcher reply requires one object")
		}
		if err := b.accept(reply, openPanel); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("native launcher reply: %w", err)
	}
	return io.EOF
}

func (b *nativeAsksBridge) accept(reply nativeAsksReply, openPanel func(string) error) error {
	switch reply.Kind {
	case "panel-request":
		if err := validNativePanelRequest(reply); err != nil {
			return err
		}
		b.mu.Lock()
		server := b.panel
		b.mu.Unlock()
		if server == nil {
			// Not opened: refuse this request, keep the channel.
			(&nativePanelServer{write: b.write}).respond(reply.ID, panelResourceText(http.StatusServiceUnavailable, "the Panel is not open"))
			return nil
		}
		server.request(reply)
		return nil
	case "panel-cancel":
		if reply.ID == "" || reply.Option != "" || reply.Error != "" || reply.Unlocked != nil || reply.Page != "" || reply.panelFields() {
			return errors.New("invalid native Panel cancel")
		}
		b.mu.Lock()
		server := b.panel
		b.mu.Unlock()
		if server != nil {
			server.cancel(reply.ID)
		}
		return nil
	}
	if reply.panelFields() {
		return errors.New("native launcher reply carries Panel request fields")
	}
	switch reply.Kind {
	case "session":
		if reply.Unlocked == nil || reply.ID != "" || reply.Option != "" || reply.Error != "" || reply.Page != "" || reply.Args != "" {
			return errors.New("invalid native session report")
		}
		b.session(*reply.Unlocked)
		return nil
	case "notification-click":
		if reply.Args == "" || len(reply.Args) > 1024 || reply.ID != "" || reply.Option != "" || reply.Error != "" || reply.Unlocked != nil || reply.Page != "" {
			return errors.New("invalid native notification click")
		}
		if b.onNotification != nil {
			go b.onNotification(reply.Args)
		}
		return nil
	case "notification-closed":
		if reply.ID == "" || len(reply.ID) > 128 || reply.Args != "" || reply.Option != "" || reply.Error != "" || reply.Unlocked != nil || reply.Page != "" {
			return errors.New("invalid native notification close")
		}
		if b.onNotificationClosed != nil {
			go b.onNotificationClosed(reply.ID)
		}
		return nil
	case "open-panel":
		if reply.ID != "" || reply.Option != "" || reply.Error != "" || reply.Unlocked != nil || reply.Args != "" {
			return errors.New("invalid native Panel request")
		}
		if _, err := parsePanelStartPage(reply.Page); err != nil {
			return err
		}
		return openPanel(reply.Page)
	case "answer", "close", "deferred", "failed", "ack":
		if reply.ID == "" || reply.Unlocked != nil || reply.Page != "" || reply.Args != "" ||
			(reply.Kind != "answer" && reply.Option != "") || (reply.Kind == "answer" && reply.Option == "") ||
			(reply.Kind != "failed" && reply.Error != "") || (reply.Kind == "failed" && (reply.Error == "" || len(reply.Error) > 4096)) {
			return errors.New("invalid native dialog reply")
		}
	default:
		return errors.New("unknown native launcher reply")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if reply.ID != b.activeID || b.active == nil {
		return nil // A dismissed or replaced dialog has no remaining reply path.
	}
	if (b.activeKind == "report" && reply.Kind == "answer") || (b.activeKind == "question" && reply.Kind == "ack") {
		return errors.New("native reply does not match the current dialog")
	}
	response := nativeAsksResponse{option: reply.Option}
	if reply.Kind == "deferred" || !b.unlocked {
		response.err = errPromptNotShown
	} else if reply.Kind == "failed" {
		response.err = errors.New("native dialog: " + reply.Error)
	}
	select {
	case b.active <- response:
	default:
	}
	return nil
}
