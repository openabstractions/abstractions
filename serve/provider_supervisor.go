package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// storageInventorySource is the storage contract a declared inventory source
// serves.
const storageInventorySource = "abstraction.storage/inventory-source@1"

// providerTiming bounds supervision: how often a provider is probed while it
// is not ready and once it is, the first and largest restart backoff, and how
// long a child must stay up for its backoff to reset.
type providerTiming struct {
	probeWaiting, probeReady time.Duration
	backoffFirst, backoffMax time.Duration
	stable                   time.Duration
	probeBudget              time.Duration
	// inventoryBudget bounds one inventory-source call. A source reads whole
	// stores by stat, which takes longer than a readiness probe.
	inventoryBudget time.Duration
}

var defaultProviderTiming = providerTiming{probeWaiting: 200 * time.Millisecond, probeReady: 2 * time.Second,
	backoffFirst: 250 * time.Millisecond, backoffMax: 30 * time.Second, stable: time.Minute, probeBudget: 2 * time.Second,
	inventoryBudget: 60 * time.Second}

// providerSupervisor runs one declaration: it attaches to the provider, or
// launches it as a child on first activation and restarts it with backoff
// after it exits, and probes its readiness with abstraction.facade/endpoint@1
// Describe through a connection bound to the declared program, or over mutual
// TLS for a remote runtime.
type providerSupervisor struct {
	p        *runtimeProviders
	file     providerFile
	endpoint string

	mu        sync.Mutex
	readiness wire.DeclarationReadiness
	why       string
	restarts  int64
	child     *os.Process
	described []wire.ServiceState
	accepted  []string
	started   bool

	wanted     chan struct{}
	wantedOnce sync.Once
	stop       chan struct{}
	stopOnce   sync.Once
	done       chan struct{}
}

func newProviderSupervisor(p *runtimeProviders, f providerFile) *providerSupervisor {
	s := &providerSupervisor{p: p, file: f, wanted: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	if !f.Declaration.remote() {
		s.endpoint = providerEndpointPath(f.Declaration.Endpoint)
	}
	s.readiness = wire.DeclarationReadinessIdle
	if f.Declaration.Activation != wire.ActivationOnDemand.String() {
		s.readiness, s.why = wire.DeclarationReadinessUnreachable, "not probed yet"
	}
	return s
}

// state is the declaration and the supervisor's reading of it.
func (s *providerSupervisor) state() wire.DeclarationState {
	s.mu.Lock()
	defer s.mu.Unlock()
	described := slices.Clone(s.described)
	if described == nil {
		described = []wire.ServiceState{}
	}
	return wire.DeclarationState{Declaration: s.file.Declaration.wire(), DeclaredBy: s.file.DeclaredBy, DeclaredUnixMs: s.file.DeclaredAt,
		Role: s.file.Declaration.role(), Readiness: s.readiness, Why: s.why, Restarts: s.restarts, Described: described, Accepted: slices.Clone(s.accepted)}
}

// set records a reading and tells the watchers when it changed.
func (s *providerSupervisor) set(readiness wire.DeclarationReadiness, why string) {
	s.mu.Lock()
	changed := s.readiness != readiness || s.why != why
	s.readiness, s.why = readiness, why
	s.mu.Unlock()
	if changed {
		s.p.notify()
	}
}

// activate asks for an on-demand provider; it never blocks.
func (s *providerSupervisor) activate() {
	s.wantedOnce.Do(func() { close(s.wanted) })
}

// ready reports whether the supervisor currently reads the provider ready.
func (s *providerSupervisor) ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readiness == wire.DeclarationReadinessReady
}

// activatingReason is the unavailable reason a mediated call reads when it
// gave up waiting for an on-demand provider's readiness before its own
// deadline or the runtime's launch budget passed (FAC-R8). The launch keeps
// running in the background; a later call finds the provider ready or
// not_ready with its own reason.
func (s *providerSupervisor) activatingReason() string {
	return "activating:" + s.file.Declaration.Name
}

