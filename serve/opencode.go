// opencode.go writes OpenCode's own configuration from the router's
// inventory, so nobody types a model name into opencode.json by hand.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/client"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
)

const opencodeUsage = `Usage: openabstractions opencode <command>

Commands:
  configure   write OpenCode's provider block from the router's servable
              chat models, so nobody types a model name into OpenCode

configure [--config PATH] [--provider-id ID] [--scope local|remote] [--dry-run]
  --config PATH      OpenCode's global config file
                      (default: ~/.config/opencode/opencode.json)
  --provider-id ID   the provider key configure writes (default: openabstractions)
  --scope local|remote  the options.scope and request guarantee configure
                      writes: local-only or hosted-allowed (default: local)
  --dry-run           print the provider block instead of writing it

configure reads the router's servable chat families through the runtime and
writes or updates only the provider.<id> block, and a "model" key when the
config names no default, leaving every other key untouched. It refuses a
.jsonc config by name: OpenCode reads comments from it that this command
cannot preserve, so hand-edit that file instead. Running it again writes the
identical file unchanged. A machine with no servable chat family writes
nothing and says why.

Every command accepts --endpoint, --timeout and --json. The endpoint is --endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the installed runtime.

Exit codes: 0 written or unchanged, 1 runtime not resolved or transport
failure, 2 usage, 3 typed refusal (forbidden, jsonc_unsupported), 4
unavailable, 6 unknown.
`

// Defaults configure writes when the caller names none of its own.
const (
	openCodeDefaultProviderID = "openabstractions"
	openCodeDefaultScope      = "local"
	openCodeNPM               = "@openabstractions/opencode"
	openCodeDisplayName       = "OpenAbstractions"
	// openCodeDefaultContext is the context-window limit configure writes
	// for a model whose host reports none: router.thrift's Alias carries an
	// optional context_length, filled from the host's own metadata (LM
	// Studio's max_context_length, Ollama's model_info context_length,
	// Lemonade's max_context_window), and this default applies only when
	// that field is absent or zero.
	openCodeDefaultContext = 32768
	openCodeDefaultOutput  = 4096
	// routerCodeForbidden is router.thrift's own error_codes word for a
	// caller the router's policy does not permit; the service host sends it
	// when the caller lacks abstraction.router/inventory.read.
	routerCodeForbidden = "forbidden"
)

var openCodeProviderIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// openCodeLimit is OpenCode's models.<id>.limit shape.
type openCodeLimit struct {
	Context int64 `json:"context"`
	Output  int64 `json:"output"`
}

// openCodeModel is one entry of OpenCode's provider.<id>.models map: the
// family carries the id, the alias supplies name.
type openCodeModel struct {
	Name     string        `json:"name"`
	Limit    openCodeLimit `json:"limit"`
	ToolCall bool          `json:"tool_call"`
}

// openCodeOptions is OpenCode's provider.<id>.options for this development
// provider, matching adopters/opencode/README.md's "Configure a local model".
type openCodeOptions struct {
	Scope      string   `json:"scope"`
	Guarantees []string `json:"guarantees"`
}

// openCodeProvider is OpenCode's provider.<id> object.
type openCodeProvider struct {
	Name    string                   `json:"name"`
	NPM     string                   `json:"npm"`
	Options openCodeOptions          `json:"options"`
	Models  map[string]openCodeModel `json:"models"`
}

func opencodeCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, opencodeUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "opencode: a command is required", "openabstractions opencode --help")
	}
	if args[0] != "configure" {
		return commandMistake(diagnostics, fmt.Sprintf("opencode: no command called %q", args[0]), "openabstractions opencode --help")
	}
	return opencodeConfigure(args[1:], output, diagnostics)
}

