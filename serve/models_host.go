package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// Hosting a model is one registry declaration of role provider: the model host
// serves abstraction.inference/chat@1 on its own OA endpoint with an explicit
// allowlist, and the runtime mediates to it like any other native inference
// provider (facade CONTRACT.md FAC-R8). The declaration carries the engine path
// and the objects, so what the provider loads is decided here and never by the
// provider looking around the machine.
const (
	// modelHostName is the declaration, the router host and the provider
	// identity a hosted model is listed under.
	modelHostName = "modelhost"
	// modelHostProgram is the provider's program, installed beside the
	// runtime's own operator programs.
	modelHostProgram = "modelhostd"
	// modelHostEmbed is the embedding profile this provider publishes when the
	// engine was started with embeddings.
	modelHostEmbed = "abstraction.inference/embed@1"
)

// modelHostArgument names the flags a declaration carries, so reading a
// declaration back gives the same command a person typed.
const (
	argEndpoint       = "--endpoint"
	argEngine         = "--engine"
	argModel          = "--model"
	argResource       = "--resource-endpoint"
	argIdle           = "--idle"
	argEmbed          = "--embeddings"
	argEngineArg      = "--engine-arg"
	argRuntimeProgram = "--runtime-program"
)

// hostedModel is one entry of the allowlist as the declaration spells it.
type hostedModel struct {
	Name   string `json:"name"`
	Store  string `json:"store"`
	Object string `json:"object"`
}

func (m hostedModel) argument() string { return m.Name + "=" + m.Store + "/" + m.Object }

// hostSettings is everything a declaration of the model host carries besides
// its models: read from an existing declaration, then overridden by the flags
// this command was given.
type hostSettings struct {
	program  string
	endpoint string
	engine   string
	resource string
	idle     string
	embed    bool
	extra    []string
}

// modelsHostCommand declares one object as hosted, or takes one back.
func modelsHostCommand(command string, args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, modelsUsage)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options serviceOptions
	options.bind(flags)
	engine := flags.String("engine", "", "absolute path of the llama-server this machine already has")
	program := flags.String("program", "", "the model host provider's program")
	providerEndpointName := flags.String("provider-endpoint", modelHostName, "the provider's own OA endpoint name")
	resourceEndpoint := flags.String("resource-endpoint", "", "where abstraction.resource/leases@1 answers")
	as := flags.String("as", "", "the name callers ask for; the store's own name by default")
	idle := flags.Duration("idle", 0, "unload a model nothing has asked for")
	embeddings := flags.Bool("embeddings", false, "start the engine with embeddings and publish embed@1")
	var engineArgs repeated
	flags.Var(&engineArgs, "engine-arg", "one extra argument for the engine")
	positional, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, command, modelsUsage, args, err)
	}
	if command == "models hosts" {
		if len(positional) != 0 {
			return flagMistake(diagnostics, command, modelsUsage, "takes no arguments")
		}
	} else if len(positional) != 1 {
		return flagMistake(diagnostics, command, modelsUsage, "name exactly one object, as <store>/<id>")
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, modelsUsage, "--timeout must not be negative")
	}

	w := newWaiting(options.budget)
	defer w.stop()
	machine, source := options.machine(diagnostics, command)
	call, done := w.call()
	registry, err := machine.ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	call, done = w.call()
	list, err := registry.Declarations(call)
	done()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	if list.Outcome != wire.DeclarationListOutcomePage {
		return refusal(command, list.Outcome.String(), "")
	}
	settings, models := readHostDeclaration(list)

	if command == "models hosts" {
		if options.asJSON {
			return writeJSON(output, models)
		}
		return printHosted(output, settings, models)
	}

	store, id, ok := strings.Cut(positional[0], "/")
	if !ok || store == "" || id == "" {
		return flagMistake(diagnostics, command, modelsUsage, "name the object as <store>/<id>, the way the inventory names it")
	}

	if command == "models unhost" {
		kept := slices.DeleteFunc(models, func(m hostedModel) bool { return m.Store == store && m.Object == id })
		if len(kept) == len(models) {
			return refusal(command, "unknown", store+"/"+id+" is not hosted")
		}
		if len(kept) == 0 {
			call, done = w.call()
			change, err := registry.Withdraw(call, list.Revision, modelHostName)
			done()
			if err != nil {
				return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
			}
			if change.Outcome != wire.DeclarationEditOutcomeApplied {
				return refusal(command, change.Outcome.String(), change.Reason)
			}
			return credentialReport(output, options.asJSON, change,
				fmt.Sprintf("unhosted %s/%s; the model host is withdrawn and its engine stopped; revision %s", store, id, change.Revision))
		}
		return declareHost(w, registry, list.Revision, settings, kept, command, output, options.asJSON,
			fmt.Sprintf("unhosted %s/%s", store, id))
	}

	// The name callers ask for is the store's own name for the content, so the
	// router's family and the inventory's held_in are the same model
	// (abstraction-model CONTRACT.md MODEL-C2).
	found, size, err := lookupHostedObject(w, options, diagnostics, command, store, id)
	if err != nil {
		var exit *exitError
		if errors.As(err, &exit) {
			return err
		}
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	if found == "" {
		return refusal(command, "unknown_object", "no store called "+store+" holds "+id)
	}
	name := *as
	if name == "" {
		name = found
	}

	settings = settings.with(*program, *providerEndpointName, *engine, *resourceEndpoint, *idle, *embeddings, engineArgs, options.endpoint)
	if settings.engine == "" {
		return flagMistake(diagnostics, command, modelsUsage, "--engine names the llama-server this machine already has; nothing is downloaded and nothing is looked for")
	}
	if !filepath.IsAbs(settings.engine) {
		return flagMistake(diagnostics, command, modelsUsage, "--engine must be an absolute path")
	}
	if _, err := os.Stat(settings.engine); err != nil {
		return flagMistake(diagnostics, command, modelsUsage, fmt.Sprintf("no engine at %s", settings.engine))
	}
	if settings.program == "" {
		return &exitError{exitNotResolved, fmt.Errorf("%s: no %s beside this command; pass --program", command, modelHostProgram)}
	}

	models = slices.DeleteFunc(models, func(m hostedModel) bool {
		return (m.Store == store && m.Object == id) || m.Name == name
	})
	models = append(models, hostedModel{Name: name, Store: store, Object: id})
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return declareHost(w, registry, list.Revision, settings, models, command, output, options.asJSON,
		fmt.Sprintf("hosting %s/%s as %s (%d bytes) with %s", store, id, name, size, settings.engine))
}

