package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

type providerWatchReader struct {
	*fakeOperator
	displayed atomic.Bool
	reads     chan struct{}
}

func (r *providerWatchReader) ReadQuestionContentContext(ctx context.Context, id string) (askclient.QuestionContent, error) {
	out, err := r.fakeOperator.ReadQuestionContentContext(ctx, id)
	if r.displayed.Load() {
		select {
		case r.reads <- struct{}{}:
		default:
		}
	}
	return out, err
}

func TestProviderRetryWatchKeepsValidationAcrossOpenTicks(t *testing.T) {
	for _, change := range []string{"content", "deleted", "answer", "target", "invalid-target", "session", "locked"} {
		t.Run(change, func(t *testing.T) {
			record := firstUseFixture("provider-watch", nativePresenterProgram("requester.exe"), "abstraction.credentials/apply", "credential:deepseek")
			record.Option = "allow"
			details := providerPresenterDetails(t, record, askclient.PermissionTarget{Account: "S-1-5-21-test", Program: record.About, Action: "abstraction.credentials/apply", Resource: "credential:deepseek"})
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}, details: map[string]askclient.QuestionDetails{record.ID: details}, contents: map[string]askclient.QuestionContent{record.ID: {Question: askclient.QuestionAsker{Details: details}}}}
			reader := &providerWatchReader{fakeOperator: operator, reads: make(chan struct{}, 8)}
			session := unlockedSession()
			surface := &providerTestSurface{fakeSurface: &fakeSurface{block: true, dismissed: make(chan struct{})}, supported: true}
			var effects atomic.Int64
			p := &questionPresenter{session: session, surface: surface, operator: func(context.Context) (presenterOperator, error) { return reader, nil }, account: "S-1-5-21-test", watchEvery: time.Millisecond, budget: time.Second,
				providerForms: func(context.Context, *askclient.PermissionRequest, func(context.Context) error, func(context.Context) error) (*providerKeyForm, error) {
					return &providerKeyForm{Label: "DeepSeek", Allow: func(context.Context, []byte, int64) error { effects.Add(1); return nil }}, nil
				}}
			surface.during = func() {
				reader.displayed.Store(true)
				for tick := 0; tick < 2; tick++ {
					select {
					case <-reader.reads:
					case <-surface.dismissed:
						t.Fatal("retained answer dismissed before a change")
					case <-time.After(time.Second):
						t.Fatal("watch did not observe two open ticks")
					}
				}
				select {
				case <-surface.dismissed:
					t.Fatal("unchanged retained answer dismissed")
				default:
				}
				operator.mu.Lock()
				content := operator.contents[record.ID]
				current := *content.Question.Details.Question.Question.Record
				permission := *content.Question.Details.Permission
				permission.Targets = append([]askclient.PermissionTarget{}, permission.Targets...)
				switch change {
				case "content":
					current.Text += " changed"
					content.Question.Details.Question.Question.Record = &current
				case "deleted":
					content = askclient.QuestionContent{Question: askclient.QuestionAsker{Details: askclient.QuestionDetails{Question: askclient.AttributedQuestion{Question: askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeUnknown}}}}}
				case "answer":
					current.Option = "refuse"
					content.Question.Details.Question.Question.Record = &current
				case "target":
					permission.Targets[0].Resource, permission.Targets[1].Resource = "credential:openrouter", "host:openrouter"
					content.Question.Details.Permission = &permission
				case "invalid-target":
					permission.Targets[1].Resource = "host:other"
					content.Question.Details.Permission = &permission
				case "session":
					session.revision.Add(1)
				case "locked":
					session.unlocked.Store(false)
				}
				operator.contents[record.ID] = content
				operator.mu.Unlock()
			}
			result := p.reopenProvider(t.Context(), record.ID)
			select {
			case <-surface.dismissed:
			default:
				t.Fatal("changed provider form was not dismissed", result)
			}
			if effects.Load() != 0 || len(operator.sent()) != 0 {
				t.Fatal("watch invalidation authorized an effect", effects.Load(), operator.sent())
			}
		})
	}
}
