package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

type agentStartCLI interface {
	Program() string
	Check(context.Context) error
	Close() error
}

// The launch keeps the installed CLI's native authentication context. Refuse
// alternate credentials/providers rather than silently selecting another account.
type agentStartEnvironmentRefusal struct{ Names []string }

func (e *agentStartEnvironmentRefusal) Error() string {
	return "native_profile_environment_override:" + strings.Join(e.Names, ",")
}

func agentStartSubscriptionEnvironment(env []string) ([]string, error) {
	blocked := map[string]bool{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if value != "" {
			blocked[strings.ToUpper(name)] = true
		}
	}
	var names []string
	for _, name := range startprofile.ProviderSelectors() {
		if blocked[name] {
			names = append(names, name)
		}
	}
	if len(names) != 0 {
		return nil, &agentStartEnvironmentRefusal{Names: names}
	}
	return append([]string(nil), env...), nil
}

// Read through the capability's authenticated filtered snapshot. A descriptor
// filename or client-opened runtime directory never supplies registration facts.
func registeredAgentStartResolver(args []string) agentStartResolver {
	return registeredAgentStartResolverWithAdmission(args, nil)
}

// Exact cross-process runtime invocation admission is required in addition to
// registration. The current @2 shell launch supplies no such binding, so its
// production adapter remains closed. Native fixture callers supply their own
// explicit test admission; no argv, environment value or image grants it.
func registeredAgentStartResolverWithAdmission(args []string, admission func(context.Context) error, bindings ...startprofile.CustodyBinding) agentStartResolver {
	return registeredAgentStartResolverForTarget(args, admission, "", bindings...)
}

// target is the runtime's chosen receiver context from the owned-start
// channel: empty or the recipe's own context keeps OA's own session.
func registeredAgentStartResolverForTarget(args []string, admission func(context.Context) error, target string, bindings ...startprofile.CustodyBinding) agentStartResolver {
	return registeredAgentStartResolverForSelection(args, admission, target, nil, bindings...)
}

func registeredAgentStartResolverForSelection(args []string, admission func(context.Context) error, target string, cold *startprofile.ColdResume, bindings ...startprofile.CustodyBinding) agentStartResolver {
	var binding startprofile.CustodyBinding
	if len(bindings) == 1 {
		binding = bindings[0]
	}
	return registeredAgentStartResolverCustody(args, agentStartCustody{binding: binding, admission: admission, target: target, cold: cold})
}

// registeredAgentStartResolverForList resolves the registered recipe to list
// the agent's existing conversations into sessions; it starts nothing.
func registeredAgentStartResolverForList(args []string, admission func(context.Context) error, sessions *[]receiverSession, binding startprofile.CustodyBinding) agentStartResolver {
	return registeredAgentStartResolverCustody(args, agentStartCustody{binding: binding, admission: admission, list: sessions})
}

func registeredAgentStartResolverCustody(args []string, custody agentStartCustody) agentStartResolver {
	p, parseErr := startprofile.Parse(args)
	admission := custody.admission
	return func(ctx context.Context, profile, contextName string) (*agentStartRecipe, error) {
		if admission == nil || admission(ctx) != nil {
			return nil, errors.New("runtime_start_admission_unavailable")
		}
		if parseErr != nil || p.Name != profile || p.Context != contextName {
			return nil, errors.New("profile")
		}
		program, err := os.Executable()
		if err != nil {
			return nil, err
		}
		application := dedicatedAgentApplication(program)
		if application == "" {
			return nil, errors.New("dedicated_receiver_required")
		}
		machine := receiverStartMachine()
		directory, err := machine.ResolveApplications(ctx, client.Requirements{Scope: client.ScopeLocal})
		if err != nil {
			return nil, err
		}
		read := func(ctx context.Context) (wire.ApplicationDescriptor, error) {
			page, err := directory.Observe(ctx, "", 0)
			if err != nil || page.Outcome != wire.ApplicationOutcomePage {
				return wire.ApplicationDescriptor{}, errors.New("registration_unavailable")
			}
			for _, entry := range page.Applications {
				if entry.Descriptor.Name == application && !entry.SelfRegistered {
					return entry.Descriptor, nil
				}
			}
			return wire.ApplicationDescriptor{}, errors.New("registered_receiver_required")
		}
		return resolveRegisteredAgentStart(ctx, p, program, read, receiverStartCLISelector, custody)
	}
}

