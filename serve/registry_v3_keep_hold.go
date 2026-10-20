package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Keeping an OA integration lets its program hold the card and memory, as the
// runtime lets a provider hold the card it declares (owner ruling 2026-11-12,
// architect R35). An OA integration is identified by its image: its current
// image is OA's release pin for it, the connector SHA-256 the Panel's build
// carries from the release's integration manifest (adopter_assets.json
// connector_sha256). The runtime holds no release manifest; the Panel, the
// one operator ExpectConnect admits, names the pin, and the runtime records
// it only when the registering image hashes to it (connect-kept.json): on
// the person's Connect click (panel_connect, APPLICATIONS.md "Connect is the
// Keep") and on the installing operator's re-pin of an entry the person kept
// by any route (installer_update, matchInstallerUpdate). A registration's
// manifest declares nothing that counts here.

// keptHoldWhy is the provenance of the hold rules a kept integration gets.
const keptHoldWhy = "integration keep"

// keptHoldResources are the resources a kept integration holds: the card its
// host app's models load into and the system memory they spill to.
var keptHoldResources = []string{instrument.Card0, instrument.Memory}

// keptIntegrationImage reports that the application file f is OA's release
// pin for the app: a panel_connect or installer_update record for it, of the
// same program, whose pin is f's image (its unsigned SHA-256, or the file
// identity of the image that hashed to the pin). An image that differs from
// the recorded pin, such as one the person changed or an Undo restored, is
// no kept integration.
func keptIntegrationImage(f applicationV3ManifestFile, kept connectKept) bool {
	if kept.Decision != connectDecisionSource && kept.Decision != installerUpdateSource || f.Source.Program == "" || !identity.SameSubjectProgram(kept.Program, f.Source.Program) {
		return false
	}
	if f.Image != "" {
		return strings.EqualFold(f.Image, kept.SHA256)
	}
	return kept.FileKey != "" && kept.FileKey == f.Source.FileKey
}

// permitKeptHolds writes abstraction.resource/hold on card:0 and memory for a
// kept integration's program. An existing rule, a deny included, stays as it
// is.
func (r *runtimeRights) permitKeptHolds(program string) error {
	if r == nil || r.policy == nil {
		return errors.New("integration holds: rights unavailable")
	}
	var errs []error
	for _, resource := range keptHoldResources {
		errs = append(errs, r.permitRuleWhy(r.self(), program, resourceservice.ActionHold, resource, keptHoldWhy))
	}
	return errors.Join(errs...)
}

// retireKeptHolds removes the hold rules permitKeptHolds wrote for programs
// that are no longer a kept integration: the app was removed, its Keep was
// withdrawn, or its image is no longer OA's release pin. Only a permit this
// runtime wrote with keptHoldWhy goes; a person's own rule, a deny included,
// stays.
func (r *runtimeRights) retireKeptHolds(ctx context.Context, keep []string) error {
	if r == nil || r.policy == nil {
		return errors.New("integration holds: rights unavailable")
	}
	snapshot, err := r.policy.OperatorSnapshot()
	if err != nil {
		return err
	}
	var errs []error
	for _, rule := range snapshot.Rules {
		if !rule.Permit || rule.Action != resourceservice.ActionHold || !slices.Contains(keptHoldResources, rule.Resource) ||
			slices.ContainsFunc(keep, func(program string) bool { return identity.SameSubjectProgram(program, rule.Subject.Program) }) {
			continue
		}
		errs = append(errs, r.retireKeptHold(ctx, rule.Subject, rule.Resource))
	}
	return errors.Join(errs...)
}

func (r *runtimeRights) retireKeptHold(ctx context.Context, subject rwire.Subject, resource string) error {
	for attempt := 0; attempt < installationAttempts; attempt++ {
		current := r.policy.ReadRule(subject, resourceservice.ActionHold, resource)
		if current.Outcome != rwire.RuleReadOutcomeFound || current.Record == nil || !current.Record.Rule.Permit || current.Record.SetBy != r.self() || current.Record.Why != keptHoldWhy {
			return nil
		}
		edit, err := r.policy.ChangeRuleContext(ctx, current.Revision, subject, resourceservice.ActionHold, resource, rights.RuleEdit{By: r.self(), Exact: true}, ctx.Err)
		if err != nil {
			return err
		}
		switch edit.Outcome {
		case rwire.PolicyEditOutcomeApplied:
			return nil
		case rwire.PolicyEditOutcomeConflict:
			continue
		}
		return fmt.Errorf("integration holds: retire %s for %s: %s", resource, subject.Program, edit.Outcome)
	}
	return fmt.Errorf("integration holds: retire %s for %s: the policy kept changing", resource, subject.Program)
}

