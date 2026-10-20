package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
)

const catalogueCompanionLimit = 128 << 10

type catalogueEnrollmentCompanion struct {
	Version                        int
	App, Revision, Question, Image string
	Host                           catalogueHostRecord
}

func (c catalogueEnrollmentCompanion) matches(app string, m selfMark) bool {
	return c.Version == 1 && c.App == app && m.Kind == "agent" && m.Agent != nil && !m.Removing &&
		m.Agent.State == "enrolled" && m.Agent.AnsweredBy == "operator" &&
		c.Revision != "" && c.Revision == m.RegistrationRevision &&
		c.Question != "" && c.Question == m.Question &&
		c.Image != "" && c.Image == m.ProgramKey
}

func catalogueCompanionPath(dir string) string {
	return filepath.Join(dir, catalogueHostParty+"-catalogue-enrollment-v1.json")
}

type catalogueCompanionSlots struct {
	Version          int
	Current, Pending catalogueEnrollmentCompanion
}

func (c catalogueEnrollmentCompanion) valid() bool {
	return c.Version == 1 && c.App == catalogueAgentApplication && agentHex(c.Revision, 24) &&
		len(c.Question) <= 128 && c.Image != "" && len(c.Image) <= 1024 &&
		validCatalogueHosts(map[string]catalogueHostRecord{catalogueHostParty: c.Host})
}

func decodeCatalogueSlots(raw []byte) (catalogueCompanionSlots, error) {
	if raw == nil {
		return catalogueCompanionSlots{Version: 1}, nil
	}
	var slots catalogueCompanionSlots
	if registrationStrict(raw, &slots) != nil || slots.Version != 1 ||
		(slots.Current != (catalogueEnrollmentCompanion{}) && !slots.Current.valid()) ||
		(slots.Pending != (catalogueEnrollmentCompanion{}) && !slots.Pending.valid()) {
		return catalogueCompanionSlots{}, errors.New("applications: invalid catalogue enrollment companion")
	}
	return slots, nil
}

// The mark owner serializes transitions. Preserve the exact committed row
// while staging its successor. Once the mark CAS commits, Pending is readable
// and becomes Current on the next stage. No second commit is necessary.
func writeCatalogueCompanion(ctx context.Context, dir string, value catalogueEnrollmentCompanion, mark selfMark) error {
	if !value.valid() {
		return errors.New("applications: invalid catalogue enrollment companion")
	}
	return cas.ChangeLimitContext(ctx, catalogueCompanionPath(dir), catalogueCompanionLimit, func(raw []byte) ([]byte, error) {
		slots, err := decodeCatalogueSlots(raw)
		if err != nil {
			return nil, err
		}
		if slots.Pending.matches(value.App, mark) {
			slots.Current = slots.Pending
		}
		slots.Pending = value
		return json.Marshal(slots)
	})
}

func (c catalogueEnrollmentCompanion) samePending(app string, m selfMark) bool {
	return c.Version == 1 && c.App == app && m.Agent != nil && !m.Removing && m.Agent.State == "pending" &&
		c.Revision == m.RegistrationRevision && c.Image == m.ProgramKey &&
		(c.Question == "" || c.Question == m.Question)
}

func loadCatalogueCompanion(ctx context.Context, dir, app string, mark selfMark) (catalogueEnrollmentCompanion, error) {
	raw, err := cas.ReadLimitContext(ctx, catalogueCompanionPath(dir), catalogueCompanionLimit)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = nil, nil
	}
	if err != nil {
		return catalogueEnrollmentCompanion{}, err
	}
	slots, err := decodeCatalogueSlots(raw)
	if err != nil {
		return catalogueEnrollmentCompanion{}, err
	}
	if slots.Pending.matches(app, mark) || slots.Pending.samePending(app, mark) {
		return slots.Pending, nil
	}
	if slots.Current.matches(app, mark) {
		return slots.Current, nil
	}
	return catalogueEnrollmentCompanion{}, nil
}

func readCatalogueCompanion(ctx context.Context, dir, app string, mark selfMark) (catalogueHostRecord, error) {
	value, err := loadCatalogueCompanion(ctx, dir, app, mark)
	if err != nil || !value.matches(app, mark) {
		return catalogueHostRecord{}, err
	}
	return value.Host, nil
}

// Only the existing installed-tools owner can mint the source proof carried
// into an automatic catalogue-bound connector update.
func catalogueMaintenanceSource(update *applicationAgentUpdate, capture agentUpdateCapture) bool {
	return capture.Application == catalogueAgentApplication && update.Maintenance &&
		update.SourceRelease.provenFor(update.Source) && update.SourceRelease.Origin == agentReleaseOriginInstalled &&
		capture.Release.provenFor(capture.Staged) && agentReleaseSameContent(update.SourceRelease, capture.Release)
}
