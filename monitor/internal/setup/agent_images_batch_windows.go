package setup

import (
	"context"
	"errors"
	"fmt"
	cas "github.com/openabstractions/abstraction-cas/go"
	identity "github.com/openabstractions/abstraction-identity"
	"path/filepath"
)

// ErrAgentUpdateRestartRequired is the Panel's word for an approved update
// that waits for its apps to close. Windows publication goes ahead over a
// running helper. A sharing violation there is another program's handle on a
// connector file; its error keeps the native status, which the Panel words as
// "in use by another program".
var ErrAgentUpdateRestartRequired = errors.New("active connection image requires restart")
var ErrAgentUpdateDirectoriesDiffer = errors.New("connection update batch directories differ")
var ErrAgentUpdateResumeProofRequired = errors.New("staged image requires service-owned resume proof")

// PublishAgentUpdates admits every old/staged pair with native delete custody
// before the first rename. A helper an agent app runs is published over: the
// app keeps the previous image until it restarts (owner R16, 2026-11-09: open
// Claude and Codex sessions held every update back indefinitely). Another
// program's open handle blocks the whole batch intact.
func PublishAgentUpdates(ctx context.Context, stages []*StagedAgentUpdate) error {
	if len(stages) == 0 {
		return nil
	}
	root := filepath.Dir(stages[0].Old.Program())
	for _, s := range stages {
		if !identity.SamePathName(filepath.Dir(s.Old.Program()), root) || !identity.SamePathName(filepath.Dir(s.Image.Program()), root) {
			return ErrAgentUpdateDirectoriesDiffer
		}
	}
	// The account can have presenters in multiple Windows sessions. Serialize
	// the complete custody barrier through the existing cross-process CAS owner.
	unlock, err := cas.LockContext(ctx, filepath.Join(root, ".oa-agent-update-batch"), false)
	if err != nil {
		return err
	}
	defer unlock()
	for _, s := range stages {
		if s.ResumeRequired {
			return ErrAgentUpdateResumeProofRequired
		}
		if err := s.Check(ctx); err != nil {
			return err
		}
	}
	for _, s := range stages {
		if err := errors.Join(s.Old.Close(), s.Image.Close()); err != nil {
			return err
		}
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	type event struct {
		admitted bool
		err      error
	}
	events := make(chan event, len(stages)*2)
	release := make(chan struct{})
	for _, s := range stages {
		go func(s *StagedAgentUpdate) {
			result, err := replaceAgentImageAtBackup(work, s.Old.Program(), s.oldPin, s.Image.Program(), s.imagePin, s.Image.Program()+".previous", func(boundary string) {
				if boundary == "admitted" {
					events <- event{admitted: true}
					<-release
				}
			})
			if err != nil {
				err = fmt.Errorf("previous image retained at %s; staged image %s: %w", result.Backup, s.Image.Program(), err)
			}
			events <- event{err: err}
		}(s)
	}
	admitted, finished := 0, 0
	var failures []error
	for admitted < len(stages) && finished == 0 {
		e := <-events
		if e.admitted {
			admitted++
		} else {
			finished++
			failures = append(failures, e.err)
			cancel()
		}
	}
	close(release)
	for finished < len(stages) {
		e := <-events
		if !e.admitted {
			finished++
			failures = append(failures, e.err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	// Every member is published. Earlier stages and backups of these
	// connectors are stale; this update's backup stays for its Undo.
	for _, s := range stages {
		removeStaleAgentImageCopies(ctx, root, s.Old.Program(), s.Image.Program(), s.Image.Program()+".previous")
	}
	removeSetAsideAgentImages(ctx, root, nil)
	return nil
}
