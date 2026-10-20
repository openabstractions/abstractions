package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
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
	// pytorchName is the OA PyTorch engine's own provider: its declaration,
	// router host and provider identity (pytorch CONTRACT.md PYTORCH-D1).
	pytorchName = "pytorch"
	// pytorchProgram is that provider's program, beside the runtime's own
	// operator programs when Setup installed it.
	pytorchProgram = "pytorchd"
)

// modelHostArgument names the flags a declaration carries, so reading a
// declaration back gives the same command a person typed.
const (
	argEndpoint       = "--endpoint"
	argEngine         = "--engine"
	argModel          = "--model"
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
	// state is the model host's private state folder, where engine install
	// wrote the engine folder's record; empty is the provider's default.
	state string
	// media is the video and image engine the provider also serves.
	media hostMedia
	// card is the number of the card the engines load into; empty is 0. The
	// serve line carries it as --resource card:N, the declaration as the
	// resource card:N: one value.
	card string
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
	// card is the card its engine loads into; empty takes the model host's.
	card string
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
// store:* resource among them) and the contracts other than the inference
// ones this command declares (the inventory source a store:* resource
// needs). A declaration this command writes first takes the defaults.
type declaredFields struct {
	present    bool
	activation wire.Activation
	transport  wire.DeclarationTransport
	guarantees []string
	resources  []string
	contracts  []string
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
		return "native providers are not available on macOS in 0.4.0: their caller check needs an XPC endpoint"
	}
	return ""
}