// start begins supervision once.
func (s *providerSupervisor) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	// The runtime is the only reader of an inventory source, and it wants one
	// as soon as it serves.
	if slices.Contains(s.file.Declaration.Contracts, storageInventorySource) {
		s.activate()
	}
	// A native inference provider that declares the profiles it serves is one
	// the router surveys: the router is the one directory of the models on
	// this machine, and a model no list names is a model no application can
	// ask for. Such a provider is activated as soon as the runtime serves. It
	// costs a process and no weights — the model host loads a file on the
	// first call that names it — and a declaration that leaves its profiles
	// out is reached through its own mediated endpoint and stays idle until a
	// resolution asks for it (FAC-R8).
	if d := s.file.Declaration; slices.Contains(d.Contracts, inference.Contract) && len(d.Models) > 0 && len(d.resources("profile")) > 0 {
		s.activate()
	}
	go s.run()
}

// halt stops supervision, ends a launched child and waits for both.
func (s *providerSupervisor) halt() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
}

// pid is the running child's process id, or 0.
func (s *providerSupervisor) pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child == nil {
		return 0
	}
	return s.child.Pid
}

func (s *providerSupervisor) wait(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.stop:
		return false
	case <-t.C:
		return true
	}
}

func (s *providerSupervisor) run() {
	defer close(s.done)
	if s.file.Declaration.Activation != wire.ActivationOnDemand.String() {
		s.attach()
		return
	}
	select {
	case <-s.wanted:
	case <-s.stop:
		return
	}
	backoff := s.p.timing.backoffFirst
	for launches := 0; ; launches++ {
		up := time.Now()
		if !s.launch(launches) {
			return
		}
		if time.Since(up) >= s.p.timing.stable {
			backoff = s.p.timing.backoffFirst
		}
		if !s.wait(backoff) {
			return
		}
		backoff = min(backoff*2, s.p.timing.backoffMax)
	}
}

// arguments substitutes the endpoint name into the declared arguments.
func (s *providerSupervisor) arguments() []string {
	out := slices.Clone(s.file.Declaration.Arguments)
	for i, a := range out {
		if a == providerEndpointToken {
			out[i] = s.file.Declaration.Endpoint
		}
	}
	return out
}

// launch runs one child until it exits (true) or supervision stops (false).
func (s *providerSupervisor) launch(launches int) bool {
	d := s.file.Declaration
	cmd := exec.Command(d.Program, s.arguments()...)
	if launches > 0 {
		s.mu.Lock()
		s.restarts++
		s.mu.Unlock()
	}
	if err := cmd.Start(); err != nil {
		s.set(wire.DeclarationReadinessRestarting, "launch:"+err.Error())
		return true
	}
	s.mu.Lock()
	s.child = cmd.Process
	s.mu.Unlock()
	s.set(wire.DeclarationReadinessStarting, "launched")
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		s.mu.Lock()
		s.child = nil
		s.mu.Unlock()
	}()
	ready := false
	for {
		interval := s.p.timing.probeWaiting
		if ready {
			interval = s.p.timing.probeReady
		}
		t := time.NewTimer(interval)
		select {
		case <-s.stop:
			t.Stop()
			//unchecked: best-effort termination; a failed Kill (e.g. the process already exited) is fine since the wait below still proceeds
			cmd.Process.Kill()
			<-exited
			s.set(wire.DeclarationReadinessIdle, "stopped")
			return false
		case err := <-exited:
			t.Stop()
			why := "exited"
			if err != nil {
				why = "exited:" + err.Error()
			}
			s.set(wire.DeclarationReadinessRestarting, why)
			return true
		case <-t.C:
		}
		readiness, why := s.probe()
		switch readiness {
		case wire.DeclarationReadinessReady:
			ready = true
		case wire.DeclarationReadinessRefused, wire.DeclarationReadinessNotReady:
			ready = false
		default:
			// A launched child that does not answer yet is still starting.
			readiness, ready = wire.DeclarationReadinessStarting, false
		}
		s.set(readiness, why)
	}
}

