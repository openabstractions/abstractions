package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
)

// oneWaveJobs is the runtime's job service for inference work behind the
// shipped job method policy (host.JobPolicyFromRights) and first-use rights,
// served to this test process over the native Program route.
type oneWaveJobs struct {
	r        *runtimeRights
	a        *runtimeAsks
	book     *asks.Book
	executor *runtimeInferenceJobExecutor
	provider *acceptanceprovider.Provider
	applied  *bool
	program  string
}

func newOneWaveJobs(t *testing.T) *oneWaveJobs {
	t.Helper()
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), append(append([]string{}, firstUseActions...), contentReadAction, contentWriteAction))
	if err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	r := &runtimeRights{policy: policy, owner: a.rights.owner, firstUse: a.admitFirstUse, pairedInput: a.admitPairedInput, pendingQuestions: a.pendingQuestions}
	a.rights = r
	content := &runtimeContent{rights: r}
	routes := router.New(router.NewHosted("replicate", "https://api.replicate.com", router.WireReplicatePredictions, "replicate"))
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer fixture"}, nil
	})
	routes.Survey()
	applied := false
	provider, err := inference.New(inference.Config{Router: routes, Decide: r.decideInference, PrepareContentWrite: content.prepareWrite,
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			applied = true
			return nil, "not_permitted"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	execution := inference.NewJobExecution(provider, func(context.Context, string, string) (inference.Subject, inference.ContentOutcome) {
		return inference.Subject{}, inference.ContentForbidden
	}, func(err error) { t.Error(err) })
	execution.SetPendingQuestions(r.pendingJobQuestions)
	executor := &runtimeInferenceJobExecutor{base: &preservedJobExecutor{}, inference: execution}
	jobs, err := acceptanceprovider.OpenWithExecutor(t.TempDir(), "owner", executor)
	if err != nil {
		t.Fatal(err)
	}
	return &oneWaveJobs{r: r, a: a, book: book, executor: executor, provider: jobs, applied: &applied}
}

// bind is the job host's binding of a native peer (runtime jobHost.authorizeBinding).
func (w *oneWaveJobs) bind(peer *identity.Peer) (acceptanceprovider.Binding, error) {
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		return acceptanceprovider.Binding{}, err
	}
	path, err := peer.Path.AtLeast(listen.Program.Path)
	if err != nil {
		return acceptanceprovider.Binding{}, err
	}
	scope, err := acceptanceprovider.LocalSubjectScope("windows", subject.Account, filepath.Clean(path))
	if err != nil {
		return acceptanceprovider.Binding{}, err
	}
	w.program = subject.Program
	return acceptanceprovider.Binding{Scope: scope, Origin: "local", Subject: &acceptanceprovider.AuthenticatedSubject{
		AccountKind: "windows", Account: subject.Account, Executable: filepath.Clean(path), Program: subject.Program}}, nil
}

// submit sends s once over a fresh native connection.
func (w *oneWaveJobs) submit(t *testing.T, s jobapi.Submission) jobapi.AcceptanceResult {
	t.Helper()
	endpoint := shortMediationEndpoint(t)
	l, err := listen.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err == nil {
			err = acceptanceprovider.HandleConnectionWithBindingPolicy(ctx, conn, w.provider, w.bind, host.JobPolicyFromRights(w.r, host.JobRightsActions))
		}
		done <- err
	}()
	result, err := jobapi.NewRecoverableAcceptanceClient(listen.FrameClient{Endpoint: endpoint, Timeout: 25 * time.Second, MaxFrame: acceptanceprovider.MaxFrameBytes}).Submit(s)
	if serverErr := <-done; err != nil || serverErr != nil {
		t.Fatal(err, serverErr)
	}
	return result
}

// pending is the ids of the questions still pending about the test program.
func (w *oneWaveJobs) pending(t *testing.T) []string {
	t.Helper()
	records, _, err := w.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, q := range records {
		if q.Pending() && samePrograms(q.About, w.program) {
			ids = append(ids, q.ID)
		}
	}
	return ids
}

func (w *oneWaveJobs) allow(t *testing.T, ids ...string) {
	t.Helper()
	operator := policyRules{policy: w.r.policy, by: rwire.Subject{Account: w.r.owner, Program: appProgram("Abstraction Panel")}, asks: w.a}
	for _, id := range ids {
		record, found, err := w.book.ApplicationRecord(id)
		if err != nil || !found {
			t.Fatalf("question %s: %v", id, err)
		}
		metadata := askclient.RecordMetadata{Key: record.Key, About: record.About, Text: record.Text}
		if _, err := grants.AnswerQuestion(context.Background(), pendingQuestion{record: metadata, book: w.book}, operator, w.r.owner, id, "allow"); err != nil {
			t.Fatal(err)
		}
	}
}

func pictureJob(provider *acceptanceprovider.Provider, key, model string) jobapi.Submission {
	spec := jobwire.Encode(&jobwire.Document{Request: &jobwire.Request{Profile: jobwire.ProfileImageBatch, Image: jobwire.ImageRequest{
		Model: model, Mode: jobwire.ModeGenerate, Prompt: "frogs", Size: "1024x1024", Count: 1,
		Guarantees: []jobwire.RequestGuarantee{jobwire.RequestGuaranteeHostedAllowed}, Credential: "replicate", Extensions: map[string]string{},
	}}})
	window, _ := provider.Bind("history-reader").GetHistoryWindow()
	return jobapi.Submission{Identity: jobapi.RequestIdentity{Key: key, HistoryEpoch: window.HistoryEpoch}, Kind: inference.InferenceJobKind, Spec: spec,
		RequiredGuarantees: []string{inference.RecoverableUpstreamGuarantee}}
}

