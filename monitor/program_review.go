package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

// The native Registry owns image capture, conditional edits and person Keep.
// The Panel sends only the complete freshly observed Spec, never edited facets.
type panelProgramRegistry interface {
	Registrations(context.Context, string, int64) (registration.Page, error)
	Replace(context.Context, string, registration.RegistrationSpec) (registration.Change, error)
	Observe(context.Context, string, time.Duration) (registration.Page, error)
}

type programReviewRequest struct {
	Action   string `json:"action"`
	Key      string `json:"key"`
	Revision string `json:"revision"`
}

type programReviewResult struct {
	Outcome string                     `json:"outcome"`
	Reason  string                     `json:"reason,omitempty"`
	Record  *registration.Registration `json:"record,omitempty"`
}

func programRegistrationList(ctx context.Context, owner panelProgramRegistry) ([]registration.Registration, error) {
	rows := []registration.Registration{}
	cursor, revision := "", ""
	seen := map[string]bool{}
	for page := 0; page < 16; page++ {
		answer, err := owner.Registrations(ctx, cursor, 64)
		if err != nil {
			return nil, err
		}
		if answer.Outcome != registration.OutcomePage || revision != "" && answer.Revision != revision {
			return nil, fmt.Errorf("program registrations could not be read: %s", answer.Outcome.String())
		}
		revision = answer.Revision
		for _, record := range answer.Registrations {
			if record.Key == "" || seen[record.Key] {
				return nil, errors.New("program registration inventory repeated an entry")
			}
			seen[record.Key] = true
			rows = append(rows, record)
		}
		if answer.Cursor == "" {
			return rows, nil
		}
		if answer.Cursor == cursor {
			return nil, errors.New("program registration inventory did not advance")
		}
		cursor = answer.Cursor
	}
	return nil, errors.New("program registration inventory exceeded its page bound")
}

// programReviewOpen reports a pending program whose native review question
// is open now. A registration asks nothing (VISION 2026-10-06, amended
// 2026-11-10): a pending program waits for the person's Review click here,
// which asks.
func programReviewOpen(record registration.Registration) bool {
	return strings.HasPrefix(record.Reason, "Review the open OpenAbstractions question")
}

func reviewProgram(ctx context.Context, owner panelProgramRegistry, request programReviewRequest) (programReviewResult, error) {
	rows, err := programRegistrationList(ctx, owner)
	if err != nil {
		return programReviewResult{}, err
	}
	var current *registration.Registration
	for i := range rows {
		if rows[i].Key == request.Key {
			current = &rows[i]
			break
		}
	}
	if current == nil || current.Revision != request.Revision {
		return programReviewResult{Outcome: "conflict", Reason: "The program changed. Read it again before reviewing."}, nil
	}
	if current.State == registration.StateDisabled {
		return programReviewResult{Outcome: "conflict", Reason: "This program is disabled; its existing choice is preserved."}, nil
	}
	if current.State == registration.StatePendingApproval && current.Verification != registration.VerificationChanged && programReviewOpen(*current) {
		return programReviewResult{Outcome: "pending", Reason: "Review the existing native OpenAbstractions question.", Record: current}, nil
	}
	if current.IdentityEvidence != registration.IdentityEvidenceProgram || len(current.Spec.Providers) == 0 ||
		len(current.Spec.Applications) != 0 || len(current.Spec.Remotes) != 0 {
		return programReviewResult{Outcome: "unsupported", Reason: "This program needs its existing registration owner."}, nil
	}
	if current.Verification != registration.VerificationChanged && current.State != registration.StatePendingApproval {
		return programReviewResult{Outcome: "conflict", Reason: "The program no longer needs this image review."}, nil
	}
	change, err := owner.Replace(ctx, current.Revision, current.Spec)
	if err != nil {
		return programReviewResult{}, err
	}
	if change.Outcome != registration.OutcomeApplied {
		return programReviewResult{Outcome: change.Outcome.String(), Reason: change.Reason}, nil
	}
	// A committed replacement captures a new image and remains pending. It is
	// never a Keep answer or evidence that the provider became eligible.
	observation, err := owner.Observe(ctx, "", 0)
	if err != nil {
		return programReviewResult{}, err
	}
	if observation.Outcome != registration.OutcomePage {
		return programReviewResult{Outcome: "unavailable", Reason: "The new review could not be observed."}, nil
	}
	rows, err = programRegistrationList(ctx, owner)
	if err != nil {
		return programReviewResult{}, err
	}
	for _, record := range rows {
		if record.Key == request.Key && record.Revision == change.Revision && change.Reason == "approval:unavailable" {
			return programReviewResult{Outcome: "unavailable", Reason: "The native question is unavailable. Retry this review explicitly.", Record: &record}, nil
		}
		if record.Key == request.Key && record.Revision == change.Revision && record.State == registration.StatePendingApproval &&
			record.IdentityEvidence == registration.IdentityEvidenceProgram && reflect.DeepEqual(record.Spec, current.Spec) &&
			(record.Verification == registration.VerificationVerified || record.Verification == registration.VerificationUnverified) {
			return programReviewResult{Outcome: "pending", Reason: "Review this program in the native OpenAbstractions question.", Record: &record}, nil
		}
	}
	return programReviewResult{Outcome: "conflict", Reason: "The reviewed image changed before readback."}, nil
}

func (p *servicePanel) programReviewOwner(ctx context.Context) (panelProgramRegistry, error) {
	if p.programRegistry != nil {
		return p.programRegistry, nil
	}
	return panelMachine().ResolveRegistryV3(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

func (p *servicePanel) programReviews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	var request programReviewRequest
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || request.Action != "review" || !rightsText(request.Key, 4096) || request.Key == "" ||
			!rightsText(request.Revision, 128) || request.Revision == "" || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "one current program review required", http.StatusBadRequest)
			return
		}
	}
	owner, err := p.programReviewOwner(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	if r.Method == http.MethodPost {
		result, err := reviewProgram(ctx, owner, request)
		if err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, result)
		return
	}
	rows, err := programRegistrationList(ctx, owner)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, struct {
		Registrations []registration.Registration `json:"registrations"`
	}{rows})
}
