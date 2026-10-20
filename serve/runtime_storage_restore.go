package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	download "github.com/openabstractions/abstraction-download/go"
	"github.com/openabstractions/abstraction-download/go/archive"
	identity "github.com/openabstractions/abstraction-identity"
	job "github.com/openabstractions/abstraction-job/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

type archiveRestoreAdmission struct {
	Actor                    storageservice.ArchiveActor
	Store, Occurrence, Proof string
}

var errArchiveRestoreSettlement = errors.New("storage: native archive placement is awaiting job settlement")

func archiveAdmission(rec *job.Record) (archiveRestoreAdmission, download.Spec, bool) {
	var a archiveRestoreAdmission
	spec, err := download.SpecOf(rec)
	if err != nil || rec.Kind != download.Kind || !spec.ArchiveOnly ||
		json.Unmarshal(spec.ArchiveAdmission, &a) != nil || rec.ID != a.Occurrence ||
		a.Store != runtimeContentStore || len(a.Proof) != 64 {
		return a, spec, false
	}
	return a, spec, true
}

func (c *runtimeContent) archiveRestoreJobs() (*job.FileStore, error) {
	return job.NewFileStore(filepath.Join(c.store.Root(), "archive-restore-jobs"))
}

func (c *runtimeContent) archiveRestorePanel(subject rwire.Subject) bool {
	return subject.Account == c.rights.owner && c.rights.isPanel(subject.Program)
}

func (c *runtimeContent) archiveRestorePolicy(ctx context.Context, subject rwire.Subject, digest string) error {
	for _, resource := range []string{storageservice.RemovalAccount, ResourceStore(c.store.Name())} {
		if err := c.rights.storageRemovalGateFor(ctx, subject, resource); err != nil {
			return err
		}
	}
	// The retained inline click supplies the missing authority only for this
	// private placement. An explicit existing denial still vetoes it.
	outcome := c.decision(ctx, subject, contentWriteAction, digest)
	switch {
	case rightsclient.PermittedOrNotGranted(outcome):
		return nil
	case outcome == rwire.DecisionOutcomeUnavailable:
		return storageservice.ErrPolicyUnavailable
	default:
		return errContentDenied
	}
}

