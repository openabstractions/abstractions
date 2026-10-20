package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
)

// The model host's declaration serves chat, preload and embed for the hosted
// objects, and the PyTorch provider's serves video and image for the
// PyTorch sets, each model for its own profile only; both name the card
// their engines load into (modelhost README "Video", pytorch PYTORCH-D1;
// ROUTE-D9).
func TestModelsHostDeclarationsRouteChatEmbedVideoAndImage(t *testing.T) {
	settings := hostSettings{program: filepath.Join(t.TempDir(), "modelhostd"), endpoint: "modelhost", engine: "llama-server",
		resource: "leases", embed: true, media: hostMedia{videoModel: "hummingbird-xt-i2v", videoKind: videoKindTorch,
			torchPython: `C:\venv\python.exe`, torchSource: `C:\src\infer`, imageModel: "flux2-klein-4b"},
		torch: torchSettings{program: filepath.Join(t.TempDir(), "pytorchd")}}
	settings = settings.withTorchDefaults()
	models := []hostedModel{{Name: "qwen3-4b", Store: "hf", Object: "qwen3"}}
	host := modelHostDeclaration(settings, models, []string{"runtime"})
	torch := pytorchDeclaration(settings, []string{"runtime"})
	if want := []string{inference.Contract, inference.PreloadContract, inference.EmbedContract}; !slices.Equal(host.Contracts, want) {
		t.Fatalf("model host contracts %v, want %v", host.Contracts, want)
	}
	if want := []string{"card:0", "profile:chat", "profile:embed"}; !slices.Equal(host.Resources, want) {
		t.Fatalf("model host resources %v, want %v", host.Resources, want)
	}
	if want := []string{inference.VideoContract, inference.ImageContract}; torch.Name != pytorchName || !slices.Equal(torch.Contracts, want) {
		t.Fatalf("PyTorch provider %s contracts %v, want %v", torch.Name, torch.Contracts, want)
	}
	if want := []string{"card:0", "profile:video", "profile:image"}; !slices.Equal(torch.Resources, want) {
		t.Fatalf("PyTorch provider resources %v, want %v", torch.Resources, want)
	}
	for _, moved := range []string{argTorchPython, argTorchSource, argVideoKind, argImageModel, argVideoModel} {
		if slices.Contains(host.Arguments, moved) {
			t.Fatalf("the model host's serve line names %s: %v", moved, host.Arguments)
		}
	}
	var hosts []*router.Host
	for _, d := range []wire.Declaration{host, torch} {
		profiles := declarationOf(d).mediatedInferenceProfiles()
		expect := listen.ServerExpectation{Principal: identity.User{Kind: "posix", UID: 1}, Program: d.Program}
		h, err := router.NewNative(d.Name, "endpoint-"+d.Name, expect, d.Models, profiles)
		if err != nil {
			t.Fatal(err)
		}
		hosts = append(hosts, h)
	}
	routes := router.New(hosts...)
	routes.Survey()
	for _, c := range []struct {
		model, profile, host string
	}{
		{"qwen3-4b", router.ProfileChat, modelHostName},
		{"qwen3-4b", router.ProfileEmbed, modelHostName},
		{"qwen3-4b", router.ProfileVideo, ""},
		{"qwen3-4b", router.ProfileImage, ""},
		{"hummingbird-xt-i2v", router.ProfileVideo, pytorchName},
		{"hummingbird-xt-i2v", router.ProfileChat, ""},
		{"flux2-klein-4b", router.ProfileImage, pytorchName},
		{"flux2-klein-4b", router.ProfileVideo, ""},
	} {
		decision, _ := routes.Route(router.Request{Model: c.model, Profile: c.profile})
		got := ""
		if decision != nil && decision.Verdict != router.NoHost {
			got = decision.Host
		}
		if got != c.host {
			t.Fatalf("%s for %s: routed to %q (%+v), want %q", c.model, c.profile, got, decision, c.host)
		}
	}
	// The arguments read back give the same settings.
	read, readModels := readHostDeclaration(wire.DeclarationList{Declarations: []wire.DeclarationState{{Declaration: host}, {Declaration: torch}}})
	if read.media.videoModel != "hummingbird-xt-i2v" || read.media.kind() != videoKindTorch || read.media.imageModel != "flux2-klein-4b" ||
		read.media.torchPython != `C:\venv\python.exe` || read.media.torchSource != `C:\src\infer` || read.card != "0" || read.torch.card != "0" ||
		len(readModels) != 1 || !read.embed || read.legacyTorch {
		t.Fatalf("read back %+v %v", read, readModels)
	}
}

