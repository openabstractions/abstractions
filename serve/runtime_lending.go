package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	lend "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Lending on the provider rail: abstraction.storage/lend@1 is served by a
// declared provider that writes into other applications' directories. The
// runtime mediates it — an application resolves this runtime, never the
// provider's endpoint — and decides abstraction.storage/lend on resource
// engine:<name> for the calling program before it forwards.
const (
	// ActionLend gates lending. Writing into an engine's directory is an
	// action on a resource the person permits once, through the same first-use
	// question as any other program right (DECISION.md rule 3).
	ActionLend = "abstraction.storage/lend"
	// storageLending is the contract a declared lending provider serves.
	storageLending = "abstraction.storage/lend@1"
	// lendingCallBudget bounds one forwarded call. A lend walks every store by
	// stat and may copy a file the caller asked for.
	lendingCallBudget = 5 * time.Minute
	// lendingPageLimit is the largest page the contract answers.
	lendingPageLimit = 256
)

// lendingLaunchBudget is the runtime's launch budget (FAC-R8): the most a call
// waits for an on-demand lending provider to launch and answer its readiness
// probe. A caller's own, shorter deadline still governs inside that budget.
// A variable so tests can shorten it rather than block for the real budget.
var lendingLaunchBudget = 30 * time.Second

// ResourceEngine is the rights resource of lend for one engine.
func ResourceEngine(name string) string { return "engine:" + name }

func lendingEndpoint(options runtimeFlags) (string, error) {
	switch {
	case options.isolated != "":
		return bootstrap.Endpoint(options.isolated + "-lend")
	case options.endpoint != "":
		return options.endpoint + "-lend", nil
	}
	return options.defaultEndpoint("lend-v1")
}

// lendingSupervisor is the first declaration serving the lending contract.
func (p *runtimeProviders) lendingSupervisor() *providerSupervisor {
	for _, s := range p.sorted() {
		d := s.file.Declaration
		if !d.remote() && slices.Contains(d.Contracts, storageLending) {
			return s
		}
	}
	return nil
}

// lendingProvider is the reader of the declared lending provider. An on-demand
// provider is asked for here and takes a moment to launch and answer its first
// probe, so this waits for it inside the caller's own deadline and never
// longer than lendingLaunchBudget (FAC-R8) rather than refusing a first lend
// the person just permitted. When the caller's deadline or the launch budget
// passes first, it returns the reason "activating:<name>": the launch keeps
// running in the background, and the next call finds the provider ready or
// not_ready with its own reason. An empty reason means client is usable.
func (p *runtimeProviders) lendingProvider(ctx context.Context) (client *lend.LendingClient, reason string) {
	const noneDeclared = "no lending provider is declared and ready"
	if p == nil {
		return nil, noneDeclared
	}
	deadline := time.Now().Add(lendingLaunchBudget)
	for {
		s := p.lendingSupervisor()
		if s == nil {
			return nil, noneDeclared
		}
		s.activate()
		if s.ready() {
			transport, err := s.transport(ctx, lendingCallBudget)
			if err != nil {
				p.report(err)
				return nil, "the lending provider did not answer"
			}
			return lend.NewLendingClient(transport), ""
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, s.activatingReason()
		}
		p.mu.Lock()
		changes := p.changes
		p.mu.Unlock()
		wait := 200 * time.Millisecond
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-changes:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
	}
}

// runtimeLending serves abstraction.storage/lend@1 to applications over the
// declared provider. Every call decides ActionLend on the engine it names for
// the bound caller, through the runtime's rights policy, and admits the
// first-use question on a rule nobody has answered yet.
type runtimeLending struct {
	providers *runtimeProviders
	rights    *runtimeRights
}

// permitted decides ActionLend on engine for caller. It returns "" when the
// call may proceed, and otherwise the refusal word. asking admits the
// first-use question, which listing does not.
func (l *runtimeLending) permitted(ctx context.Context, caller inference.Subject, engine string, asking bool) string {
	if l == nil || l.rights == nil || caller.Account == "" || caller.Program == "" || caller.Account != l.rights.owner {
		return "not_permitted"
	}
	subject := rwire.Subject{Account: caller.Account, Program: caller.Program}
	decide := l.rights.decide
	if asking {
		decide = l.rights.decideAsking
	}
	switch decide(ctx, subject, ActionLend, ResourceEngine(engine)).Outcome {
	case rwire.DecisionOutcomePermitted:
		return ""
	case rwire.DecisionOutcomeUnavailable:
		return "unavailable"
	}
	return "not_permitted"
}

