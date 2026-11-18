package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// Rights changes from the command line are requests (rights RIGHTS-O17,
// architect ruling A of 2026-11-22): the person confirms each in a popup the
// runtime words, and the command waits within --wait for the answer. A
// command running elevated is exempt while the elevated-callers default is
// on, and its change applies at once.

// defaultChangeWait is how long a change command waits for the person.
const defaultChangeWait = 60 * time.Second

// pendingEditWait is how long provider and inference server commands wait
// for the person; tests shorten it.
var pendingEditWait = defaultChangeWait

// maxChangeCall is the longest single wait the runtime serves.
const maxChangeCall = 30 * time.Second

// changeOperator is the rights operator surface a change request uses.
type changeOperator interface {
	RequestChangeContext(context.Context, rightswire.PolicyChange, int64) (rightswire.ChangeRequestState, error)
	ObserveChangeRequestContext(context.Context, string, int64) (rightswire.ChangeRequestState, error)
}

// requestChange requests change and waits within wait for it to settle,
// following the request in calls of at most maxChangeCall. timeout is each
// call's own budget beside its wait. A lost reply is uncertain.
func requestChange(operator changeOperator, change rightswire.PolicyChange, wait, timeout time.Duration) (rightswire.ChangeRequestState, error) {
	deadline := time.Now().Add(wait)
	slice := func() time.Duration { return min(max(time.Until(deadline), 0), maxChangeCall) }
	first := slice()
	ctx, cancel := context.WithTimeout(context.Background(), first+timeout)
	state, err := operator.RequestChangeContext(ctx, change, first.Milliseconds())
	cancel()
	for err == nil && state.Outcome == rightswire.ChangeRequestOutcomePending {
		next := slice()
		if next <= 0 {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), next+timeout)
		state, err = operator.ObserveChangeRequestContext(ctx, state.ID, next.Milliseconds())
		cancel()
	}
	return state, err
}

// changeFailure is the error a settled request ends a change command with,
// or nil when it applied.
func changeFailure(command string, state rightswire.ChangeRequestState) error {
	switch state.Outcome {
	case rightswire.ChangeRequestOutcomeApplied:
		return nil
	case rightswire.ChangeRequestOutcomePending:
		return &exitError{exitWaiting, fmt.Errorf("%s: pending: confirm it in the OpenAbstractions popup before %s; follow it with: openabstractions rights requests --follow %s", command, state.Expires, state.ID)}
	case rightswire.ChangeRequestOutcomeRefused:
		return refusal(command, "refused", "the person answered Not now; nothing changed")
	case rightswire.ChangeRequestOutcomeExpired:
		return refusal(command, "expired", "nobody answered within ten minutes; nothing changed")
	case rightswire.ChangeRequestOutcomeForbidden:
		return rightsOperatorRefusal(command, "forbidden", "")
	}
	return refusal(command, state.Outcome.String(), "")
}

// changeUncertain is the error for a lost reply.
func changeUncertain(command string, err error) error {
	if unknownChangeMethod(err) {
		return &exitError{exitRefusedCall, fmt.Errorf("%s: this runtime predates change requests; update OpenAbstractions, or make the change in the Panel", command)}
	}
	return &exitError{exitNotResolved, fmt.Errorf("%s: outcome uncertain; run openabstractions rights requests before another change: %w", command, err)}
}

// unknownChangeMethod reports a runtime that does not serve change requests.
func unknownChangeMethod(err error) bool {
	var service *rightswire.ServiceError
	return errors.As(err, &service) && service.Code == rightswire.ServiceErrorCodeUnknownMethod
}

// changeWords prints a request's id and expiry beside its outcome.
func pendingWords(state rightswire.ChangeRequestState) string {
	switch state.Outcome {
	case rightswire.ChangeRequestOutcomePending:
		return fmt.Sprintf("; request %s waits for the person until %s", state.ID, state.Expires)
	case rightswire.ChangeRequestOutcomeApplied:
		if state.Exempt {
			return fmt.Sprintf("; request %s applied as administrator", state.ID)
		}
		return fmt.Sprintf("; request %s allowed", state.ID)
	}
	if state.ID != "" {
		return "; request " + state.ID
	}
	return ""
}