func opencodeConfigure(args []string, output, diagnostics io.Writer) error {
	command := "opencode configure"
	if containsHelp(args) {
		_, err := io.WriteString(output, opencodeUsage)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options serviceOptions
	options.bind(flags)
	configPath := flags.String("config", "", "OpenCode global config file")
	providerID := flags.String("provider-id", openCodeDefaultProviderID, "the provider key configure writes")
	scope := flags.String("scope", openCodeDefaultScope, "local or remote")
	dryRun := flags.Bool("dry-run", false, "print the provider block instead of writing it")
	positional, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, command, opencodeUsage, args, err)
	}
	if len(positional) != 0 {
		return flagMistake(diagnostics, command, opencodeUsage, "takes no arguments")
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, opencodeUsage, "--timeout must not be negative")
	}
	if !openCodeProviderIDPattern.MatchString(*providerID) {
		return flagMistake(diagnostics, command, opencodeUsage, "--provider-id must be a lowercase identifier starting with a letter")
	}
	guarantee, err := openCodeGuarantee(*scope)
	if err != nil {
		return flagMistake(diagnostics, command, opencodeUsage, err.Error())
	}
	path := *configPath
	if path == "" {
		path, err = defaultOpenCodeConfigPath()
		if err != nil {
			return flagMistake(diagnostics, command, opencodeUsage, err.Error())
		}
	}
	if isJSONC(path) {
		return &exitError{exitRefusedCall, fmt.Errorf("%s: refuses %s: this command cannot preserve JSONC comments; keep an opencode.json, or add the provider block to it by hand", command, path)}
	}

	w := newWaiting(options.budget)
	defer w.stop()
	machine, source := options.machine(diagnostics, command)
	call, done := w.call()
	router, err := machine.ResolveRouter(call, client.Requirements{Scope: client.ScopeLocal})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	call, done = w.call()
	snapshot, err := router.ModelsContext(call, false)
	done()
	if err != nil {
		var svcErr *routerwire.ServiceError
		if errors.As(err, &svcErr) {
			return openCodeRouterRefusal(command, svcErr)
		}
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}

	models := openCodeServableChatModels(snapshot.Models)
	return openCodeApply(output, diagnostics, opencodeApplication{
		command: command, path: path, providerID: *providerID, scope: *scope, guarantee: guarantee, dryRun: *dryRun, models: models,
	})
}

// opencodeApplication is everything openCodeApply needs once the router's
// servable chat models are known, kept apart from CLI flag parsing and the
// router call so it can be exercised directly with synthetic models.
type opencodeApplication struct {
	command, path, providerID, scope, guarantee string
	dryRun                                      bool
	models                                      map[string]openCodeModel
}

// openCodeApply prints why when there is nothing to write, prints the
// provider block for --dry-run, or merges it into the config file at path
// and reports what changed. It never opens the file for --dry-run and never
// writes a file byte-identical to what is already there.
func openCodeApply(output, diagnostics io.Writer, app opencodeApplication) error {
	if len(app.models) == 0 {
		_, err := fmt.Fprintf(output, "%s: no servable chat model on this machine; add a model server (openabstractions inference server add) and grant this program abstraction.router/inventory.read and abstraction.inference/complete, then retry\n", app.command)
		return err
	}
	families := make([]string, 0, len(app.models))
	for family := range app.models {
		families = append(families, family)
	}
	slices.Sort(families)

	provider := openCodeProvider{Name: openCodeDisplayName, NPM: openCodeNPM, Options: openCodeOptions{Scope: app.scope, Guarantees: []string{app.guarantee}}, Models: app.models}

	if app.dryRun {
		block, err := json.MarshalIndent(map[string]map[string]openCodeProvider{"provider": {app.providerID: provider}}, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "%s\n", block)
		return err
	}

	existing, err := os.ReadFile(app.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return flagMistake(diagnostics, app.command, opencodeUsage, fmt.Sprintf("read %s: %v", app.path, err))
	}
	defaultModel := app.providerID + "/" + families[0]
	merged, err := openCodeMergeConfig(existing, app.providerID, provider, defaultModel)
	if err != nil {
		return flagMistake(diagnostics, app.command, opencodeUsage, fmt.Sprintf("%s: %v", app.path, err))
	}
	if bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(merged)) {
		_, err := fmt.Fprintf(output, "%s: %s already carries this provider block; unchanged\n", app.command, app.path)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(app.path), 0o700); err != nil {
		return flagMistake(diagnostics, app.command, opencodeUsage, err.Error())
	}
	if err := os.WriteFile(app.path, merged, 0o600); err != nil {
		return flagMistake(diagnostics, app.command, opencodeUsage, fmt.Sprintf("write %s: %v", app.path, err))
	}
	_, err = fmt.Fprintf(output, "%s: wrote provider %q with %d model(s) to %s\n", app.command, app.providerID, len(app.models), app.path)
	return err
}