func modelsHostCommand(command string, args []string, output, diagnostics io.Writer) error {
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
	torchProgram := flags.String("pytorch-program", "", "the PyTorch provider's program")
	providerEndpointName := flags.String("provider-endpoint", "", "the provider's own OA endpoint name; the declaration's own, else modelhost")
	resourceEndpoint := flags.String("resource-endpoint", "", "where abstraction.resource/leases@1 answers")
	as := flags.String("as", "", "the name callers ask for; the store's own name by default")
	idle := flags.Duration("idle", 0, "unload a model nothing has asked for")
	embeddings := flags.Bool("embeddings", false, "start the engine with embeddings and publish embed@1")
	var engineArgs repeated
	flags.Var(&engineArgs, "engine-arg", "one extra argument for the engine")
	stateDir := flags.String("state", "", "the model host's state folder, where engine install recorded the engine")
	videoModel := flags.String("video-model", "", "the video model set the model host serves")
	videoKind := flags.String("video-kind", "", "sdcpp or torch, the engine the video runs on")
	videoEngine := flags.String("video-engine", "", "absolute path of the sd-server engine install staged")
	torchPython := flags.String("torch-python", "", "absolute path of the reviewed venv's python")
	torchSource := flags.String("torch-source", "", "AMD's Hummingbird-XT infer folder")
	var videoOptions repeated
	flags.Var(&videoOptions, "video-model-option", "one torch model set option, NAME=VALUE")
	imageModel := flags.String("image-model", "", "the image model set the PyTorch provider serves")
	card := flags.String("card", "", "the card the engines load into")
	positional, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, command, modelsUsage, args, err)
	}
	mediaFlags := *videoModel != "" || *videoKind != "" || *videoEngine != "" || *torchPython != "" || *torchSource != "" ||
		len(videoOptions) > 0 || *imageModel != ""
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
		if n, convErr := strconv.Atoi(*card); convErr != nil || n < 0 || strconv.Itoa(n) != *card {
			return flagMistake(diagnostics, command, modelsUsage, "--card is a card number: 0, 1, ...")
		}
	}
	for _, path := range []struct{ flag, value string }{{"--video-engine", *videoEngine}, {"--torch-python", *torchPython}, {"--torch-source", *torchSource}} {
		if path.value != "" && !filepath.IsAbs(path.value) {
			return flagMistake(diagnostics, command, modelsUsage, path.flag+" must be an absolute path")
		}
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
	call, done := w.call()
	registry, err := machine.ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	// run reads the declarations and writes the model host's one
	// declaration from them. A registry that changed in between answers
	// conflict before anything was withdrawn; run then reads again once.
	run := func() error {
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

		if command == "models unhost" {
			store, id, ok := strings.Cut(positional[0], "/")
			if !ok || store == "" || id == "" {
				return flagMistake(diagnostics, command, modelsUsage, "name the object as <store>/<id>, the way the inventory names it")
			}
			kept := slices.DeleteFunc(models, func(m hostedModel) bool { return m.Store == store && m.Object == id })
			if len(kept) == len(models) {
				return refusal(command, "unknown", store+"/"+id+" is not hosted")
			}
			// The last model with no sd-server video withdraws the model host;
			// the PyTorch provider's declaration stays as it is.
			return declareHost(w, registry, list, settings, kept, command, output, options.asJSON,
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
				said = append(said, "the PyTorch sets move from the model host's declaration to the PyTorch provider's ("+pytorchProgram+")")
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
			return &exitError{exitNotResolved, fmt.Errorf("%s: the PyTorch sets are the PyTorch provider's, and no %s is beside this command; pass --pytorch-program",
				command, pytorchProgram)}
		}
		if settings.program == "" && (len(positional) == 1 || len(models) > 0 || settings.media.hostVideo()) {
			return &exitError{exitNotResolved, fmt.Errorf("%s: no %s beside this command; pass --program", command, modelHostProgram)}
		}
		if mediaFlags || settings.legacyTorch {
			if err := proveMediaEngine(command, settings, diagnostics); err != nil {
				return err
			}
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
			models = slices.DeleteFunc(models, func(m hostedModel) bool {
				return (m.Store == store && m.Object == id) || m.Name == name
			})
			models = append(models, hostedModel{Name: name, Store: store, Object: id})
			sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
			said = append([]string{fmt.Sprintf("hosting %s/%s as %s (%d bytes) with %s", store, id, name, size, settings.engine)}, said...)
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
		return declareHost(w, registry, list, settings, models, command, output, options.asJSON, strings.Join(said, "; "))
	}
	err = run()
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
				note = "image model " + m.imageModel + " dropped: it runs on the PyTorch engine"
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
		return m, note, "--image-model runs on the PyTorch engine, which a video runs on only with --video-kind torch; sd-server serves no image"
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
	if m.videoModel != "" {
		parts = append(parts, "video "+m.videoModel+" on "+m.kind())
	}
	if m.imageModel != "" {
		parts = append(parts, "image "+m.imageModel+" on torch")
	}
	return "serving " + strings.Join(parts, " and ")
}

// proveMediaEngine asks the program the declaration will launch whether the
// engine it names is the one it would start: modelhostd for sd-server against
// its pin (modelhost MODELHOST-V1), pytorchd for the PyTorch venv and AMD's
// source for a PyTorch video, or the venv alone for an image set without one
// (pytorch PYTORCH-T1). A PyTorch video's check proves the venv the image set
// shares.
func proveMediaEngine(command string, settings hostSettings, diagnostics io.Writer) error {
	m := settings.media
	var args []string
	program, state := settings.torch.program, settings.torch.state
	what := m.kind() + " video"
	switch {
	case m.torchVideo():
		args = []string{"engine", "check", "--set", m.videoModel, argPython, m.torchPython, argSource, m.torchSource}
	case m.videoModel != "" && m.videoEngine != "":
		program, state = settings.program, settings.state
		args = []string{"engine", "check", "--kind", videoKindSdCpp, "--engine", m.videoEngine}
	case m.imageModel != "":
		what = "torch image"
		args = []string{"engine", "check", "--set", m.imageModel, argPython, m.torchPython}
	default:
		return nil
	}
	if state != "" {
		args = append(args, argState, state)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var stderr strings.Builder
	check := exec.CommandContext(ctx, program, args...)
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
	return providerEndpointPath("resource-table-v1")
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
	var stderr strings.Builder
	args := []string{"engine", "check", "--engine", engine}
	if settings.state != "" {
		args = append(args, argState, settings.state)
	}
	for _, extra := range settings.extra {
		args = append(args, argEngineArg, extra)
	}
	check := exec.CommandContext(ctx, program, args...)
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
// guarantees, its resources other than card and profile, and its contracts
// other than the ones this command declares.
func readDeclared(d *wire.Declaration, declares []string) (declaredFields, string) {
	declared := declaredFields{present: true, activation: d.Activation, transport: d.Transport, guarantees: slices.Clone(d.Guarantees)}
	for _, contract := range d.Contracts {
		if !slices.Contains(declares, contract) {
			declared.contracts = append(declared.contracts, contract)
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
		case strings.HasPrefix(resource, "profile:"):
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
	argIdle: false, argState: false, argEngineArg: true, argModel: true, argVideoModel: false,
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
// alone or with =value) and stops where that package stops, at "--". Each
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
			passthrough = append(passthrough, args[i])
			continue
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
			if card, ok := strings.CutPrefix(value, "card:"); ok {
				settings.card = card
			}
		case argIdle:
			settings.idle = value
		case argState:
			settings.state = value
		case argEngineArg:
			settings.extra = append(settings.extra, value)
		case argModel:
			if m, ok := parseHostedModel(value); ok {
				models = append(models, m)
			}
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
			if card, ok := strings.CutPrefix(value, "card:"); ok {
				settings.card = card
			}
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
	for _, m := range models {
		arguments = append(arguments, argModel, m.argument())
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
		if !media.hostVideo() {
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
func declareHost(w *waiting, registry *client.Registry, list wire.DeclarationList, settings hostSettings, models []hostedModel,
	command string, output io.Writer, asJSON bool, said string) error {
	programs, err := modelHostRuntimePrograms()
	if err != nil {
		return fmt.Errorf("%s: runtime program: %w", command, err)
	}
	settings = settings.withTorchDefaults()
	steps := []declarationStep{
		{name: pytorchName, who: "the PyTorch provider", existing: existingDeclaration(list, pytorchName)},
		{name: modelHostName, who: "the model host", existing: existingDeclaration(list, modelHostName)},
	}
	if settings.media.torchAny() {
		if settings.torch.program == "" {
			return &exitError{exitNotResolved, fmt.Errorf("%s: the PyTorch sets are the PyTorch provider's, and no %s is beside this command; pass --pytorch-program",
				command, pytorchProgram)}
		}
		d := pytorchDeclaration(settings, programs)
		steps[0].next = &d
	}
	if len(models) > 0 || settings.media.hostVideo() {
		d := modelHostDeclaration(settings, models, programs)
		steps[1].next = &d
	}
	if steps[0].next == nil && steps[1].next == nil && steps[0].existing == nil && steps[1].existing == nil {
		return refusal(command, "invalid", "the model host would serve nothing: host an object, or set --video-model or --image-model")
	}
	revision := list.Revision
	var last *wire.DeclarationChange
	notes := []string{said}
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

// declarationStep is one provider's declaration as it is and as this command
// would write it; next is nil when the provider would serve nothing.
type declarationStep struct {
	name, who      string
	existing, next *wire.Declaration
}

// apply makes the registry say step.next at revision: declared when there
// was none, withdrawn and declared again when it differs, withdrawn when next
// is nil. It returns nil when nothing changed. A conflict before anything was
// written is a hostConflict when first is set, so the command reads again.
func (step declarationStep) apply(w *waiting, registry *client.Registry, revision, command string, first bool) (*wire.DeclarationChange, string, error) {
	if step.next == nil && step.existing == nil || step.next != nil && sameHostDeclaration(step.existing, *step.next) {
		return nil, "", nil
	}
	note := ""
	// The registry takes a name once, so an existing declaration, a
	// hand-made one among them, is withdrawn and declared again under the
	// same name at the next revision: one declaration, updated in place.
	if step.existing != nil {
		call, done := w.call()
		withdrawn, err := registry.Withdraw(call, revision, step.name)
		done()
		if err != nil {
			return nil, "", &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		if withdrawn.Outcome == wire.DeclarationEditOutcomeConflict && first {
			return nil, "", &hostConflict{refusal(command, withdrawn.Outcome.String(), withdrawn.Reason)}
		}
		if withdrawn.Outcome != wire.DeclarationEditOutcomeApplied {
			return nil, "", refusal(command, withdrawn.Outcome.String(), withdrawn.Reason)
		}
		if step.next == nil {
			return &withdrawn, step.who + " is withdrawn and its engines stopped", nil
		}
		revision = withdrawn.Revision
		// The provider reads its serve line only when it starts, so a
		// running provider restarts its engines under the new one.
		note = step.who + " restarts its engines with the new declaration"
	}
	call, done := w.call()
	change, err := registry.Declare(call, revision, *step.next)
	done()
	if err != nil {
		return nil, "", &exitError{exitNotResolved, fmt.Errorf("%s: %w%s", command, err, restoreHost(w, registry, revision, step.existing))}
	}
	if change.Outcome == wire.DeclarationEditOutcomeConflict && step.existing == nil && first {
		return nil, "", &hostConflict{refusal(command, change.Outcome.String(), change.Reason)}
	}
	if change.Outcome != wire.DeclarationEditOutcomeApplied {
		return nil, "", refusal(command, change.Outcome.String(), change.Reason+restoreHost(w, registry, revision, step.existing))
	}
	return &change, note, nil
}

// hostConflict is a registry that changed between this command's read and
// its first write, before anything was withdrawn; the command reads again
// once (an atomic replace is TODO "Atomic registry replace").
type hostConflict struct{ err error }

func (c *hostConflict) Error() string { return c.err.Error() }
func (c *hostConflict) Unwrap() error { return c.err }

// restoreHost declares a withdrawn declaration again at the revision its
// withdrawal returned, after the new one was refused, and says how that
// went. Nothing was withdrawn when existing is nil.
func restoreHost(w *waiting, registry *client.Registry, revision string, existing *wire.Declaration) string {
	if existing == nil {
		return ""
	}
	call, done := w.call()
	restored, err := registry.Declare(call, revision, *existing)
	done()
	if err == nil && restored.Outcome == wire.DeclarationEditOutcomeApplied {
		return "; the earlier " + existing.Name + " declaration is declared again as it was"
	}
	return fmt.Sprintf("; the earlier %s declaration was withdrawn and could not be declared again, run the command again; it read: %s %s",
		existing.Name, existing.Program, strings.Join(existing.Arguments, " "))
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
	machine, source, err := options.machine(diagnostics, command, modelsUsage)
	if err != nil {
		return "", 0, err
	}
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
			errs = append(errs, operator.permitCardRules(by, program, cards)...)
		}
		if err := errors.Join(errs...); err != nil {
			complain("runtime rights:", err)
		}
	}
}
