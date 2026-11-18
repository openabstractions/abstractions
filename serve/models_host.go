package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/redact"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	hostdeclaration "github.com/openabstractions/abstraction-provider-modelhost/go/declaration"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// Hosting a model is one registry declaration of role provider: the model host
// serves abstraction.inference/chat@1 on its own OA endpoint with an explicit
// allowlist, and the runtime mediates to it like any other native inference
// provider (facade CONTRACT.md FAC-R8). The declaration carries the engine path
// and the objects, so what the provider loads is decided here and never by the
// provider looking around the machine.
const (
	// modelHostName is the declaration, the router host and the provider
	// identity a hosted model is listed under (product.ModelHost, llama.cpp).
	modelHostName = product.ModelHost
	// modelHostProgram is the provider's program, installed beside the
	// runtime's own operator programs.
	modelHostProgram = "modelhostd"
	// modelHostEmbed is the embedding profile this provider publishes when the
	// engine was started with embeddings.
	modelHostEmbed = "abstraction.inference/embed@1"
	// pytorchName is the Diffusers provider as the models host declares it:
	// its declaration, router host and provider identity (pytorch CONTRACT.md
	// PYTORCH-D1; product.DiffusersModelsHost).
	pytorchName = product.DiffusersModelsHost
	// pytorchProgram is that provider's program, beside the runtime's own
	// operator programs when Setup installed it.
	pytorchProgram = "pytorchd"
)

// modelHostArgument names the flags a declaration carries, so reading a
// declaration back gives the same command a person typed.
const (
	argEndpoint       = "--endpoint"
	argConfig         = "--config"
	argEngine         = "--engine"
	argModel          = "--model"
	argModelContent   = "--model-content"
	argResource       = "--resource-endpoint"
	argHold           = "--resource"
	argIdle           = "--idle"
	argEmbed          = "--embeddings"
	argEngineArg      = "--engine-arg"
	argRuntimeProgram = "--runtime-program"
	argState          = "--state"
	argVideoModel     = "--video-model"
	argVideoKind      = "--video-kind"
	argVideoEngine    = "--video-engine"
	argTorchPython    = "--torch-python"
	argTorchSource    = "--torch-source"
	argVideoOption    = "--video-model-option"
	argVideoEngineArg = "--video-engine-arg"
	argImageModel     = "--image-model"
	argImageTimeLimit = "--image-time-limit"
	// pytorchd serve's names for the venv's python and AMD's source.
	argPython = "--python"
	argSource = "--source"
)

// The video engine kinds modelhostd serves (its --video-kind).
const (
	videoKindSdCpp = "sdcpp"
	videoKindTorch = "torch"
)

// hostMedia is the video and image engine this command sets beside the chat
// models: an sd-server video in the model host's declaration (modelhost
// README "Video"), the PyTorch sets in the PyTorch provider's (pytorch
// CONTRACT.md PYTORCH-D1).
type hostMedia struct {
	// videoModel is the video model set served for profile video; empty
	// serves no video.
	videoModel string
	// videoKind is sdcpp (sd-server at videoEngine) or torch (the OA PyTorch
	// engine on torchPython with AMD's source at torchSource).
	videoKind                string
	videoEngine              string
	torchPython, torchSource string
	// options are the torch model set's options, NAME=VALUE.
	options []string
	// engineArgs are sd-server's extra arguments (modelhostd serve
	// --video-engine-arg), which a hand-made declaration may carry; only the
	// sdcpp kind reads them.
	engineArgs []string
	// imageModel is the image model set served for profile image on the
	// PyTorch engine; empty serves no image.
	imageModel string
}

func (m hostMedia) any() bool { return m.videoModel != "" || m.imageModel != "" }

// hostVideo reports a video the model host serves: one on sd-server.
func (m hostMedia) hostVideo() bool { return m.videoModel != "" && m.kind() == videoKindSdCpp }

// torchAny reports a set the PyTorch provider serves.
func (m hostMedia) torchAny() bool { return m.torchVideo() || m.imageModel != "" }

// kind is the video kind, sdcpp when none was named.
func (m hostMedia) kind() string {
	if m.videoKind == "" {
		return videoKindSdCpp
	}
	return m.videoKind
}

// hostedModel is one entry of the allowlist as the declaration spells it.
type hostedModel struct {
	Name    string `json:"name"`
	Store   string `json:"store"`
	Object  string `json:"object"`
	Content string `json:"content,omitempty"`
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
	// configs are the store configurations modelhostd reads beside the
	// stores it finds itself (modelhostd serve --config, repeated): each
	// one an inventory source's own --config whose store holds a hosted
	// model, so the provider resolves an object in a store only that
	// source's configuration names, such as Setup's model folder.
	configs []string
	// state is the model host's private state folder, where engine install
	// wrote the engine folder's record; empty is the provider's default.
	state string
	// media is the video and image engine the provider also serves.
	media hostMedia
	// card is the number of the card the engines load into; empty is 0. The
	// serve line carries it as --resource card:N, the declaration as the
	// resource card:N: one value.
	card        string
	invalidHold string
	// passthrough are serve arguments a declaration carried that this
	// command does not set itself, such as a hand-made declaration's
	// --video-ready; they are kept as they were.
	passthrough []string
	// declared is what an existing declaration said that this command does
	// not decide; a hand-made declaration keeps it.
	declared declaredFields
	// torch is the PyTorch provider's declaration beside the model host's,
	// which media's PyTorch sets are written to.
	torch torchSettings
	// legacyTorch reports a model host serve line that still carries the
	// PyTorch engine's options, which modelhostd refuses since the engine is
	// its own provider; the next declaration moves them to that provider.
	legacyTorch bool
}

// torchSettings are what the PyTorch provider's declaration carries besides
// its sets: read from an existing declaration, then overridden by this
// command's flags.
type torchSettings struct {
	program  string
	endpoint string
	resource string
	proved   *heldApplicationManifest
	// card is the card its engine loads into; empty takes the model host's.
	card        string
	invalidHold string
	// state is pytorchd's state folder, where pytorchd models install
	// recorded the sets' places; empty is the provider's default. Only a
	// hand-made declaration names one.
	state       string
	passthrough []string
	declared    declaredFields
}

// declaredFields are the parts of an existing model host declaration this
// command keeps as they were: its activation (an attach declaration stays
// attach), transport, guarantees, resources other than card and profile (a
// store:* resource among them) and the contracts, profiles and scoped models
// this command does not manage. A declaration written first takes defaults.
type declaredFields struct {
	present    bool
	activation wire.Activation
	transport  wire.DeclarationTransport
	guarantees []string
	resources  []string
	contracts  []string
	models     []string
	extensions []wire.DeclarationExtension
	remotePin  string
}

// hostContracts are the contracts this command declares for the model host
// from its settings; image@1 among them is the one a declaration written
// before the PyTorch provider carried.
var hostContracts = []string{inference.Contract, modelHostEmbed, inference.PreloadContract, inference.VideoContract, inference.ImageContract}

// pytorchContracts are the contracts this command declares for the PyTorch
// provider.
var pytorchContracts = []string{inference.VideoContract, inference.ImageContract}

// modelsHostCommand declares one object as hosted, or takes one back, and
// sets the video and image engine: an sd-server video on the model host, the
// PyTorch sets on the PyTorch provider.
// nativeProviderLimit is why this platform runs no native provider, or ""
// where it runs them. A macOS socket endpoint cannot carry the Program proof a
// provider requires of the runtime; the macOS package ships no model host.
var nativeProviderLimit = nativeProviderLimitOn(runtime.GOOS)

func nativeProviderLimitOn(goos string) string {
	if goos == "darwin" {
		return "native providers are not available on macOS: their caller check needs an XPC endpoint"
	}
	return ""
}

const linuxModelHostRegistrationLimit = "setting up native model hosting is unavailable on Linux"

func modelsHostCommand(command string, args []string, output, diagnostics io.Writer) error {
	return modelsHostCommandOn(runtime.GOOS, command, args, output, diagnostics)
}

