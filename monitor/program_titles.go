package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// Program titles: the one rule for naming a program in any sentence a person
// reads, in the Panel and in the native prompt. OpenAbstractions' own
// programs read by their product names, a registered app by its registered
// title, a registered provider's program by its provider title
// (providerTitles), and any other program by its file stem and "(not registered)",
// never its file name. Which program is which is decided by
// identity.SameSubjectProgram, so a short alias or another spelling of the
// same file reads the same. Whether a program is one of OpenAbstractions'
// own, which drops the prompt's Publisher line, is decided by name
// (product.SameProgram): a short alias still counts, and a path through a
// junction never does, since its maker may point the junction at
// OpenAbstractions' folder after starting another program through it. A
// programTitler reads the registry once and titles every program of one
// request or one prompt.

// oaProgramTitle is one of OpenAbstractions' own programs and its name.
type oaProgramTitle struct {
	program, title string
}

// The product names. A registered app may not take one (reservedTitle).
const (
	oaProductTitle = "OpenAbstractions"
	oaPanelTitle   = "OpenAbstractions Panel"
)

// ownProgramTitles is oaOwnPrograms; a test whose asking program is the
// test binary itself replaces it, since that binary stands in for the Panel.
var ownProgramTitles = oaOwnPrograms

// oaOwnPrograms lists OpenAbstractions' own programs: this Panel, the
// runtime programs and MCP launcher installed beside it (openabstractions,
// openabstractionsw, openabstractions-mcp) or named by -runtime-program, and
// the same programs of a second installation (architect ruling 2026-11-09,
// second block). Each one beside this Panel or in an install folder counts
// by product standing (product.Shipped): the per-machine install folder,
// which only an administrator writes, vouches by path; this Panel's own
// folder and the per-user install folder, which any user-level program can
// write, vouch only for a file Windows verifies with this Panel's own signer
// (oaSameSigner). A program dropped there never takes the product title, the
// prompt's missing Publisher line or the OpenAbstractions prompt level.
func oaOwnPrograms() []oaProgramTitle {
	var own []oaProgramTitle
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	exeDir := ""
	if exe != "" {
		exeDir = filepath.Clean(filepath.Dir(exe))
		// The presenter program is OpenAbstractions itself; the Panel
		// keeps its own title.
		self := oaPanelTitle
		if presenterBuild {
			self = oaProductTitle
		}
		own = append(own, oaProgramTitle{exe, self})
	}
	protected, signed := oaStandingFolders(exeDir)
	add := func(program, title string) {
		if product.Shipped(program, protected, signed, oaSameSigner) {
			own = append(own, oaProgramTitle{program, title})
		}
	}
	var seen []string
	for _, dir := range append([]string{exeDir}, append(slices.Clone(protected), oaUserInstallFolder())...) {
		if dir == "" || slices.ContainsFunc(seen, func(s string) bool { return product.SameFolder(s, dir) }) {
			continue
		}
		seen = append(seen, dir)
		// This program's own file is listed above under whatever name it has.
		if dir != exeDir || presenterBuild {
			add(filepath.Join(dir, oaPanelFileName+suffix), oaPanelTitle)
		}
		if dir != exeDir || !presenterBuild {
			add(filepath.Join(dir, presenterFileStem+suffix), oaProductTitle)
		}
		for _, name := range oaRuntimeProgramNames {
			add(filepath.Join(dir, name+suffix), oaProductTitle)
		}
	}
	if panelExplicit != nil && panelExplicit.Program != "" {
		own = append(own, oaProgramTitle{panelExplicit.Program, oaProductTitle})
	}
	return own
}

// oaStandingFolders is product standing's folders for this Panel: the
// per-machine install folders vouch by path, and this Panel's own folder and
// the per-user install folder vouch for same-signer files.
func oaStandingFolders(exeDir string) (protected, signed []string) {
	return oaInstallFolders(), []string{exeDir, oaUserInstallFolder()}
}

