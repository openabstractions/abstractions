package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	logging "github.com/openabstractions/abstraction-logging/go"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
)

// gatewayCopyRows admits the locations emitted by the existing gateway receipt
// owner against the runtime roster before the pass can call any producer.
func gatewayCopyRows(copies []cacheproducer.Copy, authority ...func(cacheproducer.Copy) (func(context.Context) error, error)) ([]setupCopyRow, error) {
	rows := make([]setupCopyRow, 0, len(copies))
	for _, copy := range copies {
		var kind setupCopyKind
		switch copy.Kind {
		case cacheproducer.SourceCopy:
			kind = setupCopyGatewaySource
		case cacheproducer.VendorCopy:
			kind = setupCopyGatewayCache
		default:
			return nil, fmt.Errorf("setup copies: unlisted gateway placement %q at %q", copy.Kind, copy.Location)
		}
		if _, listed := kind.owner(copy.Owner); !listed {
			return nil, fmt.Errorf("setup copies: unlisted producer %q at %q", copy.Owner, copy.Location)
		}
		row := setupCopyRow{Kind: kind, Owner: copy.Owner, Location: copy.Location,
			Observe: func(ctx context.Context) (setupCopyReading, error) {
				reading, err := copy.Observe(ctx)
				if err != nil {
					return setupCopyReading{}, err
				}
				return gatewayCopyReading(reading), nil
			}}
		if copy.CanReconcile() {
			row.Repair = copy.Reconcile
		}
		if len(authority) != 0 {
			authorize, err := authority[0](copy)
			if err != nil || authorize == nil {
				row.Observe = func(context.Context) (setupCopyReading, error) {
					return setupCopyReading{State: setupCopyHumanNeeded, Reason: "current native Keep or companion designation unavailable"}, nil
				}
				row.Repair = nil
			} else {
				observe := row.Observe
				row.Observe = func(ctx context.Context) (setupCopyReading, error) {
					if err := authorize(ctx); err != nil {
						return setupCopyReading{State: setupCopyHumanNeeded, Reason: err.Error()}, nil
					}
					return observe(ctx)
				}
				if copy.CanReconcile() {
					row.Repair = func(ctx context.Context) error { return copy.ReconcileAuthorized(ctx, authorize) }
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// The producer's native receipt identifies the connection; application names
// and configured paths alone supply no repair authority. The captured Keep and
// exact image remain current at every producer write/dispatch boundary.
func (d *applicationDirectory) agentCopyAuthority(ctx context.Context, copy cacheproducer.Copy) (func(context.Context) error, error) {
	if d == nil || copy.Owner != cacheproducer.ClaudeProducer {
		return nil, errors.New("companion has no native helper designation")
	}
	_, marker, err := cacheproducer.ReadClaudeMarker(ctx, copy.Receipt)
	if err != nil || marker == nil || !marker.PluginOwned || !marker.MarketplaceOwned {
		return nil, errors.New("companion ownership unavailable")
	}
	self, err := fileid.ParseStoredFileID(marker.Self)
	if err != nil {
		return nil, err
	}
	if !d.lock(ctx) {
		return nil, ctx.Err()
	}
	name := ""
	err = d.withRegistrationReadLocked(ctx, func(_ map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		for key, m := range marks {
			if m.Kind == "agent" && m.Agent != nil && m.Agent.Account == d.owner && m.Agent.State == "enrolled" && identity.SamePathName(m.Program, self.Path()) {
				if name != "" {
					return errors.New("ambiguous companion native Keep")
				}
				name = key
			}
		}
		return nil
	})
	d.unlock()
	if err != nil || name == "" {
		return nil, errors.New("companion has no current native Keep")
	}
	var image applicationAgentImage
	current := d.currentAgentEnrollmentImage(ctx, name, &image)
	if current.Enrollment == nil || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
		return nil, errors.New("companion native Keep is unavailable")
	}
	revision, key := current.Enrollment.Revision, image.Key
	readSource := d.agentPublishedSource
	if readSource == nil {
		readSource = installedApplicationAgentSource
	}
	source, err := readSource(ctx)
	old, oldErr := updateImage(ctx, image.Program)
	if err != nil || oldErr != nil || source.Hash != old.Hash || source.Bytes != old.Bytes {
		return nil, errors.New("companion repair needs exact current published helper bytes")
	}
	return func(ctx context.Context) error {
		var held applicationAgentImage
		got := d.currentAgentEnrollmentImage(ctx, name, &held)
		if got.Enrollment == nil || got.Enrollment.State != applications.AgentEnrollmentStateEnrolled || got.Enrollment.Revision != revision || held.Key != key {
			return errors.New("companion native Keep changed; preserved")
		}
		if !updateImageMatches(ctx, old, image.Program) {
			return errors.New("kept companion helper bytes changed; preserved")
		}
		if !updateImageMatches(ctx, source, source.Program) {
			return errors.New("published companion version changed; preserved")
		}
		_, now, err := cacheproducer.ReadClaudeMarker(ctx, copy.Receipt)
		if err != nil || now == nil || !now.PluginOwned || !now.MarketplaceOwned || now.Project != marker.Project || now.State != marker.State || now.Scope != marker.Scope || now.CLI != marker.CLI || now.Marketplace != marker.Marketplace {
			return errors.New("companion ownership changed; preserved")
		}
		subject, err := fileid.ParseStoredFileID(now.Self)
		if err != nil || !identity.SamePathName(subject.Path(), image.Program) {
			return errors.New("companion native program changed; preserved")
		}
		return ctx.Err()
	}, nil
}

func gatewayCopyReading(reading cacheproducer.CopyReading) setupCopyReading {
	state := setupCopyUnknown
	switch reading.State {
	case cacheproducer.CopyCurrent:
		state = setupCopyCurrent
	case cacheproducer.CopyStale:
		state = setupCopyStale
	case cacheproducer.CopyAbsent:
		state = setupCopyAbsent
	case cacheproducer.CopyForeign:
		state = setupCopyForeign
	case cacheproducer.CopyUnreadable:
		state = setupCopyUnreadable
	case cacheproducer.CopyHumanNeeded:
		state = setupCopyHumanNeeded
	}
	return setupCopyReading{State: state, Reason: reading.Reason}
}

// Provider readiness supplies refusal evidence. endpoint@1 has no typed positive
// copy proof. A ready provider and a custom engine path both remain unknown here
// until the admitted endpoint successor can project the producer's own check.
func providerCopyRows(p *runtimeProviders) []setupCopyRow {
	if p == nil {
		return nil
	}
	var rows []setupCopyRow
	for _, supervisor := range p.sorted() {
		if supervisor.file.Declaration.Name != modelHostName {
			continue
		}
		rows = append(rows, setupCopyRow{Kind: setupCopyEngine, Owner: "provider startup",
			Location: "provider:" + supervisor.file.Declaration.Endpoint,
			Observe: func(context.Context) (setupCopyReading, error) {
				state := supervisor.state()
				if state.Readiness == wire.DeclarationReadinessRefused {
					return setupCopyReading{State: setupCopyRefused, Reason: state.Why}, nil
				}
				return setupCopyReading{State: setupCopyUnknown, Reason: "typed producer copy proof unavailable; provider " + state.Readiness.String() + ": " + state.Why}, nil
			}})
	}
	return rows
}

// Runtime-visible enrollment contributes a locator for the Panel-owned row.
// It supplies no Panel receipt reading and no image hash/currentness assertion.
func helperCopyRows(ctx context.Context, d *applicationDirectory) []setupCopyRow {
	if d == nil || !d.lock(ctx) {
		return nil
	}
	defer d.unlock()
	var rows []setupCopyRow
	for _, mark := range d.unconfirmed {
		if mark.Kind != "agent" || mark.Agent == nil || mark.Program == "" {
			continue
		}
		pending := mark.Agent.State == "pending"
		rows = append(rows, setupCopyRow{Kind: setupCopyHelper, Owner: "Panel Setup", Location: mark.Program,
			Observe: func(context.Context) (setupCopyReading, error) {
				if pending {
					return setupCopyReading{State: setupCopyHumanNeeded, Reason: "the existing native Keep question needs the person"}, nil
				}
				return setupCopyReading{State: setupCopyUnknown, Reason: "Panel Setup owns the image check and replacement behind Keep; runtime has no image check result"}, nil
			}})
	}
	return rows
}

func runtimeCopyRows(ctx context.Context, p *runtimeProviders, d *applicationDirectory, executable string, discover func(context.Context) ([]cacheproducer.Copy, error)) ([]setupCopyRow, error) {
	// The provider's startup repair is always the first participating owner.
	rows := providerCopyRows(p)
	copies, err := discover(ctx)
	if err != nil {
		return nil, err
	}
	gatewayRows, err := gatewayCopyRows(copies, func(copy cacheproducer.Copy) (func(context.Context) error, error) {
		return d.agentCopyAuthority(ctx, copy)
	})
	if err != nil {
		return nil, err
	}
	rows = append(rows, gatewayRows...)
	if executable != "" {
		rows = append(rows, setupCopyRow{Kind: setupCopyMSI, Owner: "installation", Location: filepath.Dir(executable),
			Observe: func(context.Context) (setupCopyReading, error) {
				return setupCopyReading{State: setupCopyUnknown, Reason: "installation owner verifies and repairs its payload"}, nil
			}})
	}
	rows = append(rows, helperCopyRows(ctx, d)...)
	rows = append(rows, setupCopyRow{Kind: setupCopyOfficialCache, Owner: "official application installer",
		Observe: func(context.Context) (setupCopyReading, error) {
			return setupCopyReading{State: setupCopyUnknown, Reason: "the runtime has no producer receipt naming this cache; its official installer owns verification and repair"}, nil
		}})
	return rows, nil
}

// startRuntimeSetupCopies starts the same pass after ordinary startup and an
// installer-owned upgrade restart. Provider registration/readiness changes also
// request a coalesced pass. The runtime never launches a model or a new provider
// merely to inspect copies. Stop cancels and joins the one worker.
func startRuntimeSetupCopies(ctx context.Context, options runtimeFlags, h *applicationsHost, p *runtimeProviders, report func(error), activity ...logging.Sink) func() {
	if h == nil || options.isolated != "" || options.stateDir != "" {
		return func() {}
	}
	executable, err := os.Executable()
	if err != nil {
		if report != nil {
			report(err)
		}
		return func() {}
	}
	check := func(ctx context.Context) error {
		rows, err := runtimeCopyRows(ctx, p, h.directory, executable, cacheproducer.InstalledCopies)
		if err != nil {
			return err
		}
		previous := h.setup.copies.snapshot()
		results, err := h.setup.copies.check(ctx, rows)
		if err != nil {
			return err
		}
		if len(activity) > 0 && activity[0] != nil {
			for _, result := range results {
				if result.Repaired {
					if err := recordSetupCopyRepair(activity[0], result, time.Now()); err != nil && report != nil {
						report(err)
					}
				}
			}
		}
		if report != nil {
			before := make(map[string]setupCopyReading, len(previous))
			for _, result := range previous {
				before[string(result.Kind)+"\x00"+result.Location] = result.After
			}
			for _, result := range results {
				if result.After.State == setupCopyCurrent || before[string(result.Kind)+"\x00"+result.Location] == result.After {
					continue
				}
				location := result.Location
				if location == "" {
					location = "location unavailable"
				}
				report(fmt.Errorf("setup copy %s at %s: %s (%s): %s", result.Kind, location, result.After.State, result.Owner, result.After.Reason))
			}
		}
		return nil
	}
	return startSetupCopyPasses(ctx, p, check, report)
}

func recordSetupCopyRepair(sink logging.Sink, result setupCopyResult, now time.Time) error {
	if !result.Repaired || result.After.State != setupCopyCurrent || sink == nil {
		return nil
	}
	return sink.Write(logging.Record{Schema: 1, Time: logging.Timestamp{Time: now.UTC()}, Level: logging.LevelInfo, Msg: "Connection companion repaired", Attrs: map[string]string{"connection.repair.owner": result.Owner, "connection.repair.kind": string(result.Kind)}})
}

func startSetupCopyPasses(ctx context.Context, p *runtimeProviders, check func(context.Context) error, report func(error)) func() {
	ctx, cancel := context.WithCancel(ctx)
	changed := make(chan struct{}, 1)
	signal := func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	if p != nil {
		p.Watch(signal)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				// Existing gateway setup operations have a 30-second budget.
				pass, release := context.WithTimeout(ctx, 30*time.Second)
				err := check(pass)
				release()
				if err != nil && ctx.Err() == nil && report != nil {
					report(fmt.Errorf("setup copies: %w", err))
				}
			}
		}
	}()
	signal()
	return func() { cancel(); <-done }
}