// recordingPytorch is recordingModelHost under pytorchd's name: it records
// each run's arguments to $STANDIN_ARGS and passes.
func recordingPytorch(t *testing.T) string {
	t.Helper()
	built := recordingModelHost(t, t.TempDir())
	name := pytorchProgram
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(t.TempDir(), name)
	if err := os.Rename(built, target); err != nil {
		t.Fatal(err)
	}
	return target
}

// buildPytorchd builds the real PyTorch provider from this checkout.
func buildPytorchd(t *testing.T) string {
	t.Helper()
	name := pytorchProgram
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(t.TempDir(), name)
	module, err := filepath.Abs(privateInput(t, "..", "openabstractions-flat", "abstraction-provider-pytorch", "go"))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "-C", module, "build", "-o", target, "./cmd/pytorchd")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pytorchd: %v\n%s", err, out)
	}
	return target
}

// declarationsNamed reads the registry's declarations of the model host and
// the PyTorch provider.
func declarationsNamed(t *testing.T, w *waiting, registry *client.Registry) (host, torch *wire.Declaration) {
	t.Helper()
	call, done := w.call()
	list, err := registry.Declarations(call)
	done()
	if err != nil {
		t.Fatal(err)
	}
	return existingDeclaration(list, modelHostName), existingDeclaration(list, pytorchName)
}

