package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-inference/adapters/go/gateway"
	"github.com/openabstractions/abstraction-inference/adapters/go/realtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	logging "github.com/openabstractions/abstraction-logging/go"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// inferenceHostsFile is the runtime's inference settings inside its state
// directory: the per-credential ceilings, the provider's bounds, and whether
// the products on this machine declare hosts (runtime_inference_declared.go).
// Its "local" and "hosted" entries are what the registration build wrote;
// each becomes a registry declaration of role host at the first start on this
// build and leaves the file (registry.go). An absent "declared" is true
// without "local" and false with it.
const inferenceHostsFile = "hosts.json"

// inferenceLocalHost is a model runtime on this machine: its kind and base URL.
type inferenceLocalHost struct {
	Kind string `json:"kind"`
	Base string `json:"base"`
	// Profiles and DeclaredBy are recorded for listing; empty profiles read as
	// the wire's default.
	Profiles   []string `json:"profiles,omitempty"`
	DeclaredBy string   `json:"declared_by,omitempty"`
}

// inferenceHostedHost is a provider endpoint the service reaches with a named
// credential.
type inferenceHostedHost struct {
	Name       string   `json:"name"`
	Base       string   `json:"base"`
	Wire       string   `json:"wire"`
	Credential string   `json:"credential"`
	Profiles   []string `json:"profiles,omitempty"`
	DeclaredBy string   `json:"declared_by,omitempty"`
}

type inferenceHosts struct {
	Local *[]inferenceLocalHost `json:"local"`
	// Declared adds the hosts products declare to Local; see inferenceHostsFile.
	Declared *bool                        `json:"declared,omitempty"`
	Hosted   []inferenceHostedHost        `json:"hosted"`
	Ceilings map[string]inference.Ceiling `json:"ceilings,omitempty"`
	// IdleMS and RetentionMS override the provider's bounds when positive.
	IdleMS      int64 `json:"idle_ms,omitempty"`
	RetentionMS int64 `json:"retention_ms,omitempty"`
}

// runtimeInference composes abstraction.inference/chat@1 into the user
// runtime: a router over the configured hosts, decisions from the runtime's
// rights policy, credentials applied by the in-process holder with the
// inference service as a designated consumer, ceilings kept in the state
// directory, and one log record per operation into the runtime's sink.
type runtimeInference struct {
	content  *runtimeContent
	provider *inference.Provider
	// router is the router the provider reaches hosts through; the runtime also
	// publishes it as abstraction.router/router@1.
	router *router.Router
	// resources is who holds this machine's scarce resources, published as
	// abstraction.resource/table@1 and read by the router for its residency
	// answer (runtime_resources.go).
	resources      *runtimeResources
	host           *inferenceservice.Host
	remoteHost     *inferenceservice.Host
	endpoint       string
	remoteEndpoint string
	// operator serves operator@1 and owns the host configuration, local keys
	// and audit journal (runtime_inference_operator.go).
	operator *inferenceOperator
	// gateway opens and closes the gateway window from --gateway, the
	// persisted setting and operator@1 (runtime_inference_gateway.go).
	gateway *gatewayControl
	// providers is the provider declarations the runtime supervises and offers
	// as candidates and remote hosts (provider.go); nil without them.
	providers *runtimeProviders
	// provenance tells the router what the accepted inventory sources say this
	// machine holds (runtime_inventory.go); nil without providers.
	provenance *inventoryProvenance
	// mediation owns the declaration-generation-pinned OA endpoints returned
	// by resolution for supported native providers.
	mediation *providerMediation
	// registry serves providers as abstraction.facade/registry@1 on
	// registryEndpoint (registry.go); nil without providers.
	registry         *registryHost
	registryEndpoint string
	// lending serves the declared lending provider as
	// abstraction.storage/lend@1 on lendingEndpoint (runtime_lending.go); nil
	// without providers.
	lending         *lendingHost
	lendingEndpoint string
}

