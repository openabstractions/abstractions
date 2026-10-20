package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	facade "github.com/openabstractions/abstraction-facade/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/remote"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
)

type messagePersonReader interface {
	ReadForPerson(string) (msg.Page, error)
	StopForPerson(string) (msg.StopResult, error)
}
type messageHistoryReader interface {
	ReadContext(context.Context, logclient.ApplicationLogTarget, string, int64, int64) (logclient.ApplicationLogPage, error)
}

var messagePersonOperator = func(ctx context.Context) (messagePersonReader, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/messages@1", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	t := binding.Transport()
	if t.Server == nil || t.Server.Program == "" {
		return nil, errors.New("native message operator is unavailable")
	}
	return msg.NewMessagesClient(t.WithContext(ctx)), nil
}

var messageActivityHistory = func(ctx context.Context) (messageHistoryReader, logclient.ApplicationLogTarget, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/messages@1", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, logclient.ApplicationLogTarget{}, err
	}
	t := binding.Transport()
	if t.Server == nil || t.Server.Program == "" {
		return nil, logclient.ApplicationLogTarget{}, errors.New("native message history is unavailable")
	}
	program := t.Server.Program // trusted installation/provider expectation, never a log attribute
	reader, err := panelMachine().ResolveLogApplicationReader(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	return reader, logclient.ApplicationLogTarget{Selection: logwire.ApplicationLogSelectionOneApp, Program: program}, err
}

type activityMessage struct {
	ID            string            `json:"id"`
	At            int64             `json:"at"`
	ExpiresUnixMs int64             `json:"expiresAt"`
	UpdatedAt     int64             `json:"updatedAt,omitempty"`
	Version       uint64            `json:"version,omitempty"`
	From          string            `json:"from"`
	To            string            `json:"to"`
	FromChat      string            `json:"fromChat"`
	ToChat        string            `json:"toChat"`
	Kind          string            `json:"kind"`
	Outcome       string            `json:"outcome"`
	Technical     map[string]string `json:"technical"`
	// A receiver start row names the woken conversation's folder label and
	// the start permit the person can revoke from it.
	Folder string                 `json:"folder,omitempty"`
	Revoke *activityMessageRevoke `json:"revoke,omitempty"`
	// Origin is the chain of question IDs that woke the sending turn; set,
	// it says another assistant's woken turn asked this.
	Origin string `json:"origin,omitempty"`
	// Delivery is how a woken session received the turn: copy, read_only
	// (Codex app closed), desktop_queue (the Codex app runs it, not OA) or
	// resumed (a closed Claude chat answered in place).
	Delivery          string `json:"delivery,omitempty"`
	DeliveryState     string `json:"deliveryState,omitempty"`
	UndeliveredReason string `json:"undeliveredReason,omitempty"`
	DeliveryAt        int64  `json:"deliveryAt,omitempty"`
	MetadataOnly      bool   `json:"metadataOnly,omitempty"`
}