// The platform choice governs CLI availability. Engine checks and declarations
// still use the host operating system.
func modelsHostCommandOn(goos, command string, args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, modelsUsage)
		return err
	}
	// Declaring a hosted model names a provider this platform cannot start;
	// listing and removing what an earlier version declared still work.
	if command == "models host" && nativeProviderLimit != "" {
		return &exitError{exitRefusedCall, fmt.Errorf("%s: %s", command, nativeProviderLimit)}
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options serviceOptions
	options.bind(flags)
	engine := flags.String("engine", "", "absolute path of the llama-server that modelhostd engine install staged")
	program := flags.String("program", "", "the model host provider's program")
	torchProgram := flags.String("pytorch-program", "", "the Diffusers provider's program")
	providerEndpointName := flags.String("provider-endpoint", "", "the provider's own OA endpoint name; the declaration's own, else modelhost")
	resourceEndpoint := flags.String("resource-endpoint", "", "where abstraction.resource/leases@1 answers")
	as := flags.String("as", "", "the name callers ask for; the store's own name by default")
	contentPin := flags.String("content", "", "the reviewed catalogue SHA-256 for this downloaded object")
	idle := flags.Duration("idle", 0, "unload a model nothing has asked for")
	embeddings := flags.Bool("embeddings", false, "start the engine with embeddings and publish embed@1")
	var engineArgs repeated
	flags.Var(&engineArgs, "engine-arg", "one extra argument for the engine")
	stateDir := flags.String("state", "", "the model host's state folder, where engine install recorded the engine")
	videoModel := flags.String("video-model", "", "the video model set the model host serves")
	videoKind := flags.String("video-kind", "", "sdcpp or torch, the engine the video runs on")
	videoEngine := flags.String("video-engine", "", "absolute path of the sd-server engine install staged")
	getVideo := flags.Bool("get", false, "get the pinned sd-server and the video set's files through download jobs first")
	credential := flags.String("credential", "", "a registered Hugging Face credential for gated video files")
	torchPython := flags.String("torch-python", "", "absolute path of the reviewed venv's python")
	torchSource := flags.String("torch-source", "", "AMD's Hummingbird-XT infer folder")
	var videoOptions repeated
	flags.Var(&videoOptions, "video-model-option", "one torch model set option, NAME=VALUE")
	imageModel := flags.String("image-model", "", "the image model set the Diffusers provider serves")
	card := flags.String("card", "", "the card the engines load into")
	planOnly := flags.Bool("plan", false, "print the registry edits this command would make, as JSON, and write nothing")
	positional, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, command, modelsUsage, args, err)
	}
	contentGiven := false
	flags.Visit(func(f *flag.Flag) { contentGiven = contentGiven || f.Name == "content" })
	if contentGiven && *contentPin == "" {
		return flagMistake(diagnostics, command, modelsUsage, "--content requires a reviewed catalogue SHA-256")
	}
	if *getVideo && (*videoModel == "" || *videoEngine != "" || *videoKind == videoKindTorch) {
		return flagMistake(diagnostics, command, modelsUsage, "--get takes --video-model SET of an sd-server set, and no --video-engine")
	}
	if *credential != "" && (!*getVideo || !validCredentialName(*credential)) {
		return flagMistake(diagnostics, command, modelsUsage, "--credential names a registered credential and goes with --get")
	}
	mediaFlags := *videoModel != "" || *videoKind != "" || *videoEngine != "" || *torchPython != "" || *torchSource != "" ||
		len(videoOptions) > 0 || *imageModel != ""
	if contentGiven && (command != "models host" || len(positional) != 1) {
		return flagMistake(diagnostics, command, modelsUsage, "--content requires one models host object")
	}
	if *planOnly && command == "models hosts" {
		return flagMistake(diagnostics, command, modelsUsage, "--plan goes with models host and models unhost")
	}
	switch {
	case command == "models hosts":
		if len(positional) != 0 {
			return flagMistake(diagnostics, command, modelsUsage, "takes no arguments")
		}
	case command == "models host" && len(positional) == 0 && mediaFlags:
		// Only the video and image engine is set.
	case len(positional) != 1:
		if command == "models host" {
			return flagMistake(diagnostics, command, modelsUsage, "name exactly one object, as <store>/<id>, or set --video-model or --image-model")
		}
		return flagMistake(diagnostics, command, modelsUsage, "name exactly one object, as <store>/<id>")
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, modelsUsage, "--timeout must not be negative")
	}
	if digestModelName.MatchString(*as) {
		return flagMistake(diagnostics, command, modelsUsage, "--as names the model plainly, never by a file digest")
	}
	// The program is run to prove the engine and named in the declaration;
	// a bare name would be looked up on PATH.
	if *program != "" && !filepath.IsAbs(*program) {
		return flagMistake(diagnostics, command, modelsUsage, "--program must be an absolute path")
	}
	if *torchProgram != "" && !filepath.IsAbs(*torchProgram) {
		return flagMistake(diagnostics, command, modelsUsage, "--pytorch-program must be an absolute path")
	}
	if *stateDir != "" && !filepath.IsAbs(*stateDir) {
		return flagMistake(diagnostics, command, modelsUsage, "--state must be an absolute path")
	}
	if *videoKind != "" && *videoKind != videoKindSdCpp && *videoKind != videoKindTorch {
		return flagMistake(diagnostics, command, modelsUsage, "--video-kind is sdcpp or torch")
	}
	if *card != "" {
		if !validHostCardNumber(*card) {
			return flagMistake(diagnostics, command, modelsUsage, "--card is a card number: 0, 1, ...")
		}
	}
	for _, path := range []struct{ flag, value string }{{"--video-engine", *videoEngine}, {"--torch-python", *torchPython}, {"--torch-source", *torchSource}} {
		if path.value != "" && !filepath.IsAbs(path.value) {
			return flagMistake(diagnostics, command, modelsUsage, path.flag+" must be an absolute path")
		}
	}
	if command == "models host" && goos == "linux" {
		return &exitError{exitRefusedCall, fmt.Errorf("%s: %s", command, linuxModelHostRegistrationLimit)}
	}
	// An engine named on the command line is proven against the model
	// host's pin, with its state folder and engine arguments, before any
	// runtime is asked (modelhost MODELHOST-L3, MODELHOST-L5).
	proven := ""
	if command == "models host" && *engine != "" && len(positional) == 1 {
		hostProgram := *program
		if hostProgram == "" {
			hostProgram = siblingProgram(modelHostProgram)
		}
		if hostProgram != "" {
			first := hostSettings{program: hostProgram, engine: *engine, state: *stateDir, extra: engineArgs}
			if err := proveHostEngine(command, first, diagnostics); err != nil {
				return err
			}
			proven = first.proof()
		}
	}

	w := newWaiting(options.budget)
	defer w.stop()
	machine, source, err := options.machine(diagnostics, command, modelsUsage)
	if err != nil {
		return err
	}
	if *getVideo {
		hostProgram := *program
		if hostProgram == "" {
			hostProgram = siblingProgram(modelHostProgram)
		}
		staged, err := getVideoSet(w, command, source, machine, *videoModel, hostProgram, *stateDir, *credential, output)
		if err != nil {
			return err
		}
		*videoEngine = staged
	}
	call, done := w.call()
	registry, err := machine.ResolveRegistryV2(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	call, done = w.call()
	typedRegistry, err := machine.ResolveRegistryV3(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	// run reads the declarations and writes the model host's one
	// declaration from them. A registry that changed in between answers
	// conflict before anything was withdrawn; run then reads again once.
	run := func() error {
		var legacyEditor modelHostRegistryEditor = registry
		var typedEditor providerRegistrationEditor = typedRegistry
		var plan *modelHostPlan
		if *planOnly {
			// The Panel declares what this command plans, through its own
			// registry client (A-SEC6 declarer ruling, FAC-R9).
			plan = &modelHostPlan{Steps: []modelHostPlanStep{}}
			legacyEditor, typedEditor = modelHostPlanLegacy{plan}, modelHostPlanTyped{typedRegistry, plan}
		}
		call, done = w.call()
		writer, snapshotErr := newModelHostRegistryWriter(call, legacyEditor, typedEditor)
		done()
		if snapshotErr != nil {
			return &exitError{exitNotResolved, snapshotErr}
		}
		call, done = w.call()
		page, err := registry.DeclarationsPinned(call)
		list := wire.DeclarationList{Outcome: page.Outcome, Revision: page.Revision}
		for _, entry := range page.Declarations {
			list.Declarations = append(list.Declarations, entry.State)
		}
		done()
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		if list.Outcome != wire.DeclarationListOutcomePage {
			return refusal(command, list.Outcome.String(), "")
		}
		settings, models := readHostDeclaration(list)
		for _, entry := range page.Declarations {
			var kept *declaredFields
			switch entry.State.Declaration.Name {
			case modelHostName:
				kept = &settings.declared
			case pytorchName:
				kept = &settings.torch.declared
			}
			if kept != nil {
				kept.extensions = slices.Clone(entry.SupportedExtensions)
				kept.remotePin = entry.RemoteServerKeySHA256
			}
		}

		if plan != nil {
			// An earlier runtime answers no standing; every declaration then
			// reads as third_party and must match the shipped defaults.
			call, done = w.call()
			standings, _ := registry.Standings(call)
			done()
			for _, entry := range page.Declarations {
				name := entry.State.Declaration.Name
				if (name == modelHostName || name == pytorchName) && plannedFromAnotherProgram(entry.State, client.ShippedDeclaration(standings.Standings, entry.State.Declaration)) {
					return refusal(command, "forbidden", name+" was declared by another program; Setup replaces it explicitly")
				}
			}
		}

		if command == "models hosts" {
			if options.asJSON {
				return writeJSON(output, models)
			}
			return printHosted(output, settings, models)
		}

		if command == "models unhost" {
			store, id, ok := strings.Cut(positional[0], "/")
			if !ok || store == "" || id == "" {
				return flagMistake(diagnostics, command, modelsUsage, "name the object as <store>/<id>, the way the inventory names it")
			}
			count := len(models)
			kept := slices.DeleteFunc(models, func(m hostedModel) bool { return m.Store == store && m.Object == id })
			if len(kept) == count {
				// The declaration binds the manifest id; a file name or
				// locator the person hosted the object by names the same
				// object through the inventory. An object the inventory
				// cannot resolve stays "not hosted" below.
				if _, object, _, err := lookupHostedObject(w, options, diagnostics, command, store, id); err == nil && object != "" {
					kept = slices.DeleteFunc(kept, func(m hostedModel) bool { return m.Store == store && m.Object == object })
				}
			}
			if len(kept) == count {
				return refusal(command, "unknown", store+"/"+id+" is not hosted")
			}
			// The last model with no sd-server video withdraws the model host;
			// the PyTorch provider's declaration stays as it is.
			if plan != nil {
				return plan.write(declareHost(w, writer, list, settings, kept, command, io.Discard, true, ""), output)
			}
			return declareHost(w, writer, list, settings, kept, command, output, options.asJSON,
				fmt.Sprintf("unhosted %s/%s", store, id))
		}

		// The declaration names a lease endpoint only when the person passes
		// one. An earlier one that names this runtime's own is recognized by
		// asking the runtime where it serves leases@1, and dropped.
		runtimeLease := ""
		if *resourceEndpoint == "" && (settings.resource != "" || settings.torch.resource != "") {
			call, done = w.call()
			//unchecked: a runtime that cannot answer leaves only the derived endpoints to compare against
			runtimeLease, _ = runtimeLeaseEndpoint(call, machine)
			done()
		}
		settings = settings.with(*program, *providerEndpointName, *engine, *resourceEndpoint, *idle, *embeddings, engineArgs, options.endpoint, runtimeLease)
		// The PyTorch provider's declaration takes the same lease endpoint
		// rule as the model host's.
		settings.torch.resource = leaseEndpointFor(settings.torch.resource, *resourceEndpoint, options.endpoint, runtimeLease)
		if *stateDir != "" {
			// The PyTorch provider keeps its own state under the folder named
			// for this runtime (withTorchDefaults).
			settings.state, settings.torch.state = *stateDir, filepath.Join(*stateDir, pytorchName)
		}
		if *card != "" {
			settings.card, settings.torch.card = *card, *card
			settings.invalidHold, settings.torch.invalidHold = "", ""
		}
		if *torchProgram != "" {
			settings.torch.program = *torchProgram
		}
		var said []string
		if mediaFlags || settings.legacyTorch {
			media, note, mistake := settings.media.with(*videoModel, *videoKind, *videoEngine, *torchPython, *torchSource, videoOptions, *imageModel)
			if mistake != "" {
				return flagMistake(diagnostics, command, modelsUsage, mistake)
			}
			settings.media = media
			if settings.legacyTorch && media.torchAny() {
				said = append(said, "the Diffusers sets move from the model host's declaration to the Diffusers provider's ("+pytorchProgram+")")
			}
			if note != "" {
				said = append(said, note)
			}
		}
		settings = settings.withTorchDefaults()
		// Each program is needed only for what it serves: a PyTorch set alone
		// is the PyTorch provider's, a chat model or an sd-server video the
		// model host's.
		if settings.media.torchAny() && settings.torch.program == "" {
			return &exitError{exitNotResolved, fmt.Errorf("%s: the Diffusers sets are the Diffusers provider's, and no %s is beside this command; pass --pytorch-program",
				command, pytorchProgram)}
		}
		if settings.program == "" && (len(positional) == 1 || len(models) > 0 || settings.media.hostVideo()) {
			return &exitError{exitNotResolved, fmt.Errorf("%s: no %s beside this command; pass --program", command, modelHostProgram)}
		}
		checkMedia := mediaFlags || settings.legacyTorch
		if checkMedia || *torchProgram != "" && settings.media.torchAny() {
			if err := proveConfiguredMedia(command, &settings, diagnostics); err != nil {
				return err
			}
			if settings.torch.proved != nil {
				defer settings.torch.proved.Close()
			}
		}
		if checkMedia {
			if settings.media.any() {
				said = append(said, settings.media.describe())
			}
		}

		if len(positional) == 1 {
			store, id, ok := strings.Cut(positional[0], "/")
			if !ok || store == "" || id == "" {
				return flagMistake(diagnostics, command, modelsUsage, "name the object as <store>/<id>, the way the inventory names it")
			}
			// The name callers ask for is the store's own name for the content, so
			// the router's family and the inventory's held_in are the same model
			// (abstraction-model CONTRACT.md MODEL-C2).
			found, object, size, err := lookupHostedObject(w, options, diagnostics, command, store, id)
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
			// The declaration binds the manifest id the inventory lists, so
			// the reconciler finds the object still held and the catalogue
			// projection joins the alias to it; an object named by a file
			// name or locator would read as gone and lose its alias.
			given := id
			id = object
			pin := ""
			for _, prior := range models {
				if prior.Store == store && (prior.Object == id || prior.Object == given) {
					pin = prior.Content
				}
			}
			if contentGiven {
				var err error
				call, done := w.call()
				pin, err = catalogueContent(call, *contentPin, size)
				done()
				if err != nil {
					return flagMistake(diagnostics, command, modelsUsage, err.Error())
				}
			}
			name := *as
			if name == "" {
				name = found
			}
			// A hosted name is what every client lists the model by; a file
			// digest is no name for a person (owner, 2026-11-01).
			if digestModelName.MatchString(name) {
				return flagMistake(diagnostics, command, modelsUsage, "the model would be listed by a file digest; name it with --as")
			}
			models = slices.DeleteFunc(models, func(m hostedModel) bool {
				return (m.Store == store && (m.Object == id || m.Object == given)) || m.Name == name
			})
			models = append(models, hostedModel{Name: name, Store: store, Object: id, Content: pin})
			sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
			said = append([]string{fmt.Sprintf("hosting %s/%s as %s (%d bytes) with %s", store, given, name, size, settings.engine)}, said...)
		}
		if len(models) > 0 {
			if settings.engine == "" {
				return flagMistake(diagnostics, command, modelsUsage, "--engine names the llama-server that modelhostd engine install staged from the pinned llama.cpp archive; nothing is downloaded and nothing is looked for")
			}
			if settings.proof() != proven {
				if err := proveHostEngine(command, settings, diagnostics); err != nil {
					return err
				}
			}
		}
		if plan != nil {
			return plan.write(declareHost(w, writer, list, settings, models, command, io.Discard, true, ""), output)
		}
		return declareHost(w, writer, list, settings, models, command, output, options.asJSON, strings.Join(said, "; "))
	}
	return retryHostConflict(run)
}

// A conflict before the first write reruns the existing read/prepare path once.
func retryHostConflict(run func() error) error {
	err := run()
	var conflict *hostConflict
	if errors.As(err, &conflict) {
		err = run()
	}
	if errors.As(err, &conflict) {
		return conflict.err
	}
	return err
}

// with applies this command's video and image flags over what the
// declaration already carried. A change of kind drops what only the other
// kind reads: sd-server's path and arguments for torch, and the PyTorch
// interpreter, source, options and image model for sdcpp, which the note
// names. An image set runs on the PyTorch engine with its venv alone, or
// beside a PyTorch video. The mistake is a configuration that cannot run.
func (m hostMedia) with(videoModel, videoKind, videoEngine, torchPython, torchSource string, options []string, imageModel string) (hostMedia, string, string) {
	note := ""
	// An image set alone reads as kind sdcpp, since no video names a kind;
	// naming sdcpp beside it is the switch away from the PyTorch engine.
	if videoKind != "" && (videoKind != m.kind() || videoKind == videoKindSdCpp && m.imageModel != "") {
		switch videoKind {
		case videoKindTorch:
			// sd-server's path and arguments stay unwritten: the serve line
			// names them only for the sdcpp kind (hostArguments).
			if len(m.engineArgs) > 0 {
				note = "sd-server arguments " + strings.Join(m.engineArgs, " ") + " dropped: only sd-server reads them"
			}
		case videoKindSdCpp:
			if m.imageModel != "" && imageModel == "" {
				note = "image model " + m.imageModel + " dropped: it runs on the Diffusers provider"
			}
			m.torchPython, m.torchSource, m.options, m.imageModel = "", "", nil, ""
		}
		m.videoKind = videoKind
	}
	if videoModel != "" {
		m.videoModel = videoModel
	}
	if videoEngine != "" {
		m.videoEngine = videoEngine
	}
	if torchPython != "" {
		m.torchPython = torchPython
	}
	if torchSource != "" {
		m.torchSource = torchSource
	}
	if len(options) > 0 {
		m.options = append([]string(nil), options...)
	}
	if imageModel != "" {
		m.imageModel = imageModel
	}
	if m.kind() == videoKindSdCpp {
		m.videoKind = videoKindSdCpp
	}
	switch {
	case m.imageModel != "" && m.videoModel != "" && m.kind() != videoKindTorch:
		return m, note, "--image-model runs on the Diffusers provider, which a video runs on only with --video-kind torch; sd-server serves no image"
	case m.imageModel != "" && m.torchPython == "":
		return m, note, "--image-model needs --torch-python, the reviewed venv's python"
	case m.torchVideo() && (m.torchPython == "" || m.torchSource == ""):
		return m, note, "--video-kind torch needs --torch-python and --torch-source"
	case m.kind() == videoKindSdCpp && m.videoModel != "" && m.videoEngine == "":
		return m, note, "--video-engine names the sd-server that modelhostd engine install --kind sdcpp staged"
	case !m.torchVideo() && len(m.options) > 0:
		return m, note, "--video-model-option belongs to --video-kind torch"
	}
	return m, note, ""
}

// torchVideo reports a video served on the OA PyTorch engine.
func (m hostMedia) torchVideo() bool { return m.videoModel != "" && m.kind() == videoKindTorch }

// describe says what the model host serves besides chat.
func (m hostMedia) describe() string {
	var parts []string
	diffusers, _ := product.EngineTitle(product.DiffusersModelsHost)
	if m.videoModel != "" {
		on := m.kind()
		if on == videoKindTorch {
			on = diffusers
		}
		parts = append(parts, "video "+m.videoModel+" on "+on)
	}
	if m.imageModel != "" {
		parts = append(parts, "image "+m.imageModel+" on "+diffusers)
	}
	return "serving " + strings.Join(parts, " and ")
}

// waitingSaid is what the command says when the runtime keeps the model
// host's change for the person's Keep: what is set up, and that it starts
// once the person allows the change. The last approved declaration keeps
// serving meanwhile.
func (m hostMedia) waitingSaid() string {
	var names []string
	if m.videoModel != "" {
		title := m.videoModel
		if set, ok := modelhost.VideoModelSetNamed(m.videoModel); ok {
			title, _, _ = strings.Cut(set.DisplayTitle(), " (")
		}
		names = append(names, title)
	}
	if m.imageModel != "" {
		names = append(names, m.imageModel)
	}
	if len(names) == 0 {
		return "The model host is updated. The change starts once you allow the model host change in the OpenAbstractions question."
	}
	return strings.Join(names, " and ") + " is set up. It starts once you allow the model host change in the OpenAbstractions question."
}

// proveMediaEngine asks the program the declaration will launch whether the
// engine it names is the one it would start: modelhostd for sd-server against
// its pin (modelhost MODELHOST-V1), pytorchd for the PyTorch venv and AMD's
// source for a configured PyTorch video, or the venv and dependencies for a
// configured PyTorch image (pytorch PYTORCH-T1).
func proveMediaEngine(command string, settings hostSettings, diagnostics io.Writer) error {
	m := settings.media
	var args []string
	program, state := settings.torch.program, settings.torch.state
	what := m.kind() + " video"
	switch {
	case m.torchVideo():
		what = "Diffusers video"
		args = []string{"engine", "check", "--set", m.videoModel, argPython, m.torchPython, argSource, m.torchSource}
	case m.videoModel != "" && m.videoEngine != "":
		program, state = settings.program, settings.state
		args = []string{"engine", "check", "--kind", videoKindSdCpp, "--engine", m.videoEngine}
	case m.imageModel != "":
		what = "Diffusers image"
		args = []string{"engine", "check", "--set", m.imageModel, argPython, m.torchPython}
	default:
		return nil
	}
	if state != "" {
		args = append(args, argState, state)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	releaseSettings, guardErr := selectedStateGuard(ctx, state)
	if guardErr != nil {
		return guardErr
	}
	defer releaseSettings()
	var stderr strings.Builder
	check := childproc.Hidden(exec.CommandContext(ctx, program, args...))
	check.Stdout, check.Stderr = io.Discard, &stderr
	if err := check.Run(); err != nil {
		// The model host's words can quote the engine's output; they are
		// shown only as redact.Line makes them.
		first, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		// The provider's words can quote the engine's output; they are shown
		// only as redact.Line makes them.
		first = strings.TrimSpace(first)
		for _, prefix := range []string{"modelhostd engine check:", "pytorchd engine check:"} {
			first = strings.TrimSpace(strings.TrimPrefix(first, prefix))
		}
		if first = redact.Line(first, 0); first == "" {
			first = err.Error()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return flagMistake(diagnostics, command, modelsUsage, fmt.Sprintf("the %s engine is refused engine:none: %s", what, first))
		}
		return &exitError{exitNotResolved, fmt.Errorf("%s: %s cannot check the %s engine: %s", command, program, what, first)}
	}
	return nil
}

// Retain the exact local Diffusers image through the check and Registry write.
// Other platforms retain their existing media check and registration path.
func proveConfiguredMedia(command string, settings *hostSettings, diagnostics io.Writer) error {
	if !settings.media.torchAny() || runtime.GOOS != "windows" {
		return proveMediaEngine(command, *settings, diagnostics)
	}
	ctx := context.Background()
	program, err := openRegistryProgramImage(ctx, fileid.Self, settings.torch.program)
	if err != nil {
		return fmt.Errorf("%s: resolve the configured Diffusers provider: %w", command, err)
	}
	defer func() {
		if settings.torch.proved != program {
			program.Close()
		}
	}()
	check := *settings
	check.torch.program = applicationV3HeldProgram(program)
	if err := proveMediaEngine(command, check, diagnostics); err != nil {
		return err
	}
	if check.media.imageModel != "" && (check.media.hostVideo() || check.media.torchVideo()) {
		check.media.videoModel = ""
		if err := proveMediaEngine(command, check, diagnostics); err != nil {
			return err
		}
	}
	if err := program.Recheck(ctx); err != nil {
		return fmt.Errorf("%s: the configured Diffusers provider changed during its engine check: %w", command, err)
	}
	settings.torch.proved = program
	settings.torch.program = check.torch.program
	return nil
}

// withTorchDefaults fills what the PyTorch provider's declaration takes from
// the runtime and the model host when nothing named it: pytorchd beside this
// command, its own endpoint name, the model host's lease endpoint and card,
// and a state folder of its own inside the model host's when that one is
// named. An isolated runtime names the model host's state folder; inside it
// the PyTorch provider's places, quarantine and engine files stay that
// runtime's, apart from the person's default folder and from the model
// host's own files.
func (s hostSettings) withTorchDefaults() hostSettings {
	t := &s.torch
	if t.program == "" {
		t.program = siblingProgram(pytorchProgram)
	}
	if t.endpoint == "" {
		t.endpoint = pytorchName
	}
	if t.resource == "" {
		t.resource = s.resource
	}
	if t.card == "" {
		t.card = s.card
	}
	if t.state == "" && s.state != "" {
		t.state = filepath.Join(s.state, pytorchName)
	}
	return s
}

// with applies this command's flags over what the declaration already carried.
// runtimeEndpoint is the endpoint this command reached the runtime at, and
// runtimeLease what that runtime named for its leases@1, or "".
func (s hostSettings) with(program, endpoint, engine, resource string, idle time.Duration, embed bool, extra []string, runtimeEndpoint, runtimeLease string) hostSettings {
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
	s.resource = leaseEndpointFor(s.resource, resource, runtimeEndpoint, runtimeLease)
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

// leaseEndpointFor is the lease endpoint a provider's declaration carries,
// given the one it carried and the one this command was given. The runtime
// that starts the provider names its own lease endpoint in the provider's
// environment (providerEnvironment), so the declaration carries one only when
// the person names one. An endpoint an earlier models host derived or took
// from the runtime is dropped: the derived one was not served by an isolated
// runtime (live walkthrough 2026-10-02, D8), and either would override the
// environment.
func leaseEndpointFor(declared, named, runtimeEndpoint, runtimeLease string) string {
	if named != "" {
		return named
	}
	if declared != "" && (declared == runtimeLease || declared == legacyLeaseEndpoint(runtimeEndpoint) || declared == legacyLeaseEndpoint("")) {
		return ""
	}
	return declared
}

// legacyLeaseEndpoint is the lease endpoint models host wrote into a
// declaration before the runtime named its own to the providers it starts:
// <runtime endpoint>-resource-table, or the installed one. It misses an
// isolated runtime's bootstrap.Endpoint(<name>-resource-table), which
// runtimeLeaseEndpoint's answer covers.
func legacyLeaseEndpoint(runtimeEndpoint string) string {
	if runtimeEndpoint != "" {
		return runtimeEndpoint + "-resource-table"
	}
	return listen.Endpoint("resource-table-v1")
}

// runtimeLeaseEndpoint asks the runtime this command talks to where it serves
// abstraction.resource/leases@1. models host never writes the answer into a
// declaration, where it would override the endpoint the runtime names in the
// provider's environment; it only recognizes a declared endpoint that names
// this runtime's own, which an earlier models host wrote, so it is dropped.
func runtimeLeaseEndpoint(ctx context.Context, machine *client.Machine) (string, error) {
	binding, err := machine.ResolveService(ctx, client.ResourceLeasesContract, client.Requirements{Scope: wire.ScopeLocal})
	if err != nil {
		return "", err
	}
	endpoint := binding.Reference().Endpoint
	if endpoint == "" {
		return "", errors.New("the runtime answered with no endpoint")
	}
	return endpoint, nil
}

// proof names what one engine check proved: the program, the engine, the
// state folder and the engine arguments.
func (s hostSettings) proof() string {
	return strings.Join(append([]string{s.program, s.engine, s.state}, s.extra...), "\x00")
}

// proveHostEngine asks the model host program the declaration will launch
// whether engine is its pinned llama.cpp release, with the same proof a call
// makes before its lease (modelhost MODELHOST-L3), reading the record in the
// declaration's state folder and checking the declaration's engine arguments
// against the reviewed list (MODELHOST-L5). The provider owns the pin and the
// list; this command carries no copy of either.
func proveHostEngine(command string, settings hostSettings, diagnostics io.Writer) error {
	program, engine := settings.program, settings.engine
	if !filepath.IsAbs(program) {
		return flagMistake(diagnostics, command, modelsUsage, "--program must be an absolute path")
	}
	if !filepath.IsAbs(engine) {
		return flagMistake(diagnostics, command, modelsUsage, "--engine must be an absolute path")
	}
	if _, err := os.Stat(engine); err != nil {
		return flagMistake(diagnostics, command, modelsUsage, fmt.Sprintf("no engine at %s", engine))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	releaseSettings, guardErr := selectedStateGuard(ctx, settings.state)
	if guardErr != nil {
		return guardErr
	}
	defer releaseSettings()
	var stderr strings.Builder
	args := []string{"engine", "check", "--engine", engine}
	if settings.state != "" {
		args = append(args, argState, settings.state)
	}
	for _, extra := range settings.extra {
		args = append(args, argEngineArg, extra)
	}
	check := childproc.Hidden(exec.CommandContext(ctx, program, args...))
	check.Stdout, check.Stderr = io.Discard, &stderr
	err := check.Run()
	if err == nil {
		return nil
	}
	reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stderr.String()), "modelhostd engine check:"))
	var exit *exec.ExitError
	// The model host's own words can quote the engine's output; they are
	// shown only as redact.Line makes them.
	if errors.As(err, &exit) && exit.ExitCode() == 1 && reason != "" {
		return flagMistake(diagnostics, command, modelsUsage,
			fmt.Sprintf("--engine %s is refused engine:none: %s", engine, redact.Line(reason, 0)))
	}
	// engine check exits 2 naming an engine argument outside the reviewed
	// list; that is this command's flag mistake.
	if errors.As(err, &exit) && exit.ExitCode() == 2 && strings.Contains(reason, "engine argument outside the reviewed list") {
		first, _, _ := strings.Cut(reason, "\n")
		return flagMistake(diagnostics, command, modelsUsage, "--engine-arg is refused: "+redact.Line(first, 0))
	}
	// Any other outcome is a model host that cannot answer engine check,
	// usually one older than the pin; its first line says why.
	first, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
	if first = redact.Line(first, 0); first == "" {
		first = err.Error()
	}
	return &exitError{exitNotResolved, fmt.Errorf("%s: %s has no `engine check`; install the current model host provider (%s)",
		command, program, first)}
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

// readHostDeclaration reads the model host's and the PyTorch provider's
// declarations back: what they were given and what they host, the video and
// image engine and the card among it, and the fields this command keeps as
// they were. The PyTorch provider's declaration, when there is one, is the
// record of the PyTorch sets; a model host serve line that still carries them
// is read as legacyTorch. A registry with neither reads empty.
func readHostDeclaration(list wire.DeclarationList) (hostSettings, []hostedModel) {
	var settings hostSettings
	var models []hostedModel
	if d := existingDeclaration(list, modelHostName); d != nil {
		settings, models = readServeArguments(d.Arguments)
		settings.program, settings.endpoint = d.Program, d.Endpoint
		var card string
		settings.declared, card = readDeclared(d, hostContracts)
		// The serve line's --resource card:N is the card; the resource is
		// read only for a line that names none.
		if settings.card == "" {
			settings.card = card
		}
	}
	d := existingDeclaration(list, pytorchName)
	if d == nil {
		return settings, models
	}
	torch, media := readPytorchArguments(d.Arguments)
	torch.program, torch.endpoint = d.Program, d.Endpoint
	var card string
	torch.declared, card = readDeclared(d, pytorchContracts)
	if torch.card == "" {
		torch.card = card
	}
	// The PyTorch provider's own declaration names the PyTorch sets; what a
	// model host line still carried of them is dropped.
	settings.torch = torch
	if settings.media.torchVideo() {
		settings.media.videoModel, settings.media.videoKind = "", ""
	}
	if media.videoModel != "" {
		settings.media.videoModel, settings.media.videoKind = media.videoModel, videoKindTorch
	}
	settings.media.torchPython, settings.media.torchSource = media.torchPython, media.torchSource
	settings.media.options, settings.media.imageModel = media.options, media.imageModel
	return settings, models
}

// readDeclared is what a declaration said that this command does not decide,
// and the card among its resources: its activation, transport and
// guarantees, resources and scoped models of retained profiles, and contracts
// other than the ones this command declares.
func readDeclared(d *wire.Declaration, declares []string) (declaredFields, string) {
	declared := declaredFields{present: true, activation: d.Activation, transport: d.Transport, guarantees: slices.Clone(d.Guarantees)}
	for _, contract := range d.Contracts {
		if !slices.Contains(declares, contract) {
			declared.contracts = append(declared.contracts, contract)
		}
	}
	profiles := declarationOf(*d).mediatedInferenceProfiles()
	_, byProfile := router.DeclaredModels(d.Models, profiles)
	for _, profile := range profiles {
		if !slices.Contains(declares, profileContracts[profile]) {
			for _, model := range byProfile[profile] {
				declared.models = append(declared.models, profile+":"+model)
			}
		}
	}
	card := ""
	for _, resource := range d.Resources {
		number, isCard := strings.CutPrefix(resource, "card:")
		switch {
		case isCard:
			if card == "" {
				card = number
			}
		case strings.HasPrefix(resource, "profile:") && slices.Contains(declares, profileContracts[strings.TrimPrefix(resource, "profile:")]):
		default:
			declared.resources = append(declared.resources, resource)
		}
	}
	return declared, card
}

// serveFlags are the modelhostd serve flags this command writes itself, each
// taking one value; a repeatable one gathers every value. The PyTorch
// engine's flags are read from a line written before the PyTorch provider,
// to move them to its declaration.
var serveFlags = map[string]bool{
	argEndpoint: false, argRuntimeProgram: true, argEngine: false, argResource: false, argHold: false,
	argIdle: false, argState: false, argEngineArg: true, argModel: true, argModelContent: true, argVideoModel: false, argConfig: true,
	argVideoKind: false, argVideoEngine: false, argTorchPython: false, argTorchSource: false,
	argVideoOption: true, argVideoEngineArg: true, argImageModel: false, argImageTimeLimit: false,
}

// pytorchServeFlags are the pytorchd serve flags this command writes itself.
var pytorchServeFlags = map[string]bool{
	argEndpoint: false, argRuntimeProgram: true, argResource: false, argHold: false, argState: false,
	argPython: false, argSource: false, argVideoModel: false, argVideoOption: true, argImageModel: false,
}

// walkServeLine is the one reader of a provider's serve line, the arguments
// this command wrote and a hand-made declaration's alike. It accepts every
// form Go's flag package does (-x v, --x v, -x=v, --x=v, and the boolean flag
// alone or with =value) and stops at "--" or the first positional argument.
// Each
// flag among known is given to take with its value, in order, so the last
// one wins as in the provider; every other argument, and everything from
// "--" on, passes through as written.
func walkServeLine(args []string, known map[string]bool, boolean string, take func(name, value string, inline bool)) []string {
	var passthrough []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return append(passthrough, args[i:]...)
		}
		name, value, inline, isFlag := serveFlag(args[i])
		if !isFlag {
			if i == 0 && args[i] == "serve" {
				continue
			}
			return append(passthrough, args[i:]...)
		}
		if boolean != "" && name == boolean {
			take(name, value, inline)
			continue
		}
		if _, ok := known[name]; !ok {
			passthrough = append(passthrough, args[i])
			if _, _, _, next := serveFlagAt(args, i+1); !inline && i+1 < len(args) && !next && args[i+1] != "--" {
				passthrough = append(passthrough, args[i+1])
				i++
			}
			continue
		}
		if !inline {
			// A value may itself start with "-" (an engine flag), as
			// Go's flag package takes it.
			if i+1 < len(args) {
				value = args[i+1]
				i++
			}
		}
		take(name, value, true)
	}
	return passthrough
}