// requestJSON is one change request as rights requests prints it.
type requestJSON struct {
	ID       string    `json:"id"`
	Outcome  string    `json:"outcome"`
	Revision string    `json:"revision,omitempty"`
	Expires  string    `json:"expires,omitempty"`
	Exempt   bool      `json:"exempt,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Rule     *ruleJSON `json:"rule,omitempty"`
	Name     string    `json:"name,omitempty"`
	On       *bool     `json:"on,omitempty"`
	Action   string    `json:"action,omitempty"`
	// Program, Address and Rules are a declaration's program, server address
	// and exact rule set.
	Program string     `json:"program,omitempty"`
	Address string     `json:"address,omitempty"`
	Rules   []ruleJSON `json:"rules,omitempty"`
}

func requestOf(state rightswire.ChangeRequestState) requestJSON {
	out := requestJSON{ID: state.ID, Outcome: state.Outcome.String(), Revision: state.Revision, Expires: state.Expires, Exempt: state.Exempt}
	if change := state.Change; change != nil {
		out.Kind, out.Name, out.Action = change.Kind.String(), change.Name, change.Action
		if change.Rule != nil {
			rule := ruleOf(*change.Rule)
			out.Rule = &rule
		}
		out.Program, out.Address = change.Program, change.Address
		for _, rule := range change.Rules {
			out.Rules = append(out.Rules, ruleOf(rule))
		}
		if change.Kind == rightswire.ChangeKindSetDefault {
			on := change.On
			out.On = &on
		}
	}
	return out
}

type requestsReply struct {
	Command  string        `json:"command"`
	Outcome  string        `json:"outcome"`
	Requests []requestJSON `json:"requests,omitempty"`
}

// rightsRequests lists this program's change requests, or follows one within
// wait.
func rightsRequests(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command, follow string, wait time.Duration, asJSON bool, output io.Writer) error {
	operator, err := resolveOperator(machine, source, call, command)
	if err != nil {
		return err
	}
	reply := requestsReply{Command: command}
	if follow != "" {
		var state rightswire.ChangeRequestState
		deadline := time.Now().Add(wait)
		for {
			next := min(max(time.Until(deadline), 0), maxChangeCall)
			ctx, cancel := context.WithTimeout(context.Background(), next+timeoutOf(call))
			state, err = operator.ObserveChangeRequestContext(ctx, follow, next.Milliseconds())
			cancel()
			if err != nil || state.Outcome != rightswire.ChangeRequestOutcomePending || time.Until(deadline) <= 0 {
				break
			}
		}
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		reply.Outcome = state.Outcome.String()
		if state.ID != "" {
			reply.Requests = []requestJSON{requestOf(state)}
		}
		failure := changeFailure(command, state)
		if state.Outcome == rightswire.ChangeRequestOutcomeUnknown {
			failure = refusal(command, "unknown", "no request "+follow+" from this program; the runtime keeps requests in memory until it restarts")
		}
		return printRequests(output, asJSON, reply, failure)
	}
	ctx, cancel := call()
	page, err := operator.ListChangeRequestsContext(ctx)
	cancel()
	if err != nil {
		if unknownChangeMethod(err) {
			return changeUncertain(command, err)
		}
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	reply.Outcome = page.Outcome.String()
	for _, state := range page.Requests {
		reply.Requests = append(reply.Requests, requestOf(state))
	}
	var failure error
	if page.Outcome != rightswire.PolicyPageOutcomePage {
		failure = rightsOperatorRefusal(command, page.Outcome.String(), "")
	}
	return printRequests(output, asJSON, reply, failure)
}

func printRequests(output io.Writer, asJSON bool, reply requestsReply, failure error) error {
	if asJSON {
		if err := writeJSON(output, reply); err != nil {
			return err
		}
		return failure
	}
	if len(reply.Requests) == 0 {
		if _, err := fmt.Fprintf(output, "%s: %s; no requests\n", reply.Command, reply.Outcome); err != nil {
			return err
		}
		return failure
	}
	table := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
	fmt.Fprintln(table, "REQUEST\tSTATE\tCHANGE\tEXPIRES")
	for _, r := range reply.Requests {
		what := r.Kind
		switch {
		case r.Rule != nil:
			what += " " + r.Rule.Action + " on " + r.Rule.Resource + " for " + shortProgramName(r.Rule.Subject.Program)
		case r.Name != "" && r.Kind == "declaration":
			what += fmt.Sprintf(" %s (%d rules)", r.Name, len(r.Rules))
		case r.Name != "":
			what += " " + r.Name
		case r.Action != "":
			what += " " + r.Action
		}
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", r.ID, r.Outcome, what, r.Expires)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	return failure
}

// requestRuleChange is grant and revoke as a change request.
func requestRuleChange(operator *rights.Operator, command string, rule rights.PolicyRule, revision string, revoke bool, why string, ttl time.Duration, wait, timeout time.Duration) (rightswire.ChangeRequestState, error) {
	change := rightswire.PolicyChange{Kind: rightswire.ChangeKindSetRule, ExpectedRevision: revision, Rule: &rule, TTLMs: ttl.Milliseconds(), Why: why}
	if revoke {
		rule.Permit = false
		change = rightswire.PolicyChange{Kind: rightswire.ChangeKindRevokeRule, ExpectedRevision: revision, Rule: &rule}
	}
	state, err := requestChange(operator, change, wait, timeout)
	if err != nil {
		return state, changeUncertain(command, err)
	}
	return state, nil
}

// followPendingEdit follows a provider declaration or inference host add the
// runtime turned into a change request (reason "pending:<id>", architect
// ruling (3) of 2026-11-22). It reports handled false for any other refusal.
// It waits as long as a rights change does, and returns nil once the person
// allowed it and it applied.
func followPendingEdit(machine *client.Machine, command, reason string, timeout time.Duration) (handled bool, err error) {
	id, ok := strings.CutPrefix(reason, "pending:")
	if !ok || id == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	operator, err := machine.ResolveRightsOperator(ctx, local)
	cancel()
	if err != nil {
		return true, &exitError{exitWaiting, fmt.Errorf("%s: pending: confirm it in the OpenAbstractions popup; follow it with: openabstractions rights requests --follow %s", command, id)}
	}
	deadline := time.Now().Add(pendingEditWait)
	state := rightswire.ChangeRequestState{Outcome: rightswire.ChangeRequestOutcomePending, ID: id}
	for state.Outcome == rightswire.ChangeRequestOutcomePending && time.Until(deadline) > 0 {
		next := min(time.Until(deadline), maxChangeCall)
		ctx, cancel := context.WithTimeout(context.Background(), next+timeout)
		state, err = operator.ObserveChangeRequestContext(ctx, id, next.Milliseconds())
		cancel()
		if err != nil {
			return true, changeUncertain(command, err)
		}
	}
	if state.Outcome == rightswire.ChangeRequestOutcomePending && state.Expires == "" {
		state.Expires = "it expires"
	}
	return true, changeFailure(command, state)
}