// models host sets up video and image in one command, writes one
// declaration per provider however often it runs, updates a hand-made one in
// place with its own arguments kept, and an sd-to-torch switch moves the
// video to the PyTorch provider and withdraws the model host left with
// nothing to serve.
func TestModelsHostSetsUpVideoAndImageIdempotently(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	program := recordingModelHost(t, t.TempDir())
	torchProgram := recordingPytorch(t)
	record := filepath.Join(t.TempDir(), "args")
	t.Setenv("STANDIN_ARGS", record)
	sdServer := filepath.Join(t.TempDir(), "sd-server.exe")
	if err := os.WriteFile(sdServer, []byte("stand-in"), 0o700); err != nil {
		t.Fatal(err)
	}
	python, source := filepath.Join(t.TempDir(), "python.exe"), t.TempDir()
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s", "--program", program, "--pytorch-program", torchProgram}

	// A hand-made declaration, as the README's provider add wrote it.
	w := newWaiting(30 * time.Second)
	defer w.stop()
	call, done := w.call()
	registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		t.Fatal(err)
	}
	call, done = w.call()
	list, err := registry.Declarations(call)
	done()
	if err != nil {
		t.Fatal(err)
	}
	// It spells flags every way Go's flag package reads them, names the video
	// model twice (modelhostd would take the last), holds a store: resource
	// and attaches to a provider a person starts.
	handMade := wire.Declaration{Name: modelHostName, Program: identity.CanonicalProgramPath(filepath.Clean(program)),
		Arguments: []string{"serve", "--endpoint", providerEndpointToken, "--video-model=fastwan2.2-ti2v-5b", "-video-engine", sdServer,
			"--video-engine-arg", "--temporal-tiling", "-video-ready=30m", "--video-model=hummingbird-xt-i2v"},
		Endpoint: "modelhost-video", Transport: wire.DeclarationTransportNative, Contracts: []string{inference.VideoContract, storageInventorySource},
		Resources: []string{"profile:video", "store:ollama"}, Models: []string{"video:fastwan2.2-ti2v-5b"}, Activation: wire.ActivationAttach}
	call, done = w.call()
	change, err := registry.Declare(call, list.Revision, handMade)
	done()
	if err != nil || change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("hand-made declaration %+v %v", change, err)
	}

	run := func(args ...string) string {
		t.Helper()
		var output, diagnostics bytes.Buffer
		if err := modelsHostCommand("models host", append(args, endpoint...), &output, &diagnostics); err != nil {
			t.Fatalf("models host %v: %v\n%s", args, err, diagnostics.String())
		}
		return output.String()
	}

	run("--video-kind", "sdcpp", "--video-model", "fastwan2.2-ti2v-5b", "--video-engine", sdServer)
	d, torch := declarationsNamed(t, w, registry)
	if d == nil || torch != nil {
		t.Fatalf("after the first run: model host %+v, PyTorch provider %+v", d, torch)
	}
	if !slices.Contains(d.Resources, "card:0") || !slices.Contains(d.Models, "video:fastwan2.2-ti2v-5b") ||
		!slices.Contains(d.Contracts, inference.VideoContract) || d.Endpoint != "modelhost-video" {
		t.Fatalf("declaration after the first run %+v", d)
	}
	if !slices.Contains(d.Arguments, "-video-ready=30m") {
		t.Fatalf("the hand-made --video-ready was lost: %v", d.Arguments)
	}
	// The video model is taken from every spelling, so the one this run
	// named is the only one left; the hand-made fields this command does
	// not decide are kept.
	if n := len(slices.DeleteFunc(slices.Clone(d.Arguments), func(a string) bool { return !strings.Contains(a, "video-model") || a == argVideoOption })); n != 1 ||
		!slices.Contains(d.Arguments, "fastwan2.2-ti2v-5b") || slices.Contains(d.Arguments, "--video-model=hummingbird-xt-i2v") {
		t.Fatalf("the video model after the first run: %v", d.Arguments)
	}
	if d.Activation != wire.ActivationAttach || !slices.Contains(d.Resources, "store:ollama") || d.Transport != wire.DeclarationTransportNative ||
		!slices.Contains(d.Contracts, storageInventorySource) {
		t.Fatalf("the hand-made activation, transport or store resource was lost: %+v", d)
	}
	if i := slices.Index(d.Arguments, argVideoEngineArg); i < 0 || i+1 >= len(d.Arguments) || d.Arguments[i+1] != "--temporal-tiling" {
		t.Fatalf("the hand-made sd-server argument was lost: %v", d.Arguments)
	}

	again := run("--video-kind", "sdcpp", "--video-model", "fastwan2.2-ti2v-5b", "--video-engine", sdServer)
	if second, _ := declarationsNamed(t, w, registry); second == nil || !slices.Equal(second.Arguments, d.Arguments) || !strings.Contains(again, "already says this") {
		t.Fatalf("a second run changed the declaration: %q %+v", again, second)
	}

	said := run("--video-kind", "torch", "--video-model", "hummingbird-xt-i2v", "--torch-python", python, "--torch-source", source,
		"--image-model", "flux2-klein-4b")
	host, s := declarationsNamed(t, w, registry)
	if host != nil {
		t.Fatalf("the model host serves nothing after the switch and is still declared: %+v", host)
	}
	if s == nil || s.Program != identity.NormalizeSubjectProgram(filepath.Clean(torchProgram)) || !slices.Contains(s.Resources, "card:0") ||
		!slices.Contains(s.Models, "video:hummingbird-xt-i2v") || !slices.Contains(s.Models, "image:flux2-klein-4b") ||
		!slices.Contains(s.Contracts, inference.ImageContract) || !slices.Contains(s.Resources, "profile:image") {
		t.Fatalf("PyTorch provider declaration after the sd-to-torch switch %+v", s)
	}
	for _, want := range [][2]string{{argPython, python}, {argSource, source}, {argVideoModel, "hummingbird-xt-i2v"}, {argImageModel, "flux2-klein-4b"}} {
		if i := slices.Index(s.Arguments, want[0]); i < 0 || i+1 >= len(s.Arguments) || s.Arguments[i+1] != want[1] {
			t.Fatalf("the PyTorch provider's serve line %v lacks %s %s", s.Arguments, want[0], want[1])
		}
	}
	// sd-server's arguments belong to the sdcpp kind: the switch drops them
	// and says so.
	if slices.Contains(s.Arguments, argVideoEngineArg) || slices.Contains(s.Arguments, "--temporal-tiling") || !strings.Contains(said, "--temporal-tiling dropped") ||
		!strings.Contains(said, "the model host is withdrawn") {
		t.Fatalf("after the switch %v; said %q", s.Arguments, said)
	}
	if again := run("--video-kind", "torch", "--video-model", "hummingbird-xt-i2v"); !strings.Contains(again, "already says this") {
		t.Fatalf("a second torch run changed a declaration: %q", again)
	}
	checks := recordedChecks(t, record)
	if !slices.ContainsFunc(checks, func(c []string) bool {
		return slices.Equal(c, []string{"engine", "check", "--kind", videoKindSdCpp, "--engine", sdServer})
	}) || !slices.ContainsFunc(checks, func(c []string) bool {
		return slices.Equal(c, []string{"engine", "check", "--set", "hummingbird-xt-i2v", argPython, python, argSource, source})
	}) {
		t.Fatalf("the video engines were not proven by their own programs: %q", checks)
	}
}