// readServeArguments reads a model host's serve line. A flag this command
// writes is taken, so a duplicate cannot survive beside the value this
// command writes; the PyTorch engine's flags a line written before the
// PyTorch provider carries are read as legacyTorch.
func readServeArguments(args []string) (hostSettings, []hostedModel) {
	var settings hostSettings
	var models []hostedModel
	var contents []string
	settings.passthrough = walkServeLine(args, serveFlags, argEmbed, func(name, value string, inline bool) {
		switch name {
		case argEmbed:
			on, err := strconv.ParseBool(value)
			settings.embed = !inline || err == nil && on
		case argEndpoint, argRuntimeProgram:
			// Rewritten from this runtime at every declaration.
		case argEngine:
			settings.engine = value
		case argResource:
			settings.resource = value
		case argHold:
			settings.card, settings.invalidHold = readHostHold(value)
		case argIdle:
			settings.idle = value
		case argState:
			settings.state = value
		case argEngineArg:
			settings.extra = append(settings.extra, value)
		case argConfig:
			settings.configs = append(settings.configs, value)
		case argModel:
			if m, ok := parseHostedModel(value); ok {
				models = append(models, m)
			}
		case argModelContent:
			contents = append(contents, value)
		case argVideoModel:
			settings.media.videoModel = value
		case argVideoKind:
			settings.media.videoKind = value
		case argVideoEngine:
			settings.media.videoEngine = value
		case argTorchPython:
			settings.media.torchPython = value
		case argTorchSource:
			settings.media.torchSource = value
		case argVideoOption:
			settings.media.options = append(settings.media.options, value)
		case argVideoEngineArg:
			settings.media.engineArgs = append(settings.media.engineArgs, value)
		case argImageModel:
			settings.media.imageModel = value
		case argImageTimeLimit:
			settings.torch.passthrough = append(settings.torch.passthrough, argImageTimeLimit, value)
		}
	})
	seenContent := map[string]bool{}
	for _, value := range contents {
		object, pin, ok := strings.Cut(value, "=")
		bound := false
		if ok && pin != "" && !seenContent[object] {
			for i := range models {
				if models[i].Store+"/"+models[i].Object == object {
					models[i].Content = pin
					bound = true
				}
			}
		}
		if !bound {
			// Preserve invalid/repeated inputs so modelhostd refuses them;
			// rewriting the declaration must never silently remove a pin.
			settings.passthrough = append(settings.passthrough, argModelContent, value)
		}
		seenContent[object] = true
	}
	m := settings.media
	settings.legacyTorch = m.torchPython != "" || m.torchSource != "" || len(m.options) > 0 || m.imageModel != "" ||
		m.videoKind == videoKindTorch || len(settings.torch.passthrough) > 0
	return settings, models
}

