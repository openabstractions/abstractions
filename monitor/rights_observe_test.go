package main

import (
	"context"
	"errors"
	facade "github.com/openabstractions/abstraction-facade/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type revisionSourceFunc func(context.Context, string, int64) (rightsclient.PolicyPage, error)

func (f revisionSourceFunc) ListPolicyContext(ctx context.Context, cursor string, limit int64) (rightsclient.PolicyPage, error) {
	return f(ctx, cursor, limit)
}

func TestRightsWatchHealthAndCancellation(t *testing.T) {
	for _, outcome := range []string{"unavailable", "forbidden"} {
		t.Run(outcome, func(t *testing.T) {
			var calls atomic.Int32
			reader := revisionSourceFunc(func(ctx context.Context, _ string, _ int64) (rightsclient.PolicyPage, error) {
				if calls.Add(1) <= 2 {
					return rightsclient.PolicyPage{Outcome: wire.PolicyPageOutcomePage, Revision: "old"}, nil
				}
				if outcome == "unavailable" {
					return rightsclient.PolicyPage{}, errors.New("outage")
				}
				return rightsclient.PolicyPage{Outcome: wire.PolicyPageOutcomeForbidden}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := followRightsRevision(ctx, reader, "old", time.Time{}, time.Millisecond, 100*time.Millisecond)
			if err != nil || got.Outcome != outcome || got.Revision != "" {
				t.Fatalf("last good state concealed refusal: %+v %v", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var calls atomic.Int32
	reader := revisionSourceFunc(func(ctx context.Context, _ string, _ int64) (rightsclient.PolicyPage, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return rightsclient.PolicyPage{}, ctx.Err()
	})
	done := make(chan error, 1)
	go func() {
		_, err := followRightsRevision(ctx, reader, "old", time.Time{}, time.Millisecond, time.Millisecond)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled watch remained live")
	}
	if calls.Load() != 1 {
		t.Fatal("cancel initiated extra IPC read", calls.Load())
	}
}

func TestPanelRightsWatchSecondOperatorExpiryAndAdmission(t *testing.T) {
	own(t)
	var allow, outage atomic.Bool
	allow.Store(true)
	panelRightsRuntime(t, &allow, &outage)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	second, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	page, err := second.ListPolicyContext(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rule := rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: "watched target", Program: program}, Action: panelRightsAction, Resource: "exact-watch-resource", Permit: true}
	h := (&servicePanel{}).handler("test-key")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- panelRequest(t, h, "/rights/observe?revision="+url.QueryEscape(page.Revision), nil) }()
	// This public operator call is independent of the Panel's edit route.
	edit, err := second.SetRuleContext(ctx, page.Revision, rule)
	if err != nil || edit.Outcome != wire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	select {
	case response := <-done:
		got := decodePanel[rightsObservation](t, response.Code, response.Body.Bytes())
		if got.Outcome != "changed" || got.Revision != edit.Revision {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second operator change did not invalidate Panel")
	}
	expiring, err := second.SetRuleForContext(ctx, edit.Revision, rule, 250*time.Millisecond, "expiry watch fixture")
	if err != nil || expiring.Outcome != wire.PolicyEditOutcomeApplied {
		t.Fatal(expiring, err)
	}
	read, err := second.ReadRuleContext(ctx, rule.Subject, rule.Action, rule.Resource)
	if err != nil || read.Record == nil {
		t.Fatal(read, err)
	}
	expiry, err := time.Parse("2006-01-02T15:04:05.000Z", read.Record.Expires)
	if err != nil {
		t.Fatal(err)
	}
	got, err := followRightsRevision(ctx, second, expiring.Revision, expiry, 10*time.Millisecond, time.Second)
	if err != nil || got.Outcome != "changed" || got.Revision != expiring.Revision {
		t.Fatal("expiry changed content revision or failed invalidation", got, err)
	}
	read, err = second.ReadRuleContext(ctx, rule.Subject, rule.Action, rule.Resource)
	if err != nil || read.Outcome != wire.RuleReadOutcomeExpired || read.Revision != expiring.Revision {
		t.Fatal("expiry not service-confirmed", read, err)
	}
	listed, err := second.ListPolicyContext(ctx, "", 16)
	if err != nil {
		t.Fatal(err)
	}
	view := presentPolicyPage(ctx, second, listed)
	if len(view.Rules) != 1 || !view.Rules[0].Expired {
		t.Fatal("expired view omitted service outcome", view)
	}
	for i, want := range []string{"forbidden", "unavailable"} {
		allow.Store(i != 0)
		outage.Store(i == 1)
		response := panelRequest(t, h, "/rights/observe?revision="+url.QueryEscape(expiring.Revision), nil)
		if got := decodePanel[rightsObservation](t, response.Code, response.Body.Bytes()); got.Outcome != want || got.Revision != "" {
			t.Fatal(got)
		}
	}
}

func TestPanelRightsWatchInputAndCancelledRequest(t *testing.T) {
	h := (&servicePanel{}).handler("test-key")
	for _, path := range []string{"/rights/observe?refresh_at=tomorrow", "/rights/observe?revision=" + url.QueryEscape(string([]byte{1}))} {
		if response := panelRequest(t, h, path, nil); response.Code != 400 {
			t.Fatal(response.Code, response.Body)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/rights/observe", nil).WithContext(ctx)
	req.Host = "127.0.0.1:8734"
	req.Header.Set("X-Panel-Key", "test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Body.Len() != 0 {
		t.Fatal("cancelled request emitted observation", w.Body)
	}
}