// activityMessageRevoke is the exact permit a woken turn ran under, edited
// through the Panel's existing rights revoke.
type activityMessageRevoke struct {
	Account  string `json:"account"`
	Program  string `json:"program"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// messageStartRevoke reads the start permit from a receiver start row of the
// trusted messages service. A malformed or other row offers no revoke.
func messageStartRevoke(a map[string]string) *activityMessageRevoke {
	if !strings.HasPrefix(a["outcome"], "receiver_start_") || a["permit_action"] != grants.ApplicationMessageStartAction {
		return nil
	}
	if _, ok := grants.MessageStartResource(a["permit_resource"]); !ok {
		return nil
	}
	account, program := a["account"], a["permit_program"]
	if !rightsText(account, 128) || !rightsText(program, 4096) || !identity.ValidSubjectProgram(program) && !remote.ValidSubjectProgram(program) {
		return nil
	}
	return &activityMessageRevoke{Account: account, Program: program, Action: a["permit_action"], Resource: a["permit_resource"]}
}

type activityMessagesPage struct {
	Outcome string            `json:"outcome"`
	Events  []activityMessage `json:"events"`
	Next    string            `json:"next"`
	AtEnd   bool              `json:"atEnd"`
}

func messageActivityLabel(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return -1
		}
		return r
	}, text)
}

func (p *servicePanel) activityMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 512 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid history cursor", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	reader, target, err := messageActivityHistory(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := reader.ReadContext(ctx, target, cursor, 64, 65536)
	if err != nil {
		panelError(w, err)
		return
	}
	out := activityMessagesPage{Outcome: page.Outcome.String(), Events: []activityMessage{}, Next: page.Next, AtEnd: page.AtEnd}
	if page.Outcome != logwire.HistoryPageOutcomePage {
		panelJSON(w, out)
		return
	}
	names := p.programTitler(ctx)
	if operator, err := messagePersonOperator(ctx); err == nil {
		if inventory, ok := operator.(interface {
			InboxesForPerson(string) (msg.InboxPage, error)
		}); ok {
			labels := map[string]string{}
			cursor := ""
			seen := map[string]bool{}
			valid := true
			for pageNumber := 0; pageNumber < 128; pageNumber++ {
				page, err := inventory.InboxesForPerson(cursor)
				if err != nil || page.Outcome != msg.OutcomePage {
					valid = false
					break
				}
				for _, row := range page.Inboxes {
					label := messageActivityLabel(row.ChatTitle)
					if label == "" {
						label = "Unnamed chat"
						if row.LastSeenUnixMs > 0 {
							label = "Chat last seen " + time.UnixMilli(row.LastSeenUnixMs).Local().Format("2006-01-02 15:04")
						}
					}
					labels[row.Program+"\x00"+row.Context] = label
				}
				if page.Cursor == "" {
					cursor = ""
					break
				}
				if seen[page.Cursor] {
					valid = false
					break
				}
				seen[page.Cursor] = true
				cursor = page.Cursor
			}
			if valid && cursor == "" {
				names.chatNames = labels
			}
		}
	}
	for _, record := range page.Records {
		a := record.Attrs
		if record.Msg != "Application message" || a["oa_message"] != "1" {
			continue
		}
		id := a["message_id"]
		if id == "" || len(id) > 128 || !utf8.ValidString(id) || messageActivityLabel(id) != id {
			continue
		}
		at, e1 := strconv.ParseInt(a["accepted_at"], 10, 64)
		expiry, e2 := strconv.ParseInt(a["expires_at"], 10, 64)
		if e1 != nil || e2 != nil || at <= 0 || expiry <= at {
			continue
		}
		if a["kind"] != "note" && a["kind"] != "question" && a["kind"] != "reply" {
			continue
		}
		updated, _ := strconv.ParseInt(a["updated_at"], 10, 64)
		version, _ := strconv.ParseUint(a["state_version"], 10, 64)
		if version > 4 {
			continue
		}
		from, known := names.title(a["from_program"])
		if !known {
			from = "Unidentified app"
		}
		to, known := names.title(a["to_program"])
		if !known {
			to = "Unidentified app"
		}
		out.Events = append(out.Events, activityMessage{ID: id, At: at, ExpiresUnixMs: expiry, UpdatedAt: updated, Version: version, From: messageActivityLabel(from), To: messageActivityLabel(to), Kind: a["kind"], Outcome: messageActivityLabel(a["outcome"]), Technical: map[string]string{"from": a["from"], "to": a["to"], "from_program": a["from_program"], "to_program": a["to_program"], "context": a["to_context"], "origin_chain": a["origin_chain"], "delivery": a["delivery"]}, Folder: messageActivityLabel(a["to_folder"]), Revoke: messageStartRevoke(a), Origin: messageActivityLabel(a["origin_chain"]), Delivery: messageActivityLabel(a["delivery"]), MetadataOnly: a["metadata_only"] == "1"})
		if delivery := a["delivery_state"]; delivery == "delivered" || delivery == "undelivered" {
			event := &out.Events[len(out.Events)-1]
			event.DeliveryState = delivery
			event.DeliveryAt, _ = strconv.ParseInt(a["delivery_at"], 10, 64)
			if event.DeliveryAt < 0 {
				event.DeliveryAt = 0
			}
			if delivery == "undelivered" {
				event.UndeliveredReason = messageActivityLabel(a["undelivered_reason"])
			}
			event.Technical["delivery_state"] = delivery
			event.Technical["undelivered_reason"] = event.UndeliveredReason
			event.Technical["delivery_at"] = strconv.FormatInt(event.DeliveryAt, 10)
		}
		fromContext := a["return_context"]
		if fromContext == "" {
			fromContext = a["from_context"]
		}
		out.Events[len(out.Events)-1].FromChat = names.chatTitle(a["from_program"], fromContext)
		out.Events[len(out.Events)-1].ToChat = names.chatTitle(a["to_program"], a["to_context"])
	}
	panelJSON(w, out)
}

type activityMessageBody struct {
	Outcome       string `json:"outcome"`
	ID            string `json:"id,omitempty"`
	Headline      string `json:"headline,omitempty"`
	Body          string `json:"body,omitempty"`
	ExpiresUnixMs int64  `json:"expiresAt,omitempty"`
}

func (p *servicePanel) activityMessageBody(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" || len(id) > 128 || !utf8.ValidString(id) || messageActivityLabel(id) != id {
		http.Error(w, "invalid message id", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	reader, err := messagePersonOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := reader.ReadForPerson(id)
	if err != nil {
		panelError(w, err)
		return
	}
	out := activityMessageBody{Outcome: page.Outcome.String()}
	if page.Outcome == msg.OutcomePage {
		if page.Cursor != "" || page.Revision != "" || len(page.Headers) != 0 || len(page.Messages) > 1 {
			panelError(w, errors.New("invalid operator message page"))
			return
		}
		if len(page.Messages) == 1 {
			m := page.Messages[0]
			if m.Header.MessageID != id {
				panelError(w, errors.New("operator message identity changed"))
				return
			}
			if m.Header.ExpiresUnixMs > time.Now().UnixMilli() {
				out.ID = id
				out.Headline = m.Header.Subject
				out.Body = m.Body
				out.ExpiresUnixMs = m.Header.ExpiresUnixMs
			}
		}
	}
	panelJSON(w, out)
}

func (p *servicePanel) messageActivityRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/activity/messages", guard(key, p.activityMessages))
	mux.HandleFunc("/activity/message", guard(key, p.activityMessageBody))
	mux.HandleFunc("/activity/message/stop", guard(key, p.activityMessageStop))
}

// activityMessageStop stops the turn one question woke (VISION 2026-10-31:
// Activity can stop a running woken turn as well as revoke the direction).
// The id is the question's message ID; the start row's ID ends in ":start".
func (p *servicePanel) activityMessageStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	id := strings.TrimSuffix(r.URL.Query().Get("id"), ":start")
	if id == "" || len(id) > 128 || !utf8.ValidString(id) || messageActivityLabel(id) != id {
		http.Error(w, "invalid message id", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := messagePersonOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	result, err := operator.StopForPerson(id)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, map[string]string{"outcome": result.Outcome.String(), "reason": messageActivityLabel(result.Reason)})
}