// A model host declaration written before the PyTorch provider carries the
// PyTorch engine's options, which modelhostd serve refuses, saying how to
// move them. The next models host run splits it: the chat model stays with
// the model host, and the image set moves to the PyTorch provider's own
// declaration with card:0, proven by pytorchd engine check.
func TestModelsHostSplitsADeclarationThatCarriedThePyTorchEngine(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	program := recordingModelHost(t, t.TempDir())
	torchProgram := recordingPytorch(t)
	record := filepath.Join(t.TempDir(), "args")
	t.Setenv("STANDIN_ARGS", record)
	engine := filepath.Join(t.TempDir(), "llama-server.exe")
	if err := os.WriteFile(engine, []byte("stand-in"), 0o700); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(t.TempDir(), "python.exe")
	// Built before the wait starts: a go build on a loaded machine took over
	// 100 s, which the 30 s budget of the service calls below must not pay.
	modelhostd := buildModelhostd(t)
	w := newWaiting(30 * time.Second)
	defer w.stop()
	call, done := w.call()
	registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		t.Fatal(err)
	}
	call, done = w.call()
	list, err := registry.Declarations(call)
	done()
	if err != nil {
		t.Fatal(err)
	}
	legacy := wire.Declaration{Name: modelHostName, Program: identity.CanonicalProgramPath(filepath.Clean(program)),
		Arguments: []string{"serve", argEndpoint, providerEndpointToken, argEngine, engine, argResource, "leases", argHold, "card:0",
			argModel, "tiny=fake/tiny", argTorchPython, python, argImageModel, "flux2-klein-4b", argImageTimeLimit, "20m"},
		Endpoint: "modelhost", Transport: wire.DeclarationTransportNative, Contracts: []string{inference.Contract, inference.ImageContract},
		Resources: []string{"card:0", "profile:chat", "profile:image"}, Models: []string{"chat:tiny", "image:flux2-klein-4b"},
		Activation: wire.ActivationAttach, Role: wire.DeclarationRoleProvider}
	call, done = w.call()
	change, err := registry.Declare(call, list.Revision, legacy)
	done()
	if err != nil || change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("legacy declaration %+v %v", change, err)
	}

	// The real model host refuses that serve line by name until it is
	// declared again.
	args := slices.Clone(legacy.Arguments)
	args[slices.Index(args, providerEndpointToken)] = fmt.Sprintf("oa-models-host-legacy-%d", os.Getpid())
	refused := exec.Command(modelhostd, append(args, argState, t.TempDir())...)
	var refusal bytes.Buffer
	refused.Stderr = &refusal
	var exit *exec.ExitError
	if err := refused.Run(); !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(refusal.String(), "pytorchd") ||
		!strings.Contains(refusal.String(), "openabstractions models host") {
		t.Fatalf("modelhostd serve with the PyTorch options: %v, stderr %q", err, refusal.String())
	}

	var output, diagnostics bytes.Buffer
	if err := modelsHostCommand("models host", []string{"--image-model", "flux2-klein-4b", "--program", program, "--pytorch-program", torchProgram,
		"--endpoint", options.endpoint, "--timeout", "30s"}, &output, &diagnostics); err != nil {
		t.Fatalf("models host: %v\n%s", err, diagnostics.String())
	}
	if !strings.Contains(output.String(), "move from the model host's declaration to the PyTorch provider's") {
		t.Fatalf("the split was not said: %q", output.String())
	}
	host, torch := declarationsNamed(t, w, registry)
	if host == nil || !slices.Equal(host.Contracts, []string{inference.Contract, inference.PreloadContract}) || !slices.Equal(host.Resources, []string{"card:0", "profile:chat"}) ||
		!slices.Equal(host.Models, []string{"tiny"}) || host.Activation != wire.ActivationAttach {
		t.Fatalf("the model host after the split %+v", host)
	}
	for _, moved := range []string{argTorchPython, argImageModel, argImageTimeLimit, videoKindTorch} {
		if slices.Contains(host.Arguments, moved) {
			t.Fatalf("the model host still names %s: %v", moved, host.Arguments)
		}
	}
	if torch == nil || torch.Program != identity.NormalizeSubjectProgram(filepath.Clean(torchProgram)) ||
		!slices.Equal(torch.Contracts, []string{inference.ImageContract}) || !slices.Equal(torch.Resources, []string{"card:0", "profile:image"}) ||
		!slices.Equal(torch.Models, []string{"image:flux2-klein-4b"}) || torch.Activation != wire.ActivationOnDemand ||
		!slices.Contains(torch.Arguments, argRuntimeProgram) {
		t.Fatalf("the PyTorch provider after the split %+v", torch)
	}
	for _, want := range [][2]string{{argPython, python}, {argImageModel, "flux2-klein-4b"}, {argImageTimeLimit, "20m"}, {argHold, "card:0"}, {argResource, "leases"}} {
		if i := slices.Index(torch.Arguments, want[0]); i < 0 || i+1 >= len(torch.Arguments) || torch.Arguments[i+1] != want[1] {
			t.Fatalf("the PyTorch provider's serve line %v lacks %s %s", torch.Arguments, want[0], want[1])
		}
	}
	if checks := recordedChecks(t, record); !slices.ContainsFunc(checks, func(c []string) bool {
		return slices.Equal(c, []string{"engine", "check", "--set", "flux2-klein-4b", argPython, python})
	}) {
		t.Fatalf("pytorchd engine check did not prove the moved image set: %q", checks)
	}
}

