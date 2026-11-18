package product

import (
	_ "embed"
	"encoding/json"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
)

// The release's component table (A-SEC6, architect ruling 2026-11-22 (1)):
// each OpenAbstractions component id, the program file it ships and the
// provider names that program serves. Product standing needs both the
// program's own standing (Shipped) and a name this table lists for that
// program, so a shipped program declared under a name it does not ship has
// none. Who declared a provider grants nothing.

//go:embed component_providers.json
var componentProvidersJSON []byte

// ProviderComponent is one row of the component table.
type ProviderComponent struct {
	ID      string   `json:"id"`
	Program string   `json:"program"`
	Names   []string `json:"names"`
	// InstanceNames are prefixes a name completes with "-" and lowercase
	// hexadecimal digits, as Setup names one provider per chosen folder.
	InstanceNames []string `json:"instance_names,omitempty"`
	// Declarers lists, per provider name, the components whose own
	// declaration of that name has product standing (declarer amendment).
	Declarers map[string][]string `json:"declarers,omitempty"`
}

// declarerProgram is one of OA's other programs a declaration's recorded
// declarer may resolve to.
type declarerProgram struct {
	ID           string `json:"id"`
	Program      string `json:"program"`
	OriginalName string `json:"original_name,omitempty"`
}

var providerComponents, declarerPrograms = func() ([]ProviderComponent, []declarerProgram) {
	var table struct {
		Components       []ProviderComponent `json:"components"`
		DeclarerPrograms []declarerProgram   `json:"declarer_programs"`
	}
	if err := json.Unmarshal(componentProvidersJSON, &table); err != nil {
		panic("product: component table: " + err.Error())
	}
	return table.Components, table.DeclarerPrograms
}()

// The components that act for whichever program runs them and never
// declare in their own name (declarer amendment): the command line and the
// MCP gateway.
var neverDeclarers = []string{"oa:cli", "oa:mcp"}

// ProviderComponents returns a copy of the component table.
func ProviderComponents() []ProviderComponent {
	out := make([]ProviderComponent, len(providerComponents))
	for i, c := range providerComponents {
		out[i] = ProviderComponent{ID: c.ID, Program: c.Program, Names: slices.Clone(c.Names), InstanceNames: slices.Clone(c.InstanceNames), Declarers: maps.Clone(c.Declarers)}
	}
	return out
}

// programStem is a program path's file name without a Windows ".exe",
// folded to lower case on Windows, where file names ignore case.
func programStem(program string) string {
	base := filepath.Base(filepath.Clean(program))
	if runtime.GOOS == "windows" {
		base = strings.ToLower(base)
		base = strings.TrimSuffix(base, ".exe")
	}
	return base
}

func instanceOf(name, prefix string) bool {
	rest, ok := strings.CutPrefix(name, prefix+"-")
	return ok && rest != "" && strings.Trim(rest, "0123456789abcdef") == ""
}

// ShipsProvider reports whether the table lists name for the component
// whose program file program names. It judges the file name only; Shipped
// judges where the file is and who signed it.
func ShipsProvider(program, name string) bool {
	if program == "" || name == "" || !filepath.IsAbs(program) {
		return false
	}
	stem := programStem(program)
	for _, c := range providerComponents {
		if stem != c.Program {
			continue
		}
		if slices.Contains(c.Names, name) || slices.ContainsFunc(c.InstanceNames, func(prefix string) bool { return instanceOf(name, prefix) }) {
			return true
		}
	}
	return false
}

// ShippedProgram reports that a provider declaration names a program
// OpenAbstractions shipped under a name that program ships: its program
// passes Shipped and the table lists its name for that program
// (ShipsProvider). serve adds the declaration's source to read its standing.
func ShippedProgram(program, name string, protected, signed []string, sameSigner func(string) bool) bool {
	return ShipsProvider(program, name) && Shipped(program, protected, signed, sameSigner)
}

// SetupInventoryProvider reports a provider name Setup declares through
// inventoryd itself, outside any installation declaration: OA's picture
// store and a chosen chat model folder. inventoryd starts no program, loads
// no code and runs no command its arguments name; they select only its
// endpoint, configuration file and home folder. Product standing for such a
// declaration therefore needs no installation declaration to repeat.
func SetupInventoryProvider(program, name string) bool {
	return programStem(program) == "inventoryd" && (name == "oa-setup-picture" || instanceOf(name, "oa-setup-chat"))
}

// stemIs reports whether a program's file name, without ".exe", is name,
// ignoring case on Windows.
func stemIs(program, name string) bool {
	if runtime.GOOS == "windows" {
		return programStem(program) == strings.ToLower(name)
	}
	return programStem(program) == name
}

// DeclarerComponent resolves the program the runtime recorded as a
// declaration's declarer to OA's component id, or "" (declarer amendment,
// ruling C point 4). The program needs product standing (Shipped). In a
// protected folder, which only an administrator writes, its file name names
// the component. Elsewhere the file must also carry the component's signed
// version resource: originalName reads the OriginalFilename Windows verified,
// so a copy of another OA program renamed to a declarer's file name resolves
// to nothing. A component with no version resource resolves only from a
// protected folder.
func DeclarerComponent(program string, protected, signed []string, sameSigner func(string) bool, originalName func(string) string) string {
	if !Shipped(program, protected, signed, sameSigner) {
		return ""
	}
	inProtected := slices.ContainsFunc(protected, func(folder string) bool { return InFolder(program, folder) })
	for _, d := range declarerPrograms {
		if !stemIs(program, d.Program) {
			continue
		}
		if inProtected || d.OriginalName != "" && originalName != nil && strings.EqualFold(originalName(program), d.OriginalName) {
			return d.ID
		}
		return ""
	}
	for _, c := range providerComponents {
		if stemIs(program, c.Program) && inProtected {
			return c.ID
		}
	}
	return ""
}

// ListsDeclarer reports whether the component table lists component as a
// declarer of name for the component whose program file program names. The
// command line and the MCP gateway are never declarers.
func ListsDeclarer(program, name, component string) bool {
	if component == "" || slices.Contains(neverDeclarers, component) {
		return false
	}
	for _, c := range providerComponents {
		if stemIs(program, c.Program) && slices.Contains(c.Declarers[name], component) {
			return true
		}
	}
	return false
}

// VerifiedOriginalName is the OriginalFilename of a program's version
// resource from a file Windows verifies as trusted, "" for any other file
// and on every other platform. Each answer is kept for ten minutes under the
// file's path and identity.
func VerifiedOriginalName(program string) string {
	if runtime.GOOS != "windows" || program == "" {
		return ""
	}
	key := signerCacheKey(program)
	now := time.Now()
	originalMu.Lock()
	known, ok := originalNames[key]
	originalMu.Unlock()
	if ok && now.Sub(known.at) < signerAge {
		return known.key
	}
	name := ""
	if code, _, _, err := identity.VerifyFilePublisher(program, nil); err == nil && code.Trusted {
		name = code.OriginalName
	}
	originalMu.Lock()
	originalNames[key] = signerAnswer{at: now, key: name}
	originalMu.Unlock()
	return name
}

var (
	originalMu    sync.Mutex
	originalNames = map[string]signerAnswer{}
)
