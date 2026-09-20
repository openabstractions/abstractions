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
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-inference/go/gateway"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	logging "github.com/openabstractions/abstraction-logging/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// inferenceHostsFile is the runtime's inference host configuration inside its
// state directory: the local runtimes to reach, the hosted hosts and their
// credential names, and per-credential ceilings. The `inference host` commands
// and the Panel write it. The local hosts are the "local" entries plus, when
// "declared" is true, the hosts the products on this machine declare
// (runtime_inference_declared.go). An absent "declared" is true without
// "local" and false with it, and "local": [] still selects none. An absent
// "hosted" selects no hosted host.
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
	router         *router.Router
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
	// mediation owns the declaration-generation-pinned OA endpoints returned
	// by resolution for supported native providers.
	mediation *providerMediation
	// registry serves providers as abstraction.facade/registry@1 on
	// registryEndpoint (registry.go); nil without providers.
	registry         *registryHost
	registryEndpoint string
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
	var mediationErr error
	if r.mediation != nil {
		mediationErr = r.mediation.Close()
	}
	if r.providers != nil {
		r.providers.Close()
	}
	if r.registry != nil {
		r.registry.Close()
	}
	return errors.Join(mediationErr, r.host.Close(), r.remoteHost.Close(), r.provider.Close())
}

func inferenceEndpoint(options runtimeFlags) (string, error) {
	switch {
	case options.isolated != "":
		return bootstrap.Endpoint(options.isolated + "-inference")
	case options.endpoint != "":
		return options.endpoint + "-inference", nil
	}
	return bootstrap.Endpoint("inference-v1")
}

func inferenceRemoteEndpoint(options runtimeFlags) (string, error) {
	switch {
	case options.isolated != "":
		return bootstrap.Endpoint(options.isolated + "-inference-remote")
	case options.endpoint != "":
		return options.endpoint + "-inference-remote", nil
	}
	return bootstrap.Endpoint("inference-remote-v1")
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

func inferenceRouter(config inferenceHosts, report func(error)) (*router.Router, error) {
	hosts, err := inferenceHostList(config, report)
	if err != nil {
		return nil, err
	}
	return router.New(hosts...), nil
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

// inferenceHostList is the router hosts a configuration names: its local
// entries, the declared local hosts no entry names, and its hosted hosts.
func inferenceHostList(config inferenceHosts, report func(error)) ([]*router.Host, error) {
	var hosts []*router.Host
	named := map[string]bool{}
	if config.Local != nil {
		for _, l := range *config.Local {
			h, err := localRouterHost(l)
			if err != nil {
				return nil, err
			}
			named[h.Name] = true
			hosts = append(hosts, h)
		}
	}
	if config.usesDeclarations() {
		for _, h := range declaredLocalHosts(report) {
			if !named[h.Name] {
				hosts = append(hosts, h)
			}
		}
	}
	for _, h := range config.Hosted {
		if h.Name == "" || h.Base == "" || h.Wire == "" {
			return nil, errors.New("inference: a hosted host needs a name, base and wire")
		}
		if h.Wire == router.WireRemote {
			// A remote runtime is a registry declaration (provider.go).
			if report != nil {
				report(fmt.Errorf("inference: hosted host %s names wire %s; declare it with provider add --remote", h.Name, h.Wire))
			}
			continue
		}
		hosted := router.NewHosted(h.Name, h.Base, h.Wire, h.Credential)
		hosted.DeclaredBy, hosted.Profiles = h.DeclaredBy, h.Profiles
		hosts = append(hosts, hosted)
	}
	return hosts, nil
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
	// Provider declarations are optional: a runtime whose providers directory
	// cannot be read serves inference without them and reports why.
	providers, err := openProviders(state, report)
	if err != nil {
		report(fmt.Errorf("runtime providers: %w", err))
		providers = nil
	}
	r, err := inferenceRouter(config, report)
	if err != nil {
		return nil, err
	}
	if providers != nil {
		hosts := append(mustHosts(inferenceHostList(config, report)), providers.nativeInferenceHosts()...)
		r.SetHosts(append(hosts, providers.remoteHosts()...)...)
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
	ceilings, err := inference.OpenCeilings(filepath.Join(dir, "ceilings.json"), config.Ceilings, nil)
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
		provider.Close()
		return nil, err
	}
	h, err := inferenceservice.ListenForPlacement(endpoint, provider, inference.ExecutionLocal)
	if err != nil {
		provider.Close()
		return nil, err
	}
	h.OnError = report
	h.Operator = operator
	remoteEndpoint, err := inferenceRemoteEndpoint(options)
	if err != nil {
		h.Close()
		provider.Close()
		return nil, err
	}
	remoteHost, err := inferenceservice.ListenForPlacement(remoteEndpoint, provider, inference.ExecutionRemote)
	if err != nil {
		h.Close()
		provider.Close()
		return nil, err
	}
	remoteHost.OnError = report
	// The original endpoint remains the concrete local-execution binding.
	composed := &runtimeInference{provider: provider, router: r, host: h, remoteHost: remoteHost, endpoint: endpoint, remoteEndpoint: remoteEndpoint, operator: operator, content: content}
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
		composed.mediation, err = newProviderMediation(endpoint, provider, providers, report)
		if err != nil {
			composed.Close()
			return nil, err
		}
		providers.Watch(operator.refreshHosts)
		registryAt, err := registryEndpoint(options)
		if err == nil {
			composed.registry, err = listenRegistry(registryAt, &runtimeRegistry{providers: providers, operator: operator}, c.owner, report)
		}
		if err != nil {
			composed.Close()
			return nil, fmt.Errorf("runtime registry: %w", err)
		}
		composed.registryEndpoint = registryAt
	}
	if err := composed.gateway.start(options.gateway); err != nil {
		composed.Close()
		return nil, fmt.Errorf("--gateway %s: %w", options.gateway, err)
	}
	return composed, nil
}

// configure publishes chat@1 and its router as router@1, and registers the
// inference rights actions. No rule is granted: an application calls complete
// only under an explicit rule, and reads or routes only under one.
func (r *runtimeInference) configure(o *host.Options, routerEndpoint string) {
	if r == nil {
		return
	}
	o.Inference, o.InferenceEndpoint, o.InferenceRemoteEndpoint = r, r.endpoint, r.remoteEndpoint
	r.content.configure(o)
	o.Router, o.RouterEndpoint = r.router, routerEndpoint
	o.RightsActions = append(o.RightsActions, iwire.ResourceActions...)
	if r.providers != nil {
		o.Providers = r.providers
		o.Registry, o.RegistryEndpoint = r.registry, r.registryEndpoint
		o.RightsActions = append(o.RightsActions, ActionInventoryProvide, ActionProviderManage)
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
	return bootstrap.Endpoint("router-v1")
}

// mustHosts is the hosts of a configuration inferenceRouter already accepted.
func mustHosts(hosts []*router.Host, _ error) []*router.Host { return hosts }

// close releases a composed service the runtime never took over.
func (r *runtimeInference) close() {
	if r != nil {
		r.Close()
	}
}

// LiveAvailable publishes the live profile on the shared inference endpoint.
func (r *runtimeInference) LiveAvailable() bool { return true }