func (l *runtimeLending) Lend(ctx context.Context, caller inference.Subject, store, object, engine string, copy bool) lend.LendResult {
	if engine == "" {
		return lend.LendResult{Outcome: lend.LendOutcomeInvalid, Detail: "an engine is required"}
	}
	switch l.permitted(ctx, caller, engine, true) {
	case "unavailable":
		return lend.LendResult{Outcome: lend.LendOutcomeUnavailable, Detail: "the rights policy did not answer"}
	case "not_permitted":
		return lend.LendResult{Outcome: lend.LendOutcomeNotPermitted,
			Detail: fmt.Sprintf("this program holds no %s rule on %s", ActionLend, ResourceEngine(engine))}
	}
	client, reason := l.providers.lendingProvider(ctx)
	if client == nil {
		return lend.LendResult{Outcome: lend.LendOutcomeUnavailable, Detail: reason}
	}
	result, err := client.Lend(store, object, engine, copy)
	if err != nil {
		l.providers.report(fmt.Errorf("lending: %w", err))
		return lend.LendResult{Outcome: lend.LendOutcomeUnavailable, Detail: "the lending provider did not answer"}
	}
	return result
}

// Unlend reads the entry's own engine from the ledger, then decides the rule
// for that engine: an entry is taken back under the right that placed it.
func (l *runtimeLending) Unlend(ctx context.Context, caller inference.Subject, entry string) lend.UnlendResult {
	client, reason := l.providers.lendingProvider(ctx)
	if client == nil {
		return lend.UnlendResult{Outcome: lend.UnlendOutcomeUnavailable, Detail: reason}
	}
	engine, found, err := lentAt(client, entry)
	switch {
	case err != nil:
		l.providers.report(fmt.Errorf("lending: %w", err))
		return lend.UnlendResult{Outcome: lend.UnlendOutcomeUnavailable, Detail: "the lending provider did not answer"}
	case !found:
		return lend.UnlendResult{Outcome: lend.UnlendOutcomeUnknown, Detail: "no lend of that id"}
	}
	switch l.permitted(ctx, caller, engine, true) {
	case "unavailable":
		return lend.UnlendResult{Outcome: lend.UnlendOutcomeUnavailable, Detail: "the rights policy did not answer"}
	case "not_permitted":
		return lend.UnlendResult{Outcome: lend.UnlendOutcomeNotPermitted,
			Detail: fmt.Sprintf("this program holds no %s rule on %s", ActionLend, ResourceEngine(engine))}
	}
	result, err := client.Unlend(entry)
	if err != nil {
		l.providers.report(fmt.Errorf("lending: %w", err))
		return lend.UnlendResult{Outcome: lend.UnlendOutcomeUnavailable, Detail: "the lending provider did not answer"}
	}
	return result
}

// Lends filters each entry through the rule for its own engine. Listing asks
// no first-use question: a person answers one because a program tried to lend,
// never because it read the ledger.
func (l *runtimeLending) Lends(ctx context.Context, caller inference.Subject, continuation string, limit int64) lend.LendPage {
	refuse := func(outcome lend.LendsOutcome) lend.LendPage {
		return lend.LendPage{Outcome: outcome, Lends: []lend.Lend{}}
	}
	if word := l.permitted(ctx, caller, "", false); word == "unavailable" {
		return refuse(lend.LendsOutcomeUnavailable)
	} else if caller.Account == "" || caller.Program == "" {
		return refuse(lend.LendsOutcomeNotPermitted)
	}
	client, _ := l.providers.lendingProvider(ctx)
	if client == nil {
		return refuse(lend.LendsOutcomeUnavailable)
	}
	page, err := client.Lends(continuation, limit)
	if err != nil {
		l.providers.report(fmt.Errorf("lending: %w", err))
		return refuse(lend.LendsOutcomeUnavailable)
	}
	if page.Outcome != lend.LendsOutcomePage {
		return page
	}
	kept := make([]lend.Lend, 0, len(page.Lends))
	for _, entry := range page.Lends {
		if l.permitted(ctx, caller, entry.Engine, false) == "" {
			kept = append(kept, entry)
		}
	}
	page.Lends = kept
	return page
}

