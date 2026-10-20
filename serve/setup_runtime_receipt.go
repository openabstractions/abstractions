package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	cas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identityapi "github.com/openabstractions/abstraction-identity/go/abstraction/identity/api"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

const setupReceiptLimit = int64(identityapi.IPCDefaultMaxFrame)
const setupRequestCooldown = 24 * time.Hour

var errSetupReceiptOccurrence = errors.New("setup: stale question occurrence")
var errSetupReceiptCaller = errors.New("setup: presenter authority changed")
var errSetupReceiptRequest = errors.New("setup: unknown app request")

// Presenter state names a Book occurrence; it never contains a person's answer.
type setupRuntimeReceipt struct {
	Version    int                    `json:"version"`
	Occurrence string                 `json:"occurrence"`
	Phase      string                 `json:"phase"`
	Started    time.Time              `json:"started"`
	Key        string                 `json:"key,omitempty"`
	Request    string                 `json:"request,omitempty"`
	Admission  *setupRuntimeAdmission `json:"admission,omitempty"`
}

type setupRuntimeAdmission struct {
	Key     string `json:"key"`
	Digest  string `json:"digest"`
	Request string `json:"request,omitempty"`
}

func decodeSetupRuntimeReceipt(raw []byte) (*setupRuntimeReceipt, error) {
	if raw == nil {
		return nil, nil
	}
	var v setupRuntimeReceipt
	// Unpublished development receipts predate the Book and confer no answer.
	var version struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &version) == nil && version.Version == 1 {
		var old struct {
			Version    int       `json:"version"`
			Occurrence string    `json:"occurrence"`
			Phase      string    `json:"phase"`
			Started    time.Time `json:"started"`
			Answered   time.Time `json:"answered,omitempty"`
		}
		if applicationV3Strict(raw, &old) != nil || !agentHex(old.Occurrence, 24) || old.Started.IsZero() || old.Phase != "started" && old.Phase != "declined" && old.Phase != "completed" {
			return nil, errors.New("setup: invalid development receipt")
		}
		return nil, nil
	}
	if applicationV3Strict(raw, &v) != nil || v.Version != 3 {
		return nil, errors.New("setup: invalid runtime receipt")
	}
	if v.Admission != nil && (!agentHex(v.Admission.Key, 24) || !agentHex(v.Admission.Digest, 32)) {
		return nil, errors.New("setup: invalid admission identity")
	}
	current := agentHex(v.Occurrence, 16) && !v.Started.IsZero() && (v.Phase == "started" || v.Phase == "completed")
	admitting := v.Admission != nil && v.Occurrence == "" && v.Started.IsZero() && v.Phase == "" && v.Key == "" && v.Request == ""
	if !current && !admitting {
		return nil, errors.New("setup: invalid runtime receipt")
	}
	return &v, nil
}

func setupQuestionOwner(d *applicationDirectory) *runtimeAsks {
	if d.agentQuestions == nil {
		return nil
	}
	return d.agentQuestions.a
}

func (q *setupRequests) suppressRequestLocked(ctx context.Context, d *applicationDirectory) (bool, error) {
	if q.lastRequest != nil {
		return true, nil
	}
	if q.receiptPath == "" {
		return false, nil
	}
	raw, err := cas.ReadLimitContext(ctx, q.receiptPath, setupReceiptLimit)
	if err != nil {
		return false, err
	}
	v, err := decodeSetupRuntimeReceipt(raw)
	if err != nil || v == nil {
		return false, err
	}
	a := setupQuestionOwner(d)
	if a == nil || a.book == nil {
		return false, errors.New("setup: question owner unavailable")
	}
	var r asks.Record
	var found bool
	if v.Admission != nil {
		r, _, err = a.book.ObserveApplication(a.scope, "setup:"+v.Admission.Key)
		found = r.ID != ""
		if err == nil && !found {
			// The marker supplies no pending evidence. An older current Book
			// question, if any, still owns its own expiry and cooldown.
			r, found, err = a.book.ApplicationRecord(v.Occurrence)
		}
	} else {
		r, found, err = a.book.ApplicationRecord(v.Occurrence)
	}
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if r.Key != asks.SetupModelsKey || !agentHex(r.About, 24) {
		return false, errors.New("setup: question occurrence differs")
	}
	if r.Option == "not_now" && r.AnsweredBy == asks.AnsweredByOperator {
		return q.clock().Before(r.Answered.Add(setupRequestCooldown)), nil
	}
	return r.Pending() && q.clock().Before(a.book.PendingUntil(r)), nil
}

