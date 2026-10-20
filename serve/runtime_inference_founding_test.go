package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-inference/adapters/go/gateway"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// foundingBound binds every window connection to one program.
type foundingBound struct{ subject inference.Subject }

func (b foundingBound) Subject() inference.Subject { return b.subject }
func (foundingBound) Rung() string                 { return "fixture/bound" }
func (foundingBound) Peer() *identity.Peer         { return nil }
func (foundingBound) Recheck() error               { return nil }
func (foundingBound) Close() error                 { return nil }

// questionsBeforeFirstSpend is how many first-use questions a client with no
// rules meets before its first hosted call completes. P-M9's ceiling is one;
// complete on the server and the credential's use are asked separately
// until the coordinator decides how to pair them (TODO.md, P-M9 first
// spend). The count flips to 1 with that decision.
const questionsBeforeFirstSpend = 2

// The founding use through the runtime's own decision point and asks book
// (P-M9, INF-W5, INF-W10): a client with no rules and any key lists the hosted
// server's model, its first chat call is held while the person answers every
// question it raised, and completes; a revoke asks again and holds the next
// call, which the person's refuse answers with its reason.
func TestFoundingUseThroughTheRuntimesDecisionPoint(t *testing.T) {
	dir := t.TempDir()
	r := testRights(t, dir)
	if err := r.install([]string{inference.ActionComplete, credentials.ActionApply}, nil); err != nil {
		t.Fatal(err)
	}
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{Credentials: true})
	if err != nil {
		t.Fatal(err)
	}
	r.policy.Definitions = catalog
	a, book := firstUseBook(t)
	a.rights = r
	r.firstUse, r.awaitQuestions = a.admitFirstUse, a.awaitQuestions
	c := &runtimeCredentials{runtimeRights: r}
	jan := inference.Subject{Account: r.owner, Program: appProgram("jan")}
	panel := rwire.Subject{Account: r.owner, Program: r.operators[len(r.operators)-1]}

	var posts sync.Mutex
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model"},{"id":"deepseek-reasoner","object":"model"}]}`))
			return
		}
		io.Copy(io.Discard, r.Body)
		posts.Lock()
		upstreamCalls++
		posts.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	routes := router.New(router.NewHosted("deepseek", upstream.URL, router.WireOpenAICompatible, "deepseek"))
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer fixture"}, nil
	})
	routes.Survey()
	provider, err := inference.New(inference.Config{Router: routes, SurveyAge: time.Hour, Decide: r.decideInference,
		// The holder's Apply decides use through the runtime's decision
		// point, which admits the credential's first-use question.
		Apply: func(ctx context.Context, s inference.Subject, _, name, _ string) (map[string]string, string) {
			outcome, err := c.decide(ctx, cwire.Subject{Account: s.Account, Program: s.Program}, credentials.ActionApply, credentials.ResourceFor(name))
			if err != nil || outcome != rwire.DecisionOutcomePermitted.String() {
				return nil, "not_permitted"
			}
			return map[string]string{"Authorization": "Bearer fixture"}, "applied"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })

	// The presenter fixture: each question the window holds a call on is
	// answered with answer, an allow committed as the Panel commits it.
	var mu sync.Mutex
	answer := "allow"
	var held []string
	o := &inferenceOperator{credentials: c, router: routes}
	window, err := gateway.Listen("127.0.0.1:0", gateway.Config{Chat: provider, Account: jan.Account, Keys: o.verifyKey, Models: o.windowModels,
		Bind:         func(net.Conn, time.Time) (gateway.Bound, error) { return foundingBound{jan}, nil },
		QuestionWait: 10 * time.Second,
		Await: func(ctx context.Context, s inference.Subject, ids []string) bool {
			mu.Lock()
			reply := answer
			for _, id := range ids {
				if !slices.Contains(held, id) {
					held = append(held, id)
				}
			}
			mu.Unlock()
			go func() {
				for _, id := range ids {
					if _, _, err := book.AnswerApplication(id, reply); err != nil {
						t.Error(err)
						return
					}
					if reply != "allow" {
						continue
					}
					snapshot, err := r.policy.OperatorSnapshot()
					if err != nil {
						t.Error(err)
						return
					}
					if edit, err := a.applyOperatorAllow(context.Background(), panel, snapshot.Revision, id, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
						t.Errorf("the Allow of %s: %+v %v", id, edit, err)
					}
				}
			}()
			return r.awaitQuestions(ctx, rwire.Subject{Account: s.Account, Program: s.Program}, ids)
		}})
	if err != nil {
		t.Fatal(err)
	}
	go window.Serve(context.Background())
	t.Cleanup(func() { window.Close() })
	base := "http://" + window.Addr().String() + "/v1"
	send := func(method, path string, body []byte) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer placeholder")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	// No rules yet: the model list names the hosted server's models.
	status, listed := send(http.MethodGet, "/models", nil)
	var models struct{ Data []struct{ ID string } }
	if json.Unmarshal([]byte(listed), &models); status != http.StatusOK || len(models.Data) != 2 || models.Data[1].ID != "deepseek-reasoner" {
		t.Fatalf("models before any rule: %d %s", status, listed)
	}

	chat := []byte(`{"model":"deepseek-reasoner","messages":[{"role":"user","content":"hi"}]}`)
	status, answered := send(http.MethodPost, "/chat/completions", chat)
	if status != http.StatusOK || !strings.Contains(answered, "Hello.") {
		t.Fatalf("first call: %d %s", status, answered)
	}
	records, _, err := book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	for _, q := range records {
		if samePrograms(q.About, jan.Program) {
			asked++
		}
	}
	mu.Lock()
	heldOn := len(held)
	mu.Unlock()
	if asked != questionsBeforeFirstSpend || heldOn != questionsBeforeFirstSpend {
		t.Fatalf("%d questions admitted and %d held on before the first spend, want %d", asked, heldOn, questionsBeforeFirstSpend)
	}

	// A revoke removes the rule: the next call asks again, is held, and the
	// person's refuse answers it with its reason before any upstream request.
	subject := rwire.Subject{Account: jan.Account, Program: jan.Program}
	if err := r.policy.Revoke(subject, inference.ActionComplete, inference.ResourceHost("deepseek")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	answer, held = "refuse", nil
	mu.Unlock()
	posts.Lock()
	before := upstreamCalls
	posts.Unlock()
	status, answered = send(http.MethodPost, "/chat/completions", chat)
	if status != http.StatusForbidden || !strings.Contains(answered, "rights:not_granted") {
		t.Fatalf("after the revoke: %d %s", status, answered)
	}
	mu.Lock()
	heldOn = len(held)
	mu.Unlock()
	posts.Lock()
	after := upstreamCalls
	posts.Unlock()
	if heldOn != 1 || after != before {
		t.Fatalf("after the revoke the call was held on %d questions and reached the server %d times", heldOn, after-before)
	}
}