// oaRuntimeProgramNames are the runtime programs and the MCP launcher an
// installation puts beside the Panel; oaPanelFileName is the Panel's own.
var oaRuntimeProgramNames = []string{"openabstractions", "openabstractionsw", "openabstractions-mcp"}

const oaPanelFileName = "Abstraction Panel"

// oaInstallFolders, oaUserInstallFolder and oaSameSigner are product
// standing's inputs (product.InstallFolders, product.UserInstallFolder and
// product.SameSigner). A test replaces them.
var (
	oaInstallFolders    = product.InstallFolders
	oaUserInstallFolder = product.UserInstallFolder
	oaSameSigner        = product.SameSigner
)

// programCacheKey keys a cached answer about a program file by its path and
// its file identity (identity.ProgramKey), so a file replaced at the same
// path misses the cache and is verified again.
func programCacheKey(program string) string {
	return program + "\x00" + identity.ProgramKey(program)
}

// agentConnectors are OpenAbstractions' own agent connectors, titled by the
// agent each one connects (nativecli owns the list). Setup installs them
// under its private agent-integrations folder and registers them under these
// names.
var agentConnectors = func() []struct{ name, title string } {
	var connectors []struct{ name, title string }
	for _, base := range nativecli.AgentApplicationBases() {
		title, _ := nativecli.AgentTitle(base)
		connectors = append(connectors, struct{ name, title string }{base, title})
	}
	return connectors
}()

// agentConnectorTitle titles a connector by a registered name a rule names
// without a program: the exact name, or the Darwin name with its "-<hex
// digest>" suffix. A program's file is titled by agentConnectorImage.
func agentConnectorTitle(name string) (string, bool) {
	for _, c := range agentConnectors {
		if name == c.name {
			return c.title, true
		}
		if digest, ok := strings.CutPrefix(name, c.name+"-"); ok && (len(digest) == 32 || len(digest) == 64) && strings.Trim(digest, "0123456789abcdef") == "" {
			return c.title, true
		}
	}
	return "", false
}

// registeredConnectorTitle is the agent a registration names when Setup made
// it: a local entry its own program did not register (SelfRegistered), whose
// program is a connector image in Setup's folder (agentConnectorImage) and
// whose name is that image's routing name. A program that registered itself
// under a connector name reads by its own title and "(added itself)".
func registeredConnectorTitle(entry fwire.ApplicationEntry) (string, bool) {
	if entry.SelfRegistered || entry.Scope != fwire.ScopeLocal {
		return "", false
	}
	application, title, ok := agentConnectorImage(entry.Descriptor.Program)
	if !ok || application != entry.Descriptor.Name {
		return "", false
	}
	return title, true
}

// agentConnectorRoot is Setup's folder of installed connector images; a test
// replaces it.
var agentConnectorRoot = func() string {
	home, err := setup.CurrentHome()
	if err != nil || home.Isolated {
		return ""
	}
	return filepath.Join(home.Config, "agent-integrations")
}

// agentConnectorImage is a connector image's routing name and agent title:
// its file name passes the identity owner's rule (nativecli.AgentApplicationName:
// the exact "oa-<agent>-integration.exe" on Windows, the exact name or one
// with a validated 16-byte digest on Darwin, nothing elsewhere) and the
// program is the file of that name in Setup's folder.
func agentConnectorImage(program string) (application, title string, ok bool) {
	leaf := programFileName(program)
	application = nativecli.AgentApplicationName(leaf)
	if application == "" {
		return "", "", false
	}
	title, ok = agentConnectorTitle(nativecli.AgentApplicationBase(application))
	root := agentConnectorRoot()
	if !ok || root == "" || !product.SameProgram(filepath.Join(root, leaf), program) {
		return "", "", false
	}
	return application, title, true
}