// readPytorchArguments reads the PyTorch provider's serve line: its
// settings, and its sets with the venv and source they run on.
func readPytorchArguments(args []string) (torchSettings, hostMedia) {
	var settings torchSettings
	var media hostMedia
	settings.passthrough = walkServeLine(args, pytorchServeFlags, "", func(name, value string, _ bool) {
		switch name {
		case argEndpoint, argRuntimeProgram:
			// Rewritten from this runtime at every declaration.
		case argResource:
			settings.resource = value
		case argHold:
			settings.card, settings.invalidHold = readHostHold(value)
		case argState:
			settings.state = value
		case argPython:
			media.torchPython = value
		case argSource:
			media.torchSource = value
		case argVideoModel:
			media.videoModel, media.videoKind = value, videoKindTorch
		case argVideoOption:
			media.options = append(media.options, value)
		case argImageModel:
			media.imageModel = value
		}
	})
	return settings, media
}

// serveFlag reads one argument as a flag: its name in this command's --name
// spelling, its inline value after "=", and whether it is a flag at all.
func serveFlag(arg string) (name, value string, inline, isFlag bool) {
	if len(arg) < 2 || arg[0] != '-' || arg == "--" {
		return "", "", false, false
	}
	bare := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
	if bare == "" || bare[0] == '-' || bare[0] == '=' {
		return "", "", false, false
	}
	name, value, inline = strings.Cut(bare, "=")
	return "--" + name, value, inline, true
}