// An image set alone declares the PyTorch provider with its venv and no
// source or video; naming an sd-server video beside it drops it with a note,
// since sd-server serves no image.
func TestModelsHostImageSetAloneAndItsSwitchToSdCpp(t *testing.T) {
	alone, note, mistake := hostMedia{}.with("", "", "", `C:\venv\python.exe`, "", nil, "flux2-klein-4b")
	if mistake != "" || note != "" {
		t.Fatalf("an image set alone: %q %q", note, mistake)
	}
	settings := hostSettings{resource: "leases", media: alone}.withTorchDefaults()
	args := pytorchArguments(settings, nil)
	want := []string{"serve", argEndpoint, providerEndpointToken, argResource, "leases", argHold, "card:0",
		argPython, `C:\venv\python.exe`, argImageModel, "flux2-klein-4b"}
	if !slices.Equal(args, want) {
		t.Fatalf("arguments %q, want %q", args, want)
	}
	if host := hostArguments(settings, nil, nil); slices.Contains(host, argImageModel) || slices.Contains(host, argTorchPython) {
		t.Fatalf("the model host's serve line names the image set: %q", host)
	}
	read, _ := readHostDeclaration(wire.DeclarationList{Declarations: []wire.DeclarationState{{Declaration: wire.Declaration{Name: pytorchName, Arguments: args}}}})
	if read.media.imageModel != "flux2-klein-4b" || read.media.torchPython != `C:\venv\python.exe` || read.media.videoModel != "" || read.legacyTorch {
		t.Fatalf("read back %+v", read.media)
	}
	switched, note, mistake := read.media.with("fastwan2.2-ti2v-5b", videoKindSdCpp, `C:\sd\sd-server.exe`, "", "", nil, "")
	if mistake != "" || switched.imageModel != "" || switched.torchPython != "" || !strings.Contains(note, "image model flux2-klein-4b dropped") {
		t.Fatalf("the switch to sd-server: %+v %q %q", switched, note, mistake)
	}
}

// A configuration that cannot run is a flag mistake before anything is
// declared.
func TestModelsHostRefusesAnIncompleteMediaEngine(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"--image-model", "flux2-klein-4b"}, "--image-model needs --torch-python"},
		{[]string{"--image-model", "flux2-klein-4b", "--torch-python", filepath.Join(os.TempDir(), "python.exe"),
			"--video-model", "fastwan2.2-ti2v-5b", "--video-engine", filepath.Join(os.TempDir(), "sd-server.exe")}, "sd-server serves no image"},
		{[]string{"--video-kind", "torch", "--video-model", "hummingbird-xt-i2v"}, "--torch-python and --torch-source"},
		{[]string{"--video-model", "fastwan2.2-ti2v-5b"}, "--video-engine names the sd-server"},
		{[]string{"--video-kind", "cuda"}, "--video-kind is sdcpp or torch"},
		{[]string{"--image-model", "flux2-klein-4b", "--pytorch-program", "pytorchd"}, "--pytorch-program must be an absolute path"},
	} {
		var output, diagnostics bytes.Buffer
		err := modelsHostCommand("models host", append(c.args, "--program", filepath.Join(t.TempDir(), "modelhostd"),
			"--endpoint", options.endpoint, "--timeout", "30s"), &output, &diagnostics)
		if err == nil || !strings.Contains(diagnostics.String()+err.Error(), c.says) {
			t.Fatalf("%v: %v, diagnostics %q; wanted %q", c.args, err, diagnostics.String(), c.says)
		}
	}
}

// Replacing a declaration withdraws it first, since the registry takes a name
// once. When the registry refuses the new one, the withdrawn declaration is
// declared again as it was, so a refused run leaves the model host serving.
func TestModelsHostRestoresTheDeclarationARefusalWithdrew(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	w := newWaiting(30 * time.Second)
	defer w.stop()
	call, done := w.call()
	registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		t.Fatal(err)
	}
	listed := func() wire.DeclarationList {
		t.Helper()
		call, done := w.call()
		list, err := registry.Declarations(call)
		done()
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	program := filepath.Join(t.TempDir(), "modelhostd.exe")
	handMade := wire.Declaration{Name: modelHostName, Program: identity.CanonicalProgramPath(program),
		Arguments: []string{"serve", "--endpoint", providerEndpointToken, "--video-model", "fastwan2.2-ti2v-5b", "--video-engine", program},
		Endpoint:  "modelhost-video", Transport: wire.DeclarationTransportNative, Contracts: []string{inference.VideoContract},
		Resources: []string{"profile:video"}, Models: []string{"video:fastwan2.2-ti2v-5b"}, Activation: wire.ActivationAttach}
	call, done = w.call()
	change, err := registry.Declare(call, listed().Revision, handMade)
	done()
	if err != nil || change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("hand-made declaration %+v %v", change, err)
	}
	list := listed()
	existing := existingHostDeclaration(list)
	// The registry refuses a declaration of the program asking for it
	// (program:self): this test executable.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	settings := hostSettings{program: self, endpoint: "modelhost-video", resource: "leases",
		media: hostMedia{videoModel: "fastwan2.2-ti2v-5b", videoKind: videoKindSdCpp, videoEngine: program}}
	err = declareHost(w, registry, list, settings, nil, "models host", &bytes.Buffer{}, false, "serving")
	if err == nil || !strings.Contains(err.Error(), "declared again as it was") {
		t.Fatalf("a refused replacement: %v", err)
	}
	after := existingHostDeclaration(listed())
	if after == nil || after.Program != existing.Program || !slices.Equal(after.Arguments, existing.Arguments) || after.Endpoint != existing.Endpoint {
		t.Fatalf("the withdrawn declaration was not restored: %+v", after)
	}
}

