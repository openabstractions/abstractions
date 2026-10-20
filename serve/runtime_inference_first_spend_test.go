package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-inference/adapters/go/gateway"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// CC-10: actual framed requests keep every pre-acceptance first-use gate
// inside the one caller-owned admission lifetime. The upstream is inert DATA.
func TestNativeImageCollectsCompleteInputAndOutputBeforeWaiting(t *testing.T) {
	if err := identity.CanEver(listen.Program); err != nil || runtime.GOOS == "darwin" {
		t.Skip("native Program proof unavailable")
	}
	r, a, _ := inputAsks(t)
	r.awaitQuestions = a.awaitQuestions
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		t.Fatal(err)
	}
	r.policy.Definitions = catalog
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	subject := inference.Subject{Account: r.owner, Program: identity.CanonicalProgramPath(filepath.Clean(exe))}
	bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
	content, err := composeContent(runtimeFlags{endpoint: "native1447-gather"}, t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	digest := digestOf(editInputPNG)
	grantInput(t, r, subject, inference.InputImage, contentReadAction, contentWriteAction)
	if out := content.write(t.Context(), subject, digest, editInputPNG); out != inference.ContentResolved {
		t.Fatal(out)
	}
	if err := r.policy.Revoke(bound, contentReadAction, "input:image"); err != nil {
		t.Fatal(err)
	}
	r.firstUse = a.admitFirstUse
	r.pairedInput = a.admitPairedInput
	var posts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			fmt.Fprint(w, `{"data":[{"id":"gpt-image-1"}]}`)
			return
		}
		posts.Add(1)
		http.Error(w, "unexpected paid request", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	routes := router.New(router.NewHosted("openai", upstream.URL, router.WireOpenAICompatible, ""))
	routes.Survey()
	c := &runtimeCredentials{runtimeRights: r}
	provider, err := inference.New(inference.Config{Router: routes, SurveyAge: time.Hour,
		Decide: r.decideInference, Standing: c.standingInference, AskInput: r.askInput,
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			t.Error("unexpected credential")
			return nil, "not_permitted"
		},
		ResolveContent: content.read, PrepareContentWrite: content.prepareWrite})
	if err != nil {
		t.Fatal(err)
	}
	isolated, _ := isolatedRuntime(t)
	host, err := inferenceservice.Listen(isolated.endpoint, provider)
	if err != nil {
		provider.Close()
		t.Fatal(err)
	}
	gathered := make(chan []string, 1)
	host.AwaitQuestions = func(ctx context.Context, s inference.Subject, ids []string) (bool, error) {
		gathered <- ids
		return r.awaitNativeQuestions(ctx, s, ids)
	}
	serveCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Serve(serveCtx) }()
	defer func() { stop(); <-done; provider.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	returned := make(chan iwire.Admission, 1)
	go func() {
		v, e := iwire.NewImageClient(listen.FrameClient{Endpoint: isolated.endpoint}.WithContext(ctx)).Start(iwire.ImageRequest{Model: "gpt-image-1", Mode: iwire.ImageModeEdit, Prompt: "inert", Size: "1024x1024", Count: 1, ImageDigest: digest, ImageMediaType: "image/png", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Extensions: map[string]string{}})
		if e != nil {
			v = iwire.Admission{Outcome: iwire.StartOutcomeUnavailable}
		}
		returned <- v
	}()
	select {
	case ids := <-gathered:
		if len(ids) != 3 {
			t.Fatalf("first pass gathered %d questions, want complete/input/output: %v", len(ids), ids)
		}
	case v := <-returned:
		t.Fatalf("returned before collecting questions: %+v", v)
	case <-time.After(2 * time.Second):
		t.Fatal("no native question wait")
	}
	if posts.Load() != 0 {
		t.Fatal("provider effect before answers")
	}
	if err := r.policy.Set(bound, inference.ActionComplete, "host:openai", false); err != nil {
		t.Fatal(err)
	}
	// The other questions must settle too; a Block is a refusal, never a retry.
	for _, profile := range []string{"input:image", "output:image"} {
		for _, action := range []string{contentWriteAction, contentReadAction} {
			if err := r.policy.Set(bound, action, profile, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	select {
	case v := <-returned:
		if v.Outcome == iwire.StartOutcomeAccepted || v.Reason != "rights:not_granted content:read:forbidden output:forbidden" {
			t.Fatalf("gathered refusal: %+v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("settled questions did not release admission")
	}
	if posts.Load() != 0 {
		t.Fatal("refused request sent paid work")
	}
}

func TestNativeFirstUseCredentialInputAndOutputAdmission(t *testing.T) {
	if err := identity.CanEver(listen.Program); err != nil || runtime.GOOS == "darwin" {
		t.Skip("native Program proof unavailable")
	}
	for _, gate := range []string{"complete", "credential", "input", "output"} {
		for _, end := range []string{"allow", "saved allow", "refuse", "block", "close", "deadline", "scope changed", "complete revoked", "complete revoked at effect"} {
			if gate == "complete" && (end == "scope changed" || end == "complete revoked") {
				continue
			}
			t.Run(gate+"/"+end, func(t *testing.T) {
				r, a, book := inputAsks(t)
				r.awaitQuestions = a.awaitQuestions
				if err := r.policy.RegisterAction(credentials.ActionApply); err != nil {
					t.Fatal(err)
				}
				catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{Credentials: true})
				if err != nil {
					t.Fatal(err)
				}
				r.policy.Definitions = catalog
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				subject := inference.Subject{Account: r.owner, Program: identity.CanonicalProgramPath(filepath.Clean(exe))}
				bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
				if gate != "complete" {
					if err := r.policy.Set(bound, inference.ActionComplete, "host:fixture", true); err != nil {
						t.Fatal(err)
					}
				}
				asked := make(chan string, 8)
				r.firstUse = func(s rwire.Subject, publisher func() asks.Publisher, action, resource string) string {
					id := a.admitFirstUse(s, publisher, action, resource)
					asked <- id
					return id
				}
				r.pairedInput = func(s rwire.Subject, publisher func() asks.Publisher, resource string) string {
					id := a.admitPairedInput(s, publisher, resource)
					asked <- id
					return id
				}
				state := t.TempDir()
				content, err := composeContent(runtimeFlags{endpoint: "native1447-content"}, state, r)
				if err != nil {
					t.Fatal(err)
				}
				digest := digestOf(editInputPNG)
				if gate == "input" {
					grantInput(t, r, subject, inference.InputImage, contentReadAction, contentWriteAction)
					if out := content.write(t.Context(), subject, digest, editInputPNG); out != inference.ContentResolved {
						t.Fatal(out)
					}
					if err := r.policy.Revoke(bound, contentReadAction, "input:image"); err != nil {
						t.Fatal(err)
					}
				}
				var posts, admits atomic.Int32
				workRelease, workStarted := make(chan struct{}), make(chan struct{}, 1)
				var releaseOnce sync.Once
				releaseWork := func() { releaseOnce.Do(func() { close(workRelease) }) }
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.Method == http.MethodGet {
						fmt.Fprint(w, `{"data":[{"id":"fixture-model"}]}`)
						return
					}
					posts.Add(1)
					workStarted <- struct{}{}
					select {
					case <-workRelease:
					case <-req.Context().Done():
						return
					}
					if strings.HasSuffix(req.URL.Path, "/audio/speech") {
						w.Header().Set("Content-Type", "audio/wav")
						w.Write(append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 96)...))
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}))
				defer upstream.Close()
				defer releaseWork()
				credential := ""
				if gate == "credential" {
					credential = "fixture"
				}
				holder, err := credentials.Open(credentials.Config{Backend: &privateReleaseMemory{values: map[string][]byte{}}, Namespace: "native-admission1447"})
				if err != nil {
					t.Fatal(err)
				}
				var storedRevision string
				if credential != "" {
					stored := holder.Store(cwire.Subject{Account: subject.Account, Program: subject.Program}, "", cwire.Registration{Name: credential, Kind: "bearer", Scope: cwire.Scope{Targets: []string{"127.0.0.1"}, Consumers: []string{inference.Contract}}, Secret: []byte("NATIVE1447_INERT_VALUE")})
					if stored.Outcome != cwire.StoreOutcomeStored {
						t.Fatal(stored)
					}
					storedRevision = stored.Revision
				}
				if end == "scope changed" {
					r.awaitQuestions = func(ctx context.Context, who rwire.Subject, ids []string) bool {
						allowed := a.awaitQuestions(ctx, who, ids)
						if allowed {
							switch gate {
							case "credential":
								if result := holder.Revoke(cwire.Subject{Account: subject.Account, Program: subject.Program}, storedRevision, credential); result.Outcome != cwire.RevokeOutcomeRevoked {
									t.Error(result)
								}
							case "input":
								if err := content.changeInput(ctx, digest, false, func(index *requestInputIndex) { index.Inputs = nil }); err != nil {
									t.Error(err)
								}
							case "output":
								if err := r.policy.Set(bound, contentReadAction, "output:speech", false); err != nil {
									t.Error(err)
								}
							}
						}
						return allowed
					}
				}
				c := &runtimeCredentials{runtimeRights: r, holder: holder}
				mediated := gate == "credential" && (end == "allow" || end == "close" || end == "complete revoked at effect")
				routeHost := router.NewHosted("fixture", upstream.URL, router.WireOpenAICompatible, credential)
				if mediated {
					routeHost.BindingID = "native1447-binding"
				}
				routes := router.New(routeHost)
				routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) { return nil, nil })
				routes.Survey()
				ended := make(chan error, 4)
				captured := make(chan context.Context, 4)
				cfg := inference.Config{Router: routes, SurveyAge: time.Hour, Standing: c.standingInference,
					Admit: func(context.Context, *router.Host, string) (string, bool) { admits.Add(1); return "", true },
					Decide: func(ctx context.Context, s inference.Subject, action, resource string) (string, error) {
						select {
						case captured <- ctx:
						default:
						}
						word, err := r.decideInference(ctx, s, action, resource)
						if gate == "complete" && end == "complete revoked at effect" && word == "permitted" {
							if err := r.policy.Set(bound, inference.ActionComplete, "host:fixture", false); err != nil {
								t.Error(err)
							}
						}
						return word, err
					}, AskInput: r.askInput,
					Apply: func(ctx context.Context, s inference.Subject, consumer, name, target string) (map[string]string, string) {
						result := holder.Apply(ctx, cwire.Use{Subject: cwire.Subject{Account: s.Account, Program: s.Program}, Consumer: consumer, Name: name, Target: target}, c.decide)
						if gate == "credential" && end == "complete revoked at effect" && result.Outcome == cwire.ApplyOutcomeApplied {
							if err := r.policy.Set(bound, inference.ActionComplete, "host:fixture", false); err != nil {
								t.Error(err)
							}
						}
						return result.Headers, result.Outcome.String()
					},
					ResolveContent: func(ctx context.Context, s inference.Subject, digest string, limit int64) ([]byte, inference.ContentOutcome) {
						data, out := content.read(ctx, s, digest, limit)
						if gate == "input" && end == "complete revoked at effect" && out == inference.ContentResolved {
							if err := r.policy.Set(bound, inference.ActionComplete, "host:fixture", false); err != nil {
								t.Error(err)
							}
						}
						return data, out
					},
					PrepareContentWrite: func(ctx context.Context, s inference.Subject, profile, media string, limit int64) (inference.ContentCommitter, inference.ContentOutcome) {
						commit, out := content.prepareWrite(ctx, s, profile, media, limit)
						if commit != nil {
							actual := commit
							commit = func(commitCtx context.Context, digest string, data []byte) inference.ContentOutcome {
								if r.nativeAdmissionQuestions(commitCtx, rwire.Subject{Account: s.Account, Program: s.Program}) != nil {
									t.Error("accepted publication inherited ingress wait")
								}
								return actual(commitCtx, digest, data)
							}
						}
						if gate == "output" && end == "complete revoked at effect" && out == inference.ContentResolved {
							if err := r.policy.Set(bound, inference.ActionComplete, "host:fixture", false); err != nil {
								t.Error(err)
							}
						}
						return commit, out
					},
				}
				provider, err := inference.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				isolated, _ := isolatedRuntime(t)
				endpoint := isolated.endpoint
				var host *inferenceservice.Host
				if mediated {
					// This inert hosted upstream exercises the mediated frame and
					// exact binding under its required remote execution placement.
					host, err = inferenceservice.ListenMediated(endpoint, provider, inference.ExecutionRemote, inference.ExecutionBinding{Host: "fixture", BindingID: "native1447-binding"})
				} else {
					host, err = inferenceservice.Listen(endpoint, provider)
				}
				if err != nil {
					provider.Close()
					t.Fatal(err)
				}
				host.AwaitQuestions = func(ctx context.Context, s inference.Subject, ids []string) (bool, error) {
					retry, err := r.awaitNativeQuestions(ctx, s, ids)
					ended <- ctx.Err()
					return retry, err
				}
				serveCtx, stop := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- host.Serve(serveCtx) }()
				defer func() { stop(); <-done; provider.Close() }()
				budget := 3 * time.Second
				if end == "deadline" {
					budget = 250 * time.Millisecond
				}
				call, cancel := context.WithTimeout(context.Background(), budget)
				defer cancel()
				transport := listen.FrameClient{Endpoint: endpoint, Timeout: 3 * time.Second}.WithContext(call)
				type reply struct {
					admission iwire.Admission
					err       error
				}
				returned := make(chan reply, 1)
				go func() {
					if gate == "output" {
						v, e := iwire.NewSpeechClient(transport).Start(iwire.SpeechRequest{Model: "fixture-model", Voice: "alloy", Text: "inert", Format: iwire.SpeechFormatWav, Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}})
						returned <- reply{v, e}
						return
					}
					parts := []iwire.Part{{Kind: iwire.PartKindText, Text: "inert"}}
					if gate == "input" {
						parts = append(parts, iwire.Part{Kind: iwire.PartKindImage, Digest: digest, MediaType: "image/png"})
					}
					v, e := iwire.NewChatClient(transport).Start(iwire.Request{Model: "fixture-model", Credential: credential, Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: parts}}})
					returned <- reply{v, e}
				}()
				var id string
				select {
				case id = <-asked:
				case v := <-returned:
					t.Fatalf("Start escaped first-use gate: %+v %v", v.admission, v.err)
				case <-time.After(time.Second):
					t.Fatal("no first-use question")
				}
				if id == "" {
					t.Fatal("empty first-use question")
				}
				nativeCtx := <-captured
				collector := r.nativeAdmissionQuestions(nativeCtx, bound)
				if collector == nil {
					t.Fatal("active native frame did not own its collector")
				}
				if deadline, ok := nativeCtx.Deadline(); !ok || time.Until(deadline) > 35*time.Second || time.Until(deadline) < 30*time.Second {
					t.Fatalf("native aggregate call deadline: %v %t", deadline, ok)
				}
				frame := nativeCtx.Value(collector).(*listen.FramedCall)
				peer := inference.PeerFromContext(nativeCtx)
				derived, stopDerived := context.WithCancel(nativeCtx)
				defer stopDerived()
				job, stopJob := context.WithTimeout(inference.WithPeer(context.Background(), peer), 10*time.Second)
				defer stopJob()
				background := context.WithValue(inference.WithAsked(inference.WithPeer(context.Background(), peer), collector), collector, frame)
				for name, ctx := range map[string]context.Context{"gateway collector with native peer": inference.WithAsked(job, &inference.Asked{}), "replaced collector": inference.WithAsked(nativeCtx, &inference.Asked{}), "derived lifetime": derived, "queued job ten-second context": job, "job with generic collector": inference.WithAsked(job, &inference.Asked{}), "background copied metadata": background} {
					if r.nativeAdmissionQuestions(ctx, bound) != nil {
						t.Fatalf("%s acquired native wait", name)
					}
				}
				if gate == "credential" && end == "allow" {
					assertJobFirstUseDoesNotOwnNativeWait(t, r, subject, peer, upstream.URL)
					otherCtx, stopOther := context.WithTimeout(context.Background(), 2*time.Second)
					otherReturn := make(chan error, 1)
					go func() {
						_, err := iwire.NewChatClient(listen.FrameClient{Endpoint: endpoint, Timeout: 2 * time.Second}.WithContext(otherCtx)).Start(iwire.Request{Model: "fixture-model", Credential: credential, Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "other call"}}}}})
						otherReturn <- err
					}()
					var other context.Context
					select {
					case other = <-captured:
					case <-time.After(time.Second):
						stopOther()
						t.Fatal("concurrent call did not reach admission")
					}
					otherCollector := inference.AskedFrom(other)
					mixed := context.WithValue(nativeCtx, collector, other.Value(otherCollector))
					if r.nativeAdmissionQuestions(mixed, bound) != nil || r.nativeAdmissionQuestions(inference.WithAsked(nativeCtx, otherCollector), bound) != nil {
						stopOther()
						t.Fatal("another call's frame/collector acquired native wait")
					}
					for reached := false; !reached; {
						select {
						case otherID := <-asked:
							reached = otherID == id
						case <-time.After(time.Second):
							stopOther()
							t.Fatal("second credential decision blocked behind waiting holder")
						}
					}
					stopOther()
					select {
					case err := <-otherReturn:
						if err == nil {
							t.Fatal("cancelled other request succeeded")
						}
					case <-time.After(time.Second):
						t.Fatal("other caller did not close")
					}
				}
				var admission iwire.Admission
				select {
				case v := <-returned:
					t.Fatalf("pending first-use gate returned: %+v %v", v.admission, v.err)
				case <-time.After(10 * time.Millisecond):
				}
				if posts.Load() != 0 {
					t.Fatal("paid request started before permission")
				}
				switch end {
				case "allow", "scope changed", "complete revoked", "complete revoked at effect":
					if end == "complete revoked" {
						if err := r.policy.Set(bound, inference.ActionComplete, "host:fixture", false); err != nil {
							t.Fatal(err)
						}
					}
					if _, _, err := book.AnswerApplication(id, "allow"); err != nil {
						t.Fatal(err)
					}
					snapshot, err := r.policy.OperatorSnapshot()
					if err != nil {
						t.Fatal(err)
					}
					if edit, err := a.applyOperatorAllow(t.Context(), rwire.Subject{Account: r.owner, Program: appProgram("panel")}, snapshot.Revision, id, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
						t.Fatal(edit, err)
					}
				case "saved allow", "block":
					actions, resource := []string{inference.ActionComplete}, "host:fixture"
					switch gate {
					case "credential":
						actions, resource = []string{credentials.ActionApply}, credentials.ResourceFor(credential)
					case "input":
						actions, resource = []string{contentWriteAction, contentReadAction}, "input:image"
					case "output":
						actions, resource = []string{contentWriteAction, contentReadAction}, "output:speech"
					}
					for _, action := range actions {
						if err := r.policy.Set(bound, action, resource, end == "saved allow"); err != nil {
							t.Fatal(err)
						}
					}
				case "refuse":
					if _, _, err := book.AnswerApplication(id, "refuse"); err != nil {
						t.Fatal(err)
					}
				case "close":
					cancel()
				}
				select {
				case v := <-returned:
					admission = v.admission
					if (end == "allow" || end == "saved allow") && (v.err != nil || v.admission.Outcome != iwire.StartOutcomeAccepted) {
						t.Fatalf("Allow did not admit: %+v %v", v.admission, v.err)
					}
					if end != "allow" && end != "saved allow" && v.admission.Outcome == iwire.StartOutcomeAccepted {
						t.Fatal("non-Allow admitted work")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("first-use gate did not settle")
				}
				select {
				case <-nativeCtx.Done():
				case <-time.After(time.Second):
					t.Fatal("framed admission did not close")
				}
				if r.nativeAdmissionQuestions(nativeCtx, bound) != nil {
					t.Fatal("stale frame retained native wait")
				}
				if end == "allow" || end == "saved allow" {
					if admission.Operation == "" {
						t.Fatal("accepted admission omitted operation")
					}
					select {
					case <-workStarted:
					case <-time.After(time.Second):
						t.Fatal("accepted operation did not survive ingress close")
					}
					releaseWork()
					observeCtx, stopObserve := context.WithTimeout(t.Context(), 2*time.Second)
					defer stopObserve()
					observeTransport := listen.FrameClient{Endpoint: endpoint, Timeout: 2 * time.Second}.WithContext(observeCtx)
					var page iwire.DeltaPage
					for !page.AtEnd && observeCtx.Err() == nil {
						if gate == "output" {
							page, err = iwire.NewSpeechClient(observeTransport).Observe(admission.Operation, page.Next, 32, 65536, 200)
						} else {
							page, err = iwire.NewChatClient(observeTransport).Observe(admission.Operation, page.Next, 32, 65536, 200)
						}
						if err != nil {
							t.Fatal(err)
						}
						if page.Outcome != iwire.PageOutcomePage {
							t.Fatalf("accepted operation observe: %+v", page)
						}
					}
					completed := false
					if page.AtEnd && len(page.Deltas) > 0 {
						last := page.Deltas[len(page.Deltas)-1]
						if gate == "output" {
							completed = last.SpeechEnd != nil && last.SpeechEnd.Outcome == iwire.ReplyOutcomeCompleted
						} else {
							completed = last.End != nil && last.End.Outcome == iwire.ReplyOutcomeCompleted
						}
					}
					if !completed {
						t.Fatalf("accepted work did not complete independently: %+v", page)
					}
					if posts.Load() != 1 {
						t.Fatalf("one Start issued %d paid requests", posts.Load())
					}
					if admits.Load() != 1 {
						t.Fatalf("one Start invoked provider admission %d times", admits.Load())
					}
				} else if admits.Load() != 0 {
					t.Fatalf("refused Start invoked provider admission %d times", admits.Load())
				}
				select {
				case err := <-ended:
					if (end == "close" || end == "deadline") && err == nil {
						t.Fatal("closed caller left gate context live")
					}
				case <-time.After(time.Second):
					t.Fatal("gate work not joined")
				}
				if end != "allow" && end != "saved allow" && posts.Load() != 0 {
					t.Fatal("refused/cancelled request reached upstream")
				}
			})
		}
	}
}