// agentConnectorProgramTitle titles an installed connector image by its file
// alone: the image under the exact connector name in Setup's folder, signed
// by this Panel's own signer (oaSameSigner, oaOwnPrograms' rule for the
// per-user install folder; each image is a copy of OA's signed gateway).
// Setup's folder is the person's own to write, and any program may copy
// itself there under a free connector name or a digest name; such a file
// reads as its agent only through Setup's registration of it
// (registeredConnectorTitle).
func agentConnectorProgramTitle(program string) (string, bool) {
	application, title, ok := agentConnectorImage(program)
	if !ok || nativecli.AgentApplicationBase(application) != application || !oaSameSigner(program) {
		return "", false
	}
	return title, true
}

// oaOwnProgram reports one of OpenAbstractions' own programs: the Panel, the
// runtime, or an agent connector by its installed image or registration. An
// own program shows no Publisher line.
func (t *programTitler) oaOwnProgram(program string) bool {
	if program == "" {
		return false
	}
	own := ownProgramTitles()
	if t != nil {
		own = t.own
	}
	for _, o := range own {
		if product.SameProgram(o.program, program) {
			return true
		}
	}
	if _, ok := agentConnectorProgramTitle(program); ok {
		return true
	}
	if t != nil {
		for _, entry := range t.registered {
			if _, ok := registeredConnectorTitle(entry); ok && product.SameProgram(entry.Descriptor.Program, program) {
				return true
			}
		}
	}
	return false
}

// programFileName is the last element of a program path recorded by either
// convention: a record names a Windows program whether this build runs on
// Windows or, in its cross-platform tests, on Linux, and a plain
// filepath.Base splits only on the host's own separator.
func programFileName(program string) string {
	if idx := strings.LastIndexAny(program, "/\\"); idx >= 0 && idx+1 < len(program) {
		return program[idx+1:]
	}
	return program
}

// programStem is a program's file name without its extension.
func programStem(program string) string {
	name := programFileName(program)
	if i := strings.LastIndex(name, "."); i > 0 {
		name = name[:i]
	}
	return name
}

// unregisteredProgramName is how a program no title names reads.
func unregisteredProgramName(program string) string {
	return programStem(program) + " (not registered)"
}

// reservedTitle reports a title an app may not take; the Facade client owns
// the rule (client.ReservedApplicationTitle).
func reservedTitle(title string) bool { return client.ReservedApplicationTitle(title) }

// programTitler titles programs against OpenAbstractions' own programs and
// one read of the registry. The zero value and nil know only the own
// programs.
type programTitler struct {
	own        []oaProgramTitle
	registered []fwire.ApplicationEntry
	providers  []providerState
	known      map[string]string
	chatNames  map[string]string
}

// providerTitle is one provider's title and what it does in a few words
// (architect ruling 2026-11-09): the providers OpenAbstractions ships read
// by their product names, an engine by its upstream project ("llama.cpp",
// "Diffusers"; second block), and any other provider by its registered name
// in words, never the raw name.
type providerTitle struct{ title, does string }

var providerTitles = map[string]providerTitle{
	"local-stores":     {"Model folders", "Finds models other apps downloaded"},
	"localai":          {"LocalAI", "Models on this device"},
	"oa-setup-chat":    {"Chat model", "Runs the chat model Setup chose"},
	"oa-setup-picture": {"Picture model folder", "Finds the picture models in OpenAbstractions' own folder"},
}

