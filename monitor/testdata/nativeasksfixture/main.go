// Command nativeasksfixture owns disposable XPC runtime state for the native
// launcher test. It is not installed or used as a product service.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	cas "github.com/openabstractions/abstraction-cas/go/api"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 4 && (len(os.Args) != 5 || os.Args[4] != "choices") {
		return fmt.Errorf("usage: nativeasksfixture XPC_PREFIX STATE PANEL_PROGRAM [choices]")
	}
	ordinary := len(os.Args) == 5
	prefix, state, panel := os.Args[1], os.Args[2], os.Args[3]
	self, err := os.Executable()
	if err != nil {
		return err
	}
	endpoint := func(name string) string { return "xpc:" + prefix + "." + name }
	book, err := asks.LoadApplicationBook(filepath.Join(state, "questions.json"))
	if err != nil {
		return err
	}
	ids := map[string]string{}
	permissions := []string{"answer", "stale", "close"}
	if ordinary {
		permissions = []string{"permission"}
	}
	for _, name := range permissions {
		record, outcome, err := book.AskApplication("native-fixture", self, name, asks.FirstUseKey,
			map[string]string{"program": "/fixture/" + name + ".app", "action": "abstraction.model/lookup", "resource": "hf"}, listen.Seen{Why: "isolated fixture admission"})
		if err != nil || outcome != "pending" {
			return fmt.Errorf("admit %s: %s %v", name, outcome, err)
		}
		ids[name] = record.ID
	}
	sink, err := logging.OpenFileSink(filepath.Join(state, "logs.jsonl"))
	if err != nil {
		return err
	}
	defer sink.Close()
	policy, err := rights.LoadDecisionPolicy(filepath.Join(state, "policy.json"), []string{host.QuestionAskAction})
	if err != nil {
		return err
	}
	h, err := host.Listen(host.Options{
		Endpoint: endpoint("runtime"), LogEndpoint: endpoint("logging"), ConfigEndpoint: endpoint("config"),
		Sink: sink, ConfigStore: cas.BoundedFileStore{MaxBytes: 1 << 20}, ConfigUserKey: filepath.Join(state, "config.json"), ConfigWithoutMachine: true,
		QuestionBook: book, QuestionEndpoint: endpoint("asks"),
		QuestionAskPolicy: host.AskPolicyFromRights(policy),
		QuestionOperator: func(ctx context.Context, peer *identity.Peer) error {
			path, err := peer.Path.AtLeast(listen.Program.Path)
			if err != nil || (path != panel && path != self) || ctx.Err() != nil {
				return fmt.Errorf("fixture operator refused")
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	defer h.Close()
	if errs := h.StartupErrors(); len(errs) > 0 {
		return fmt.Errorf("startup: %v", errs)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if ordinary {
			if err := runChoiceCaller(ctx, endpoint("runtime"), state, self, policy); err != nil {
				fmt.Fprintln(os.Stderr, err)
				stop()
			}
			return
		}
		for ctx.Err() == nil {
			if _, err := os.Stat(filepath.Join(state, "stale-now")); err == nil {
				call, cancel := context.WithTimeout(ctx, 5*time.Second)
				machine := client.NewVerified(endpoint("runtime"), listen.ServerExpectation{Principal: identity.User{Kind: "posix", UID: os.Getuid(), GID: -1}, Program: self})
				operator, err := machine.ResolveAsksOperator(call, facade.Requirements{Scope: facade.ScopeLocal})
				if err == nil {
					decision, err := operator.AnswerQuestionIfPendingContext(call, ids["stale"], "refuse")
					if err == nil {
						_ = os.WriteFile(filepath.Join(state, "stale-outcome"), []byte(decision.Outcome.String()), 0600)
						fmt.Fprintln(os.Stderr, "stale fixture public answer:", decision.Outcome.String())
					} else {
						fmt.Fprintln(os.Stderr, "stale fixture public answer:", err)
					}
				} else {
					fmt.Fprintln(os.Stderr, "stale fixture public operator:", err)
				}
				cancel()
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	if !ordinary {
		ready, _ := json.Marshal(ids)
		if err := os.WriteFile(filepath.Join(state, "ready.json"), ready, 0600); err != nil {
			return err
		}
	}
	return h.Serve(ctx)
}

// The fixture is an ordinary application adopter. It retains references and
// observes outcomes through public native IPC; it never answers via the Book.
func runChoiceCaller(ctx context.Context, endpoint, state, self string, policy *rights.DecisionPolicy) error {
	machine := client.NewVerified(endpoint, listen.ServerExpectation{Principal: identity.User{Kind: "posix", UID: os.Getuid(), GID: -1}, Program: self})
	app, err := machine.ResolveAsksChoices(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return fmt.Errorf("fixture choice application: %w", err)
	}
	now := time.Now()
	deniedRequest := askclient.ChoiceRequest{RequestRef: askclient.RequestRef{Key: "before-rule", ReplayUntilMs: now.Add(2 * time.Minute).UnixMilli()}, Title: "Admission proof", PendingUntilMs: now.Add(time.Minute).UnixMilli(), Options: []askclient.ChoiceOption{{ID: "ok", Label: "OK"}}}
	denied, err := app.AskContext(ctx, deniedRequest)
	if err != nil || denied.Outcome != askclient.ChoiceOutcomeForbidden {
		return fmt.Errorf("ordinary admission before its exact rule: %+v %v", denied, err)
	}
	if err := policy.Set(rightsclient.Subject{Account: strconv.Itoa(os.Getuid()), Program: self}, host.QuestionAskAction, host.QuestionAskResource, true); err != nil {
		return err
	}
	before, err := os.ReadFile(filepath.Join(state, "policy.json"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(state, "rights-before.json"), before, 0600); err != nil {
		return err
	}
	proof, _ := json.Marshal(map[string]string{"before": "forbidden", "grantAction": host.QuestionAskAction, "grantResource": host.QuestionAskResource, "grantProgram": self})
	if err := os.WriteFile(filepath.Join(state, "choice-admission.json"), proof, 0600); err != nil {
		return err
	}
	requests := map[string]askclient.ChoiceRequest{}
	ids := map[string]string{}
	for _, name := range []string{"answer", "stale", "close", "expire"} {
		now := time.Now()
		pending := now.Add(time.Minute)
		request := askclient.ChoiceRequest{RequestRef: askclient.RequestRef{Key: name, ReplayUntilMs: now.Add(2 * time.Minute).UnixMilli()},
			Title: "Allow System — app fixture " + name, PendingUntilMs: pending.UnixMilli(),
			Options: []askclient.ChoiceOption{{ID: "replace", Label: "Replace file"}, {ID: "keep", Label: "Keep existing file"}, {ID: "cancel", Label: "Cancel this delivery"}}}
		requests[name] = request
		retained, _ := json.Marshal(requests)
		if err := os.WriteFile(filepath.Join(state, "choice-requests.json"), retained, 0600); err != nil {
			return err
		}
		admitted, err := app.AskContext(ctx, request)
		if err != nil || admitted.Outcome != askclient.ChoiceOutcomePending {
			return fmt.Errorf("ordinary admit %s: %+v %v", name, admitted, err)
		}
		ids[name] = admitted.ID
	}
	ready, _ := json.Marshal(ids)
	if err := os.WriteFile(filepath.Join(state, "ready.json"), ready, 0600); err != nil {
		return err
	}
	for ctx.Err() == nil {
		observations := map[string]askclient.ChoiceObservation{}
		for name, request := range requests {
			observation, err := app.ObserveContext(ctx, request.RequestRef, 0)
			if err != nil {
				return err
			}
			observations[name] = observation
		}
		data, _ := json.Marshal(observations)
		if err := os.WriteFile(filepath.Join(state, "choice-observations.json"), data, 0600); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}