// INF-W10: a saved decision settles the waiting call even when nobody answers
// the old question. The same rights-change wake covers an explicit Block.
func TestPendingFirstUseWaitSettlesOnSavedDecision(t *testing.T) {
	for _, kind := range []string{"allow", "block", "default"} {
		t.Run(kind, func(t *testing.T) {
			r, a, _ := inputAsks(t)
			subject := rwire.Subject{Account: r.owner, Program: appProgram("waiting")}
			const target = "host:deepseek"
			if kind == "default" {
				if err := r.policy.ConfigureDefaults(r.owner, []rights.AccountDefault{{Name: "first-use-model", Action: inference.ActionComplete, Resource: target}}, nil); err != nil {
					t.Fatal(err)
				}
				revision, _, err := r.policy.DefaultsSnapshot(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if edit, err := r.policy.SetDefault(revision, "first-use-model", false, subject, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Fatalf("disable default: %+v %v", edit, err)
				}
			}
			asked := &inference.Asked{}
			r.decideAsking(inference.WithAsked(context.Background(), asked), subject, inference.ActionComplete, target)
			if len(asked.IDs()) != 1 {
				t.Fatalf("pending questions: %v", asked.IDs())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := make(chan bool, 1)
			go func() { result <- a.awaitQuestions(ctx, subject, asked.IDs()) }()
			if kind == "default" {
				revision, _, err := r.policy.DefaultsSnapshot(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if edit, err := r.policy.SetDefault(revision, "first-use-model", true, subject, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Fatalf("enable default: %+v %v", edit, err)
				}
			} else if err := r.policy.Set(subject, inference.ActionComplete, target, kind == "allow"); err != nil {
				t.Fatal(err)
			}
			select {
			case allowed := <-result:
				if allowed != (kind != "block") {
					t.Fatalf("saved %s released with allowed=%t", kind, allowed)
				}
			case <-time.After(2 * time.Second):
				cancel()
				<-result
				t.Fatalf("saved %s left the call waiting for a question answer", kind)
			}
		})
	}
}

// A generic deadline or window-owned collector leaves the wait with its owner.
// Framed native positives are exercised through the real service above.
func TestUnframedInferenceDecisionLeavesWaitingToItsOwner(t *testing.T) {
	for _, collector := range []bool{false, true} {
		t.Run(fmt.Sprint(collector), func(t *testing.T) {
			r, a, _ := inputAsks(t)
			var waits atomic.Int32
			r.awaitQuestions = func(context.Context, rwire.Subject, []string) bool { waits.Add(1); return false }
			r.firstUse = a.admitFirstUse
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if collector {
				ctx = inference.WithAsked(ctx, &inference.Asked{})
			}
			word, err := r.decideInference(ctx, inference.Subject{Account: r.owner, Program: appProgram("queued-job")}, inference.ActionComplete, "host:fixture")
			if err != nil || word != "not_granted" || waits.Load() != 0 {
				t.Fatalf("unowned admission wait: %s %v %d", word, err, waits.Load())
			}
		})
	}
}

// A desktop client's placeholder key is any non-empty key that is not in the
// minted local-key spelling (INF-W4). Its grant spends under each hosted
// server's declared credential (INF-W5); a damaged minted key stays refused.
func TestGatewayKeyRuleAcceptsAPlaceholder(t *testing.T) {
	o := &inferenceOperator{}
	subject := inference.Subject{Account: "S-1-5-21-fixture", Program: appProgram("jan")}
	for _, key := range []string{"placeholder", "sk-anything", "x"} {
		grant, word := o.verifyKey(context.Background(), subject, key)
		if word != gateway.KeyVerified || !grant.Declared || grant.Credential != "" {
			t.Fatalf("key %q: %+v %s", key, grant, word)
		}
	}
	for _, key := range []string{"", localKeyPrefix + "damaged"} {
		if grant, word := o.verifyKey(context.Background(), subject, key); word != gateway.KeyUnknown || grant.Declared {
			t.Fatalf("key %q: %+v %s", key, grant, word)
		}
	}
}

// A gateway call refused for want of a first-use answer names the question it
// admitted, and the runtime holds it (INF-W10): an unanswered question until
// the window's wait ends, a refuse at once, and an allow until its permit is
// committed.
func TestGatewayCallWaitsForItsFirstUseQuestion(t *testing.T) {
	r, a, book := inputAsks(t)
	jan := rwire.Subject{Account: r.owner, Program: appProgram("jan")}
	asked := &inference.Asked{}
	ctx := inference.WithAsked(context.Background(), asked)
	if d := r.decideAsking(ctx, jan, inference.ActionComplete, "host:deepseek"); d.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("decision %+v", d)
	}
	ids := asked.IDs()
	if len(ids) != 1 {
		t.Fatalf("the refusal named %v, want one question", ids)
	}
	// The pending question is named again by the next refusal.
	again := &inference.Asked{}
	r.decideAsking(inference.WithAsked(context.Background(), again), jan, inference.ActionComplete, "host:deepseek")
	if got := again.IDs(); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("a second refusal named %v, want %v", got, ids)
	}

	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if a.awaitQuestions(short, jan, ids) {
		t.Fatal("an unanswered question released the call")
	}
	// Another program's question holds nothing.
	if a.awaitQuestions(context.Background(), rwire.Subject{Account: r.owner, Program: appProgram("other")}, ids) {
		t.Fatal("another program's question released the call")
	}

	held, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	released := make(chan bool, 1)
	go func() { released <- a.awaitQuestions(held, jan, ids) }()
	if _, _, err := book.AnswerApplication(ids[0], "allow"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	panel := rwire.Subject{Account: r.owner, Program: appProgram("panel")}
	if edit, err := a.applyOperatorAllow(context.Background(), panel, snapshot.Revision, ids[0], func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("the Allow %+v %v", edit, err)
	}
	if !<-released {
		t.Fatal("an allowed question did not release the call")
	}

	refused := &inference.Asked{}
	bob := rwire.Subject{Account: r.owner, Program: appProgram("bob")}
	r.decideAsking(inference.WithAsked(context.Background(), refused), bob, inference.ActionComplete, "host:deepseek")
	if _, _, err := book.AnswerApplication(refused.IDs()[0], "refuse"); err != nil {
		t.Fatal(err)
	}
	if a.awaitQuestions(held, bob, refused.IDs()) {
		t.Fatal("a refused question released the call")
	}
}
