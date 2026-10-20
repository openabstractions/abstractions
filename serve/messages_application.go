package main

import (
	"context"
	"errors"
	"path/filepath"
	"sort"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

var errMessageApplicationUnknown = errors.New("message application unregistered")
var errMessageApplicationUnavailable = errors.New("message application unavailable")
var errMessageApplicationAmbiguous = errors.New("message application ambiguous")

// Resolve only registered helper identity, never inboxes, presence or bodies.
// A person's agent label (nativecli.AgentReceiverName, the gateway's table)
// names its connector first; then an exact registration wins; family aliases
// require one current kept image.
// Registration without usable enrollment remains unavailable, never absent.
func (d *applicationDirectory) resolveMessageApplication(ctx context.Context, name string) (string, error) {
	return d.resolveMessageApplicationWith(ctx, name, nativecli.AgentApplicationBase, nativecli.AgentApplicationName)
}

func (d *applicationDirectory) resolveMessageApplicationWith(ctx context.Context, name string, family func(string) string, imageName func(string) string) (string, error) {
	name = nativecli.AgentReceiverName(name)
	if !d.lock(ctx) {
		return "", ctx.Err()
	}
	exact := false
	names := map[string]bool{}
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		for key := range legacy {
			names[key] = true
		}
		if d.v3 != nil {
			for key := range d.v3.entries {
				names[key] = true
			}
		}
		exact = names[name]
		return nil
	})
	d.unlock()
	if err != nil {
		return "", errMessageApplicationUnavailable
	}
	if exact {
		return name, nil
	}
	base := family(name)
	if base != name || base == "" {
		return "", errMessageApplicationUnknown
	}
	registered := false
	var choices []string
	ordered := make([]string, 0, len(names))
	for key := range names {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		if family(key) != base {
			continue
		}
		registered = true
		var image applicationAgentImage
		current := d.currentAgentEnrollmentImage(ctx, key, &image)
		if current.Outcome != applications.OutcomePage || current.Enrollment == nil || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled || imageName(filepath.Base(image.Program)) != key {
			continue
		}
		choices = append(choices, key)
	}
	if len(choices) == 1 {
		return choices[0], nil
	}
	if len(choices) > 1 {
		return "", errMessageApplicationAmbiguous
	}
	if registered {
		return "", errMessageApplicationUnavailable
	}
	return "", errMessageApplicationUnknown
}