// An image set alone runs on the OA PyTorch engine with the venv's python
// and no source. models host proves that venv with pytorchd engine check for
// the image set, declares the PyTorch provider with image@1, the set scoped
// to profile image and card:0, and declares no model host; the real pytorchd
// accepts the declared serve line and serves image@1 for the set. The engine
// itself is the stand-in of the PyTorch provider's own image tests: a real
// launch needs the pinned venv and the set's files.
func TestModelsHostImageSetRunsOnTheRealPytorchProvider(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	torchProgram := recordingPytorch(t)
	record := filepath.Join(t.TempDir(), "args")
	t.Setenv("STANDIN_ARGS", record)
	python, state := filepath.Join(t.TempDir(), "python.exe"), t.TempDir()
	torchState := filepath.Join(state, pytorchName)
	var output, diagnostics bytes.Buffer
	// No model host program is needed for a PyTorch set alone. The state
	// folder an isolated runtime names holds the PyTorch provider's own.
	if err := modelsHostCommand("models host", []string{"--image-model", "flux2-klein-4b", "--torch-python", python,
		"--program", filepath.Join(t.TempDir(), "absent", "modelhostd.exe"), "--pytorch-program", torchProgram, "--state", state,
		"--endpoint", options.endpoint, "--timeout", "30s"}, &output, &diagnostics); err != nil {
		t.Fatalf("models host: %v\n%s", err, diagnostics.String())
	}
	checks := recordedChecks(t, record)
	want := []string{"engine", "check", "--set", "flux2-klein-4b", argPython, python, argState, torchState}
	if len(checks) != 1 || !slices.Equal(checks[0], want) {
		t.Fatalf("engine checks %q, want %q", checks, want)
	}
	w := newWaiting(30 * time.Second)
	defer w.stop()
	call, done := w.call()
	registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		t.Fatal(err)
	}
	host, d := declarationsNamed(t, w, registry)
	if host != nil {
		t.Fatalf("an image set alone declared the model host: %+v", host)
	}
	if d == nil || !slices.Equal(d.Contracts, []string{inference.ImageContract}) || !slices.Equal(d.Models, []string{"image:flux2-klein-4b"}) ||
		!slices.Equal(d.Resources, []string{"card:0", "profile:image"}) || d.Endpoint != pytorchName {
		t.Fatalf("declaration %+v", d)
	}
	for _, absent := range []string{argVideoKind, argSource, argVideoModel, argTorchPython, argEngine, argResource} {
		if slices.Contains(d.Arguments, absent) {
			t.Fatalf("an image set alone declared %s: %v", absent, d.Arguments)
		}
	}
	if i := slices.Index(d.Arguments, argState); i < 0 || d.Arguments[i+1] != torchState {
		t.Fatalf("the PyTorch provider's state is not %s: %v", torchState, d.Arguments)
	}

	// The real PyTorch provider takes the declared line as the runtime would
	// start it, with the endpoint token filled in; its state stays in the
	// folder this test named.
	pytorchd := buildPytorchd(t)
	args := slices.Clone(d.Arguments)
	for i, a := range args {
		if a == providerEndpointToken {
			args[i] = fmt.Sprintf("oa-models-host-image-%d", os.Getpid())
		}
	}
	serve := exec.Command(pytorchd, args...)
	stdout, err := serve.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	serve.Stderr = &stderr
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- serve.Wait() }()
	t.Cleanup(func() {
		// Only the process this test started, by its own handle.
		//unchecked: the process may already have exited
		_ = serve.Process.Kill()
		<-exited
	})
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	select {
	case line, ok := <-lines:
		if !ok || !strings.Contains(line, "pytorchd serving flux2-klein-4b (image@1)") {
			t.Fatalf("pytorchd serve %q said %q; stderr %s", args, line, stderr.String())
		}
	case err := <-exited:
		t.Fatalf("pytorchd serve %q exited: %v\n%s", args, err, stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatalf("pytorchd serve %q did not start within 60 s\n%s", args, stderr.String())
	}
}