// providerDisplay is a provider's title and caption words for its name. A
// product title belongs only to a shipped declaration
// (shippedProvider); any other reads by its name in words, whatever name it
// chose. Callers keep the title only when reservedTitle allows it.
func providerDisplay(name string, shipped bool) (title, does string) {
	name = withoutInstanceSuffix(name)
	const general = "Provides services to your apps"
	if shipped {
		if known, ok := providerTitles[name]; ok {
			return known.title, known.does
		}
		if title, caption, ok := product.EnginePresentation(name); ok {
			if caption == "" {
				caption = general
			}
			return title, caption
		}
	}
	words := strings.Join(strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' }), " ")
	if words == "" {
		return "Unnamed provider", general
	}
	return strings.ToUpper(words[:1]) + words[1:], general
}

// withoutInstanceSuffix drops a trailing lowercase hexadecimal instance ID
// or digest fragment, such as "-1828", from a provider name.
func withoutInstanceSuffix(name string) string {
	i := strings.LastIndex(name, "-")
	if i <= 0 || len(name)-i-1 < 4 || strings.Trim(name[i+1:], "0123456789abcdef") != "" {
		return name
	}
	return name[:i]
}

// shippedProvider reports a provider declaration OpenAbstractions shipped,
// which may read by its product title: the runtime's standing for it is
// product (A-SEC6, architect ruling 2026-11-22 (1)). The runtime computes it
// once, from the declared program's product standing and the component
// table's names for that program; the Panel reads only its own machine's
// runtime and derives nothing from declared_by or its own folder. An absent
// standing, from a runtime that does not send one, reads as third_party.
func shippedProvider(state providerState) bool {
	return state.Standing != nil && *state.Standing == fwire.ProductStandingProduct
}

// providerState is a provider declaration and the runtime's standing for it,
// which registry@2 Standings carries beside the published listing (A-SEC6).
// A runtime that sends no standing leaves Standing nil.
type providerState struct {
	fwire.DeclarationState
	Standing *fwire.ProductStanding
}

// withStandings joins declarations to the runtime's standings by name and
// program; a declaration the standings do not carry has none.
func withStandings(states []fwire.DeclarationState, standings []fwire.ProviderStanding) []providerState {
	out := make([]providerState, 0, len(states))
	for _, state := range states {
		entry := providerState{DeclarationState: state}
		for _, s := range standings {
			if s.Name == state.Declaration.Name && s.Program == state.Declaration.Program {
				standing := s.Standing
				entry.Standing = &standing
				break
			}
		}
		out = append(out, entry)
	}
	return out
}

// readProviderStandings reads the runtime's standing of every declaration. A
// failed read, or a runtime before Standings, gives none, and every
// declaration reads as third_party.
func readProviderStandings(ctx context.Context, machine *facade.Machine) []fwire.ProviderStanding {
	registry, err := machine.ResolveRegistryV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil
	}
	list, err := registry.Standings(ctx)
	if err != nil || list.Outcome != fwire.DeclarationListOutcomePage {
		return nil
	}
	return list.Standings
}

// readProviderDeclarations reads the registered provider declarations; a
// failed read names no provider.
func readProviderDeclarations(ctx context.Context) []providerState {
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	machine := panelMachine()
	registry, err := machine.ResolveRegistry(readCtx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil
	}
	list, err := registry.Declarations(readCtx)
	if err != nil {
		return nil
	}
	var out []fwire.DeclarationState
	for _, state := range list.Declarations {
		if state.Role == fwire.DeclarationRoleProvider && state.Declaration.Program != "" && state.Declaration.Name != "" {
			out = append(out, state)
		}
	}
	return withStandings(out, readProviderStandings(readCtx, machine))
}

// newProgramTitler titles against the given registry entries.
func newProgramTitler(registered []fwire.ApplicationEntry) *programTitler {
	return &programTitler{own: ownProgramTitles(), registered: registered, known: map[string]string{}}
}

// loadProgramTitler reads the registry once through apps; an unreadable
// registry leaves registered apps untitled.
func loadProgramTitler(ctx context.Context, apps setupApplications, err error) *programTitler {
	var registered []fwire.ApplicationEntry
	if err == nil && apps != nil {
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if page, err := apps.Observe(readCtx, "", 0); err == nil && (page.Outcome == fwire.ApplicationOutcomePage || page.Outcome == fwire.ApplicationOutcomeStale) {
			registered = page.Applications
		}
	}
	return newProgramTitler(registered)
}

// programTitler is this Panel's titler for one request.
func (p *servicePanel) programTitler(ctx context.Context) *programTitler {
	apps, err := p.setupApplicationsForPanel(ctx)
	titler := loadProgramTitler(ctx, apps, err)
	titler.providers = readProviderDeclarations(ctx)
	return titler
}