// Serve serves chat@1 and operator@1. The gateway window, when open, closes
// when the IPC host stops.
func (r *runtimeInference) Serve(ctx context.Context) error {
	if r.mediation != nil {
		if err := r.mediation.Start(ctx); err != nil {
			return err
		}
		defer r.mediation.Close()
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The router's model catalogue carries what the stores hold for as long
	// as the runtime serves.
	go r.provenance.run(serveCtx)
	served := make(chan error, 2)
	go func() { served <- r.host.Serve(serveCtx) }()
	go func() { served <- r.remoteHost.Serve(serveCtx) }()
	err := <-served
	cancel()
	err = errors.Join(err, <-served)
	r.gateway.stop()
	return err
}

// EmbedAvailable publishes embed@1 beside chat@1 on the same endpoint.
func (r *runtimeInference) EmbedAvailable() bool { return true }

// TranscriptionAvailable publishes transcription@1 beside chat@1 on the same endpoint.
func (r *runtimeInference) TranscriptionAvailable() bool { return true }

// SpeechAvailable publishes speech@1 beside chat@1 on the same endpoint.
func (r *runtimeInference) SpeechAvailable() bool { return true }

// ImageAvailable publishes image@1 beside chat@1 on the same endpoint.
func (r *runtimeInference) ImageAvailable() bool { return true }

// OperatorAvailable publishes operator@1 beside chat@1 on the same endpoint.
func (r *runtimeInference) OperatorAvailable() bool { return r.operator != nil }

func (r *runtimeInference) Close() error {
	r.gateway.stop()
	var mediationErr, providersErr, registryErr, lendingErr error
	if r.mediation != nil {
		mediationErr = r.mediation.Close()
	}
	if r.providers != nil {
		providersErr = r.providers.Close()
	}
	if r.registry != nil {
		registryErr = r.registry.Close()
	}
	if r.lending != nil {
		lendingErr = r.lending.Close()
	}
	r.resources.close()
	return errors.Join(mediationErr, providersErr, registryErr, lendingErr, r.host.Close(), r.remoteHost.Close(), r.provider.Close())
}

func inferenceEndpoint(options runtimeFlags) (string, error) {
	switch {
	case options.isolated != "":
		return bootstrap.Endpoint(options.isolated + "-inference")
	case options.endpoint != "":
		return options.endpoint + "-inference", nil
	}
	return options.defaultEndpoint("inference-v1")
}

func inferenceRemoteEndpoint(options runtimeFlags) (string, error) {
	switch {
	case options.isolated != "":
		return bootstrap.Endpoint(options.isolated + "-inference-remote")
	case options.endpoint != "":
		return options.endpoint + "-inference-remote", nil
	}
	return options.defaultEndpoint("inference-remote-v1")
}

func loadInferenceHosts(path string) (inferenceHosts, error) {
	var config inferenceHosts
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, fmt.Errorf("inference: %s: %w", path, err)
	}
	return config, nil
}

// usesDeclarations reports whether product declarations add local hosts.
func (c inferenceHosts) usesDeclarations() bool {
	if c.Declared != nil {
		return *c.Declared
	}
	return c.Local == nil
}

// routerHosts is every host the router reaches: the registry's declarations
// of role host, the mediated native inference providers, and the remote
// runtimes. Nothing else names a host.
func routerHosts(providers *runtimeProviders) []*router.Host {
	if providers == nil {
		return nil
	}
	hosts := providers.hostRouterHosts()
	hosts = append(hosts, providers.nativeInferenceHosts()...)
	return append(hosts, providers.remoteHosts()...)
}

// localRouterHost is the router host of one local entry.
func localRouterHost(l inferenceLocalHost) (*router.Host, error) {
	var h *router.Host
	switch l.Kind {
	case "lemonade":
		h = router.Lemonade(l.Base)
	case "lmstudio":
		h = router.LMStudio(l.Base)
	case "ollama":
		h = router.Ollama(l.Base)
	case "docker-model-runner":
		h = router.DockerModelRunner(l.Base)
	case "foundry-local":
		h = router.FoundryLocal(l.Base)
	case "whispercpp":
		h = router.WhisperCPP(l.Base)
	case "piper":
		h = router.Piper(l.Base)
	case "comfyui":
		h = router.ComfyUI(l.Base)
	case "swarmui":
		h = router.SwarmUI(l.Base)
	default:
		return nil, fmt.Errorf("inference: unknown local host kind %q", l.Kind)
	}
	h.DeclaredBy, h.Profiles = l.DeclaredBy, l.Profiles
	return h, nil
}

