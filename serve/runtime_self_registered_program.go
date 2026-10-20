package main

import (
	"strings"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
)

// This private routing handle cannot be an application facet name. It selects
// the existing PROGRAM custody record; it creates no application projection.
const selfRegisteredProgramPrefix = "program:"

func selfRegisteredProgramName(key string) string { return selfRegisteredProgramPrefix + key }

func (k *selfRegisteredQuestions) selfName(name string) string {
	if strings.HasPrefix(name, selfRegisteredProgramPrefix) {
		return name
	}
	if key, ok := k.d.programSelfName(name); ok {
		return selfRegisteredProgramName(key)
	}
	return name
}

func (k *selfRegisteredQuestions) selfEntry(name string) (wire.ApplicationDescriptor, selfMark, bool) {
	name = k.selfName(name)
	key, program := strings.CutPrefix(name, selfRegisteredProgramPrefix)
	if !program {
		return k.d.selfEntry(name)
	}
	record, source, mark, ok := k.d.programSelfEntry(key)
	if !ok {
		return wire.ApplicationDescriptor{}, selfMark{}, false
	}
	// Both display values come from held registration observations: the
	// product name, else the title of an application the program carries,
	// registered or still pending in this registration's own manifest.
	// selfRegisteredTitle reads the program's file stem when neither exists.
	title := record.ProductName
	for _, application := range record.Applications {
		if title != "" {
			break
		}
		title = k.d.applicationTitle(application.Name)
	}
	for _, facet := range record.Spec.Applications {
		if title != "" {
			break
		}
		if !client.ReservedApplicationTitle(facet.Manifest.Title) {
			title = facet.Manifest.Title
		}
	}
	return wire.ApplicationDescriptor{Name: source.Program, Program: source.Program, Title: title}, mark, true
}

func (k *selfRegisteredQuestions) selfEntries() map[string]selfMark {
	entries := k.d.selfEntries()
	for name := range entries {
		if k.selfName(name) != name {
			delete(entries, name)
		}
	}
	for key, mark := range k.d.programSelfEntries() {
		entries[selfRegisteredProgramName(key)] = mark
	}
	return entries
}

func (k *selfRegisteredQuestions) blockRegisterSelf(name, program string) error {
	if strings.HasPrefix(name, selfRegisteredProgramPrefix) {
		return k.a.rights.blockRegisterSelfBecause(program, "removed by the person from OpenAbstractions")
	}
	return k.a.rights.blockRegisterSelf(program)
}

func (k *selfRegisteredQuestions) setQuestion(name, program, question, revision string) bool {
	if key, ok := strings.CutPrefix(name, selfRegisteredProgramPrefix); ok {
		return k.d.setProgramQuestion(k.ctx, key, program, question, revision)
	}
	return k.d.setQuestionForRevision(k.ctx, name, program, question, revision)
}

func (k *selfRegisteredQuestions) keepOwned(name, program, question string) bool {
	if key, ok := strings.CutPrefix(name, selfRegisteredProgramPrefix); ok {
		return k.d.keepProgramOwned(k.ctx, key, program, question)
	}
	return k.d.keepOwned(k.ctx, name, program, question)
}

func (k *selfRegisteredQuestions) removeOwned(name, program, question string, block func() error) error {
	if key, ok := strings.CutPrefix(name, selfRegisteredProgramPrefix); ok {
		return k.d.removeProgramSelfRegistered(k.ctx, key, program, question, block)
	}
	return k.d.removeSelfRegistered(k.ctx, name, program, question, block)
}