func (c *runtimeContent) restoreRemovedArchive(ctx context.Context, questions *runtimeAsks, peer *identity.Peer, actor storageservice.ArchiveActor, selection string) storageservice.ArchiveRestoreReply {
	answer := func(out string) storageservice.ArchiveRestoreReply {
		return storageservice.ArchiveRestoreReply{Outcome: out}
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || subject.Account != c.rights.owner || !c.rights.isPanelPeer(ctx, peer) ||
		actor.Account != subject.Account || !identity.SameSubjectProgram(actor.Program, subject.Program) {
		return answer("forbidden")
	}
	jobs, err := c.archiveRestoreJobs()
	if err != nil {
		return answer("unavailable")
	}
	rows, err := questions.book.InlineStorageRemoved(ctx)
	if err != nil {
		return answer("unavailable")
	}
	occurrence, expected, _ := strings.Cut(selection, ".")
	seen := make(map[string]bool)
	result := answer("page")
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, row := range rows {
		if row.Store != c.store.Name() || row.Size <= 0 || seen[row.Digest] || row.Account != subject.Account ||
			!identity.SameSubjectProgram(row.Program, subject.Program) {
			continue
		}
		seen[row.Digest] = true
		if occurrence != "" && occurrence != row.Occurrence {
			continue
		}
		rec, err := jobs.Load(row.Occurrence)
		if err != nil && !errors.Is(err, job.ErrNotFound) {
			return answer("unavailable")
		}
		if rec != nil {
			admission, spec, ok := archiveAdmission(rec)
			if !ok || admission.Actor.Account != subject.Account || !identity.SameSubjectProgram(admission.Actor.Program, subject.Program) ||
				spec.Artifact.Digest != row.Digest || spec.Artifact.Size != row.Size {
				return answer("unavailable")
			}
			if occurrence != "" {
				return answer(archiveSelectionOutcome(rec.State, admission.Proof == expected))
			}
		}
		_, already := c.store.Find(row.Digest)
		canPlace := rec == nil && !already && c.archiveRestorePolicy(ctx, subject, row.Digest) == nil
		if occurrence == "" {
			entry := storageservice.ArchiveRestoreEntry{Digest: row.Digest, Bytes: row.Size, At: row.At.UnixMilli()}
			if rec != nil {
				switch rec.State {
				case job.StateComplete:
					entry.Status = "restored"
				case job.StateFailed, job.StateCancelled:
					entry.Status = "unavailable"
				default:
					entry.Status = "restoring"
				}
			}
			if canPlace {
				if selected, ok := (archive.Service{}).SelectRestore(probe, row.Digest, row.Size); ok {
					entry.Available, entry.Occurrence = true, row.Occurrence+"."+selected.Proof()
				}
			}
			result.Entries = append(result.Entries, entry)
			if len(result.Entries) == 12 {
				break
			}
			continue
		}
		if !canPlace {
			return answer("refused")
		}
		selected, ok := (archive.Service{}).SelectRestore(probe, row.Digest, row.Size)
		if !ok || selected.Proof() != expected {
			return answer("unavailable")
		}
		a := archiveRestoreAdmission{Actor: actor, Store: row.Store, Occurrence: row.Occurrence, Proof: selected.Proof()}
		raw, err := json.Marshal(a)
		if err != nil {
			return answer("unavailable")
		}
		_, err = download.SubmitArchiveRestore(jobs, row.Occurrence, download.Artifact{Digest: row.Digest, Size: row.Size}, raw)
		if errors.Is(err, job.ErrInvalid) {
			existing, loadErr := jobs.Load(row.Occurrence)
			if loadErr == nil {
				retained, kept, ok := archiveAdmission(existing)
				if ok && retained.Actor == actor && kept.Artifact.Digest == row.Digest && kept.Artifact.Size == row.Size {
					return answer(archiveSelectionOutcome(existing.State, retained.Proof == expected))
				}
			}
		}
		if err != nil {
			return answer("unavailable")
		}
		return answer("queued")
	}
	if occurrence != "" {
		return answer("not_found")
	}
	return result
}

func archiveSelectionOutcome(state job.State, matches bool) string {
	switch {
	case !matches:
		return "refused"
	case state == job.StateComplete:
		return "restored"
	case state.Terminal():
		return "unavailable"
	default:
		return "queued"
	}
}

func (c *runtimeContent) archiveRestoreWorker(ctx context.Context, questions *runtimeAsks, place storageservice.ArchivePlacement, release storageservice.ArchiveRelease) {
	jobs, err := c.archiveRestoreJobs()
	if err != nil {
		slog.Warn("Storage archive restore jobs could not start", "error", err)
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	settled := make(map[string]bool)
	for ctx.Err() == nil {
		records, _ := jobs.List()
		for _, rec := range records {
			if ctx.Err() != nil {
				return
			}
			a, spec, ok := archiveAdmission(rec)
			if !ok {
				continue
			}
			if rec.State.Terminal() {
				if !settled[rec.ID] {
					err := errors.Join(download.CleanArchiveRestore(jobs, rec), release(a.Actor, rec.ID, spec.Artifact.Digest, spec.Artifact.Size))
					if err != nil {
						slog.Warn("Storage archive restore staging could not be cleaned", "id", rec.ID, "error", err)
					} else {
						settled[rec.ID] = true
					}
				}
				continue
			}
			if !jobs.Claimable(rec) {
				continue
			}
			unlock, err := cas.LockContext(ctx, filepath.Join(c.store.Root(), "archive-restore-worker"), false)
			if err != nil {
				return
			}
			err = c.runArchiveRestore(ctx, questions, jobs, rec.ID, a, spec.Artifact, place)
			if err != nil && ctx.Err() == nil {
				slog.Warn("Storage archive restore did not complete", "id", rec.ID, "error", err)
				if !errors.Is(err, errArchiveRestoreSettlement) {
					settleArchiveRestoreFailure(jobs, rec.ID)
				}
			}
			unlock()
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
		}
	}
}

func (c *runtimeContent) runArchiveRestore(ctx context.Context, questions *runtimeAsks, jobs *job.FileStore, id string, a archiveRestoreAdmission, item download.Artifact, place storageservice.ArchivePlacement) error {
	subject := rwire.Subject{Account: a.Actor.Account, Program: a.Actor.Program}
	check := func(ctx context.Context, call storageservice.WriteCall) error {
		if call.Stage == storageservice.WriteAppend || call.Stage == storageservice.WriteCommit {
			return ctx.Err()
		}
		if a.Store != c.store.Name() || !c.currentArchiveRemoval(ctx, questions, subject, a.Occurrence, item) {
			return errContentDenied
		}
		if err := c.archiveRestorePolicy(ctx, subject, item.Digest); err != nil {
			return err
		}
		if call.Stage == storageservice.WritePublish {
			selected, ok := (archive.Service{}).SelectRestore(ctx, item.Digest, item.Size)
			if !ok || selected.Proof() != a.Proof {
				return download.ErrArchiveRestoreUnavailable
			}
		}
		if call.Stage == storageservice.WriteBegin || call.Stage == storageservice.WritePublish {
			if err := c.keepInput(ctx, item.Digest); err != nil {
				return storageservice.ErrPolicyUnavailable
			}
		}
		return nil
	}
	if _, found := c.store.Find(item.Digest); found {
		// A previous native commit can survive a crash while the Download job
		// still says Transferred. Only the exact writer receipt can settle it.
		if !c.archiveRestorePanel(subject) || !c.currentArchiveRemoval(ctx, questions, subject, a.Occurrence, item) {
			return errContentDenied
		}
		if err := place(ctx, a.Actor, id, item.Digest, item.Size, nil, check); err != nil {
			return err
		}
		_, err := download.RunArchiveRestore(ctx, jobs, id, nil, nil)
		if err != nil {
			return errors.Join(errArchiveRestoreSettlement, err)
		}
		return finishArchiveRestore(jobs, id)
	}
	if err := check(ctx, storageservice.WriteCall{}); err != nil {
		return err
	}
	owner := archive.Service{}
	selected, ok := owner.SelectRestore(ctx, item.Digest, item.Size)
	if !ok || selected.Proof() != a.Proof {
		return download.ErrArchiveRestoreUnavailable
	}
	checkSpace := c.checkArchiveRestoreSpace
	if checkSpace == nil {
		checkSpace = checkLocalDownloadSpace
	}
	file, err := download.RunArchiveRestore(ctx, jobs, id, selected, checkSpace)
	if err != nil {
		return err
	}
	if err := checkSpace(c.store.Root(), item.Size); err != nil {
		return err
	}
	body, err := os.Open(file)
	if err != nil {
		return err
	}
	err = place(ctx, a.Actor, id, item.Digest, item.Size, body, check)
	if closeErr := body.Close(); err != nil {
		return errors.Join(err, closeErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return finishArchiveRestore(jobs, id)
}

func finishArchiveRestore(jobs *job.FileStore, id string) error {
	if err := download.NewRunner(jobs, "archive-restore").TakeDelivery(id); err != nil {
		return errors.Join(errArchiveRestoreSettlement, err)
	}
	finished, err := jobs.Load(id)
	if err != nil || finished.State != job.StateComplete {
		return errors.Join(errArchiveRestoreSettlement, err)
	}
	return nil
}

func (c *runtimeContent) currentArchiveRemoval(ctx context.Context, questions *runtimeAsks, subject rwire.Subject, occurrence string, item download.Artifact) bool {
	rows, err := questions.book.InlineStorageRemoved(ctx)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.Store == c.store.Name() && row.Digest == item.Digest {
			return row.Occurrence == occurrence && row.Size == item.Size && row.Account == subject.Account &&
				identity.SameSubjectProgram(row.Program, subject.Program)
		}
	}
	return false
}

func settleArchiveRestoreFailure(jobs *job.FileStore, id string) {
	claim, err := jobs.Claim(id, "archive-restore", time.Minute)
	if err != nil {
		return
	}
	_, err = jobs.Update(id, claim.Lease.Epoch, func(rec *job.Record) error {
		rec.State = job.StateFailed
		return nil
	})
	if err != nil {
		_ = jobs.Release(id, claim.Lease.Epoch)
	}
}
