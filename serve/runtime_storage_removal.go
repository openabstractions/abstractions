package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	storage "github.com/openabstractions/abstraction-storage/go"
	api "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

const (
	contentRemoveAction = "abstraction.storage/content.remove"
	contentHoldsAction  = "abstraction.storage/holds.manage"
	runtimeContentStore = "runtime-content"
)

func (r *runtimeInference) configureStorageRemoval(o *host.Options, questions *runtimeAsks) {
	if runtime.GOOS != "windows" || r == nil || questions == nil || questions.book == nil || r.content == nil || r.content.rights == nil ||
		r.resources == nil || r.resources.book == nil || r.providers == nil {
		return
	}
	rights := r.content.rights
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	r.content.recoverStorageRemovals(ctx, questions)
	cancel()
	o.StorageRemoval = &storageservice.RemovalOptions{
		Gate: rights.storageRemovalGate,
		Holds: func(ctx context.Context, peer *identity.Peer, resource string) error {
			// The contract default is the bound owning program on self.
			// The component independently binds that same native scope.
			subject, err := rightsclient.SubjectFromPeer(peer)
			if err != nil || subject.Account != rights.owner || resource != storageservice.HoldsSelf {
				return errors.New("storage: hold caller unavailable")
			}
			decision := rights.decide(ctx, subject, contentHoldsAction, resource)
			switch {
			case decision.Outcome == rwire.DecisionOutcomeUnavailable:
				return storageservice.ErrPolicyUnavailable
			case rightsclient.PermittedOrNotGranted(decision.Outcome):
				return nil
			default:
				return errors.New("storage: hold refused")
			}
		},
		Sources: r.providers.storageRemovalSources,
		InlineAnswer: func(ctx context.Context, peer *identity.Peer, store, digest, observation, occurrence string, size int64) error {
			return questions.answerStorageRemoval(ctx, rights, peer, store, digest, observation, occurrence, size)
		},
		WithoutLease: func(_ context.Context, digest string, action func() error) error {
			busy, err := r.resources.book.WithNoLease(digest, action)
			if busy {
				return storage.ErrRemovalBusy
			}
			return err
		},
		CompleteInline: questions.book.CompleteInlineStorageRemoval,
		ArchiveRestore: func(ctx context.Context, peer *identity.Peer, actor storageservice.ArchiveActor, selection string) storageservice.ArchiveRestoreReply {
			return r.content.restoreRemovedArchive(ctx, questions, peer, actor, selection)
		},
		ArchiveRestoreWork: func(ctx context.Context, place storageservice.ArchivePlacement, release storageservice.ArchiveRelease) {
			r.content.archiveRestoreWorker(ctx, questions, place, release)
		},
		Records:    casapi.BoundedFileStore{MaxBytes: storageservice.MaxRecordFileBytes},
		RecordPath: filepath.Join(r.content.store.Root(), "declared-holds.json"),
		Grace:      30 * time.Second,
		Removed: func(digest string, size int64) {
			slog.Info(fmt.Sprintf("Removed %s (%d bytes)", digest, size))
		},
	}
	o.RightsActions = append(o.RightsActions, contentRemoveAction, contentHoldsAction)
}