// openCodeGuarantee is the request guarantee configure's chosen scope writes,
// matching adopters/opencode/README.md's local and hosted examples.
func openCodeGuarantee(scope string) (string, error) {
	switch scope {
	case "local":
		return "abstraction.inference/local-only@1", nil
	case "remote":
		return "abstraction.inference/hosted-allowed@1", nil
	default:
		return "", fmt.Errorf("--scope must be local or remote, not %q", scope)
	}
}

// defaultOpenCodeConfigPath is OpenCode's own documented global config path
// (https://opencode.ai/docs/config/), joined against the caller's own home
// directory on every platform.
func defaultOpenCodeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json"), nil
}

func isJSONC(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".jsonc")
}

// openCodeRouterRefusal turns a router ServiceError into this command's exit
// and, for the missing-right case, a message naming the Panel and the exact
// right to grant.
func openCodeRouterRefusal(command string, svcErr *routerwire.ServiceError) error {
	reason := svcErr.Message
	if string(svcErr.Code) == routerCodeForbidden {
		reason = "this program lacks abstraction.router/inventory.read on abstraction.router/inventory; grant it in the OpenAbstractions Panel, or run openabstractions rights grant, then retry"
	}
	return refusal(command, string(svcErr.Code), reason)
}

// openCodeServableChatModels chooses one servable chat-serving alias per
// family, the family's own names sorted so the choice is deterministic, and
// keys the result by family so a caller can build OpenCode's models map and
// its default "model" value directly. A family with no servable chat alias
// is left out, so an empty result is exactly "no servable chat family".
func openCodeServableChatModels(families []routerwire.Family) map[string]openCodeModel {
	models := map[string]openCodeModel{}
	for _, family := range families {
		names := slices.Clone(family.Names)
		slices.SortFunc(names, func(a, b routerwire.Alias) int { return strings.Compare(a.Name, b.Name) })
		for _, alias := range names {
			if !alias.Servable || !openCodeServesChat(alias.Profiles) {
				continue
			}
			context := int64(openCodeDefaultContext)
			if alias.ContextLength > 0 {
				context = alias.ContextLength
			}
			models[family.Family] = openCodeModel{
				Name:     alias.Name,
				Limit:    openCodeLimit{Context: context, Output: openCodeDefaultOutput},
				ToolCall: true,
			}
			break
		}
	}
	return models
}

// openCodeServesChat is true when a host's own reported profiles for an
// alias name chat, or when it reports none: router.thrift documents that an
// empty Alias.Profiles defers to the host's own default profiles, and every
// host kind's own default (defaultProfiles, runtime_inference_operator.go)
// includes chat.
func openCodeServesChat(profiles []string) bool {
	return len(profiles) == 0 || slices.Contains(profiles, "chat")
}

// openCodeMergeConfig applies the provider block, and a default "model" key
// when the document names none, to an existing OpenCode config document,
// leaving every other key untouched. An empty or absent file starts from {}.
func openCodeMergeConfig(existing []byte, providerID string, provider openCodeProvider, defaultModel string) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	trimmed := bytes.TrimSpace(existing)
	if len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &doc); err != nil {
			return nil, fmt.Errorf("not a JSON object: %w", err)
		}
	}
	providerBlock, err := json.Marshal(provider)
	if err != nil {
		return nil, err
	}
	providers := map[string]json.RawMessage{}
	if raw, ok := doc["provider"]; ok {
		if err := json.Unmarshal(raw, &providers); err != nil {
			return nil, fmt.Errorf("the existing \"provider\" key is not a JSON object: %w", err)
		}
	}
	providers[providerID] = providerBlock
	if doc["provider"], err = json.Marshal(providers); err != nil {
		return nil, err
	}
	if _, has := doc["model"]; !has {
		if doc["model"], err = json.Marshal(defaultModel); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return nil, err
	}
	//unchecked: bytes.Buffer.WriteByte never returns a non-nil error
	pretty.WriteByte('\n')
	return pretty.Bytes(), nil
}