// machineProgramTitler is the native prompt's titler, through the runtime it
// presents questions for.
func machineProgramTitler(ctx context.Context) *programTitler {
	resolveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	apps, err := panelMachine().ResolveApplications(resolveCtx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return loadProgramTitler(ctx, nil, err)
	}
	titler := loadProgramTitler(ctx, apps, nil)
	titler.providers = readProviderDeclarations(ctx)
	return titler
}

// title is a program's title and whether one names it.
func (t *programTitler) title(program string) (string, bool) {
	if t == nil {
		t = &programTitler{own: ownProgramTitles()}
	}
	if program == "" {
		return "", false
	}
	if title, ok := t.known[program]; ok {
		return title, title != ""
	}
	title := ""
	for _, o := range t.own {
		if product.SameProgram(o.program, program) {
			title = o.title
			break
		}
	}
	if title == "" {
		title, _ = agentConnectorProgramTitle(program)
	}
	if title == "" {
		for _, entry := range t.registered {
			if entry.Scope == fwire.ScopeLocal && identity.SameSubjectProgram(entry.Descriptor.Program, program) {
				title = appEntryTitle(entry)
				if connector, ok := registeredConnectorTitle(entry); ok {
					title = connector
				}
				break
			}
		}
	}
	if title == "" {
		for _, state := range t.providers {
			if identity.SameSubjectProgram(state.Declaration.Program, program) {
				// A provider may not take a product name (reservedTitle).
				if candidate, _ := providerDisplay(state.Declaration.Name, shippedProvider(state)); !reservedTitle(candidate) {
					title = candidate
				}
				break
			}
		}
	}
	if t.known != nil {
		t.known[program] = title
	}
	return title, title != ""
}

// isOwn reports whether program is one of OpenAbstractions' own programs,
// which no prompt gives a Publisher line (architect ruling C, 2026-11-08).
func (t *programTitler) isOwn(program string) bool {
	own := ownProgramTitles()
	if t != nil && t.own != nil {
		own = t.own
	}
	for _, o := range own {
		if product.SameProgram(o.program, program) {
			return true
		}
	}
	return false
}

// applicationTitle is the display title of the application a rule names by
// name alone, with no program: its registered title, else a connector name
// read as its agent.
func (t *programTitler) applicationTitle(name string) (string, bool) {
	if title, ok := t.registeredApplicationTitle(name); ok || name == "" {
		return title, ok
	}
	return agentConnectorTitle(name)
}

// registeredApplicationTitle is the display title of the application
// registered on this computer under name, and whether one is registered. A
// registration under a connector name reads as its agent only when Setup
// made it (registeredConnectorTitle).
func (t *programTitler) registeredApplicationTitle(name string) (string, bool) {
	if t == nil || name == "" {
		return "", false
	}
	for _, entry := range t.registered {
		if entry.Scope == fwire.ScopeLocal && entry.Descriptor.Name == name {
			if connector, ok := registeredConnectorTitle(entry); ok {
				return connector, true
			}
			title := appEntryTitle(entry)
			return title, title != ""
		}
	}
	return "", false
}

// chatTitle uses only the typed Messages operator inventory. Presence and
// status never establish a bound inbox or a chat name.
func (t *programTitler) chatTitle(program, context string) string {
	if t != nil {
		for _, entry := range t.registered {
			if identity.SameSubjectProgram(entry.Descriptor.Program, program) {
				if label := t.chatNames["app:"+entry.Descriptor.Name+"\x00"+context]; label != "" {
					return label
				}
			}
		}
	}
	return "Unnamed chat"
}

// name is a program's title, else unregisteredProgramName.
func (t *programTitler) name(program string) string {
	if title, ok := t.title(program); ok {
		return title
	}
	return unregisteredProgramName(program)
}

