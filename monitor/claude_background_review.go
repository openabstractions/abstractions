package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// This requests an existing service-admitted one-time content change question.
// Its answer changes no OA Rights rule. Only a fresh, exact operator answer can
// continue the separately owned host-settings transaction.
func (p *servicePanel) reviewClaudeBackground(ctx context.Context, cli claudePanelCLI, state string) (bool, error) {
	return p.reviewClaudeBackgroundScope(ctx, cli, state, "messages")
}
func (p *servicePanel) reviewClaudeAsksBackground(ctx context.Context, cli claudePanelCLI, state string) (bool, error) {
	return p.reviewClaudeBackgroundScope(ctx, cli, state, "asks")
}
func (p *servicePanel) reviewClaudeBackgroundScope(ctx context.Context, cli claudePanelCLI, state, scope string) (bool, error) {
	if err := cli.Check(ctx); err != nil {
		return false, err
	}
	application, err := panelMachine().ResolveAsks(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return false, err
	}
	operator, err := panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return false, err
	}
	program, err := os.Executable()
	if err != nil {
		return false, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return false, err
	}
	created := time.Now().UTC().Truncate(time.Millisecond)
	expires := created.Add(2 * time.Minute)
	ctx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	rule, target, replacement := "mcp__OpenAbstractions__oa_messages_watch", "Claude Code's user background-message permission", "persistently allow only OpenAbstractions message-header checks while idle; preserve other permissions; Disable removes only the approval OpenAbstractions adds"
	if scope == "asks" {
		rule, target, replacement = "mcp__OpenAbstractions__oa_asks_watch", "Claude Code's user background-Asks permission", "allow idle eligible-Asks ID checks and wake notices; no answering enrollment or person-only answers; preserve messages and other permissions; undo only this approval"
	} else if scope != "messages" {
		return false, errors.New("background scope unavailable")
	}
	frozen, _ := json.Marshal([]string{cli.Program(), cli.FileKey(), state, rule, hex.EncodeToString(nonce[:])})
	digest := sha256.Sum256(frozen)
	question := askclient.ApplicationQuestion{RequestKey: "claude-watch-" + hex.EncodeToString(nonce[:]), Key: "content.replace", Slots: map[string]string{
		"target":      target,
		"replacement": replacement,
		"code":        hex.EncodeToString(digest[:10]), "until": expires.Format(time.RFC3339),
	}}
	observation, err := application.AskContext(ctx, question)
	id := ""
	for err == nil {
		if observation.Answer != nil {
			if id != "" && id != observation.Answer.ID {
				return false, errors.New("background review occurrence changed")
			}
			id = observation.Answer.ID
		}
		if observation.Outcome != askclient.ObservationOutcomePending {
			break
		}
		observation, err = application.ObserveContext(ctx, question.RequestKey, 1000)
	}
	if err != nil || ctx.Err() != nil {
		if id != "" {
			cleanup, close := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer close()
			_, _ = operator.RetireQuestionContext(cleanup, id)
		}
		return false, errors.New("background review did not complete")
	}
	if observation.Outcome != askclient.ObservationOutcomeAnswered || id == "" || observation.Answer == nil || observation.Answer.Pending || observation.Answer.Option != "once" || !observation.Answer.Yes || observation.Answer.Kept {
		return false, errors.New("background review is unavailable")
	}
	attributed, err := operator.ReadAttributedQuestionContext(ctx, id)
	if err != nil {
		return false, err
	}
	if !approvedClaudeBackground(attributed.Question, id, program, question, created, expires, time.Now().UTC()) {
		return false, nil
	}
	if err := cli.Check(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func approvedClaudeBackground(read askclient.OperatorQuestion, id, program string, question askclient.ApplicationQuestion, created, expires, now time.Time) bool {
	if !now.Before(expires) || read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil || read.AnsweredBy == nil || *read.AnsweredBy != askclient.AnswererOperator {
		return false
	}
	record := read.Record
	if record.ID != id || record.Key != "content.replace" || record.Key != question.Key || record.About != question.Slots["target"] || record.Asker != asks.AskerDisplay(program) || record.Option != "once" || !record.Yes || record.Kept {
		return false
	}
	template, ok := asks.Find(question.Key)
	if !ok {
		return false
	}
	text, err := template.Render(asks.Ask{Asker: asks.AskerDisplay(program), Slots: question.Slots})
	if err != nil || text != record.Text {
		return false
	}
	answered, err := time.Parse(time.RFC3339Nano, record.Answered)
	return err == nil && !answered.Before(created) && answered.Before(expires) && !answered.After(now)
}
