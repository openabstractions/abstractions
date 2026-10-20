package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// programMissingReason is the reading of a local provider declaration whose
// program image is gone (architect ruling 2026-11-09, item 4): the router and
// the resolver skip it, and the declaration stays registered until the
// person or Setup withdraws it. A stale declaration from a deleted dev
// worktree otherwise kept its models routed to a program
// that cannot start.
const programMissingReason = "program:missing"

// programImageMissing reports whether a declared program path names no file.
// Only an absolute path is read; any error other than not-exist leaves the
// declaration as it was, so an unreadable folder does not withdraw a provider.
func programImageMissing(program string) bool {
	if program == "" || !filepath.IsAbs(program) {
		return false
	}
	_, err := os.Stat(program)
	return errors.Is(err, fs.ErrNotExist)
}

// recheckProgram reads whether the supervisor's local program image exists
// and records it. It reports whether the reading changed; the caller tells
// the watchers. A remote runtime, a host, and a declaration this platform
// cannot run (unavailableReason) are not read.
func (s *providerSupervisor) recheckProgram() bool {
	d := s.file.Declaration
	if d.remote() || d.host() || s.unavailableReason() != "" {
		return false
	}
	missing := programImageMissing(d.Program)
	s.mu.Lock()
	changed := s.missing != missing
	s.missing = missing
	s.mu.Unlock()
	if s.p != nil {
		s.p.logProgramPresence(d.Name, d.Program, missing)
	}
	return changed
}

// programMissing reports the last reading of recheckProgram.
func (s *providerSupervisor) programMissing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.missing
}

// recheckPrograms re-reads every supervised declaration's program image and
// tells the watchers once when any reading changed, so the router's host
// list and the registry listing follow a removed or restored program. It
// must be called without p.mu held.
func (p *runtimeProviders) recheckPrograms() {
	changed := false
	for _, s := range p.sorted() {
		if s.recheckProgram() {
			changed = true
		}
	}
	if changed {
		p.notify()
	}
}

// logProgramPresence writes one line when a declaration's program goes
// missing and one when it is back. The last reported state is kept per
// declaration name, so a replaced supervisor of the same declaration and
// repeated reconciles say nothing new.
func (p *runtimeProviders) logProgramPresence(name, program string, missing bool) {
	if missing {
		_, loaded := p.programLogged.LoadOrStore(name, true)
		if loaded && !p.programLogged.CompareAndSwap(name, false, true) {
			return
		}
		if p.report != nil {
			p.report(fmt.Errorf("provider %s: program %s is missing; the router skips it until the program is back or the declaration is withdrawn", name, program))
		}
		return
	}
	if p.programLogged.CompareAndSwap(name, true, false) && p.report != nil {
		p.report(fmt.Errorf("provider %s: program %s is back; the router offers it again", name, program))
	}
}