// keptIntegrations lists the program of each enabled registration with an
// application the person kept, an entry the app registered itself (source
// self) in state enabled, whose current image is OA's recorded release pin
// for it (keptIntegrationImage). An installer's re-pin keeps the person's
// Keep of the entry and starts a new registration generation, so the entry's
// own state, not the generation, is the Keep here. known is false when the
// list could not be read, so no hold is retired on a failed read.
func (d *applicationDirectory) keptIntegrations(ctx context.Context) (out []keptIntegration, known bool) {
	if d == nil || !d.lock(ctx) {
		return nil, false
	}
	defer d.unlock()
	if d.v3 == nil {
		return nil, false
	}
	connected := map[string]connectKept{}
	raw, err := cas.ReadLimitContext(ctx, filepath.Join(d.dir, connectKeptFile), connectKeptLimit)
	if errors.Is(err, os.ErrNotExist) || err == nil && raw == nil {
		return nil, true
	}
	if err != nil || json.Unmarshal(raw, &connected) != nil {
		return nil, false
	}
	err = d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		for _, key := range d.v3.programKeys() {
			record, ok := d.v3.programRecord(key)
			source, has := d.v3.programSources[key]
			approval := d.v3.programApprovals[key]
			if !ok || !has || source.Program == "" || approval.Pending || approval.Removing || record.State != registration.StateEnabled ||
				record.Verification == registration.VerificationChanged || record.Verification == registration.VerificationMissing {
				continue
			}
			for name, f := range d.v3.files {
				e, listed := d.v3.entries[name]
				kept, recorded := connected[name]
				if listed && recorded && e.State == "enabled" && e.Source == "self" && f.Source == source && keptIntegrationImage(f, kept) {
					out = append(out, keptIntegration{Name: name, Program: source.Program, Record: kept})
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, false
	}
	return out, true
}

// keptIntegration is one kept OA integration: its app name, its program and
// the connect-kept record that makes its image the release pin.
type keptIntegration struct {
	Name, Program string
	Record        connectKept
}

// writeKeptHolds writes the hold rules of every kept integration once per
// connect-kept record and retires those of a program that is no longer one.
// It runs after a Keep, after the Keep a Connect click records, after a
// Remove, and at start, which covers a Keep given before these rules
// existed, a write that failed and an image that changed while OA was
// stopped. A record whose holds were written is not written again, so the
// person's Remove of a hold row lasts; a new pin record, or the end of the
// Keep, starts over. Each failure is reported; the Keep stands.
func (d *applicationDirectory) writeKeptHolds(ctx context.Context) {
	if d == nil {
		return
	}
	if err := d.reconcileInstallerKeepReceipts(ctx); err != nil && d.connectReport != nil {
		d.connectReport(err)
	}
	if d.programHoldKeep == nil {
		return
	}
	kept, known := d.keptIntegrations(ctx)
	programs := make([]string, 0, len(kept))
	written := map[string]connectKept{}
	for _, k := range kept {
		programs = append(programs, k.Program)
		if k.Record.HoldsWritten {
			written[k.Name] = k.Record
			continue
		}
		if err := d.programHoldKeep(k.Program); err != nil {
			if d.programConfigReport != nil {
				d.programConfigReport(fmt.Errorf("registration: hold rules for kept integration %s: %w", k.Program, err))
			}
			continue
		}
		written[k.Name] = k.Record
	}
	if !known {
		return
	}
	if d.programHoldRetire != nil {
		if err := d.programHoldRetire(ctx, programs); err != nil && d.programConfigReport != nil {
			d.programConfigReport(fmt.Errorf("registration: retire hold rules of integrations no longer kept: %w", err))
		}
	}
	if err := d.markHoldsWritten(ctx, written); err != nil && d.programConfigReport != nil {
		d.programConfigReport(fmt.Errorf("registration: hold rules record: %w", err))
	}
}

// markHoldsWritten sets HoldsWritten on the records in written, each only
// while it is still the record the holds were written for, and clears it on
// every other record, whose Keep ended.
func (d *applicationDirectory) markHoldsWritten(ctx context.Context, written map[string]connectKept) error {
	if !d.lock(ctx) {
		return ctx.Err()
	}
	defer d.unlock()
	err := cas.ChangeLimitContext(ctx, filepath.Join(d.dir, connectKeptFile), connectKeptLimit, func(current []byte) ([]byte, error) {
		kept := map[string]connectKept{}
		if current == nil || json.Unmarshal(current, &kept) != nil {
			return current, nil
		}
		changed := false
		for name, record := range kept {
			was, ok := written[name]
			want := ok && was.Decision == record.Decision && was.At.Equal(record.At) && was.Program == record.Program && was.SHA256 == record.SHA256 && was.FileKey == record.FileKey
			if record.HoldsWritten != want {
				record.HoldsWritten = want
				kept[name] = record
				changed = true
			}
		}
		if !changed {
			return current, nil
		}
		return json.Marshal(kept)
	})
	if errors.Is(err, cas.ErrNoValue) {
		return nil
	}
	return err
}