// TestAFreshInferenceJobNamesAllThreeQuestionsInOneRefusal: an agent's first
// picture job reaches the runtime's job service with no submit, complete or
// output rule. The one forbidden refusal names the job:inference submit
// question, the complete question on the image host and the output:image
// question, submit first (job JOB-A16, backlog RIGHTS-FLOW amendment "One
// wave of questions"). J-PIC on 2026-11-10 showed the person the submit
// popup, and the other two only after the agent's one retry was spent.
// The credential is never applied while the submit question waits, and its
// word never reaches the caller.
func TestAFreshInferenceJobNamesAllThreeQuestionsInOneRefusal(t *testing.T) {
	w := newOneWaveJobs(t)
	first := w.submit(t, pictureJob(w.provider, "first-picture", "black-forest-labs/flux-schnell"))
	want := w.pending(t)
	if len(want) != 3 {
		t.Fatalf("want the submit, complete and output:image questions pending about %s, got %v", w.program, want)
	}
	named := asks.QuestionsPending(first.Reason)
	if first.Outcome != jobapi.AcceptanceOutcomeForbidden || first.Receipt != nil || !strings.HasPrefix(first.Reason, "rights:not_granted rights:question_pending:") || !slices.Equal(named, want) {
		t.Fatalf("first picture refusal %s %q must name %v", first.Outcome, first.Reason, want)
	}
	submitQuestion, actions := named[0], map[string]bool{}
	for _, id := range named {
		record, _, err := w.book.ApplicationRecord(id)
		if err != nil {
			t.Fatal(err)
		}
		_, action, _, _ := asks.FirstUse(record.Key, record.About, record.Text)
		actions[action] = true
		if id == submitQuestion && action != grants.JobSubmitAction {
			t.Fatalf("the refusal must name the submit question first, got %s", action)
		}
	}
	if !actions[grants.JobSubmitAction] || !actions[inference.ActionComplete] || !actions[contentWriteAction] {
		t.Fatalf("questions asked about %v", actions)
	}
	if *w.applied {
		t.Fatal("questions-only admission applied the credential")
	}
	// The person allows complete and output and leaves submit waiting. The
	// next refusal names the submit question alone; the credential's own
	// refusal, which full admission reports, never reaches this caller.
	w.allow(t, named[1:]...)
	again := w.submit(t, pictureJob(w.provider, "first-picture", "black-forest-labs/flux-schnell"))
	if again.Outcome != jobapi.AcceptanceOutcomeForbidden || again.Reason != "rights:not_granted rights:question_pending:"+submitQuestion || *w.applied {
		t.Fatalf("refusal with only submit waiting %s %q (credential applied %v)", again.Outcome, again.Reason, *w.applied)
	}
	binding, err := w.bindingFor(t)
	if err != nil {
		t.Fatal(err)
	}
	s := pictureJob(w.provider, "first-picture", "black-forest-labs/flux-schnell")
	if outcome, reason := w.executor.CheckSubjectAdmission(binding, s.Kind, s.Spec, s.RequiredGuarantees); outcome != jobapi.AcceptanceOutcomeForbidden || !strings.HasPrefix(reason, "credential:") {
		t.Fatalf("full admission should refuse on the credential: %s %q", outcome, reason)
	}
	// A model no host serves raises no question and its reason is dropped.
	*w.applied = false
	unknown := w.submit(t, pictureJob(w.provider, "unknown-host", "nobody/serves-this"))
	if unknown.Outcome != jobapi.AcceptanceOutcomeForbidden || unknown.Reason != "rights:not_granted rights:question_pending:"+submitQuestion || *w.applied {
		t.Fatalf("unknown host refusal %s %q", unknown.Outcome, unknown.Reason)
	}
	if got := w.pending(t); !slices.Equal(got, []string{submitQuestion}) {
		t.Fatalf("pending after the unknown host %v", got)
	}
}

// TestABlockedSubmitAsksNoOtherQuestion: a stored Block on the program's
// job:inference submission refuses at once. The executor's admission does not
// run, and no complete or output question is raised.
func TestABlockedSubmitAsksNoOtherQuestion(t *testing.T) {
	w := newOneWaveJobs(t)
	binding, err := w.bindingFor(t)
	if err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: binding.Subject.Account, Program: binding.Subject.Program}
	if err := w.r.policy.Set(subject, grants.JobSubmitAction, host.JobKindResource(inference.InferenceJobKind), false); err != nil {
		t.Fatal(err)
	}
	r := w.submit(t, pictureJob(w.provider, "blocked", "black-forest-labs/flux-schnell"))
	if r.Outcome != jobapi.AcceptanceOutcomeForbidden || len(asks.QuestionsPending(r.Reason)) != 0 {
		t.Fatalf("blocked submit %s %q", r.Outcome, r.Reason)
	}
	if got := w.pending(t); len(got) != 0 || *w.applied {
		t.Fatalf("a blocked submit asked %v (credential applied %v)", got, *w.applied)
	}
}

// bindingFor binds this test process as the job host would.
func (w *oneWaveJobs) bindingFor(t *testing.T) (acceptanceprovider.Binding, error) {
	t.Helper()
	peer, _ := applicationV3NativePeer(t)
	return w.bind(peer)
}
