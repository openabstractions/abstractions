package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// TestDownloadExecutionDecidesUseQuietly is RIGHTS-FLOW G3, decided as
// option (a): admission asks, execution decides quietly (INF-A6). On both
// credential routes a fresh program's admission raises the use's first-use
// question; after the person's Allow the submission is admitted; a rule
// removed before the job runs fails its credential application
// not_permitted with no question raised; and the next admission asks again.
func TestDownloadExecutionDecidesUseQuietly(t *testing.T) {
	state := t.TempDir()
	r := testRights(t, state)
	holder, err := credentials.Open(credentials.Config{Backend: &privateReleaseMemory{values: map[string][]byte{}}, Namespace: "download-quiet"})
	if err != nil {
		t.Fatal(err)
	}
	c := &runtimeCredentials{runtimeRights: r, holder: holder, host: &credservice.Host{}}
	c.host.Authorization = credservice.AuthorizationConfig{Admit: c.admitScoped, Quiet: c.checkScoped, Legacy: c.checkLegacy}
	r.configure(&host.Options{Credentials: c.host, OnError: func(err error) { t.Error(err) }}, nil)
	var actions []string
	for _, action := range slices.Concat(firstUseActions, cwire.ResourceActions) {
		if !slices.Contains(actions, action) {
			actions = append(actions, action)
		}
	}
	if err := r.install(actions, nil); err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	a.rights = r
	r.firstUse, r.pendingQuestions, r.pairedInput, r.typedFirstUse = a.admitFirstUse, a.pendingQuestions, a.admitPairedInput, a.admitTypedService
	operator := cwire.Subject{Account: r.owner, Program: r.operators[0]}
	panel := rwire.Subject{Account: r.owner, Program: r.operators[len(r.operators)-1]}
	if v := holder.Store(operator, "", cwire.Registration{Name: "hf", Kind: "bearer", Scope: cwire.Scope{Targets: []string{"huggingface.co"}, Consumers: []string{downloadserve.CredentialConsumer}}, Secret: []byte("QUIET_FIXTURE_HF")}); v.Outcome != cwire.StoreOutcomeStored {
		t.Fatalf("store hf: %s", v.Outcome)
	}
	const allow, remove = true, false
	spec := request.Encode(&request.Request{Sources: []request.Source{{Scheme: "http", Locator: "https://huggingface.co/gated/model.gguf", Credential: "hf"}}})

	questionsAbout := func(program string) (all, pending []string) {
		t.Helper()
		records, _, err := book.ApplicationHistory()
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range records {
			if samePrograms(q.About, program) {
				all = append(all, q.ID)
				if q.Pending() {
					pending = append(pending, q.ID)
				}
			}
		}
		return all, pending
	}

	for _, route := range []string{downloadserve.CredentialGuarantee, downloadserve.ScopedCredentialGuarantee} {
		t.Run(route, func(t *testing.T) {
			program := filepath.Join(t.TempDir(), "fresh-agent.exe")
			subject := acceptanceprovider.AuthenticatedSubject{AccountKind: "windows", Account: r.owner, Executable: program, Program: program}
			binding := acceptanceprovider.Binding{Scope: "agent-scope", Origin: "local", Subject: &subject}
			required := []string{downloadserve.CredentialGuarantee}
			apply := c.ApplySubjectCredential
			// The original route's question is a first-use question, saved
			// and revoked as a rule; the scoped route's is a typed
			// permission, saved and undone as its effect.
			edit := func(allow bool, id string) (rwire.PolicyEditOutcome, error) {
				snapshot, err := r.policy.OperatorSnapshot()
				if err != nil {
					return 0, err
				}
				if allow {
					saved, err := a.applyOperatorAllow(context.Background(), panel, snapshot.Revision, id, func() error { return nil })
					return saved.Outcome, err
				}
				for _, rule := range snapshot.Rules {
					if rule.Subject.Account == r.owner && samePrograms(rule.Subject.Program, program) {
						if err := r.policy.Revoke(rule.Subject, rule.Action, rule.Resource); err != nil {
							return 0, err
						}
						return rwire.PolicyEditOutcomeApplied, nil
					}
				}
				return rwire.PolicyEditOutcomeInvalid, nil
			}
			if route == downloadserve.ScopedCredentialGuarantee {
				required = request.ScopedCredentialGuarantees
				apply = c.ApplyScopedSubjectCredential
				edit = func(allow bool, id string) (rwire.PolicyEditOutcome, error) {
					snapshot, err := r.policy.OperatorSnapshot()
					if err != nil {
						return 0, err
					}
					change := a.undoPermissionEffect
					if allow {
						change = a.applyPermissionAllow
					}
					saved, err := change(context.Background(), panel, snapshot.Revision, id, func() error { return nil })
					return saved.Outcome, err
				}
			}
			admit := func() (string, string) {
				outcome, reason := downloadserve.HTTPExecution{Credentials: c}.CheckSubjectAdmission(binding, download.Kind, spec, required)
				return outcome.String(), reason
			}

			// Admission asks and names its question.
			outcome, reason := admit()
			_, pending := questionsAbout(program)
			if outcome != "forbidden" || len(pending) != 1 || !slices.Equal(asks.QuestionsPending(reason), pending) {
				t.Fatalf("first admission %s %q, want forbidden naming the pending question %v", outcome, reason, pending)
			}

			// The person allows: the submission is admitted.
			if _, _, err := book.AnswerApplication(pending[0], "allow"); err != nil {
				t.Fatal(err)
			}
			if outcome, err := edit(allow, pending[0]); err != nil || outcome != rwire.PolicyEditOutcomeApplied {
				t.Fatalf("the Allow of %s: %s %v", pending[0], outcome, err)
			}
			if outcome, reason := admit(); reason != "" {
				t.Fatalf("admission after Allow %s %q, want admitted", outcome, reason)
			}
			if _, err := apply(context.Background(), subject, "hf", "huggingface.co"); err != nil {
				t.Fatalf("execution with the rule: %v", err)
			}

			// The rule is removed before the job runs.
			if outcome, err := edit(remove, pending[0]); err != nil || outcome != rwire.PolicyEditOutcomeApplied {
				t.Fatalf("removing the Allow of %s: %s %v", pending[0], outcome, err)
			}
			before, _ := questionsAbout(program)
			_, err = apply(context.Background(), subject, "hf", "huggingface.co")
			var refused *download.CredentialError
			if !errors.As(err, &refused) || refused.Outcome != "not_permitted" {
				t.Fatalf("execution after the rule's removal: %v, want credential:not_permitted:hf", err)
			}
			if after, _ := questionsAbout(program); !slices.Equal(after, before) {
				t.Fatalf("execution raised a question: %v, before %v", after, before)
			}

			// Admission still asks.
			outcome, reason = admit()
			_, pending = questionsAbout(program)
			if outcome != "forbidden" || len(pending) != 1 || !slices.Equal(asks.QuestionsPending(reason), pending) {
				t.Fatalf("admission after the removal %s %q, want forbidden naming the pending question %v", outcome, reason, pending)
			}
		})
	}
}