// serveFlagAt is serveFlag of args[i], not a flag past the end.
func serveFlagAt(args []string, i int) (string, string, bool, bool) {
	if i >= len(args) {
		return "", "", false, false
	}
	return serveFlag(args[i])
}

// existingHostDeclaration is the model host's declaration, nil when there is
// none.
func existingHostDeclaration(list wire.DeclarationList) *wire.Declaration {
	return existingDeclaration(list, modelHostName)
}

// existingDeclaration is the declaration called name, nil when there is none.
func existingDeclaration(list wire.DeclarationList, name string) *wire.Declaration {
	for _, state := range list.Declarations {
		if state.Declaration.Name == name {
			d := state.Declaration
			return &d
		}
	}
	return nil
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

// hostArguments is the model host's declared serve command line for these
// settings and models; readHostDeclaration reads it back. It names no
// PyTorch engine option: those are the PyTorch provider's (pytorchArguments).
func hostArguments(settings hostSettings, models []hostedModel, runtimePrograms []string) []string {
	arguments := []string{"serve", argEndpoint, providerEndpointToken}
	if settings.engine != "" {
		arguments = append(arguments, argEngine, settings.engine)
	}
	// A lease endpoint only when the person named one; otherwise the runtime
	// that starts the model host names its own (ABSTRACTION_RESOURCE_ENDPOINT).
	if settings.resource != "" {
		arguments = append(arguments, argResource, settings.resource)
	}
	// The card the engines hold, the same value as the declaration's card:N
	// resource (modelhostd serve --resource).
	arguments = append(arguments, argHold, ResourceCard(settings.cardNumber()))
	for _, program := range runtimePrograms {
		arguments = append(arguments, argRuntimeProgram, program)
	}
	if settings.idle != "" {
		arguments = append(arguments, argIdle, settings.idle)
	}
	if settings.embed {
		arguments = append(arguments, argEmbed)
	}
	if settings.state != "" {
		arguments = append(arguments, argState, settings.state)
	}
	for _, extra := range settings.extra {
		arguments = append(arguments, argEngineArg, extra)
	}
	for _, config := range settings.configs {
		arguments = append(arguments, argConfig, config)
	}
	for _, m := range models {
		arguments = append(arguments, argModel, m.argument())
		if m.Content != "" {
			arguments = append(arguments, argModelContent, m.Store+"/"+m.Object+"="+m.Content)
		}
	}
	if media := settings.media; media.hostVideo() {
		arguments = append(arguments, argVideoKind, videoKindSdCpp, argVideoModel, media.videoModel)
		if media.videoEngine != "" {
			arguments = append(arguments, argVideoEngine, media.videoEngine)
			for _, arg := range media.engineArgs {
				arguments = append(arguments, argVideoEngineArg, arg)
			}
		}
	}
	return append(arguments, settings.passthrough...)
}

// pytorchArguments is the PyTorch provider's declared serve command line:
// pytorchd serve with the venv, AMD's source for a video, the sets and the
// card (pytorch PYTORCH-D1).
func pytorchArguments(settings hostSettings, runtimePrograms []string) []string {
	t, media := settings.torch, settings.media
	arguments := []string{"serve", argEndpoint, providerEndpointToken}
	// A lease endpoint only when the person named one; otherwise the runtime
	// that starts the provider names its own (ABSTRACTION_RESOURCE_ENDPOINT).
	if t.resource != "" {
		arguments = append(arguments, argResource, t.resource)
	}
	arguments = append(arguments, argHold, ResourceCard(t.cardNumber()))
	for _, program := range runtimePrograms {
		arguments = append(arguments, argRuntimeProgram, program)
	}
	if t.state != "" {
		arguments = append(arguments, argState, t.state)
	}
	arguments = append(arguments, argPython, media.torchPython)
	if media.torchVideo() {
		arguments = append(arguments, argSource, media.torchSource, argVideoModel, media.videoModel)
		for _, option := range media.options {
			arguments = append(arguments, argVideoOption, option)
		}
	}
	if media.imageModel != "" {
		arguments = append(arguments, argImageModel, media.imageModel)
	}
	return append(arguments, t.passthrough...)
}

// modelHostDeclaration is the one model host declaration the settings and models
// describe: chat and embed for the hosted objects, video for an sd-server
// video, each profile with its own models, and the card its engines load
// into.
func modelHostDeclaration(settings hostSettings, models []hostedModel, runtimePrograms []string) wire.Declaration {
	var contracts, profiles, names []string
	media := settings.media
	if len(models) > 0 {
		contracts, profiles = append(contracts, inference.Contract), append(profiles, router.ProfileChat)
		// The model host evaluates an application's chat prefix ahead of its
		// request (modelhost MODELHOST-W1; inference INF-PL2). preload@1 is no
		// profile: its host is the chat host.
		contracts = append(contracts, inference.PreloadContract)
		if settings.embed {
			contracts, profiles = append(contracts, modelHostEmbed), append(profiles, router.ProfileEmbed)
		}
	}
	for _, m := range models {
		// Beside video, a chat model names its profiles: a bare name would
		// also be served for video (provider add --model, ROUTE-D9).
		if !media.hostVideo() && len(settings.declared.contracts) == 0 {
			names = append(names, m.Name)
			continue
		}
		names = append(names, router.ProfileChat+":"+m.Name)
		if settings.embed {
			names = append(names, router.ProfileEmbed+":"+m.Name)
		}
	}
	if media.hostVideo() {
		contracts, profiles = append(contracts, inference.VideoContract), append(profiles, router.ProfileVideo)
		names = append(names, router.ProfileVideo+":"+media.videoModel)
	}
	return nativeCardDeclaration(modelHostName, settings.program, settings.endpoint, hostArguments(settings, models, runtimePrograms),
		settings.cardNumber(), contracts, profiles, names, settings.declared)
}

// pytorchDeclaration is the PyTorch provider's declaration: video@1 for a
// PyTorch video and image@1 for an image set, each model scoped to its
// profile, and the card its engine loads into (pytorch PYTORCH-D1).
func pytorchDeclaration(settings hostSettings, runtimePrograms []string) wire.Declaration {
	var contracts, profiles, names []string
	media := settings.media
	if media.torchVideo() {
		contracts, profiles = append(contracts, inference.VideoContract), append(profiles, router.ProfileVideo)
		names = append(names, router.ProfileVideo+":"+media.videoModel)
	}
	if media.imageModel != "" {
		contracts, profiles = append(contracts, inference.ImageContract), append(profiles, router.ProfileImage)
		names = append(names, router.ProfileImage+":"+media.imageModel)
	}
	t := settings.torch
	return nativeCardDeclaration(pytorchName, t.program, t.endpoint, pytorchArguments(settings, runtimePrograms),
		t.cardNumber(), contracts, profiles, names, t.declared)
}

// nativeCardDeclaration is a native provider's declaration that names the card its
// engines load weights into beside its profiles, so Declare writes the
// program its card rules through the one card-rules writer (facade
// CONTRACT.md FAC-R3): the provider holds what it loads. What a declaration
// already said that this command does not decide is kept: an attach
// declaration stays attach, a store:* resource stays.
func nativeCardDeclaration(name, program, endpoint string, arguments []string, card string, contracts, profiles, models []string,
	declared declaredFields) wire.Declaration {
	resources := []string{ResourceCard(card)}
	for _, profile := range profiles {
		resources = append(resources, "profile:"+profile)
	}
	activation, transport, guarantees := wire.ActivationOnDemand, wire.DeclarationTransportNative, []string{inference.GuaranteeLocalOnly}
	if declared.present {
		activation, transport, guarantees = declared.activation, declared.transport, slices.Clone(declared.guarantees)
		resources = append(resources, declared.resources...)
		contracts = append(contracts, declared.contracts...)
		models = append(models, declared.models...)
	}
	return wire.Declaration{Name: name, Program: identity.NormalizeSubjectProgram(filepath.Clean(program)),
		Arguments: arguments, Endpoint: endpoint, Transport: transport,
		Contracts: contracts, Guarantees: guarantees, Resources: resources, Models: models,
		Activation: activation, Role: wire.DeclarationRoleProvider}
}

// cardNumber is the card the engines load into, 0 unless one was named.
func (s hostSettings) cardNumber() string {
	if s.card == "" {
		return "0"
	}
	return s.card
}

// cardNumber is the card the PyTorch engine loads into, 0 unless one was
// named.
func (t torchSettings) cardNumber() string {
	if t.card == "" {
		return "0"
	}
	return t.card
}

// sameHostDeclaration reports whether a declaration already says everything
// the new one would, so declaring it again would change nothing.
func sameHostDeclaration(existing *wire.Declaration, next wire.Declaration) bool {
	if existing == nil {
		return false
	}
	return existing.Program == next.Program && existing.Endpoint == next.Endpoint && existing.Transport == next.Transport &&
		existing.Activation == next.Activation && slices.Equal(existing.Arguments, next.Arguments) &&
		slices.Equal(existing.Contracts, next.Contracts) && slices.Equal(existing.Guarantees, next.Guarantees) &&
		slices.Equal(existing.Resources, next.Resources) && slices.Equal(existing.Models, next.Models)
}

// declareHost writes the declarations the settings and the models describe:
// the model host's for the hosted objects and an sd-server video, and the
// PyTorch provider's for the PyTorch sets, each in place of its existing one.
// A declaration with nothing left to serve is withdrawn, and one that would
// change nothing is not written again. The PyTorch provider's is written
// first, so a model host declaration that still carried the PyTorch sets
// loses them only once they are declared there.
func declareHost(w *waiting, registry modelHostRegistryEditor, list wire.DeclarationList, settings hostSettings, models []hostedModel,
	command string, output io.Writer, asJSON bool, said string) error {
	for _, hold := range []string{settings.invalidHold, settings.torch.invalidHold} {
		if hold != "" {
			return refusal(command, "invalid", "the declared --resource "+redact.Line(hold, 0)+" is unsupported; name card:0, card:1, ... or explicitly select --card")
		}
	}
	programs, err := modelHostRuntimePrograms()
	if err != nil {
		return fmt.Errorf("%s: runtime program: %w", command, err)
	}
	settings = settings.withTorchDefaults()
	settings.configs = hostStoreConfigs(list, settings.configs, models)
	steps := []declarationStep{
		{name: pytorchName, who: "the Diffusers provider", existing: existingDeclaration(list, pytorchName), extensions: settings.torch.declared.extensions, remotePin: settings.torch.declared.remotePin},
		{name: modelHostName, who: "the model host", existing: existingDeclaration(list, modelHostName), extensions: settings.declared.extensions, remotePin: settings.declared.remotePin},
	}
	if settings.media.torchAny() || len(settings.torch.declared.contracts) > 0 {
		if settings.torch.program == "" {
			return &exitError{exitNotResolved, fmt.Errorf("%s: the Diffusers sets are the Diffusers provider's, and no %s is beside this command; pass --pytorch-program",
				command, pytorchProgram)}
		}
		d := pytorchDeclaration(settings, programs)
		steps[0].next = &d
	}
	if len(models) > 0 || settings.media.hostVideo() || len(settings.declared.contracts) > 0 {
		d := modelHostDeclaration(settings, models, programs)
		steps[1].next = &d
	}
	// Validate the complete plan before generating its typed declaration or
	// making its first edit. Scoped retention must keep the existing field refusal.
	for _, step := range steps {
		if step.next != nil {
			if field := validProviderDeclaration(declarationOf(*step.next)); field != "" {
				return refusal(command, "invalid", step.name+":"+field)
			}
		}
	}
	if steps[0].next != nil {
		step := &steps[0]
		if command == "models unhost" {
			step.next = step.existing
		} else if writer, ok := registry.(*modelHostRegistryWriter); ok &&
			(step.existing == nil || samePrograms(step.existing.Program, step.next.Program)) {
			spec, buildErr := writer.pyTorchSpec(*step.next, settings.torch)
			if buildErr != nil {
				return buildErr
			}
			step.spec = spec
			step.recheckImage = settings.torch.proved != nil && spec != nil && managedPyTorchRecipes(*spec)
		}
	}
	if steps[1].next != nil {
		file := providerFile{Version: providerFileVersion, Declaration: declarationOf(*steps[1].next)}
		file.Declaration.SupportedExtensions = extensionsOf(settings.declared.extensions)
		_, spec, buildErr := legacyRegistrationSpec(file)
		if buildErr != nil {
			return buildErr
		}
		configured := make([]hostdeclaration.Model, 0, len(models))
		for _, model := range models {
			configured = append(configured, hostdeclaration.Model{Name: model.Name, Store: model.Store, Object: model.Object})
		}
		built, buildErr := hostdeclaration.Build(spec.Program, spec.Providers[0], settings.engine, configured)
		if buildErr != nil {
			return buildErr
		}
		steps[1].spec = &built
	}
	if steps[0].next == nil && steps[1].next == nil && steps[0].existing == nil && steps[1].existing == nil {
		return refusal(command, "invalid", "the model host would serve nothing: host an object, or set --video-model or --image-model")
	}
	revision := list.Revision
	var last *wire.DeclarationChange
	notes := []string{said}
	waiting := false
	for _, step := range steps {
		change, note, err := step.apply(w, registry, revision, command, last == nil)
		if err != nil && last != nil {
			// The first declaration is written and this one is not: a run
			// again writes what is still missing.
			return fmt.Errorf("%w; %s's declaration is written and %s's is not; run `openabstractions models host` again",
				err, steps[0].who, step.who)
		}
		if err != nil {
			return err
		}
		if change == nil {
			continue
		}
		last, revision = change, change.Revision
		notes = append(notes, note)
		waiting = waiting || change.Reason == approvalPendingReason
	}
	if waiting && !asJSON {
		// The change waits for the person's Keep: every note but "serving",
		// then what starts once the person allows it.
		var kept []string
		for _, note := range notes {
			for _, part := range strings.Split(note, "; ") {
				if part != "" && part != settings.media.describe() {
					kept = append(kept, part)
				}
			}
		}
		_, err := fmt.Fprintf(output, "%s; revision %s\n%s\n", strings.Join(kept, "; "), revision, settings.media.waitingSaid())
		return err
	}
	if last == nil {
		if asJSON {
			return writeJSON(output, map[string]string{"outcome": "unchanged", "revision": revision})
		}
		_, err := fmt.Fprintf(output, "%s; what is declared already says this; revision %s\n", said, revision)
		return err
	}
	return credentialReport(output, asJSON, *last, strings.Join(slices.DeleteFunc(notes, func(n string) bool { return n == "" }), "; ")+
		"; revision "+last.Revision)
}

// inventorySourceContract is the contract a declared storage inventory
// source serves; its store:<name> resources name the stores it reads.
const inventorySourceContract = "abstraction.storage/inventory-source@1"

// hostStoreConfigs is the store configurations the model host's serve line
// carries. modelhostd finds a hosted object with its own reading of this
// machine's stores (the model bridge's local stores, MODELBRIDGE-L1), which
// reads only the stores its configuration names beyond the ones every
// program's own files name. A store an inventory source reads only through
// its own --config, such as Setup's model folder (an extra root of
// Setup's inventoryd), is unknown to it otherwise, and the object `models
// host` found through the runtime's inventory is refused when a call loads
// it (owner's preview, 2026-11-01: native:object:unknown). So each hosted
// model's store contributes the --config of the declared inventory source
// that reads it. A configuration no declared inventory source carries was
// written by hand and is kept; an inventory source's configuration whose
// store no longer holds a hosted model is dropped.
func hostStoreConfigs(list wire.DeclarationList, existing []string, models []hostedModel) []string {
	stores := map[string]bool{}
	for _, m := range models {
		stores[m.Store] = true
	}
	sourced := map[string]bool{}
	var configs []string
	add := func(path string) {
		if path != "" && !slices.Contains(configs, path) {
			configs = append(configs, path)
		}
	}
	var wanted []string
	for _, state := range list.Declarations {
		d := state.Declaration
		if !slices.Contains(d.Contracts, inventorySourceContract) {
			continue
		}
		path := serveConfig(d.Arguments)
		if path == "" {
			continue
		}
		sourced[path] = true
		for _, resource := range d.Resources {
			if store, ok := strings.CutPrefix(resource, "store:"); ok && stores[store] {
				wanted = append(wanted, path)
			}
		}
	}
	for _, path := range existing {
		if !sourced[path] {
			add(path)
		}
	}
	for _, path := range wanted {
		add(path)
	}
	return configs
}

// serveConfig is the last --config a serve line names, as Go's flag
// package reads it; empty when it names none.
func serveConfig(args []string) string {
	config := ""
	walkServeLine(args, map[string]bool{argConfig: false}, "", func(name, value string, _ bool) {
		if name == argConfig {
			config = value
		}
	})
	return config
}

func validHostCardNumber(number string) bool {
	n, err := strconv.Atoi(number)
	return err == nil && n >= 0 && strconv.Itoa(n) == number
}

func readHostHold(value string) (string, string) {
	if card, ok := strings.CutPrefix(value, "card:"); ok && validHostCardNumber(card) {
		return card, ""
	}
	return "", fmt.Sprintf("%q", value)
}

// declarationStep is one provider's declaration as it is and as this command
// would write it; next is nil when the provider would serve nothing.
type declarationStep struct {
	name, who      string
	existing, next *wire.Declaration
	extensions     []wire.DeclarationExtension
	remotePin      string
	spec           *registration.RegistrationSpec
	recheckImage   bool
}

// modelHostRegistryEditor is the existing registry edit surface used by one
// declaration step; the resolved client remains its production implementation.
type modelHostRegistryEditor interface {
	DeclarePinned(context.Context, string, wire.PinnedDeclaration) (wire.DeclarationChange, error)
	ReplacePinned(context.Context, string, wire.PinnedDeclaration) (wire.DeclarationChange, error)
	Withdraw(context.Context, string, string) (wire.DeclarationChange, error)
}

// modelHostRegistryWriter captures canonical revisions before reading the
// compatibility declarations. Every typed update retains that conditional fence.
type modelHostRegistryWriter struct {
	modelHostRegistryEditor
	typed   providerRegistrationEditor
	current []registration.Registration
}

func newModelHostRegistryWriter(ctx context.Context, legacy modelHostRegistryEditor, typed providerRegistrationEditor) (*modelHostRegistryWriter, error) {
	w := &modelHostRegistryWriter{modelHostRegistryEditor: legacy, typed: typed}
	cursor := ""
	for {
		page, err := typed.Registrations(ctx, cursor, 64)
		if err != nil {
			return nil, err
		}
		if page.Outcome != registration.OutcomePage {
			return nil, fmt.Errorf("registrations: %s: %s", page.Outcome.String(), page.Reason)
		}
		w.current = append(w.current, page.Registrations...)
		if page.Cursor == "" {
			return w, nil
		}
		if page.Cursor == cursor {
			return nil, errors.New("registry continuation did not advance")
		}
		cursor = page.Cursor
	}
}

func (w *modelHostRegistryWriter) pyTorchSpec(next wire.Declaration, settings torchSettings) (*registration.RegistrationSpec, error) {
	var prior *registration.ProviderFacet
	ref := registration.FacetReference{Kind: registration.FacetKindProvider, Name: pytorchName}
	for _, row := range w.current {
		if samePrograms(row.Spec.Program, next.Program) {
			if facet, ok := registrationFacetValue(row.Spec, ref).(registration.ProviderFacet); ok {
				prior = &facet
				break
			}
		}
	}
	if settings.proved == nil && (prior == nil || len(prior.Recipes) == 0 && len(prior.Engines) == 0 && len(prior.ConfigKeys) == 0) {
		return nil, nil
	}
	if settings.proved != nil {
		if err := settings.proved.Recheck(context.Background()); err != nil {
			return nil, fmt.Errorf("the configured Diffusers provider changed before registration: %w", err)
		}
	}
	file := providerFile{Version: providerFileVersion, Declaration: declarationOf(next)}
	file.Declaration.SupportedExtensions = extensionsOf(settings.declared.extensions)
	_, spec, err := legacyRegistrationSpec(file)
	if err != nil {
		return nil, err
	}
	facet := &spec.Providers[0]
	if prior != nil {
		facet.ConfigKeys = slices.Clone(prior.ConfigKeys)
		facet.Engines = slices.Clone(prior.Engines)
		for _, recipe := range prior.Recipes {
			scope := slices.DeleteFunc(slices.Clone(recipe.Modalities), func(modality string) bool {
				return !slices.Contains(facet.Models, registration.ScopedModel{Model: recipe.Model, Modality: modality})
			})
			if len(scope) != 0 {
				recipe.Modalities = scope
				facet.Recipes = append(facet.Recipes, recipe)
			}
		}
	}
	if settings.proved != nil {
		attachPyTorchRecipes(facet)
	}
	return &spec, nil
}

func (w *modelHostRegistryWriter) WriteConfigured(ctx context.Context, spec registration.RegistrationSpec, recheckImage bool) (wire.DeclarationChange, error) {
	var matched *registration.Registration
	for i := range w.current {
		if samePrograms(w.current[i].Spec.Program, spec.Program) {
			if matched != nil {
				return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "registration:ambiguous:program"}, nil
			}
			matched = &w.current[i]
		}
	}
	var change registration.Change
	var err error
	if matched == nil {
		change, err = w.typed.Register(ctx, spec)
	} else {
		// The generated clone preserves every unrelated typed facet and all
		// program-level fields while replacing this explicitly configured facet.
		before, encodeErr := registrationEncoded(registration.EncodeRegistrationSpec, &matched.Spec)
		if encodeErr != nil {
			return wire.DeclarationChange{}, encodeErr
		}
		copy, decodeErr := registration.DecodeRegistrationSpec(before)
		if decodeErr != nil {
			return wire.DeclarationChange{}, decodeErr
		}
		copy.Providers = mergeProviderFacets(copy.Providers, spec.Providers, func(v registration.ProviderFacet) string { return v.Name })
		after, encodeErr := registrationEncoded(registration.EncodeRegistrationSpec, copy)
		if encodeErr != nil {
			return wire.DeclarationChange{}, encodeErr
		}
		// Explicit Diffusers reconfiguration must reach the native receiver
		// even when only the verified image bytes have changed.
		if !recheckImage && slices.Equal(after, before) {
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: matched.Revision, Reason: "unchanged"}, nil
		}
		change, err = w.typed.Replace(ctx, matched.Revision, *copy)
	}
	if err != nil {
		return wire.DeclarationChange{}, err
	}
	result := wire.DeclarationChange{Revision: change.Revision, Reason: change.Reason}
	switch change.Outcome {
	case registration.OutcomeApplied:
		result.Outcome = wire.DeclarationEditOutcomeApplied
	case registration.OutcomeConflict:
		result.Outcome = wire.DeclarationEditOutcomeConflict
	case registration.OutcomeInvalid:
		result.Outcome = wire.DeclarationEditOutcomeInvalid
	default:
		return result, fmt.Errorf("registration: %s: %s", change.Outcome.String(), change.Reason)
	}
	return result, err
}

