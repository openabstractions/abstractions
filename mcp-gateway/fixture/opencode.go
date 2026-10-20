// Shared adopter test support. No installed state or production entry point is owned here.
package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const OpenCodeProviderFixtureArg = "-oa-opencode-provider-fixture"

func OpenCodeProviderFixtureLifetime(args []string) (time.Duration, error) {
	if len(args) != 7 && len(args) != 8 {
		return 0, errors.New("fixture requires five arguments and optional lifetime seconds")
	}
	if len(args) == 7 {
		return 2 * time.Minute, nil
	}
	seconds, err := strconv.Atoi(args[7])
	if err != nil || seconds < 1 || seconds > 1500 {
		return 0, errors.New("fixture lifetime must be 1..1500 seconds")
	}
	return time.Duration(seconds) * time.Second, nil
}

func RunOpenCodeProviderFixtureArgs(args []string) int {
	lifetime, err := OpenCodeProviderFixtureLifetime(args)
	if err != nil {
		return 2
	}
	return runOpenCodeProviderFixture(args[2], args[3], args[4], args[5], args[6], lifetime)
}

type openCodeOperation struct {
	Mode  string `json:"mode"`
	Label string `json:"label"`
	Tool  string `json:"tool,omitempty"`
}

type openCodeFixtureChat struct {
	dir   string
	label string
}

func (f openCodeFixtureChat) Start(request iwire.Request) (iwire.Admission, error) {
	raw, _ := json.Marshal(request)
	if file, err := os.OpenFile(filepath.Join(f.dir, "requests.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = file.Write(append(raw, '\n'))
		_ = file.Close()
	}
	mode := "tool"
	if len(request.Tools) == 0 || f.label == "firststart" {
		mode = "final"
	}
	if f.label == "incapable" && len(request.Tools) > 0 {
		return iwire.Admission{Outcome: iwire.StartOutcomeUnsupportedFeature,
			Reason: "feature:abstraction.inference/tools@1", Host: f.label, Model: request.Model}, nil
	}
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.Kind == iwire.PartKindToolResult {
				mode = "final"
			}
			if part.Kind == iwire.PartKindText && strings.Contains(part.Text, "OA_CANCEL_PROBE") {
				mode = "cancel"
			}
		}
	}
	operation := "oc-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	toolName := ""
	if mode == "tool" && f.label == "question" {
		wanted := "oa_asks_answer"
		if raw, err := os.ReadFile(filepath.Join(f.dir, "question-tool.txt")); err == nil {
			wanted = string(raw)
		}
		for _, tool := range request.Tools {
			if tool.Name == wanted || strings.HasSuffix(tool.Name, "_"+wanted) {
				if toolName != "" {
					return iwire.Admission{}, errors.New("duplicate question tool")
				}
				toolName = tool.Name
			}
		}
		names := make([]string, 0, len(request.Tools))
		for _, tool := range request.Tools {
			names = append(names, tool.Name)
		}
		diagnostic, _ := json.Marshal(struct {
			Wanted, Selected string
			Tools            []string
		}{wanted, toolName, names})
		if err := os.WriteFile(filepath.Join(f.dir, "question-dispatch.json"), diagnostic, 0600); err != nil {
			return iwire.Admission{}, err
		}
		if toolName == "" {
			return iwire.Admission{}, errors.New("requested question tool unavailable")
		}
	}
	state, _ := json.Marshal(openCodeOperation{Mode: mode, Label: f.label, Tool: toolName})
	if err := os.WriteFile(filepath.Join(f.dir, operation+".json"), state, 0o600); err != nil {
		return iwire.Admission{}, err
	}
	return iwire.Admission{Outcome: iwire.StartOutcomeAccepted, Operation: operation, Host: f.label,
		Model: request.Model, RetentionMs: 60000, IdleMs: 10000, RetainedDeltas: 16}, nil
}

