package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
)

// Program titles: the one rule for naming a program in any sentence a person
// reads, in the Panel and in the native prompt. OpenAbstractions' own
// programs read by their product names, a registered app by its registered
// title, and any other program by its file stem and "(not registered)",
// never its file name. Which program is which is decided by
// identity.SameSubjectProgram, so a short alias or another spelling of the
// same file reads the same. A programTitler reads the registry once and
// titles every program of one request or one prompt.

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

// oaOwnPrograms lists OpenAbstractions' own programs: this Panel, and the
// runtime programs installed beside it (openabstractions and
// openabstractionsw) or named by -runtime-program.
func oaOwnPrograms() []oaProgramTitle {
	var own []oaProgramTitle
	exe, err := os.Executable()
	if err == nil && exe != "" {
		own = append(own, oaProgramTitle{exe, oaPanelTitle})
		suffix := ""
		if runtime.GOOS == "windows" {
			suffix = ".exe"
		}
		for _, name := range []string{"openabstractions", "openabstractionsw"} {
			own = append(own, oaProgramTitle{filepath.Join(filepath.Dir(exe), name+suffix), oaProductTitle})
		}
	}
	if panelExplicit != nil && panelExplicit.Program != "" {
		own = append(own, oaProgramTitle{panelExplicit.Program, oaProductTitle})
	}
	return own
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
	known      map[string]string
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
	return loadProgramTitler(ctx, apps, err)
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
	return loadProgramTitler(ctx, apps, nil)
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
		if identity.SameSubjectProgram(o.program, program) {
			title = o.title
			break
		}
	}
	if title == "" {
		for _, entry := range t.registered {
			if entry.Scope == fwire.ScopeLocal && identity.SameSubjectProgram(entry.Descriptor.Program, program) {
				title = appEntryTitle(entry)
				break
			}
		}
	}
	if t.known != nil {
		t.known[program] = title
	}
	return title, title != ""
}

// name is a program's title, else unregisteredProgramName.
func (t *programTitler) name(program string) string {
	if title, ok := t.title(program); ok {
		return title
	}
	return unregisteredProgramName(program)
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
