package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// Discovery diagnostics contain only fixed outcome words. They are separate
// from delivery records and carry no caller, receiver or chat content.
func (r *messagesReceiver) recordMessageDiscovery(operation string, outcome msg.Outcome, reason string) {
	if r.host == nil || r.host.messageAudit == nil {
		return
	}
	switch reason {
	case "", "receiver", "context_title", "kind", "text", "lifetime", "not_found", "ambiguous_receiver", "receiver_unavailable", "receiver_lapsed", "receiver_withdrawn", "bound:access_target", "request", "discovery_incomplete", "peer_unavailable", "unreachable", "queued", messageNoReachableChat, messagePrepareWakeUnconfirmed:
	default:
		if strings.HasPrefix(reason, "access:") {
			reason = "access_required"
		} else {
			reason = "other"
		}
	}
	if reason == "" {
		reason = "none"
	}
	entry := logging.Record{Schema: logging.SchemaVersion, Time: logging.At(time.Now()), Level: logging.LevelInfo, Msg: "Application message discovery",
		Attrs: map[string]string{"operation": operation, "outcome": outcome.String(), "reason": reason}}
	if err := r.host.messageAudit.sink.Write(entry); err != nil {
		r.reportMessageError("record discovery", err)
	}
}

func (h *applicationsHost) configureMessageAudit(sink logging.Sink) error {
	audit, err := newMessageAudit(sink)
	if err != nil {
		return err
	}
	h.messageAudit, h.messages.audit = audit, audit
	if h.directory != nil {
		h.directory.setPresenceLapsed(func(program, application, contextName string, at time.Time) {
			if err := audit.presenceLapsed(program, application, contextName, at); err != nil && h.report != nil {
				h.report(err)
			}
		})
	}
	return nil
}

// Invoke outside native admission and every directory/CAS callback. The
// canonical entry survives restart with its original epoch and digest. A lost
// append reply may repeat that same MessageID; Activity joins it as one row.
func (h *applicationsHost) flushMessageAudit(ctx context.Context, key, token string) error {
	h.messageAuditMu.Lock()
	defer h.messageAuditMu.Unlock()
	r, err := h.messages.readRecord(ctx, key)
	if err != nil || r == nil || r.Token != token || r.Alias || r.Audit == nil {
		return err
	}
	entry, err := h.messages.auditProjection(ctx, key, r)
	if err != nil {
		return err
	}
	if r.AuditDone && (r.AuditOutcome == entry.Outcome || r.AuditOutcome == "" && r.Audit.Outcome == entry.Outcome) {
		return nil
	}
	if h.messageAudit == nil {
		return errors.New("messages: audit unavailable")
	}
	if err = h.messageAudit.append(entry); err != nil {
		return err
	}
	if err = h.messages.at("audit_appended"); err != nil {
		return err
	}
	return cas.ChangeLimitContext(ctx, h.messages.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, errors.New("messages: audit acceptance missing")
		}
		var profile struct{ Profile, Token string }
		if err := json.Unmarshal(raw, &profile); err != nil {
			return nil, err
		}
		if profile.Token != token {
			return raw, nil
		}
		if profile.Profile == "messages-expired@1" {
			var expired messageExpiredRecord
			if err := messageStoredJSON(raw, &expired); err != nil {
				return nil, err
			}
			expired.AuditDone = true
			expired.AuditOutcome = entry.Outcome
			return json.Marshal(expired)
		}
		var current messageRecord
		if err := messageStoredJSON(raw, &current); err != nil {
			return nil, err
		}
		current.AuditDone = true
		current.AuditOutcome = entry.Outcome
		return json.Marshal(current)
	})
}

func (s *messageStore) auditProjection(ctx context.Context, key string, r *messageRecord) (*messageAuditEntry, error) {
	entry := *r.Audit
	if r.Delivery != nil {
		entry.DeliveryState = r.Delivery.State.String()
		entry.UndeliveredReason = string(r.Delivery.Reason)
		entry.DeliveryAt = r.Delivery.At
	}
	if r.Profile == "messages-expired@1" || r.Forward != nil {
		return &entry, nil
	}
	state, err := s.currentState(ctx, key, r)
	if err != nil {
		if errors.Is(err, errMessageStateInvalid) && r.Delivery != nil {
			if r.AuditOutcome != "" {
				entry.Outcome = r.AuditOutcome
			}
			return &entry, nil
		}
		return nil, err
	}
	if len(state.Events) > 0 {
		entry.UpdatedAt = state.Events[len(state.Events)-1].At
	}
	entry.Version = state.Version
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil {
		return nil, err
	}
	switch {
	case state.Held:
		entry.Outcome = "awaiting_permission"
	case request.Kind == msg.KindQuestion:
		entry.Outcome = state.TaskState.String()
	case state.Refused:
		entry.Outcome = "refused"
	case r.Acked:
		entry.Outcome = "delivered"
		entry.UpdatedAt = s.now().UnixMilli()
	default:
		entry.Outcome = "sent"
	}
	return &entry, nil
}