// composeInference needs the composed credentials: its policy decides and its
// holder applies. Without a state directory there is neither, and inference is
// omitted.
func composeInference(options runtimeFlags, c *runtimeCredentials, sink logging.Sink, report func(error)) (*runtimeInference, error) {
	if c == nil {
		if options.gateway != "" {
			return nil, errors.New("--gateway needs the runtime's credentials and rights state; give --state-dir")
		}
		return nil, nil
	}
	state, err := credentialsState(options)
	if err != nil || state == "" {
		return nil, err
	}
	dir := filepath.Join(state, "inference")
	content, err := composeContent(options, state, c.runtimeRights)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// The state the registration build wrote moves to the registry once.
	if err := migrateProviderState(state, report); err != nil {
		report(fmt.Errorf("runtime providers: migration: %w", err))
	}
	config, err := loadInferenceHosts(filepath.Join(dir, inferenceHostsFile))
	if err != nil {
		return nil, err
	}
	// Declarations are optional: a runtime whose providers directory cannot
	// be read serves inference without them and reports why. It then reaches
	// no host either, because the registry is the one directory of hosts.
	providers, err := openProviders(state, config.usesDeclarations(), report)
	if err != nil {
		report(fmt.Errorf("runtime providers: %w", err))
		providers = nil
	}
	r := router.New()
	// Who holds the card is measured once, by the table, and read from there.
	resources := composeResources(state, r, c.runtimeRights, report)
	r.SetResidency(tableResidency{table: resources.table})
	if providers != nil {
		providers.routerHostStates = func() []router.HostState {
			states, _, _, _, _ := r.Residency(false)
			return states
		}
		r.SetHosts(routerHosts(providers)...)
	}
	runtimeProgram := c.operators[0]
	// The router reads hosted listings as the runtime's own program.
	r.UseCredentials(func(ctx context.Context, consumer, name, target string) (map[string]string, error) {
		result := c.holder.Apply(ctx, cwire.Use{Subject: cwire.Subject{Account: c.owner, Program: runtimeProgram}, Consumer: consumer, Name: name, Target: target}, c.decide)
		if result.Outcome != cwire.ApplyOutcomeApplied {
			return nil, &router.CredentialRefusal{Outcome: result.Outcome.String(), Name: name}
		}
		return result.Headers, nil
	})
	limits := map[string]inference.Ceiling{}
	if providers != nil {
		limits = declarationBudgets(providers.hostFiles())
	}
	ceilings, err := inference.OpenCeilings(filepath.Join(dir, "ceilings.json"), limits, nil)
	if err != nil {
		return nil, err
	}
	journal, err := inference.OpenJournal(filepath.Join(dir, inferenceJournalFile), 0, nil)
	if err != nil {
		return nil, err
	}
	operator, err := newInferenceOperator(dir, c, r, ceilings, journal, report)
	if err != nil {
		return nil, err
	}
	cfg := inference.Config{Router: r, Ceilings: ceilings, OnError: report, ResolveContent: content.read, PrepareContentWrite: content.prepareWrite,
		LiveDialer: realtime.Dial,
		// Admit waits for an on-demand declared native provider's readiness
		// (FAC-R8) once the router has picked it; nil providers admits every
		// host unconditionally.
		Admit: providers.nativeAdmit,
		Decide: func(ctx context.Context, s inference.Subject, action, resource string) (string, error) {
			return c.decideAsking(ctx, rwire.Subject{Account: s.Account, Program: s.Program}, action, resource).Outcome.String(), nil
		},
		Apply: func(ctx context.Context, s inference.Subject, consumer, name, target string) (map[string]string, string) {
			result := c.holder.Apply(ctx, cwire.Use{Subject: cwire.Subject{Account: s.Account, Program: s.Program}, Consumer: consumer, Name: name, Target: target}, c.decide)
			return result.Headers, result.Outcome.String()
		},
		Record: func(rec inference.Record) {
			if err := journal.Append(rec); err != nil {
				report(err)
			}
			if sink == nil {
				return
			}
			attrs := map[string]string{"profile": rec.Profile, "route": rec.Route, "rung": rec.Rung, "operation": rec.Operation, "account": rec.Account, "program": rec.Program, "host": rec.Host,
				"model": rec.Model, "family": rec.Family, "credential": rec.Credential, "outcome": rec.Outcome, "reason": rec.Reason,
				"ceiling": rec.Ceiling, "tokens_in": strconv.FormatInt(rec.TokensIn, 10), "tokens_out": strconv.FormatInt(rec.TokensOut, 10),
				"audio_seconds": strconv.FormatInt(rec.AudioSeconds, 10), "characters": strconv.FormatInt(rec.Characters, 10), "wall_ms": strconv.FormatInt(rec.WallMS, 10)}
			for k, v := range attrs {
				if v == "" {
					delete(attrs, k)
				}
			}
			if err := sink.Write(logging.Record{Time: logging.At(time.Now()), Level: logging.LevelInfo, Msg: "abstraction.inference/chat@1", Attrs: attrs}); err != nil {
				report(err)
			}
		}}
	if config.IdleMS > 0 {
		cfg.Idle = time.Duration(config.IdleMS) * time.Millisecond
	}
	if config.RetentionMS > 0 {
		cfg.Retention = time.Duration(config.RetentionMS) * time.Millisecond
	}
	provider, err := inference.New(cfg)
	if err != nil {
		return nil, err
	}
	endpoint, err := inferenceEndpoint(options)
	if err != nil {
		//unchecked: tearing down what was already built before returning err, which this call already reports
		provider.Close()
		return nil, err
	}
	h, err := inferenceservice.ListenForPlacement(endpoint, provider, inference.ExecutionLocal)
	if err != nil {
		//unchecked: tearing down what was already built before returning err, which this call already reports
		provider.Close()
		return nil, err
	}
	h.OnError = report
	h.Operator = operator
	remoteEndpoint, err := inferenceRemoteEndpoint(options)
	if err != nil {
		//unchecked: tearing down what was already built before returning err, which this call already reports
		h.Close()
		//unchecked: tearing down what was already built before returning err, which this call already reports
		provider.Close()
		return nil, err
	}
	remoteHost, err := inferenceservice.ListenForPlacement(remoteEndpoint, provider, inference.ExecutionRemote)
	if err != nil {
		//unchecked: tearing down what was already built before returning err, which this call already reports
		h.Close()
		//unchecked: tearing down what was already built before returning err, which this call already reports
		provider.Close()
		return nil, err
	}
	remoteHost.OnError = report
	// The original endpoint remains the concrete local-execution binding.
	composed := &runtimeInference{provider: provider, router: r, resources: resources, host: h, remoteHost: remoteHost, endpoint: endpoint, remoteEndpoint: remoteEndpoint, operator: operator, content: content}
	composed.gateway = &gatewayControl{path: filepath.Join(dir, inferenceGatewayFile), report: report,
		open: func(address string) (*gateway.Window, error) {
			return openGateway(address, composed, cfg.Record, report)
		}}
	operator.gateway = composed.gateway
	if providers != nil {
		providers.decide = func(ctx context.Context, program, action, resource string) bool {
			return c.runtimeRights.decide(ctx, rwire.Subject{Account: c.owner, Program: program}, action, resource).Outcome == rwire.DecisionOutcomePermitted
		}
		providers.remotesChanged = operator.refreshHosts
		composed.providers, operator.providers = providers, providers
		c.host.OnStored = operator.credentialStored
		composed.provenance = newInventoryProvenance(providers, r, report)
		composed.mediation, err = newProviderMediation(endpoint, provider, providers, report)
		if err != nil {
			//unchecked: tearing down what was already built before returning err, which this call already reports
			composed.Close()
			return nil, err
		}
		providers.Watch(operator.refreshHosts)
		registryAt, err := registryEndpoint(options)
		if err == nil {
			composed.registry, err = listenRegistry(registryAt, &runtimeRegistry{providers: providers, operator: operator}, c.owner, report)
		}
		if err != nil {
			//unchecked: tearing down what was already built before returning err, which this call already reports
			composed.Close()
			return nil, fmt.Errorf("runtime registry: %w", err)
		}
		composed.registryEndpoint = registryAt
		// Lending is mediated like every other provider call: an application
		// resolves this runtime, and the runtime decides the engine's rule
		// before it reaches the provider that writes the link.
		lendingAt, err := lendingEndpoint(options)
		if err == nil {
			composed.lending, err = listenLending(lendingAt, &runtimeLending{providers: providers, rights: c.runtimeRights}, c.owner, report)
		}
		if err != nil {
			//unchecked: tearing down what was already built before returning err, which this call already reports
			composed.Close()
			return nil, fmt.Errorf("runtime lending: %w", err)
		}
		composed.lendingEndpoint = lendingAt
	}
	if err := composed.gateway.start(options.gateway); err != nil {
		//unchecked: tearing down what was already built before returning err, which this call already reports
		composed.Close()
		return nil, fmt.Errorf("--gateway %s: %w", options.gateway, err)
	}
	return composed, nil
}