// The card is one value: --card N writes --resource card:N on each serve
// line and the card:N resource, and a hand-made --resource card:N reads back
// as that card.
func TestModelsHostCardIsOneValue(t *testing.T) {
	settings := hostSettings{program: filepath.Join(t.TempDir(), "modelhostd"), resource: "leases", card: "1",
		media: hostMedia{videoModel: "fastwan2.2-ti2v-5b", videoKind: videoKindSdCpp, videoEngine: `C:\sd\sd-server.exe`}}
	d := modelHostDeclaration(settings, nil, nil)
	if i := slices.Index(d.Arguments, argHold); i < 0 || d.Arguments[i+1] != "card:1" || !slices.Contains(d.Resources, "card:1") || slices.Contains(d.Resources, "card:0") {
		t.Fatalf("--card 1 declared %v with resources %v", d.Arguments, d.Resources)
	}
	torch := hostSettings{resource: "leases", card: "1", media: hostMedia{imageModel: "flux2-klein-4b", torchPython: `C:\venv\python.exe`}}.withTorchDefaults()
	p := pytorchDeclaration(torch, nil)
	if i := slices.Index(p.Arguments, argHold); i < 0 || p.Arguments[i+1] != "card:1" || !slices.Contains(p.Resources, "card:1") || slices.Contains(p.Resources, "card:0") {
		t.Fatalf("--card 1 declared the PyTorch provider %v with resources %v", p.Arguments, p.Resources)
	}
	for _, args := range [][]string{
		{"serve", "--resource", "card:1"}, {"serve", "--resource=card:1"}, {"serve", "-resource", "card:1"}, {"serve", "-resource=card:1"},
	} {
		read, _ := readHostDeclaration(wire.DeclarationList{Declarations: []wire.DeclarationState{
			{Declaration: wire.Declaration{Name: modelHostName, Arguments: args, Resources: []string{"card:0"}}},
			{Declaration: wire.Declaration{Name: pytorchName, Arguments: args, Resources: []string{"card:0"}}}}})
		if read.card != "1" || slices.Contains(read.passthrough, "card:1") || read.torch.card != "1" || slices.Contains(read.torch.passthrough, "card:1") {
			t.Fatalf("%v read back card %q and %q, passthrough %v and %v", args, read.card, read.torch.card, read.passthrough, read.torch.passthrough)
		}
	}
}

// The one serve-line reader takes every Go flag spelling of the flags this
// command writes, the last winning, stops where Go's flag package stops, at
// "--", and passes the rest through as written.
func TestTheServeLineReaderTakesEveryFlagSpelling(t *testing.T) {
	settings, models := readServeArguments([]string{"serve", "-engine", "a", "--engine=b", "-model=x=hf/x", "--model", "y=hf/y",
		"--embeddings=false", "-video-ready=30m", "--video-memory-limit", "4096", "--verbose", "--video-engine-arg", "--temporal-tiling",
		"--", "--engine", "c", "--model", "z=hf/z"})
	if settings.engine != "b" || len(models) != 2 || settings.embed || !slices.Equal(settings.media.engineArgs, []string{"--temporal-tiling"}) {
		t.Fatalf("read %+v %v", settings, models)
	}
	if want := []string{"-video-ready=30m", "--video-memory-limit", "4096", "--verbose", "--", "--engine", "c", "--model", "z=hf/z"}; !slices.Equal(settings.passthrough, want) {
		t.Fatalf("passthrough %v, want %v", settings.passthrough, want)
	}
	if on, _ := readServeArguments([]string{"serve", "-embeddings"}); !on.embed {
		t.Fatal("-embeddings read off")
	}
	torch, media := readPytorchArguments([]string{"serve", "-python=p", "--image-model", "flux2-klein-4b", "--wait", "5m", "--", "--python", "q"})
	if media.torchPython != "p" || media.imageModel != "flux2-klein-4b" || !slices.Equal(torch.passthrough, []string{"--wait", "5m", "--", "--python", "q"}) {
		t.Fatalf("the PyTorch provider's line read %+v %+v", torch, media)
	}
}