// apply makes the registry say step.next at revision: declared when there
// was none, withdrawn and declared again when it differs, withdrawn when next
// is nil. It returns nil when nothing changed. A conflict before anything was
// written is a hostConflict when first is set, so the command reads again.
func (step declarationStep) apply(w *waiting, registry modelHostRegistryEditor, revision, command string, first bool) (*wire.DeclarationChange, string, error) {
	typed, typedWrite := registry.(interface {
		WriteConfigured(context.Context, registration.RegistrationSpec, bool) (wire.DeclarationChange, error)
	})
	typedWrite = typedWrite && step.spec != nil
	if !typedWrite && (step.next == nil && step.existing == nil || step.next != nil && sameHostDeclaration(step.existing, *step.next)) {
		return nil, "", nil
	}
	note := ""
	call, done := w.call()
	defer done()
	var change wire.DeclarationChange
	var err error
	if typedWrite {
		change, err = typed.WriteConfigured(call, *step.spec, step.recheckImage)
		if err == nil && change.Reason == "unchanged" {
			return nil, "", nil
		}
		note = step.who + " declaration is updated"
	} else if step.next == nil {
		change, err = registry.Withdraw(call, revision, step.name)
		note = step.who + " is withdrawn and its engines stopped"
	} else {
		pinned := wire.PinnedDeclaration{Declaration: *step.next, SupportedExtensions: step.extensions, RemoteServerKeySHA256: step.remotePin}
		if step.existing == nil {
			change, err = registry.DeclarePinned(call, revision, pinned)
		} else {
			change, err = registry.ReplacePinned(call, revision, pinned)
		}
		note = step.who + " declaration is updated"
	}
	if err != nil {
		return nil, "", &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	if change.Outcome == wire.DeclarationEditOutcomeConflict && first {
		return nil, "", &hostConflict{refusal(command, change.Outcome.String(), change.Reason)}
	}
	if id, pending := strings.CutPrefix(change.Reason, "pending:"); pending && change.Outcome == wire.DeclarationEditOutcomeForbidden {
		// A declaration without product standing from the command line is a
		// request the person confirms (architect ruling (3) of 2026-11-22).
		return nil, "", &exitError{exitWaiting, fmt.Errorf("%s: pending: confirm it in the OpenAbstractions popup, then run this again; follow it with: openabstractions rights requests --follow %s", command, id)}
	}
	if change.Outcome != wire.DeclarationEditOutcomeApplied {
		return nil, "", refusal(command, change.Outcome.String(), change.Reason)
	}
	return &change, note, nil
}

// hostConflict is a registry that changed between this command's read and
// its first write, before anything was withdrawn; the command reads again
// once (an atomic replace is TODO "Atomic registry replace").
type hostConflict struct{ err error }

func (c *hostConflict) Error() string { return c.err.Error() }
func (c *hostConflict) Unwrap() error { return c.err }

func modelHostRuntimePrograms() ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	self = identity.CanonicalProgramPath(filepath.Clean(self))
	programs := []string{self}
	if runtime.GOOS == "windows" {
		for _, name := range oaProgramFiles(oaRuntimeProgramStems) {
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
// name for this object, its manifest id and the size of its weights. The
// object may be named by its id, one of its names or an entry's locator; the
// declaration binds the id, the key the runtime's reconciler, catalogue
// projection and verification share. The path stays with the provider that
// holds the store; this command never learns one.
func lookupHostedObject(w *waiting, options serviceOptions, diagnostics io.Writer, command, store, id string) (string, string, int64, error) {
	machine, source, err := options.machine(diagnostics, command, modelsUsage)
	if err != nil {
		return "", "", 0, err
	}
	call, done := w.call()
	inventory, err := machine.ResolveStorageInventory(call, client.Requirements{})
	done()
	if err != nil {
		return "", "", 0, notResolved(command, source, err)
	}
	continuation := ""
	for pages := 0; pages < 512; pages++ {
		call, done := w.call()
		page, err := inventory.List(call, continuation, 256)
		done()
		if err != nil {
			return "", "", 0, err
		}
		if page.Outcome != content.InventoryOutcomePage {
			return "", "", 0, fmt.Errorf("inventory %s", page.Outcome)
		}
		for _, m := range page.Manifests {
			if m.Manifest.Store != store || !hostedManifestNamed(m.Manifest, id) {
				continue
			}
			name := id
			if len(m.Manifest.Names) > 0 {
				name = m.Manifest.Names[0].Name
			}
			name = plainHostedName(name, m.Manifest.Descriptor)
			size := int64(0)
			for _, e := range m.Manifest.Entries {
				if e.Size > size {
					size = e.Size
				}
			}
			return name, m.Manifest.ID, size, nil
		}
		if page.Complete {
			break
		}
		continuation = page.Continuation
	}
	return "", "", 0, nil
}

// digestModelName matches a SHA-256 written out, the shape a content-addressed
// file name or manifest id has. It names no model to a person.
var digestModelName = regexp.MustCompile(`(?i)[0-9a-f]{64}`)

// plainHostedName is the name a hosted model is asked for and listed by when
// the person named none: the store's own name for the content, so the
// router's family and the inventory's held_in are the same model (MODEL-C2).
// A store name that is a file digest (Setup stores its downloads as
// <sha256>.gguf) says nothing about the model, and the router and every
// client listing it would show the digest (owner, 2026-11-01). Such an
// object is named by what its descriptor states: the family, else the
// architecture with its size label. With neither, the digest stays, and
// `models host` refuses it and asks for --as.
func plainHostedName(name string, descriptor map[string]string) string {
	if !digestModelName.MatchString(name) {
		return name
	}
	if family := descriptor["family"]; family != "" && !digestModelName.MatchString(family) {
		return family
	}
	if arch, size := descriptor["architecture"], descriptor["size_label"]; arch != "" && size != "" {
		if plain := arch + "-" + size; !digestModelName.MatchString(plain) {
			return plain
		}
	}
	return name
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
	if settings.media.any() {
		if _, err := fmt.Fprintln(output, settings.media.describe()); err != nil {
			return err
		}
	}
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

// declaredResources is each declared program paired with the resources of
// one kind its declaration names (card:<n>, store:<name>), across every
// declaration source: operator, product and the installation (facade
// CONTRACT.md FAC-R3). A declaration that names none of that kind is absent;
// the pairing is exactly what it declares, never inferred from its contracts
// or models.
func (p *runtimeProviders) declaredResources(kind string) map[string][]string {
	out := map[string][]string{}
	for _, s := range p.sorted() {
		d := s.file.Declaration
		if d.remote() || d.host() || d.Program == "" {
			continue
		}
		if named := d.resources(kind); len(named) > 0 {
			out[d.Program] = append(slices.Clone(out[d.Program]), named...)
		}
	}
	return out
}

// declaredSignature flattens a declaredResources reading into one sorted
// slice, so two readings compare by value.
func declaredSignature(programs map[string][]string) []string {
	var out []string
	for program, named := range programs {
		for _, resource := range named {
			out = append(out, program+"\x00"+resource)
		}
	}
	sort.Strings(out)
	return out
}

// writeDeclaredRules returns a sweep that writes, for each program, the rules
// one resource kind of its declaration needs through that kind's one writer,
// leaving an existing rule as it is. It runs once for the declarations already
// read when the runtime opens, which covers the installation's and a
// product's, and again whenever the set of programs and their resources of
// that kind changes, which covers a live Declare.
func writeDeclaredRules(operator *inferenceOperator, providers *runtimeProviders, kind string,
	write func(by rwire.Subject, program string, resources []string) []error) func() {
	var mu sync.Mutex
	var last []string
	return func() {
		programs := providers.declaredResources(kind)
		signature := declaredSignature(programs)
		mu.Lock()
		unchanged := slices.Equal(signature, last)
		last = signature
		mu.Unlock()
		if unchanged {
			return
		}
		by := operator.credentials.runtimeRights.self()
		var errs []error
		for program, named := range programs {
			errs = append(errs, write(by, program, named)...)
		}
		if err := errors.Join(errs...); err != nil {
			complain("runtime rights:", err)
		}
	}
}

// writeCardHoldRules writes abstraction.resource/hold on each card:<n>
// resource a declaration names, for its own program, with why "provider add"
// (facade CONTRACT.md FAC-R3): the capability that owns the resource kind
// writes the permit rule its contract needs.
func writeCardHoldRules(operator *inferenceOperator, providers *runtimeProviders) func() {
	return writeDeclaredRules(operator, providers, "card", operator.permitCardRules)
}

// writeStoreRules writes abstraction.storage/inventory.provide on each
// store:<name> an inventory source's declaration names, for its own program.
// The installation's local-stores declaration never passes through Declare;
// without this sweep the runtime accepts none of its stores and the storage
// inventory is empty on every installed machine.
func writeStoreRules(operator *inferenceOperator, providers *runtimeProviders) func() {
	return writeDeclaredRules(operator, providers, "store", operator.permitStoreRules)
}
