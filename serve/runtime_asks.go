package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// firstUseActions are the spending actions whose not_granted decision admits a
// question (research/rights-defaults/DECISION.md §2).
var firstUseActions = []string{host.JobSubmitAction, host.ModelLookupAction, inference.ActionComplete}

// maxFirstUseGenerations bounds how many retired questions one program, action
// and resource may follow with a new one.
const maxFirstUseGenerations = 16

// firstUseSlot is the asks slot bound in characters.
const firstUseSlot = 200

// runtimeAsks composes the asks application book into the runtime: questions
// programs admit under the question.ask rule, the operator profile the
// operator programs answer through, and the runtime's own first-use questions.
type runtimeAsks struct {
	book     *asks.Book
	endpoint string
	rights   *runtimeRights
	report   func(error)
	scope    string
	display  string
}

func asksEndpoint(options runtimeFlags) (string, error) {
	switch {
	case options.isolated != "":
		return bootstrap.Endpoint(options.isolated + "-asks")
	case options.endpoint != "":
		return options.endpoint + "-asks", nil
	}
	return bootstrap.Endpoint("asks-application-v1")
}

// composeAsks opens the application book in the runtime state directory. It
// needs the runtime's rights: asking is decided by a rule, and first-use
// questions follow its decisions.
func composeAsks(options runtimeFlags, r *runtimeRights, report func(error)) (*runtimeAsks, error) {
	if r == nil {
		return nil, nil
	}
	state, err := credentialsState(options)
	if err != nil || state == "" {
		return nil, err
	}
	dir := filepath.Join(state, "asks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	book, err := asks.LoadApplicationBook(filepath.Join(dir, "application.json"))
	if err != nil {
		return nil, err
	}
	endpoint, err := asksEndpoint(options)
	if err != nil {
		return nil, err
	}
	// The runtime asks as itself, in the scope the application service would
	// derive for its executable.
	self := r.self()
	data, _ := json.Marshal([]string{"asks-owner-program@1", r.kind, self.Account, self.Program})
	sum := sha256.Sum256(data)
	a := &runtimeAsks{book: book, endpoint: endpoint, rights: r, report: report,
		scope: "asks-owner-program@1:" + hex.EncodeToString(sum[:]), display: filepath.Base(self.Program)}
	r.firstUse = a.admitFirstUse
	return a, nil
}

// authorizeOperator admits the rights operator programs to answer questions.
func (a *runtimeAsks) authorizeOperator(ctx context.Context, peer *identity.Peer) error {
	if a.rights.authorizeOperator(ctx, peer) != nil {
		return asksservice.ErrOperatorForbidden
	}
	return ctx.Err()
}

// askPolicy refuses the runtime's own question key to applications, then
// decides abstraction.asks/question.ask on account.
func (a *runtimeAsks) askPolicy(decisions host.Decider) asksservice.AskPolicy {
	decide := host.AskPolicyFromRights(decisions)
	return func(ctx context.Context, peer *identity.Peer, key string) error {
		if key == asks.FirstUseKey {
			return errors.New("asks: rights.first_use is admitted by the runtime only")
		}
		return decide(ctx, peer, key)
	}
}

// configure publishes the application and operator profiles on one endpoint
// and registers the asks rights action.
func (a *runtimeAsks) configure(o *host.Options) {
	if a == nil {
		return
	}
	o.QuestionBook, o.QuestionEndpoint, o.QuestionOperator = a.book, a.endpoint, a.authorizeOperator
	o.QuestionAskPolicy = a.askPolicy(a.rights.decider())
	o.RightsActions = append(o.RightsActions, askswire.ResourceActions...)
}

// admitFirstUse admits one question as the runtime, keyed by program, action
// and resource. A pending or answered question replays without a new one; a
// retired one lets the next generation of the key admit again. Paths or
// resources longer than a slot are not asked about.
func (a *runtimeAsks) admitFirstUse(subject rwire.Subject, action, resource string) {
	if !slices.Contains(firstUseActions, action) || utf8.RuneCountInString(subject.Program) > firstUseSlot || utf8.RuneCountInString(resource) > firstUseSlot {
		return
	}
	sum := sha256.Sum256([]byte(subject.Program + "\x00" + action + "\x00" + resource))
	key := "first-use:" + hex.EncodeToString(sum[:20])
	slots := map[string]string{"program": subject.Program, "action": action, "resource": resource}
	via := listen.Seen{Why: "admitted by the runtime after a not_granted decision"}
	for generation := 0; generation < maxFirstUseGenerations; generation++ {
		requestKey := key
		if generation > 0 {
			requestKey += "." + strconv.Itoa(generation)
		}
		_, outcome, err := a.book.AskApplication(a.scope, a.display, requestKey, asks.FirstUseKey, slots, via)
		if err != nil {
			a.report(err)
			return
		}
		if outcome != "gone" {
			return
		}
	}
}

// decisionBudget bounds one in-process decision. A policy read normally takes
// milliseconds, and a file whose read is denied ends the read at this budget.
// The config and logging Go clients wait their DefaultTimeout, two seconds,
// by default, and the budget
// leaves the service time to answer unavailable inside that wait.
const decisionBudget = rights.DecisionReadBudget

// decide reads one decision within the caller's context and decisionBudget. A
// decision that does not read in time is unavailable. Nothing is cached.
func (r *runtimeRights) decide(ctx context.Context, subject rwire.Subject, action, resource string) rwire.Decision {
	ctx, cancel := context.WithTimeout(ctx, decisionBudget)
	defer cancel()
	return r.policy.DecideContext(ctx, subject, action, resource)
}

// Require decides through the in-process policy, within decisionBudget. A
// not_granted decision on a spending action also admits the first-use
// question; the call is refused all the same, at once.
func (r *runtimeRights) Require(ctx context.Context, peer *identity.Peer, action, resource string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		return err
	}
	decision := r.decideAsking(ctx, subject, action, resource)
	if decision.Outcome != rwire.DecisionOutcomePermitted {
		return &rightsclient.DecisionError{Outcome: decision.Outcome.String(), PolicyRevision: decision.PolicyRevision}
	}
	return nil
}

// decideAsking is decide for a subject a service already bound, admitting the
// first-use question on not_granted.
func (r *runtimeRights) decideAsking(ctx context.Context, subject rwire.Subject, action, resource string) rwire.Decision {
	decision := r.decide(ctx, subject, action, resource)
	if r.firstUse != nil && decision.Outcome == rwire.DecisionOutcomeNotGranted {
		r.firstUse(subject, action, resource)
	}
	return decision
}