// with applies this command's flags over what the declaration already carried.
func (s hostSettings) with(program, endpoint, engine, resource string, idle time.Duration, embed bool, extra []string, runtimeEndpoint string) hostSettings {
	if program != "" {
		s.program = program
	}
	if s.program == "" {
		s.program = siblingProgram(modelHostProgram)
	}
	if endpoint != "" {
		s.endpoint = endpoint
	}
	if s.endpoint == "" {
		s.endpoint = modelHostName
	}
	if engine != "" {
		s.engine = engine
	}
	if resource != "" {
		s.resource = resource
	}
	if s.resource == "" {
		s.resource = defaultLeaseEndpoint(runtimeEndpoint)
	}
	if idle > 0 {
		s.idle = idle.String()
	}
	if embed {
		s.embed = true
	}
	if len(extra) > 0 {
		s.extra = append([]string(nil), extra...)
	}
	return s
}

// defaultLeaseEndpoint is where the runtime this command is talking to serves
// abstraction.resource/leases@1. It follows the runtime's own endpoint
// selection, so a runtime started with an explicit endpoint is not confused
// with the installed one.
func defaultLeaseEndpoint(runtimeEndpoint string) string {
	if runtimeEndpoint != "" {
		return runtimeEndpoint + "-resource-table"
	}
	return providerEndpointPath("resource-table-v1")
}

