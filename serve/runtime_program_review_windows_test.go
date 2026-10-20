//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestShippedProgramGroupReviewUsesOneActualAnswerAndCapturedImages(t *testing.T) {
	for _, mode := range []string{"keep", "predecessor", "restart", "admission-retry", "panel-replace", "not_now", "outside", "changed-image", "changed-identity", "changed-revision"} {
		t.Run(mode, func(t *testing.T) {
			run := newSelfRegisteredRun(t)
			if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
				t.Fatal(err)
			}
			run.d.askKeep = nil
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(self)
			if err != nil {
				t.Fatal(err)
			}
			copyImage := func() string {
				t.Helper()
				file, err := os.CreateTemp(filepath.Dir(self), "program-review-*.exe")
				if err != nil {
					t.Fatal(err)
				}
				path := file.Name()
				t.Cleanup(func() { os.Remove(path); os.Remove(path + ".old") })
				if _, err := file.Write(data); err != nil {
					file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				return path
			}
			program, other := copyImage(), copyImage()
			if mode == "outside" {
				folder := t.TempDir()
				program, other = filepath.Join(folder, "first.exe"), filepath.Join(folder, "second.exe")
				for _, path := range []string{program, other} {
					if err := os.WriteFile(path, data, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			candidates := []shippedImageReviewCandidate{}
			originals := map[string]registration.RegistrationSpec{}
			predecessor := ""
			for i, path := range []string{program, other} {
				held, err := openRegistryProgramImage(run.ctx, fileid.Self, identity.NormalizeSubjectProgram(path))
				if err != nil {
					t.Fatal(err)
				}
				candidate := registry3Candidate(t, []string{"first", "second"}[i])
				candidate.Key = identity.NormalizeSubjectProgram(path)
				candidate.Spec.Program = candidate.Key
				candidate.State = registration.StateEnabled
				candidate.IdentityEvidence = registration.IdentityEvidenceProgram
				candidate.Verification = registration.VerificationChanged
				if mode == "predecessor" && i == 0 {
					candidate.State = registration.StatePendingApproval
					run.d.askKeep = run.k.ask
				}
				source := &applicationV3Source{Program: candidate.Key, FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof}
				held.Close()
				change := run.d.publishProgramRegistration(run.ctx, "", true, candidate, source, "fixture-native-owner")
				if change.Outcome != registration.OutcomeApplied {
					t.Fatal(change)
				}
				candidates = append(candidates, shippedImageReviewCandidate{Key: candidate.Key, Revision: change.Revision, Cause: "shipped:unrecorded"})
				originals[candidate.Key] = candidate.Spec
				if mode == "predecessor" && i == 0 {
					_, _, mark, ok := run.d.programSelfEntry(candidate.Key)
					if !ok || mark.Question == "" {
						t.Fatal("missing old native question")
					}
					predecessor = mark.Question
				}
			}
			errors := make(chan error, 16)
			g := &shippedProgramReview{report: func(err error) { errors <- err }}
			g.snapshot(run.ctx, candidates)
			g.attach(run.k)
			if mode == "outside" {
				if g.question != "" || len(g.members) != 0 {
					t.Fatal("outside identical native images entered service popup", g.members)
				}
				return
			}
			if g.question == "" {
				select {
				case err := <-errors:
					t.Fatal(err)
				default:
					t.Fatal("no native group")
				}
			}
			id := g.question
			if predecessor != "" {
				if _, found, err := run.f.a.book.ApplicationRecord(predecessor); err != nil || found {
					t.Fatal("old PROGRAM prompt remains", found, err)
				}
				if _, outcome, err := run.f.a.book.AnswerApplication(predecessor, "keep"); err != nil || outcome == "answered" {
					t.Fatal("old PROGRAM answer survived", outcome, err)
				}
			}
			g.snapshot(run.ctx, candidates)
			if g.question != id {
				t.Fatal("same snapshot duplicated native question")
			}
			if mode == "panel-replace" || mode == "restart" || mode == "admission-retry" {
				run.d.askKeep = run.k.ask
				attempts := 0
				for step := 0; step < 3; step++ {
					selected := 0
					if mode == "restart" {
						selected = step % 2
					}
					if mode == "admission-retry" && step == 0 {
						g.admit = func(map[string]string) (asks.Record, string, error) {
							attempts++
							return asks.Record{}, "unavailable", fmt.Errorf("inert native admission unavailable")
						}
					} else {
						g.admit = nil
					}
					current, _, _, ok := run.d.programReviewRecord(run.ctx, candidates[selected].Key)
					if !ok {
						t.Fatal("missing current record")
					}
					held, _, err := captureProgramReviewImage(run.ctx, current.Spec.Program)
					if err != nil {
						t.Fatal(err)
					}
					source := &applicationV3Source{Program: current.Spec.Program, FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof}
					current.State = registration.StatePendingApproval
					current.Verification = registration.VerificationUnverified
					change := run.d.publishProgramRegistration(run.ctx, current.Revision, false, current, source, "fixture-native-owner")
					held.Close()
					if mode == "admission-retry" && step == 0 {
						if change.Outcome != registration.OutcomeApplied || change.Reason != "approval:unavailable" || g.question != "" || !g.unavailable(current.Key, change.Revision) {
							t.Fatal("false pending on native admission failure", change, g.question)
						}
						run.k.resume()
						g.snapshot(run.ctx, g.latest)
						if attempts != 1 {
							t.Fatal("automatic duplicate admission", attempts)
						}
						continue
					}
					if change.Outcome != registration.OutcomeApplied || g.question == id {
						t.Fatal("Panel replacement did not update native group", change, g.question)
					}
					if _, found, err := run.f.a.book.ApplicationRecord(id); err != nil || found {
						t.Fatal("superseded group remains visible", found, err)
					}
					if _, outcome, err := run.f.a.book.AnswerApplication(id, "keep"); err != nil || outcome == "answered" {
						t.Fatal("superseded group authorized", outcome, err)
					}
					id = g.question
					_, _, mark, ok := run.d.programSelfEntry(current.Key)
					if !ok || mark.Question != "" {
						t.Fatal("parallel one-program question", mark, ok)
					}
					pending := 0
					for _, record := range run.f.a.book.List(true) {
						if record.Key == asks.ProgramReviewKey {
							pending++
						}
					}
					if pending != 1 {
						t.Fatal("visible native groups", pending)
					}
				}
				if mode == "restart" {
					run.cancel()
					if _, err := run.f.a.book.WithdrawRuntimeBound(); err != nil {
						t.Fatal(err)
					}
					run.start(t)
					if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
						t.Fatal(err)
					}
					g = &shippedProgramReview{report: func(err error) { errors <- err }}
					g.snapshot(run.ctx, nil)
					g.attach(run.k)
					run.k.resume()
					if g.question == "" || g.question == id {
						t.Fatal("restart lost grouped fresh pending review")
					}
					id = g.question
					pending := 0
					for _, record := range run.f.a.book.List(true) {
						if record.Key == asks.SelfRegisteredKey {
							t.Fatal("restart admitted per-program fallback")
						}
						if record.Key == asks.ProgramReviewKey {
							pending++
						}
					}
					if pending != 1 {
						t.Fatal("restart native groups", pending)
					}
				}
			}
			question, found, err := run.f.a.book.ApplicationRecord(id)
			if err != nil || !found || question.Key != asks.ProgramReviewKey || question.AskerProgram != "" {
				t.Fatal(question, found, err)
			}
			facts, err := asks.ProgramReviewFactsFromSlots(question.Content.Slots)
			if err != nil || len(facts.Targets) != 2 || !facts.Targets[0].Supported || !facts.Targets[1].Supported {
				t.Fatal(facts, err)
			}
			if _, err := run.f.a.authorizeAnswerer(run.ctx, answerer{subject: run.f.answerer}, question, run.f.a.scope, "keep"); err == nil {
				t.Fatal("application responder acquired group Keep")
			}
			if mode == "changed-image" {
				file := facts.Targets[0].Program
				current, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				current = append(current, 0)
				if err := os.WriteFile(file, current, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-identity" {
				file := facts.Targets[0].Program
				current, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(file, file+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, current, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-revision" {
				run.d.editProgramRegistration(run.ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
					record := s.programs[facts.Targets[0].Program]
					record.Revision = "competing-review"
					s.programs[record.Key] = record
					return registration.Change{Outcome: registration.OutcomeApplied}, nil
				})
			}
			option := "keep"
			if mode == "not_now" {
				option = "not_now"
			}
			answer(t, run.f, id, option)
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				rows, _, _, _, _, err := run.d.programRegistrations(run.ctx)
				if err != nil {
					t.Fatal(err)
				}
				kept := 0
				for _, record := range rows {
					if record.Verification != registration.VerificationChanged && record.State == registration.StateEnabled {
						kept++
					}
					if !reflect.DeepEqual(record.Spec, originals[record.Key]) {
						t.Fatal("group changed Spec", record)
					}
				}
				positive := mode == "keep" || mode == "panel-replace" || mode == "predecessor" || mode == "restart" || mode == "admission-retry"
				if positive && kept == 2 {
					return
				}
				if !positive && kept != 0 {
					t.Fatal("unanswered or stale snapshot enabled a provider")
				}
				if mode == "changed-image" || mode == "changed-identity" || mode == "changed-revision" {
					select {
					case <-errors:
						return
					default:
					}
				}
				if mode == "not_now" {
					time.Sleep(30 * time.Millisecond)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("group did not settle", mode)
		})
	}
}