// A registry that changed between the read and the first write answers
// conflict before anything was withdrawn; declareHost says so as a
// hostConflict, which the command answers by reading again once. A
// refused restore names the withdrawn declaration's serve line.
func TestModelsHostConflictBeforeAnyWriteIsRetryable(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	w := newWaiting(30 * time.Second)
	defer w.stop()
	call, done := w.call()
	registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		t.Fatal(err)
	}
	call, done = w.call()
	stale, err := registry.Declarations(call)
	done()
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "modelhostd.exe")
	other := wire.Declaration{Name: "other-provider", Program: identity.CanonicalProgramPath(program), Arguments: []string{"serve"},
		Endpoint: "other-provider", Transport: wire.DeclarationTransportNative, Contracts: []string{inference.ImageContract},
		Resources: []string{"profile:image"}, Models: []string{"image:other"}, Activation: wire.ActivationAttach, Role: wire.DeclarationRoleProvider}
	call, done = w.call()
	change, err := registry.Declare(call, stale.Revision, other)
	done()
	if err != nil || change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("another declaration %+v %v", change, err)
	}
	settings := hostSettings{program: program, endpoint: "modelhost", resource: "leases",
		media: hostMedia{imageModel: "flux2-klein-4b", torchPython: program}, torch: torchSettings{program: program}}
	err = declareHost(w, registry, stale, settings, nil, "models host", &bytes.Buffer{}, false, "serving")
	var conflict *hostConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("a write at a stale revision: %v", err)
	}
	if said := restoreHost(w, registry, stale.Revision, &other); !strings.Contains(said, "it read: "+other.Program+" serve") {
		t.Fatalf("a refused restore said %q", said)
	}
}

// The PyTorch provider's declaration takes the model host's lease endpoint
// rule: none unless the person names one, and one an earlier models host
// derived or took from the runtime is dropped (D8).
func TestThePytorchDeclarationNamesALeaseEndpointOnlyWhenNamed(t *testing.T) {
	derived := legacyLeaseEndpoint("oa-runtime")
	for _, c := range []struct{ declared, named, want string }{
		{"", "", ""},
		{derived, "", ""},
		{legacyLeaseEndpoint(""), "", ""},
		{"runtime-leases", "", ""},
		{"elsewhere", "", "elsewhere"},
		{derived, "named", "named"},
	} {
		if got := leaseEndpointFor(c.declared, c.named, "oa-runtime", "runtime-leases"); got != c.want {
			t.Fatalf("declared %q named %q: %q, want %q", c.declared, c.named, got, c.want)
		}
	}
	settings := hostSettings{media: hostMedia{imageModel: "flux2-klein-4b", torchPython: filepath.Join(t.TempDir(), "python.exe")}}.withTorchDefaults()
	if args := pytorchArguments(settings, nil); slices.Contains(args, argResource) {
		t.Fatalf("no lease endpoint was named and the serve line names one: %q", args)
	}
	// A state folder named for the model host holds the PyTorch provider's
	// own inside it; one the PyTorch declaration already names stays.
	state := filepath.Join(t.TempDir(), "isolated")
	if got := (hostSettings{state: state}).withTorchDefaults().torch.state; got != filepath.Join(state, pytorchName) {
		t.Fatalf("the PyTorch state under %s is %q", state, got)
	}
	if got := (hostSettings{state: state, torch: torchSettings{state: "kept"}}).withTorchDefaults().torch.state; got != "kept" {
		t.Fatalf("a declared PyTorch state was replaced: %q", got)
	}
}

// When the PyTorch provider's declaration is written and the model host's is
// then refused, the command says which is written and to run it again.
func TestModelsHostSaysToRunAgainWhenTheSecondDeclarationFails(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	w := newWaiting(30 * time.Second)
	defer w.stop()
	call, done := w.call()
	registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		t.Fatal(err)
	}
	call, done = w.call()
	list, err := registry.Declarations(call)
	done()
	if err != nil {
		t.Fatal(err)
	}
	// The registry refuses a declaration of the program asking for it
	// (program:self): this test executable, as the model host's program.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	settings := hostSettings{program: self, endpoint: "modelhost", engine: "llama-server",
		media: hostMedia{imageModel: "flux2-klein-4b", torchPython: filepath.Join(t.TempDir(), "python.exe")},
		torch: torchSettings{program: filepath.Join(t.TempDir(), "pytorchd.exe")}}
	err = declareHost(w, registry, list, settings, []hostedModel{{Name: "tiny", Store: "fake", Object: "tiny"}}, "models host", &bytes.Buffer{}, false, "hosting")
	if err == nil || !strings.Contains(err.Error(), "the PyTorch provider's declaration is written and the model host's is not") ||
		!strings.Contains(err.Error(), "run `openabstractions models host` again") {
		t.Fatalf("a refused second declaration: %v", err)
	}
	if host, torch := declarationsNamed(t, w, registry); host != nil || torch == nil {
		t.Fatalf("after the refusal: model host %+v, PyTorch provider %+v", host, torch)
	}
}