func (f openCodeFixtureChat) Observe(operation string, cursor, _, _, waitMS int64) (iwire.DeltaPage, error) {
	raw, err := os.ReadFile(filepath.Join(f.dir, operation+".json"))
	if err != nil {
		return iwire.DeltaPage{Outcome: iwire.PageOutcomeUnknown, Deltas: []iwire.Delta{}, Next: cursor}, nil
	}
	var state openCodeOperation
	if json.Unmarshal(raw, &state) != nil {
		return iwire.DeltaPage{}, errors.New("invalid fixture operation")
	}
	if cursor > 0 {
		if state.Mode == "cancel" {
			time.Sleep(time.Duration(min(waitMS, 50)) * time.Millisecond)
			return iwire.DeltaPage{Outcome: iwire.PageOutcomePage, Deltas: []iwire.Delta{}, Next: cursor}, nil
		}
		return iwire.DeltaPage{Outcome: iwire.PageOutcomePage, Deltas: []iwire.Delta{}, Next: cursor, AtEnd: true}, nil
	}
	if state.Mode == "cancel" {
		part := iwire.Part{Kind: iwire.PartKindText, Text: "waiting"}
		return iwire.DeltaPage{Outcome: iwire.PageOutcomePage,
			Deltas: []iwire.Delta{{Sequence: 0, Kind: iwire.DeltaKindPart, Index: 0, Part: &part}}, Next: 1}, nil
	}
	if state.Label == "unavailable" {
		part := iwire.Part{Kind: iwire.PartKindText, Text: "partial answer"}
		end := iwire.Reply{Outcome: iwire.ReplyOutcomeUnavailable, Reason: "upstream:error",
			Message: iwire.Message{Role: iwire.RoleAssistant, Parts: []iwire.Part{}}, StopReason: iwire.StopReasonNoStop,
			Usage: iwire.Usage{Input: 7, Output: 1}, Host: state.Label, Model: "fixture-model"}
		return iwire.DeltaPage{Outcome: iwire.PageOutcomePage, Deltas: []iwire.Delta{
			{Sequence: 0, Kind: iwire.DeltaKindPart, Index: 0, Part: &part},
			{Sequence: 1, Kind: iwire.DeltaKindEnd, End: &end},
		}, Next: 2, AtEnd: true}, nil
	}
	part := iwire.Part{Kind: iwire.PartKindText, Text: "native-oa-" + state.Label}
	stop := iwire.StopReasonEnd
	if state.Mode == "tool" {
		part = iwire.Part{Kind: iwire.PartKindToolCall, CallID: "oa-native-call", Name: "glob", Arguments: `{"pattern":"native-oa-never-match-*","path":"."}`}
		if state.Label == "image" {
			part = iwire.Part{Kind: iwire.PartKindToolCall, CallID: "oa-native-image-call", Name: "openabstractions_generate_image",
				Arguments: `{"model":"user.LocalApprovedKlein","prompt":"A watercolor fox reading a book under a tree","size":"512x512"}`}
			if args, err := os.ReadFile(filepath.Join(f.dir, "image-call.json")); err == nil {
				part.Arguments = string(args)
			} else if !os.IsNotExist(err) {
				return iwire.DeltaPage{}, err
			}
		} else if state.Label == "question" {
			args, err := os.ReadFile(filepath.Join(f.dir, "question-call.json"))
			if err != nil {
				return iwire.DeltaPage{}, err
			}
			part = iwire.Part{Kind: iwire.PartKindToolCall, CallID: "oa-native-question-call",
				Name: state.Tool, Arguments: string(args)}
		}
		stop = iwire.StopReasonToolCalls
	}
	end := iwire.Reply{Outcome: iwire.ReplyOutcomeCompleted, Message: iwire.Message{Role: iwire.RoleAssistant, Parts: []iwire.Part{}},
		StopReason: stop, Usage: iwire.Usage{Input: 7, Output: 3}, Host: state.Label, Model: "fixture-model"}
	return iwire.DeltaPage{Outcome: iwire.PageOutcomePage, Deltas: []iwire.Delta{
		{Sequence: 0, Kind: iwire.DeltaKindPart, Index: 0, Part: &part},
		{Sequence: 1, Kind: iwire.DeltaKindEnd, End: &end},
	}, Next: 2, AtEnd: true}, nil
}

func (f openCodeFixtureChat) Cancel(operation string) (iwire.Cancellation, error) {
	_ = os.WriteFile(filepath.Join(f.dir, "cancelled-"+operation), nil, 0o600)
	outcome := iwire.ReplyOutcomeCancelled
	return iwire.Cancellation{Outcome: iwire.CancelOutcomeCancelled, ReplyOutcome: &outcome}, nil
}

func runOpenCodeProviderFixture(endpoint, dir, label, pidDir, brokerProgram string, lifetime time.Duration) int {
	if err := os.WriteFile(filepath.Join(dir, "provider-endpoint"), []byte(endpoint), 0600); err != nil {
		return 3
	}
	if err := os.WriteFile(filepath.Join(pidDir, strconv.Itoa(os.Getpid())), nil, 0o600); err != nil {
		return 3
	}
	l, err := listen.Listen(listen.Endpoint(endpoint))
	if err != nil {
		return 4
	}
	time.AfterFunc(lifetime, func() { os.Exit(5) })
	for {
		conn, err := l.Accept()
		if err != nil {
			return 6
		}
		go func() {
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err != nil {
				return
			}
			peer, err := call.Peer()
			if err != nil {
				return
			}
			caller, err := identity.SubjectProgram(peer, listen.Program.Path)
			if err != nil || !identity.SameSubjectProgram(caller, brokerProgram) {
				return
			}
			if response, err := (&iwire.ChatDispatcher{Handler: openCodeFixtureChat{dir: dir, label: label}}).ExchangeFrame(call.Frame); err == nil {
				_ = call.Reply(response)
			}
		}()
	}
}