// attach probes a provider something else runs until supervision stops.
func (s *providerSupervisor) attach() {
	for {
		readiness, why := s.probe()
		s.set(readiness, why)
		interval := s.p.timing.probeWaiting
		if readiness == wire.DeclarationReadinessReady {
			interval = s.p.timing.probeReady
		}
		if !s.wait(interval) {
			return
		}
	}
}

// transport is the connection a probe uses: the local endpoint requiring the
// declared program as the server, or the remote runtime over mutual TLS.
func (s *providerSupervisor) transport(ctx context.Context, budget time.Duration) (wire.EndpointTransport, error) {
	d := s.file.Declaration
	if !d.remote() {
		return listen.FrameClient{Endpoint: s.endpoint, Timeout: budget, MaxFrame: 8 << 20,
			Server: &listen.ServerExpectation{Principal: s.p.principal, Program: d.Program}}.WithContext(ctx), nil
	}
	address, err := remoteAddress(d.Endpoint)
	if err != nil {
		return nil, err
	}
	config, err := d.Remote.config()
	if err != nil {
		return nil, err
	}
	client, err := remote.Client(address, config, budget, 8<<20)
	if err != nil {
		return nil, err
	}
	return client.WithContext(ctx), nil
}

// probe calls endpoint@1 Describe and reads every declared contract from it.
// An inventory source is then described through its own contract, whose
// stores the runtime accepts.
func (s *providerSupervisor) probe() (wire.DeclarationReadiness, string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.p.timing.probeBudget)
	defer cancel()
	d := s.file.Declaration
	transport, err := s.transport(ctx, s.p.timing.probeBudget)
	if err != nil {
		s.record(ctx, nil, nil)
		return wire.DeclarationReadinessUnreachable, "remote:" + err.Error()
	}
	description, err := wire.NewEndpointClient(transport).Describe()
	var refusal *wire.ServiceError
	switch {
	case errors.Is(err, listen.ErrServerUntrusted):
		s.record(ctx, nil, nil)
		return wire.DeclarationReadinessRefused, "program:" + err.Error()
	case errors.As(err, &refusal):
		s.record(ctx, nil, nil)
		return wire.DeclarationReadinessUnreachable, "describe:" + string(refusal.Code)
	case err != nil:
		s.record(ctx, nil, nil)
		return wire.DeclarationReadinessUnreachable, "describe:" + err.Error()
	case description.Outcome != wire.DescriptionOutcomeDescribed:
		s.record(ctx, nil, nil)
		return wire.DeclarationReadinessUnreachable, "describe:" + description.Outcome.String()
	}
	for _, contract := range d.Contracts {
		i := slices.IndexFunc(description.Services, func(state wire.ServiceState) bool { return state.Contract == contract })
		if i < 0 {
			s.record(ctx, description.Services, nil)
			return wire.DeclarationReadinessNotReady, "contract:" + contract + ":absent"
		}
		if state := description.Services[i]; state.Readiness != wire.ServiceReadinessReady {
			s.record(ctx, description.Services, nil)
			return wire.DeclarationReadinessNotReady, "contract:" + contract + ":" + state.Why
		}
	}
	var stores []string
	if slices.Contains(d.Contracts, storageInventorySource) {
		source, err := content.NewInventorySourceClient(transport).Describe()
		if err == nil && source.Outcome != content.SourceDescriptionOutcomeDescribed {
			err = errors.New("described " + source.Outcome.String())
		}
		if err != nil {
			s.record(ctx, description.Services, nil)
			return wire.DeclarationReadinessNotReady, "contract:" + storageInventorySource + ":" + err.Error()
		}
		for _, store := range source.Stores {
			stores = append(stores, store.Name)
		}
	}
	s.record(ctx, description.Services, stores)
	return wire.DeclarationReadinessReady, ""
}

// record keeps the described services and accepts the described stores.
func (s *providerSupervisor) record(ctx context.Context, services []wire.ServiceState, stores []string) {
	accepted := s.acceptStores(ctx, stores)
	s.mu.Lock()
	changed := !slices.Equal(s.accepted, accepted)
	s.described, s.accepted = slices.Clone(services), accepted
	s.mu.Unlock()
	if changed {
		s.p.notify()
	}
}