func (r *runtimeContent) recoverStorageRemovals(ctx context.Context, questions *runtimeAsks) {
	rows, err := questions.book.InlineStorageRecovery(ctx)
	if err != nil {
		slog.Warn("Storage removal recovery could not read the recorded answers", "error", err)
		return
	}
	answers := make(map[string]asks.InlineStorageRemoved, len(rows))
	for _, row := range rows {
		answers[row.Occurrence] = row
	}
	err = r.store.WalkRetiredRemovals(ctx, func(digest, occurrence string) {
		row, ok := answers[occurrence]
		if ctx.Err() != nil || !ok || row.Digest != digest || row.Store != r.store.Name() {
			return
		}
		err := r.store.CompleteRetiredRemoval(ctx, row.Digest, row.Observation, row.Occurrence, func() error {
			return questions.book.CompleteInlineStorageRemoval(ctx, row.Occurrence)
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("Storage removal recovery could not settle a recorded answer", "occurrence", row.Occurrence, "error", err)
		}
	})
	if err != nil {
		slog.Warn("Storage removal recovery could not enumerate its namespace", "error", err)
	}
}

func (a *runtimeAsks) answerStorageRemoval(ctx context.Context, rights *runtimeRights, peer *identity.Peer, store, digest, observation, occurrence string, size int64) error {
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || subject.Account != rights.owner || !rights.isPanelPeer(ctx, peer) || store != runtimeContentStore {
		return storageservice.ErrInlineRemovalRefused
	}
	answer := asks.InlineStorageRemoval{Account: subject.Account, Program: subject.Program, Store: store, Digest: digest, Observation: observation, Occurrence: occurrence, Size: size}
	if err := a.book.RecordInlineStorageRemoval(ctx, answer); err != nil {
		if errors.Is(err, asks.ErrInlineStorageRemovalUsed) {
			return storageservice.ErrInlineRemovalRefused
		}
		return err
	}
	return a.book.CheckInlineStorageRemoval(ctx, answer)
}

func (r *runtimeRights) storageRemovalGate(ctx context.Context, peer *identity.Peer, resource string) error {
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || !r.isPanelPeer(ctx, peer) {
		return errors.New("storage: only the Panel operator may remove content")
	}
	return r.storageRemovalGateFor(ctx, subject, resource)
}

func (r *runtimeRights) storageRemovalGateFor(ctx context.Context, subject rwire.Subject, resource string) error {
	if subject.Account != r.owner || !r.isPanel(subject.Program) ||
		(resource != storageservice.RemovalAccount && resource != ResourceStore(runtimeContentStore)) {
		return errors.New("storage: only the Panel operator may remove content")
	}
	rule := r.policy.ReadRule(subject, contentRemoveAction, resource)
	if rule.Outcome == rwire.RuleReadOutcomeUnavailable {
		return storageservice.ErrPolicyUnavailable
	}
	if rule.Outcome != rwire.RuleReadOutcomeFound || rule.Record == nil || !rule.Record.Rule.Permit ||
		rule.Record.Rule.Resource != resource || rule.Record.Rule.Subject.Account != subject.Account ||
		!identity.SameSubjectProgram(rule.Record.Rule.Subject.Program, subject.Program) {
		return errors.New("storage: exact removal permission is required")
	}
	switch r.decide(ctx, subject, contentRemoveAction, resource).Outcome {
	case rwire.DecisionOutcomePermitted:
		return nil
	case rwire.DecisionOutcomeUnavailable:
		return storageservice.ErrPolicyUnavailable
	default:
		return errors.New("storage: removal permission refused")
	}
}

// Unlike public Inventory, removal retains admitted sources that cannot
// currently answer. A declared accepted store whose current provider has not
// completed admission is unknown until the exact bound transport is ready.
func (p *runtimeProviders) storageRemovalSources(ctx context.Context) []storageservice.DesignatedSource {
	var sources []storageservice.DesignatedSource
	for _, s := range p.sorted() {
		d := s.file.Declaration
		if !slices.Contains(d.Contracts, storageInventorySource) {
			continue
		}
		s.mu.Lock()
		ready, accepted := s.readiness == wire.DeclarationReadinessReady, slices.Clone(s.accepted)
		s.mu.Unlock()
		// When startup or a refresh has not yet described the service, the
		// allowed declarations still prevent a wrongful absence proof.
		if !ready && len(accepted) == 0 && p.decide != nil {
			for _, name := range d.resources("store") {
				resource := ResourceStore(name)
				if p.decide(ctx, d.Program, ActionInventoryProvide, resource) {
					accepted = append(accepted, resource)
				}
			}
		}
		if len(accepted) == 0 {
			continue
		}
		source := storageservice.DesignatedSource{Program: d.Program}
		for _, resource := range accepted {
			source.Stores = append(source.Stores, strings.TrimPrefix(resource, "store:"))
		}
		if ready {
			if transport, err := s.transport(ctx, p.timing.inventoryBudget); err == nil {
				source.Reader = api.NewInventorySourceClient(transport)
			}
		}
		sources = append(sources, source)
	}
	return sources
}

func (r *runtimeRights) installStorageRemovalRules(options host.Options) error {
	if options.StorageRemoval == nil {
		return nil
	}
	var panels []string
	for _, program := range r.operators {
		if r.isPanel(program) {
			panels = append(panels, program)
		}
	}
	return r.installFor(panels, []installationRule{
		{contentRemoveAction, storageservice.RemovalAccount},
		{contentRemoveAction, ResourceStore(runtimeContentStore)},
	})
}