// promptName is a program's name in a popup or notification: its title,
// else questionAskerName's file stem, with no "(not registered)".
func (t *programTitler) promptName(program string) string {
	return programDisplayName(trayPublisher{Names: t, Prompt: true}, program)
}

// deviceName is how the Device page names a program that holds a resource
// or keeps the device awake (architect ruling 2026-11-09, second block): its
// title, else its signed file description, else "" and the page reads its
// file stem in words. Registration is not the question there, so no "(not
// registered)" follows.
func (t *programTitler) deviceName(program string) string {
	if title, ok := t.title(program); ok {
		return title
	}
	// A process the counters report by bare name, without a path to verify,
	// is one of Windows' own; the common ones read as Task Manager names
	// them.
	if !strings.ContainsAny(program, `\/`) {
		return windowsProcessTitles[strings.TrimSuffix(strings.ToLower(program), ".exe")]
	}
	return signedDescription(program)
}

// windowsProcessTitles are the Windows processes a graphics card list
// commonly shows by bare name, with the descriptions Windows gives them.
var windowsProcessTitles = map[string]string{
	"dwm":         "Desktop Window Manager",
	"csrss":       "Client Server Runtime Process",
	"explorer":    "Windows Explorer",
	"fontdrvhost": "Usermode Font Driver Host",
	"winlogon":    "Windows Logon Application",
}

// signedDescription is the FileDescription of a program file whose
// signature Windows verifies (identity.VerifyFile), the name Task Manager
// and the UAC prompt show ("Microsoft Edge"); "" for an unsigned file, a
// file on another machine, a reserved title or any other platform. Each
// answer is kept for signedDescriptionAge under the file's path and identity
// (programCacheKey). A test replaces it.
var signedDescription = cachedSignedDescription

const signedDescriptionAge = 10 * time.Minute

type describedProgram struct {
	at          time.Time
	description string
}

var (
	signedDescriptionsMu sync.Mutex
	signedDescriptions   = map[string]describedProgram{}
)

// deviceDescription is a signed description as the Device page shows it. A
// format character (a bidirectional override such as U+202E) could make it
// read as another name and is dropped; a long one, one with a control
// character, or a reserved title names nothing.
func deviceDescription(description string) string {
	description = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, description))
	if len(description) > 80 || strings.IndexFunc(description, unicode.IsControl) >= 0 || reservedTitle(description) {
		return ""
	}
	return description
}

func cachedSignedDescription(program string) string {
	if runtime.GOOS != "windows" || program == "" || !filepath.IsAbs(program) || strings.HasPrefix(program, `\\`) || strings.HasPrefix(program, "//") {
		return ""
	}
	cacheKey := programCacheKey(program)
	signedDescriptionsMu.Lock()
	known, ok := signedDescriptions[cacheKey]
	signedDescriptionsMu.Unlock()
	if ok && time.Since(known.at) < signedDescriptionAge {
		return known.description
	}
	description := ""
	if code, _, err := identity.VerifyFile(program, nil); err == nil && code.Trusted {
		description = deviceDescription(code.Description)
	}
	signedDescriptionsMu.Lock()
	if len(signedDescriptions) >= 512 {
		clear(signedDescriptions)
	}
	signedDescriptions[cacheKey] = describedProgram{at: time.Now(), description: description}
	signedDescriptionsMu.Unlock()
	return description
}

// names is name for every given program, in one batch.
func (t *programTitler) names(programs []string) map[string]string {
	out := map[string]string{}
	for _, program := range programs {
		if program != "" {
			out[program] = t.name(program)
		}
	}
	return out
}

// programReasonName is how a recorded reason names a program: its title, else
// its file stem, never its file name. A reason is stored, so it carries no
// "(not registered)" that may stop being true.
func programReasonName(t *programTitler, program string) string {
	if title, ok := t.title(program); ok {
		return title
	}
	return programStem(program)
}

// programTitle is one program's name for a request that names only one.
func (p *servicePanel) programTitle(ctx context.Context, program string) string {
	return p.programTitler(ctx).name(program)
}
