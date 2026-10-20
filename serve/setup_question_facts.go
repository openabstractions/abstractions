package main

import (
	"context"
	"errors"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
)

type setupFactsCheck func(context.Context, string, asks.SetupModelFact) error

func (q *setupRequests) validateFacts(ctx context.Context, d *applicationDirectory, v asks.SetupQuestionFacts) error {
	if _, err := asks.SetupFactSlots(v); err != nil {
		return err
	}
	if !d.lock(ctx) {
		return ctx.Err()
	}
	err := d.withRegistrationReadLocked(ctx, func(descriptors map[string]wire.ApplicationDescriptor) error {
		seen := map[string]bool{}
		for _, target := range v.Targets {
			current, ok := descriptors[target.Name]
			revision := "" // Published legacy directory records have no revision.
			if d.v3 != nil {
				if entry, exists := d.v3.entries[target.Name]; exists {
					ok = true
					current.Program = d.v3.files[target.Name].Source.Program
					revision = entry.Revision
				}
			}
			if seen[target.Name] || !identity.ValidSubjectProgram(target.Program) || ok && (!identity.SameSubjectProgram(current.Program, target.Program) || target.Revision != revision) || !ok && target.Revision != "" {
				return errors.New("setup: target changed")
			}
			seen[target.Name] = true
		}
		return nil
	})
	d.unlock()
	if err != nil {
		return err
	}
	check := q.factsCheck.Load()
	if check == nil {
		return errors.New("setup: model references unavailable")
	}
	if v.Code != nil {
		if err := (*check)(ctx, "code", v.Code.Ref); err != nil {
			return err
		}
	}
	if v.Picture != nil {
		if err := (*check)(ctx, "image", v.Picture.Ref); err != nil {
			return err
		}
	}
	if v.Video != nil {
		if err := setupVideoFactCheck(*v.Video); err != nil {
			return err
		}
	}
	return nil
}

// setupVideoFactCheck admits a video fact that names a reviewed video model
// set by its own name, title and use policy. The fact is display data: it
// grants nothing, and the answered work gets the pinned set itself.
func setupVideoFactCheck(v asks.SetupVideoFact) error {
	set, ok := modelhost.VideoModelSetNamed(v.Model)
	if !ok {
		return errors.New("setup: no reviewed video model set")
	}
	title, _, _ := strings.Cut(set.DisplayTitle(), " (")
	license := set.UsePolicy.Name
	if license == "" && len(set.Files) > 0 {
		license = set.Files[0].License
	}
	if v.Title != title || v.License != license || v.MemoryBytes != set.PeakBytes || v.DownloadBytes < set.Size() {
		return errors.New("setup: video model set changed")
	}
	return nil
}