// siblingProgram is one of the runtime's operator programs, beside this
// command's own executable.
func siblingProgram(name string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(filepath.Dir(filepath.Clean(exe)), name)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// readHostDeclaration reads the model host's declaration back: what it was
// given and what it hosts. A registry with no model host reads empty.
func readHostDeclaration(list wire.DeclarationList) (hostSettings, []hostedModel) {
	var settings hostSettings
	var models []hostedModel
	for _, state := range list.Declarations {
		d := state.Declaration
		if d.Name != modelHostName {
			continue
		}
		settings.program, settings.endpoint = d.Program, d.Endpoint
		for i := 0; i < len(d.Arguments); i++ {
			switch d.Arguments[i] {
			case argEngine:
				if i+1 < len(d.Arguments) {
					settings.engine = d.Arguments[i+1]
					i++
				}
			case argResource:
				if i+1 < len(d.Arguments) {
					settings.resource = d.Arguments[i+1]
					i++
				}
			case argIdle:
				if i+1 < len(d.Arguments) {
					settings.idle = d.Arguments[i+1]
					i++
				}
			case argEngineArg:
				if i+1 < len(d.Arguments) {
					settings.extra = append(settings.extra, d.Arguments[i+1])
					i++
				}
			case argEmbed:
				settings.embed = true
			case argModel:
				if i+1 < len(d.Arguments) {
					if m, ok := parseHostedModel(d.Arguments[i+1]); ok {
						models = append(models, m)
					}
					i++
				}
			}
		}
		return settings, models
	}
	return settings, models
}

func parseHostedModel(entry string) (hostedModel, bool) {
	name, object, ok := strings.Cut(entry, "=")
	if !ok {
		return hostedModel{}, false
	}
	store, id, ok := strings.Cut(object, "/")
	if !ok || name == "" || store == "" || id == "" {
		return hostedModel{}, false
	}
	return hostedModel{Name: name, Store: store, Object: id}, true
}

// declareHost writes the declaration the settings and the models describe.
func declareHost(w *waiting, registry *client.Registry, revision string, settings hostSettings, models []hostedModel,
	command string, output io.Writer, asJSON bool, said string) error {
	arguments := []string{"serve", argEndpoint, providerEndpointToken, argEngine, settings.engine, argResource, settings.resource}
	programs, err := modelHostRuntimePrograms()
	if err != nil {
		return fmt.Errorf("%s: runtime program: %w", command, err)
	}
	for _, program := range programs {
		arguments = append(arguments, argRuntimeProgram, program)
	}
	if settings.idle != "" {
		arguments = append(arguments, argIdle, settings.idle)
	}
	if settings.embed {
		arguments = append(arguments, argEmbed)
	}
	for _, extra := range settings.extra {
		arguments = append(arguments, argEngineArg, extra)
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		arguments = append(arguments, argModel, m.argument())
		names = append(names, m.Name)
	}
	contracts := []string{inference.Contract}
	profiles := []string{"profile:chat"}
	if settings.embed {
		contracts = append(contracts, modelHostEmbed)
		profiles = append(profiles, "profile:embed")
	}
	// The declaration names the card its engine loads weights into beside its
	// profiles, so Declare writes the program its own abstraction.resource/hold
	// rule (facade CONTRACT.md FAC-R3): the model host holds what it loads.
	resources := append([]string{instrument.Card0}, profiles...)
	declaration := wire.Declaration{Name: modelHostName, Program: identity.NormalizeSubjectProgram(filepath.Clean(settings.program)), Arguments: arguments,
		Endpoint: settings.endpoint, Transport: wire.DeclarationTransportNative, Contracts: contracts,
		Guarantees: []string{inference.GuaranteeLocalOnly}, Resources: resources, Models: names,
		Activation: wire.ActivationOnDemand, Role: wire.DeclarationRoleProvider}
	call, done := w.call()
	change, err := registry.Declare(call, revision, declaration)
	done()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	if change.Outcome != wire.DeclarationEditOutcomeApplied {
		return refusal(command, change.Outcome.String(), change.Reason)
	}
	return credentialReport(output, asJSON, change, said+"; revision "+change.Revision)
}

// modelHostRuntimePrograms tells an on-demand model host which installed
// runtime programs may forward to it. The command's executable and its
// runtime siblings are fixed before the provider starts.
func modelHostRuntimePrograms() ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	self = identity.CanonicalProgramPath(filepath.Clean(self))
	programs := []string{self}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"openabstractions.exe", "openabstractionsw.exe"} {
			sibling := filepath.Join(filepath.Dir(self), name)
			if _, err := os.Stat(sibling); err == nil {
				sibling = identity.CanonicalProgramPath(sibling)
				if !slices.ContainsFunc(programs, func(p string) bool { return samePrograms(p, sibling) }) {
					programs = append(programs, sibling)
				}
			}
		}
	}
	return programs, nil
}