func CopyTree(t *testing.T, source, target string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != source && entry.Name() == "node_modules" && entry.IsDir() {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		return errors.Join(copyErr, closeErr)
	}); err != nil {
		t.Fatal(err)
	}
}

func FileURL(path string) string {
	path = filepath.ToSlash(path)
	if len(path) >= 2 && path[1] == ':' {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

type OpenCodeProviderSources struct {
	Provider  string
	Facade    string
	Inference string
	Ipc       string
	Router    string
	Asks      string
	Rights    string
	Storage   string
	Logging   string
	Download  string
	Job       string
}

func FindOpenCodeProviderSources(charterRoot string) (OpenCodeProviderSources, error) {
	var err error
	charterRoot, err = filepath.Abs(charterRoot)
	if err != nil {
		return OpenCodeProviderSources{}, fmt.Errorf("absolute charter root: %w", err)
	}
	sources := OpenCodeProviderSources{Provider: filepath.Join(charterRoot, "adopters", "opencode")}
	if _, err := os.Stat(filepath.Join(sources.Provider, "index.js")); err != nil {
		return OpenCodeProviderSources{}, fmt.Errorf("OpenCode provider source: %w", err)
	}

	// The private workspace keeps generated packages under openabstractions-flat.
	// A public workflow checks the layer repositories out beside the charter
	// repository, as the facade binding workflow does.
	layouts := [][10]string{
		{
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-facade", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-inference", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-identity", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-router", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-asks", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-rights", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-storage", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-logging", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-download", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-job", "javascript"),
		},
		{
			filepath.Join(filepath.Dir(charterRoot), "abstraction-facade", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-inference", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-identity", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-router", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-asks", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-rights", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-storage", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-logging", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-download", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-job", "javascript"),
		},
	}
	if info, err := os.Stat(filepath.Join(charterRoot, "openabstractions-flat", "abstraction-identity")); err == nil {
		if !info.IsDir() {
			return OpenCodeProviderSources{}, fmt.Errorf("private workspace marker is not a directory")
		}
		// The public charter projects a partial openabstractions-flat tree. A
		// private Identity root identifies the real private layout. A sibling
		// checkout must not conceal a deleted private package.
		layouts = layouts[:1]
	} else if !os.IsNotExist(err) {
		return OpenCodeProviderSources{}, err
	}
	for _, layout := range layouts {
		complete := true
		for _, source := range layout {
			if _, err := os.Stat(filepath.Join(source, "package.json")); err != nil {
				complete = false
				break
			}
		}
		if complete {
			sources.Facade, sources.Inference, sources.Ipc, sources.Router, sources.Asks, sources.Rights = layout[0], layout[1], layout[2], layout[3], layout[4], layout[5]
			sources.Storage, sources.Logging, sources.Download = layout[6], layout[7], layout[8]
			sources.Job = layout[9]
			return sources, nil
		}
	}
	return OpenCodeProviderSources{}, fmt.Errorf("generated JavaScript packages absent from private workspace or public sibling checkouts")
}

func StageOpenCodeProviderFrom(t *testing.T, charterRoot string) string {
	t.Helper()
	sources, err := FindOpenCodeProviderSources(charterRoot)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	provider := filepath.Join(root, "provider")
	CopyTree(t, sources.Provider, provider)
	modules := filepath.Join(provider, "node_modules", "@openabstractions")
	// The provider imports @ai-sdk/provider at load. Without an explicit
	// module directory, use the adopter's own installed dependencies
	// (npm ci in adopters/opencode); without either, the provider cannot load.
	external := os.Getenv("OA_AI_SDK_PROVIDER_MODULES")
	if external == "" {
		local, err := filepath.Abs(filepath.Join(sources.Provider, "node_modules"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(local, "@ai-sdk", "provider")); err != nil {
			t.Skip("OpenCode provider dependencies are not installed: run npm ci in adopters/opencode or set OA_AI_SDK_PROVIDER_MODULES")
		}
		external = local
	}
	{
		if !filepath.IsAbs(external) {
			t.Fatalf("OA_AI_SDK_PROVIDER_MODULES must be absolute: %s", external)
		}
		resolveModule := func(path string) string {
			for i := 0; i < 8; i++ {
				link, err := os.Readlink(path)
				if err != nil {
					return path
				}
				if filepath.IsAbs(link) {
					path = link
				} else {
					path = filepath.Join(filepath.Dir(path), link)
				}
			}
			t.Fatalf("external package link chain is too deep: %s", path)
			return ""
		}
		// Bun's installed node_modules can contain junctions. Copy only the
		// external runtime dependencies from their resolved package directories.
		sdk := resolveModule(filepath.Join(external, "@ai-sdk", "provider"))
		if err := os.MkdirAll(filepath.Join(provider, "node_modules", "@ai-sdk"), 0o755); err != nil {
			t.Fatal(err)
		}
		CopyTree(t, sdk, filepath.Join(provider, "node_modules", "@ai-sdk", "provider"))
		plugin := resolveModule(filepath.Join(external, "@opencode-ai", "plugin"))
		if _, err := os.Stat(plugin); err == nil {
			if err := os.MkdirAll(filepath.Join(provider, "node_modules", "@opencode-ai"), 0o755); err != nil {
				t.Fatal(err)
			}
			CopyTree(t, plugin, filepath.Join(provider, "node_modules", "@opencode-ai", "plugin"))
			// Bun nests zod under the plugin; npm hoists it beside the plugin.
			zod := resolveModule(filepath.Join(plugin, "node_modules", "zod"))
			if _, err := os.Stat(zod); err != nil {
				zod = resolveModule(filepath.Join(external, "zod"))
			}
			CopyTree(t, zod, filepath.Join(provider, "node_modules", "zod"))
		}
	}
	CopyTree(t, sources.Facade, filepath.Join(modules, "facade"))
	CopyTree(t, sources.Inference, filepath.Join(modules, "inference"))
	CopyTree(t, sources.Ipc, filepath.Join(modules, "ipc"))
	CopyTree(t, sources.Router, filepath.Join(modules, "router"))
	CopyTree(t, sources.Asks, filepath.Join(modules, "asks"))
	CopyTree(t, sources.Rights, filepath.Join(modules, "rights"))
	CopyTree(t, sources.Storage, filepath.Join(modules, "storage-content"))
	CopyTree(t, sources.Logging, filepath.Join(modules, "logging"))
	CopyTree(t, sources.Download, filepath.Join(modules, "download-request"))
	CopyTree(t, sources.Job, filepath.Join(modules, "job-acceptance"))
	return filepath.Join(provider, "index.js")
}

func IsolatedOpenCodeEnvironment(t *testing.T, root, addon, library string) []string {
	t.Helper()
	configRoot := filepath.Join(root, "xdg-config")
	cacheRoot := filepath.Join(root, "xdg-cache")
	overrides := map[string]string{
		"XDG_CONFIG_HOME": configRoot, "XDG_DATA_HOME": filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME": cacheRoot, "XDG_STATE_HOME": filepath.Join(root, "xdg-state"),
		"ABSTRACTION_IPC_LIBRARY": library, "NO_COLOR": "1",
	}
	if addon != "" {
		overrides["ABSTRACTION_IPC_NODE"] = addon
	}
	for key, path := range overrides {
		if key != "NO_COLOR" {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
		}
	}
	CopyPreparedOpenCodeBootstrap(t, configRoot, cacheRoot)
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[strings.ToUpper(key)]; !replaced {
			env = append(env, item)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

// CopyPreparedOpenCodeBootstrap copies optional, already verified caller-owned
// bootstrap inputs into this test's isolated XDG directories. It acquires
// nothing. The caller owns the binary/package provenance and input pins.
func CopyPreparedOpenCodeBootstrap(t *testing.T, configRoot, cacheRoot string) {
	t.Helper()
	for _, item := range []struct{ input, output string }{
		{os.Getenv("OA_OPENCODE_BOOTSTRAP_CONFIG_SOURCE"), filepath.Join(configRoot, "opencode")},
		{os.Getenv("OA_OPENCODE_BOOTSTRAP_CACHE_SOURCE"), filepath.Join(cacheRoot, "opencode")},
	} {
		if item.input != "" {
			if err := copyPreparedTree(item.input, item.output); err != nil {
				t.Fatalf("prepared OpenCode bootstrap: %v", err)
			}
		}
	}
}

func copyPreparedTree(source, target string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(target) {
		return errors.New("prepared bootstrap paths must be absolute")
	}
	root, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !root.IsDir() || root.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return errors.New("prepared bootstrap root must be an ordinary directory")
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return errors.New("prepared bootstrap contains a linked or irregular entry")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0700)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("prepared bootstrap entry is not regular: %s (%v)", path, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0600) | info.Mode().Perm()&0111
		return os.WriteFile(destination, raw, mode)
	})
}

// EnvironmentWithout removes inherited fixture overrides before a caller
// supplies its own isolated values.
func EnvironmentWithout(environment []string, names ...string) []string {
	omit := make(map[string]struct{}, len(names))
	for _, name := range names {
		omit[strings.ToUpper(name)] = struct{}{}
	}
	result := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if _, found := omit[strings.ToUpper(name)]; !found {
			result = append(result, item)
		}
	}
	return result
}