// configure publishes chat@1, its router as router@1 and the resource table as
// table@1, and registers the inference rights actions. No rule is granted: an
// application calls complete only under an explicit rule, and reads or routes
// only under one. A program with no table.read rule still reads its own rows,
// which is the rule and not an omission (CONTRACT.md RES-T4).
func (r *runtimeInference) configure(o *host.Options, routerEndpoint, resourceEndpoint string) {
	if r == nil {
		return
	}
	o.Inference, o.InferenceEndpoint, o.InferenceRemoteEndpoint = r, r.endpoint, r.remoteEndpoint
	r.content.configure(o)
	o.Router, o.RouterEndpoint = r.router, routerEndpoint
	o.ResourceTable, o.ResourceTableEndpoint = r.resources.table, resourceEndpoint
	o.ResourceLeases = r.resources.book
	o.RightsActions = append(o.RightsActions, iwire.ResourceActions...)
	o.RightsActions = append(o.RightsActions, host.ResourceTableReadAction,
		resourceservice.ActionHold, resourceservice.ActionYield)
	if r.providers != nil {
		o.Providers = r.providers
		o.Registry, o.RegistryEndpoint = r.registry, r.registryEndpoint
		o.RightsActions = append(o.RightsActions, ActionInventoryProvide, ActionProviderManage, ActionInventoryRead, ActionLend)
		if r.lending != nil {
			o.Lending, o.LendingEndpoint = r.lending, r.lendingEndpoint
		}
		// What the machine holds, composed from every accepted declared source
		// and read by applications under one rule of its own.
		o.StorageInventoryPolicy = host.ContentPolicyFromRights(r.content.rights.decider(), ActionInventoryRead)
		o.StorageInventorySources = r.providers.inventorySources
	}
}

// runtimeRouterEndpoint follows the runtime's endpoint selection for router@1.
func runtimeRouterEndpoint(options runtimeFlags) (string, error) {
	switch {
	case options.isolated != "":
		return bootstrap.Endpoint(options.isolated + "-router")
	case options.endpoint != "":
		return options.endpoint + "-router", nil
	}
	return options.defaultEndpoint("router-v1")
}

// mustHosts is the hosts of a configuration inferenceRouter already accepted.
func mustHosts(hosts []*router.Host, _ error) []*router.Host { return hosts }

// close releases a composed service the runtime never took over.
func (r *runtimeInference) close() {
	if r != nil {
		//unchecked: close has no return value to report a close failure through
		r.Close()
	}
}

// LiveAvailable publishes the live profile on the shared inference endpoint.
func (r *runtimeInference) LiveAvailable() bool { return true }