// lookupHostedObject reads the runtime's own storage inventory for the store's
// name for this object and the size of its weights. The path stays with the
// provider that holds the store; this command never learns one.
func lookupHostedObject(w *waiting, options serviceOptions, diagnostics io.Writer, command, store, id string) (string, int64, error) {
	machine, source := options.machine(diagnostics, command)
	call, done := w.call()
	inventory, err := machine.ResolveStorageInventory(call, client.Requirements{})
	done()
	if err != nil {
		return "", 0, notResolved(command, source, err)
	}
	continuation := ""
	for pages := 0; pages < 512; pages++ {
		call, done := w.call()
		page, err := inventory.List(call, continuation, 256)
		done()
		if err != nil {
			return "", 0, err
		}
		if page.Outcome != content.InventoryOutcomePage {
			return "", 0, fmt.Errorf("inventory %s", page.Outcome)
		}
		for _, m := range page.Manifests {
			if m.Manifest.Store != store || !hostedManifestNamed(m.Manifest, id) {
				continue
			}
			name := id
			if len(m.Manifest.Names) > 0 {
				name = m.Manifest.Names[0].Name
			}
			size := int64(0)
			for _, e := range m.Manifest.Entries {
				if e.Size > size {
					size = e.Size
				}
			}
			return name, size, nil
		}
		if page.Complete {
			break
		}
		continuation = page.Continuation
	}
	return "", 0, nil
}

// hostedManifestNamed reports whether id names m: its id, one of its names, or
// one of its entries' locators, the way lend@1 resolves an object (MODELBRIDGE-L1).
func hostedManifestNamed(m content.Manifest, id string) bool {
	if m.ID == id {
		return true
	}
	for _, n := range m.Names {
		if n.Name == id {
			return true
		}
	}
	for _, e := range m.Entries {
		if e.Locator == id {
			return true
		}
	}
	return false
}

func printHosted(output io.Writer, settings hostSettings, models []hostedModel) error {
	if len(models) == 0 {
		_, err := fmt.Fprintln(output, "no model is hosted")
		return err
	}
	if _, err := fmt.Fprintf(output, "engine: %s\n", settings.engine); err != nil {
		return err
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
	fmt.Fprintln(table, "MODEL\tSTORE\tOBJECT")
	for _, m := range models {
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintf(table, "%s\t%s\t%s\n", m.Name, m.Store, m.Object)
	}
	return table.Flush()
}

// cardHoldPrograms is each declared program paired with the card resources
// its declaration names, as resources card:<n>, across every declaration
// source: operator, product and the installation (facade CONTRACT.md FAC-R3).
// A declaration that names no card resource holds nothing; the pairing is
// exactly what it declares, never inferred from its contracts or models.
func (p *runtimeProviders) cardHoldPrograms() map[string][]string {
	out := map[string][]string{}
	for _, s := range p.sorted() {
		d := s.file.Declaration
		if d.remote() || d.host() || d.Program == "" {
			continue
		}
		if cards := d.resources("card"); len(cards) > 0 {
			out[d.Program] = append(slices.Clone(out[d.Program]), cards...)
		}
	}
	return out
}

// cardHoldSignature flattens a cardHoldPrograms reading into one sorted
// slice, so two readings compare by value.
func cardHoldSignature(programs map[string][]string) []string {
	var out []string
	for program, cards := range programs {
		for _, card := range cards {
			out = append(out, program+"\x00"+card)
		}
	}
	sort.Strings(out)
	return out
}

// writeCardHoldRules writes abstraction.resource/hold on each card:<n>
// resource a declaration names, for its own program, with why "provider add"
// (facade CONTRACT.md FAC-R3): the capability that owns the resource kind
// writes the permit rule its contract needs, the same way Declare writes a
// store's inventory.provide, leaving an existing rule as it is. This runs
// once for the declarations already read when the runtime opens, which
// covers the installation's and a product's, and again whenever the set of
// programs and their card resources changes, which covers a live Declare.
func writeCardHoldRules(operator *inferenceOperator, providers *runtimeProviders) func() {
	var mu sync.Mutex
	var last []string
	return func() {
		programs := providers.cardHoldPrograms()
		signature := cardHoldSignature(programs)
		mu.Lock()
		unchanged := slices.Equal(signature, last)
		last = signature
		mu.Unlock()
		if unchanged {
			return
		}
		by := operator.credentials.runtimeRights.self()
		var errs []error
		for program, cards := range programs {
			for _, card := range cards {
				errs = append(errs, operator.permitRuleWhy(by, program, resourceservice.ActionHold, ResourceCard(card), providerAddWhy))
			}
		}
		if err := errors.Join(errs...); err != nil {
			complain("runtime rights:", err)
		}
	}
}