func (q *setupRequests) report(ctx context.Context, d *applicationDirectory, s rights.Subject, receipt applications.SetupPresenterReceipt, recheck func() error) applications.SetupStepReport {
	refuse := func(out applications.Outcome, reason string) applications.SetupStepReport {
		return applications.SetupStepReport{Outcome: out, Reason: reason}
	}
	if !receipt.Outcome.Known() {
		return refuse(applications.OutcomeInvalid, "receipt")
	}
	if d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied || recheck == nil || recheck() != nil {
		return refuse(applications.OutcomeForbidden, "caller")
	}
	started := receipt.Outcome == applications.SetupReceiptOutcomeStarted
	if started && (receipt.Occurrence != "" || !agentHex(receipt.Key, 24)) || !started && (!agentHex(receipt.Occurrence, 16) || receipt.Key != "" || receipt.Request != "" || len(receipt.Facts) != 0) {
		return refuse(applications.OutcomeInvalid, "occurrence")
	}
	var facts asks.SetupQuestionFacts
	if started {
		var err error
		facts, err = asks.SetupFactsFromSlots(receipt.Facts)
		if err != nil || facts.Occurrence != "" {
			return refuse(applications.OutcomeInvalid, "facts")
		}
		if err := q.validateFacts(ctx, d, facts); err != nil {
			return refuse(applications.OutcomeConflict, "plan")
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	a := setupQuestionOwner(d)
	if q.receiptPath == "" || a == nil || a.book == nil {
		return refuse(applications.OutcomeUnavailable, "setup state")
	}
	replay := false
	if started {
		_, status, err := a.book.ObserveApplication(a.scope, "setup:"+receipt.Key)
		if err != nil {
			return refuse(applications.OutcomeUnavailable, "setup state")
		}
		replay = status != "unknown"
	}
	if started && receipt.Request == "" && (facts.Requester != "" || facts.RequestUse != "") {
		return refuse(applications.OutcomeInvalid, "request facts")
	}
	var occurrence string
	changed := false
	var err error
	var digest string
	if started {
		body, _ := json.Marshal(receipt)
		sum := sha256.Sum256(body)
		digest = hex.EncodeToString(sum[:])
		// Retain the exact admission before the independent Book write. A
		// replay may finish this admission only while this identity still owns
		// the receipt; a newer successful start clears it.
		err = cas.ChangeLimitContext(ctx, q.receiptPath, setupReceiptLimit, func(raw []byte) ([]byte, error) {
			v, err := decodeSetupRuntimeReceipt(raw)
			if err != nil {
				return nil, err
			}
			if d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied || recheck() != nil {
				return nil, errSetupReceiptCaller
			}
			if v != nil && v.Admission != nil && v.Admission.Key == receipt.Key {
				if v.Admission.Digest != digest {
					return nil, errSetupReceiptOccurrence
				}
				return raw, nil
			}
			if replay {
				return raw, nil
			}
			if v != nil && v.Admission != nil {
				unfinished, _, err := a.book.ObserveApplication(a.scope, "setup:"+v.Admission.Key)
				if err != nil {
					return nil, err
				}
				if unfinished.ID != "" && unfinished.Pending() && q.clock().Before(a.book.PendingUntil(unfinished)) {
					// Finish the accepted start before admitting a distinct one.
					// A failed newer attempt must preserve its recovery identity.
					return nil, errSetupReceiptOccurrence
				}
				if unfinished.ID != "" && !unfinished.Pending() && v.Occurrence != unfinished.ID {
					// A terminal Book answer lets a new presenter finish receipt
					// reconciliation without the lost presenter's private key.
					if v.Occurrence != "" {
						if _, _, err := a.book.RetirePendingApplicationAuthorized(v.Occurrence, recheck); err != nil {
							return nil, err
						}
					}
					v.Occurrence, v.Phase, v.Started = unfinished.ID, "started", unfinished.Asked
					v.Key, v.Request = v.Admission.Key, v.Admission.Request
				}
			}
			if receipt.Request != "" {
				pending := q.currentLocked()
				if pending == nil || pending.ID != receipt.Request {
					pending = q.lastRequest
				}
				if pending == nil || pending.ID != receipt.Request || facts.Requester != pending.Application || facts.RequestUse != pending.Use {
					return nil, errSetupReceiptRequest
				}
			}
			if v == nil {
				v = &setupRuntimeReceipt{Version: 3}
			}
			v.Admission = &setupRuntimeAdmission{Key: receipt.Key, Digest: digest, Request: receipt.Request}
			return json.Marshal(v)
		})
	}
	if err == nil {
		err = cas.ChangeLimitContext(ctx, q.receiptPath, setupReceiptLimit, func(raw []byte) ([]byte, error) {
			v, err := decodeSetupRuntimeReceipt(raw)
			if err != nil {
				return nil, err
			}
			if d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied || recheck() != nil {
				return nil, errSetupReceiptCaller
			}
			if started {
				var priorQuestion string
				if v != nil && v.Key == receipt.Key && v.Request != receipt.Request {
					return nil, errSetupReceiptOccurrence
				}
				if v != nil {
					prior, found, err := a.book.ApplicationRecord(v.Occurrence)
					if err != nil {
						return nil, err
					}
					if found && prior.Key == asks.SetupModelsKey && agentHex(prior.About, 24) {
						priorQuestion = prior.ID
					}
				}
				if err := q.validateFacts(ctx, d, facts); err != nil {
					return nil, err
				}
				// The Book fingerprint includes the app request correlation as well
				// as the frozen facts, even after the in-memory request is cleared.
				identity := sha256.Sum256([]byte(receipt.Key + "\x00" + receipt.Request))
				facts.Occurrence = hex.EncodeToString(identity[:24])
				slots, err := asks.SetupFactSlots(facts)
				if err != nil {
					return nil, err
				}
				r, out, err := a.book.AskApplicationFrom(a.scope, a.display, "setup:"+receipt.Key, asks.SetupModelsKey, slots, listen.Seen{Why: "native Setup step claimed by the person-facing presenter"}, asks.Publisher{})
				if err != nil {
					return nil, err
				}
				if out == "conflict" {
					return nil, errSetupReceiptOccurrence
				}
				if out != "asked" && out != "pending" && out != "answered" {
					return nil, errors.New("setup: question admission refused")
				}
				occurrence = r.ID
				if d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied || recheck() != nil {
					return nil, errSetupReceiptCaller
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if v == nil || v.Admission == nil || v.Admission.Key != receipt.Key || v.Admission.Digest != digest {
					return raw, nil
				}
				if priorQuestion != "" && priorQuestion != r.ID {
					if _, _, err := a.book.RetirePendingApplicationAuthorized(priorQuestion, recheck); err != nil {
						return nil, err
					}
				}
				v = &setupRuntimeReceipt{Version: 3, Occurrence: r.ID, Phase: "started", Started: r.Asked, Key: receipt.Key, Request: receipt.Request}
				changed = true
			} else {
				if v == nil || v.Occurrence != receipt.Occurrence {
					return nil, errSetupReceiptOccurrence
				}
				r, found, err := a.book.ApplicationRecord(v.Occurrence)
				if err != nil {
					return nil, err
				}
				if !found || r.Key != asks.SetupModelsKey || !agentHex(r.About, 24) || r.Option != "setup" || r.AnsweredBy != asks.AnsweredByOperator {
					return nil, errSetupReceiptOccurrence
				}
				if v.Phase == "completed" {
					return raw, nil
				}
				v.Phase = "completed"
				changed = true
			}
			return json.Marshal(v)
		})
	}
	if errors.Is(err, errSetupReceiptCaller) {
		return refuse(applications.OutcomeForbidden, "caller")
	}
	if errors.Is(err, errSetupReceiptOccurrence) {
		return refuse(applications.OutcomeConflict, "occurrence")
	}
	if errors.Is(err, errSetupReceiptRequest) {
		return refuse(applications.OutcomeConflict, "request")
	}
	if err != nil {
		return refuse(applications.OutcomeUnavailable, "setup state")
	}
	if changed {
		q.pending, q.lastReceived = nil, ""
		q.lastRequest = nil
		q.signalLocked()
	}
	if started && occurrence != "" {
		// The person's Set up answer keeps the apps this occurrence names.
		q.followSetupKeep(d, a.book, occurrence)
	}
	if started {
		raw, err := cas.ReadLimitContext(ctx, q.receiptPath, setupReceiptLimit)
		if err != nil {
			return refuse(applications.OutcomeUnavailable, "setup state")
		}
		saved, err := decodeSetupRuntimeReceipt(raw)
		if err != nil || saved == nil || !replay && saved.Occurrence != occurrence {
			return refuse(applications.OutcomeConflict, "occurrence")
		}
		if d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied || recheck() != nil {
			return refuse(applications.OutcomeForbidden, "caller")
		}
		if ctx.Err() != nil {
			return refuse(applications.OutcomeUnavailable, "cancelled")
		}
	}
	return applications.SetupStepReport{Outcome: applications.OutcomePage, Question: occurrence}
}