type agentStartCustody struct {
	binding   startprofile.CustodyBinding
	admission func(context.Context) error
	target    string
	cold      *startprofile.ColdResume
	// list, when set, receives the agent's existing conversations instead
	// of a start; nothing runs a turn.
	list *[]receiverSession
}

func agentStartAccount() (string, error) {
	self, err := identity.Self()
	if err != nil {
		return "", err
	}
	return self.Account()
}

func resolveRegisteredAgentStart(ctx context.Context, p startprofile.Profile, program string, read func(context.Context) (wire.ApplicationDescriptor, error), selectCLI func(context.Context, startprofile.Profile, func(context.Context, string) ([]byte, error)) (agentStartCLI, error), custody agentStartCustody) (*agentStartRecipe, error) {
	// Capture once before any vendor preflight. The same snapshot reaches every
	// probe and the eventual print-mode invocation even if the parent changes.
	environment, err := agentStartSubscriptionEnvironment(os.Environ())
	if err != nil {
		return nil, err
	}
	if !custody.binding.Valid() || custody.admission == nil {
		return nil, sessionRefusal("session_binding_unavailable")
	}
	if strings.HasPrefix(custody.target, "chat-") && custody.cold == nil {
		return nil, sessionRefusal("cold_selection_required")
	}
	if custody.cold != nil {
		if custody.target != custody.cold.Context {
			return nil, sessionRefusal("cold_selection_invalid")
		}
		switch p.Name {
		case "claude-v1":
			if _, err := parseClaudeColdBinding(custody.cold.Binding, custody.cold.CwdKey, custody.cold.Context, true); err != nil {
				return nil, err
			}
			if _, err := custody.cold.Launch(); err != nil {
				return nil, err
			}
			// Provider/credential overrides were refused above. Cold vendor probes
			// and execution share a fresh host-session-free snapshot.
			environment = coldClaudeResumeEnvironment(environment)
		case "codex-v1":
			// A retained Codex chat names its thread alone.
			if _, err := custody.cold.Thread(); err != nil {
				return nil, sessionRefusal("cold_selection_invalid")
			}
		default:
			return nil, sessionRefusal("cold_selection_invalid")
		}
	}
	admit := func(ctx context.Context) error {
		return custody.admission(ctx)
	}
	if err := admit(ctx); err != nil {
		return nil, err
	}
	d, err := read(ctx)
	if err != nil || d.Activation == nil || !reflect.DeepEqual(d.Activation.Arguments, p.Arguments()) {
		return nil, errors.New("recipe_changed")
	}
	self, _, err := setupIdentity(ctx, program, false)
	if err != nil {
		return nil, err
	}
	var selected agentStartCLI
	var configurationRef setupIdentityRef
	var held []setupIdentityRef
	held = append(held, &self)
	closeAll := func() error {
		var result error
		if selected != nil {
			result = selected.Close()
		}
		for _, h := range held {
			result = errors.Join(result, h.Close())
		}
		return result
	}
	fail := func(err error) (*agentStartRecipe, error) { return nil, errors.Join(err, closeAll()) }
	account, accountErr := agentStartAccount()
	if accountErr != nil || custody.binding.Account != account || custody.binding.ProgramKey != self.FileID().Key() || custody.binding.RecipeDigest != p.Fingerprint(d.Program) {
		return fail(sessionRefusal("session_binding_changed"))
	}
	registered, err := fileid.Resolve(ctx, fileid.Self, d.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !registered.Equal(self.FileID()) {
		return fail(errors.New("registered_image"))
	}
	for _, path := range []string{p.Folder, p.StateDir} {
		h, _, err := setupIdentity(ctx, path, true)
		if err != nil {
			return fail(err)
		}
		held = append(held, &h)
	}
	probe := func(ctx context.Context, path string) ([]byte, error) {
		if err := admit(ctx); err != nil {
			return nil, err
		}
		var output agentStartOutput
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		output.max = 64 << 10
		output.cancel = cancel
		err := runOwnedAgentStart(call, path, p.Folder, []string{"--version"}, environment, &output, p.MemoryBytes)
		return output.data, err
	}
	selected, err = selectCLI(ctx, p, probe)
	if err != nil || selected == nil {
		return fail(errors.New("verified_cli_unavailable"))
	}
	historical := func(ctx context.Context) error {
		if configurationRef != nil && !setupIdentitiesCurrent(ctx, []setupIdentityRef{configurationRef}) {
			return sessionRefusal("session_configuration_changed")
		}
		if !setupIdentitiesCurrent(ctx, held) || selected.Check(ctx) != nil {
			return errors.New("native_image_changed")
		}
		current, err := read(ctx)
		if err != nil || !reflect.DeepEqual(current, d) {
			return errors.New("recipe_changed")
		}
		return nil
	}
	check := func(ctx context.Context) error {
		if err := admit(ctx); err != nil {
			return err
		}
		return historical(ctx)
	}
	if err = check(ctx); err != nil {
		return fail(err)
	}
	if custody.list != nil {
		if custody.target != "" || custody.cold != nil {
			return fail(sessionRefusal("session_unknown"))
		}
		sessions, err := listAgentSessions(ctx, p, selected, environment)
		if err != nil {
			return fail(err)
		}
		*custody.list = sessions
		return fail(errReceiverSessionsListed)
	}
	// codex-v1 delivers only into a retained chat's own existing thread.
	if p.Name == "codex-v1" {
		if custody.cold != nil {
			return resolveCodexChatStart(ctx, p, selected, environment, custody, check, closeAll)
		}
		return fail(sessionRefusal("session_unknown"))
	}
	if p.Name != "claude-v1" {
		return fail(errors.New("supported_budget_profile_unavailable"))
	}
	// The official CLI reference omits less common flags from --help. The
	// native-held version establishes this documented 2.1 profile: restricted
	// since 2.1.248 and permission-prompts since 2.1.259. Unknown families refuse.
	versioned, ok := selected.(interface{ Version() string })
	if !ok || !claudeStartProfileVersion(versioned.Version()) {
		return fail(errors.New("restriction_unsupported"))
	}
	if !claudeSessionProfileVersion(versioned.Version()) {
		return fail(sessionRefusal("native_configuration_unavailable"))
	}
	// The documented status command is read-only and performs no login. Accept
	// the installed program's subscription sign-in, never an API-key backend.
	var auth agentStartOutput
	auth.max = 64 << 10
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	auth.cancel = cancel
	err = runOwnedAgentStart(call, selected.Program(), p.Folder, []string{"auth", "status"}, environment, &auth, p.MemoryBytes)
	cancel()
	var status struct {
		AuthMethod      string `json:"authMethod"`
		ConfigDirectory string `json:"configDirectory"`
	}
	_, authShapeErr := claudeConfigObject(auth.data)
	if err != nil || authShapeErr != nil || json.Unmarshal(auth.data, &status) != nil || (status.AuthMethod != "claude.ai" && status.AuthMethod != "oauth_token") {
		return fail(errors.New("subscription_signin_unavailable"))
	}
	if custody.cold != nil {
		return resolveSelectedColdClaudeStart(ctx, p, self.FileID().Path(), status.ConfigDirectory, selected, environment, custody, versioned.Version(), check, closeAll, &held)
	}
	if !filepath.IsAbs(status.ConfigDirectory) {
		return fail(sessionRefusal("native_configuration_unavailable"))
	}
	configuration, _, err := setupIdentity(ctx, status.ConfigDirectory, true)
	if err != nil {
		return fail(sessionRefusal("native_configuration_unavailable"))
	}
	held = append(held, &configuration)
	configurationRef = &configuration
	configurationKey := configuration.FileID().Key()
	inbox, sessionConfiguration := p.Context, configurationKey
	if custody.target != "" && custody.target != p.Context {
		// attach-* contexts are retired (architect ruling 1, AB-1s): a retained
		// chat arrives as a cold selection, never as an attach target.
		return fail(sessionRefusal("session_unknown"))
	}
	r := &agentStartRecipe{profile: p.Name, context: p.Context, folder: p.Folder, timeout: time.Duration(p.TimeoutMS) * time.Millisecond, outputBytes: int(p.OutputBytes), memoryBytes: p.MemoryBytes, approved: true, check: check, close: closeAll}
	r.execution = &agentStartExecution{}
	r.capture = submitAgentStartMetadata
	r.run = func(ctx context.Context, prompt string, out io.Writer, memory uint64) error {
		if check(ctx) != nil {
			return errors.New("recipe_changed")
		}
		config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"OpenAbstractions": map[string]any{"command": self.FileID().Path(), "args": []string{"--state-dir", p.StateDir, "--inbox-context", inbox, "--messages-only"}}}})
		if err != nil {
			return err
		}
		budget := fmtBudget(p.BudgetCents)
		session, err := beginAgentSession(ctx, p.StateDir, custody.binding, inbox, sessionConfiguration)
		if err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		// Per-run settings suppress ordinary customizations. Managed hooks and
		// force-enabled plugins retain the vendor's policy behavior.
		arguments := []string{"-p", prompt, "--permission-mode", "dontAsk", "--permission-prompts", "none", "--restricted", "--strict-mcp-config", "--mcp-config", string(config), "--max-budget-usd", budget, "--output-format", "json", "--tools", "", "--allowedTools", messagesReceiverAllowedTools(), "--model", p.Model, "--disable-slash-commands", "--settings", `{"disableAllHooks":true,"syncClaudeAiPlugins":false,"syncClaudeAiSkills":false}`}
		arguments = append(arguments, session.arguments()...)
		// The installed CLI owns subscription sign-in in its unchanged context.
		capture, ok := out.(*agentStartOutput)
		if !ok {
			return errors.New("capture_unavailable")
		}
		if err := runOwnedAgentStartStreams(ctx, selected.Program(), p.Folder, arguments, environment, out, agentStartStderr{capture}, memory, r.execution); err != nil {
			return err
		}
		if !r.execution.ExitKnown || r.execution.ExitCode != 0 {
			return sessionRefusal("session_result_unconfirmed")
		}
		if err := session.observe(capture.data); err != nil {
			return err
		}
		// Historical creation is independent of a question that may now be
		// answered. Future execution still requires fresh native admission.
		return session.commit(ctx, historical)
	}
	return r, nil
}

func claudeStartProfileVersion(version string) bool {
	return claudeProfileVersionAtLeast(version, 259)
}

func claudeSessionProfileVersion(version string) bool {
	return claudeProfileVersionAtLeast(version, 268)
}

// claudeProfileVersionAtLeast accepts Claude Code 2.1.<minimum> and every
// later release within major 2. A new major may change the documented flags
// and refuses until it is checked.
func claudeProfileVersionAtLeast(version string, minimum uint64) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 || parts[0] != "2" {
		return false
	}
	var numbers [2]uint64
	for i, part := range parts[1:] {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
		numbers[i] = n
	}
	return numbers[0] > 1 || numbers[0] == 1 && numbers[1] >= minimum
}

func fmtBudget(cents uint64) string {
	raw := strconv.FormatUint(cents/100, 10)
	return raw + "." + fmt.Sprintf("%02d", cents%100)
}

func messagesReceiverAllowedTools() string {
	names := gateway.MessagesReceiverToolNames()
	for i := range names {
		names[i] = "mcp__OpenAbstractions__" + names[i]
	}
	return strings.Join(names, ",")
}