// lentAt reads one entry's engine from the provider's ledger.
func lentAt(client *lend.LendingClient, entry string) (string, bool, error) {
	continuation := ""
	for pages := 0; pages < 64; pages++ {
		page, err := client.Lends(continuation, lendingPageLimit)
		if err != nil {
			return "", false, err
		}
		if page.Outcome != lend.LendsOutcomePage {
			return "", false, errors.New("the ledger reads " + page.Outcome.String())
		}
		for _, e := range page.Lends {
			if e.ID == entry {
				return e.Engine, true, nil
			}
		}
		if page.Complete {
			return "", false, nil
		}
		continuation = page.Continuation
	}
	return "", false, errors.New("the ledger did not end")
}

// lendingHost serves lend@1, and endpoint@1 beside it, on its endpoint.
type lendingHost struct {
	listener listen.Listener
	lending  *runtimeLending
	owner    string
	report   func(error)
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	workers  sync.WaitGroup
	slots    chan struct{}
}

func listenLending(endpoint string, lending *runtimeLending, owner string, report func(error)) (*lendingHost, error) {
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &lendingHost{listener: listen.Sessions(l, listen.SessionOptions{MaxSessions: 16}), lending: lending, owner: owner, report: report, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 16)}, nil
}

func (h *lendingHost) Close() error {
	var err error
	h.once.Do(func() { h.cancel(); err = h.listener.Close() })
	return err
}

func (h *lendingHost) Serve(ctx context.Context) error {
	//unchecked: Close is idempotent (sync.Once); this async cancellation callback has no caller to report the error to, and Serve's own deferred Close below is the same no-op afterward
	stop := context.AfterFunc(ctx, func() { h.Close() })
	defer stop()
	defer h.workers.Wait()
	defer h.Close()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || h.ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case h.slots <- struct{}{}:
		default:
			//unchecked: dropping a connection because the worker slots are full; nothing here can act on a failed close of the connection it is already refusing
			conn.Close()
			continue
		}
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			defer func() { <-h.slots }()
			defer conn.Close()
			callCtx, cancel := context.WithTimeout(h.ctx, lendingCallBudget)
			defer cancel()
			call, err := listen.ReceiveFramed(callCtx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err == nil {
				var reply []byte
				receiver := &lendingReceiver{host: h, call: call, ctx: callCtx}
				if reply, err = (&lend.LendingDispatcher{Handler: receiver}).ExchangeFrame(call.Frame); err == nil {
					err = call.Reply(reply)
				}
			}
			if err != nil && h.report != nil && h.ctx.Err() == nil {
				h.report(fmt.Errorf("lending: %w", err))
			}
		}()
	}
}

// lendingReceiver binds each call's caller from the connection's Program proof.
type lendingReceiver struct {
	host *lendingHost
	call *listen.FramedCall
	ctx  context.Context
}

func (r *lendingReceiver) caller() inference.Subject {
	peer, err := r.call.Peer()
	if err != nil {
		return inference.Subject{}
	}
	subject, err := inferenceservice.SubjectFromPeer(peer)
	if err != nil || subject.Account != r.host.owner || r.call.Recheck() != nil {
		return inference.Subject{}
	}
	return subject
}

func (r *lendingReceiver) Lend(store, object, engine string, copy_ bool) (lend.LendResult, error) {
	return r.host.lending.Lend(r.ctx, r.caller(), store, object, engine, copy_), nil
}

func (r *lendingReceiver) Unlend(entry string) (lend.UnlendResult, error) {
	return r.host.lending.Unlend(r.ctx, r.caller(), entry), nil
}

func (r *lendingReceiver) Lends(continuation string, limit int64) (lend.LendPage, error) {
	return r.host.lending.Lends(r.ctx, r.caller(), continuation, limit), nil
}
