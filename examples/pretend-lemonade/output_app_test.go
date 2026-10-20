package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	asksbook "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	asks "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
)

func TestAppOwnedOutputChoiceRequiresNoOperatorGrant(t *testing.T) {
	book, err := asksbook.LoadApplicationBook(filepath.Join(t.TempDir(), "choices.json"))
	if err != nil {
		t.Fatal(err)
	}
	serviceRuntime(t, func(o *host.Options) {
		o.QuestionBook, o.QuestionEndpoint = book, o.JobEndpoint+"-asks"
		o.QuestionOperator = func(context.Context, *identity.Peer) error { return asksservice.ErrOperatorForbidden }
	})
	payload := []byte("same completed download")
	var transfers atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transfers.Add(1)
		http.ServeContent(w, r, "weights.bin", time.Time{}, bytes.NewReader(payload))
	}))
	defer srv.Close()
	dir := t.TempDir()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	args := []string{"--out", dir, srv.URL + "/weights.bin", strconv.Itoa(len(payload)), digest}
	var initial bytes.Buffer
	if err := run(args, &initial); err != nil {
		t.Fatal(err)
	}
	operation := submittedOperation(initial.String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	op, err := machine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	if page, err := op.ListChoicesContext(ctx, "", 64); err != nil || page.Outcome != asks.OperatorPageOutcomeForbidden {
		t.Fatalf("fixture allowed operator access: %+v %v", page, err)
	}
	for _, option := range []string{"replace", "keep", "cancel"} {
		t.Run(option, func(t *testing.T) {
			where := filepath.Join(dir, "weights.bin")
			old := []byte("existing output")
			if err := os.WriteFile(where, old, 0o600); err != nil {
				t.Fatal(err)
			}
			inputPath := filepath.Join(t.TempDir(), "choice-input")
			if err := os.WriteFile(inputPath, []byte(option+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			input, err := os.Open(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			var out bytes.Buffer
			if err := runInput(append([]string{"--ask-before-replace"}, args...), &out, input); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			want := old
			if option == "replace" {
				want = payload
			}
			got, err := os.ReadFile(where)
			if err != nil || !bytes.Equal(got, want) || submittedOperation(out.String()) != operation || transfers.Load() != 1 {
				t.Fatalf("app choice changed output/job incorrectly: %q transfers=%d err=%v\n%s", got, transfers.Load(), err, out.String())
			}
		})
	}
}

// The app's selected option and the service's retained answer can differ when
// another answer wins. Copies require observation of that retained answer.
type appOutputChoice struct {
	q                               asks.ChoiceRequest
	winner                          *asks.ChoiceAnswer
	answer, reconcile, ack, observe int
	lost                            string
	observedWinner                  bool
}

func (c *appOutputChoice) AskContext(_ context.Context, q asks.ChoiceRequest) (asks.ChoiceObservation, error) {
	if !reflect.DeepEqual(q, c.q) {
		return asks.ChoiceObservation{}, errors.New("changed app choice intent")
	}
	return asks.ChoiceObservation{Outcome: asks.ChoiceOutcomePending, ID: "record"}, nil
}

func (c *appOutputChoice) ObserveContext(_ context.Context, ref asks.RequestRef, waitMS int64) (asks.ChoiceObservation, error) {
	if ref != c.q.RequestRef || waitMS > 100 {
		return asks.ChoiceObservation{}, errors.New("invalid app observation")
	}
	c.observe++
	if c.winner != nil {
		c.observedWinner = true
		return asks.ChoiceObservation{Outcome: asks.ChoiceOutcomeAnswered, ID: c.winner.ID, Answer: c.winner}, nil
	}
	if time.Now().UnixMilli() >= c.q.PendingUntilMs {
		return asks.ChoiceObservation{Outcome: asks.ChoiceOutcomeExpired}, nil
	}
	return asks.ChoiceObservation{Outcome: asks.ChoiceOutcomePending, ID: "record"}, nil
}

func (c *appOutputChoice) record(option, actor string) {
	c.winner = &asks.ChoiceAnswer{ID: "record", Option: option, AnsweredBy: actor, AnsweredMs: time.Now().UnixMilli()}
}

func (c *appOutputChoice) AnswerContext(_ context.Context, ref asks.RequestRef, option string) (asks.ChoiceDecision, error) {
	if ref != c.q.RequestRef {
		return asks.ChoiceDecision{}, errors.New("changed app answer reference")
	}
	c.answer++
	if c.winner != nil {
		return asks.ChoiceDecision{Outcome: asks.ChoiceDecisionOutcomeConflict}, nil
	}
	if c.lost != "before" {
		c.record(option, "requesting-app")
	}
	if c.lost != "" {
		return asks.ChoiceDecision{}, errors.New("dropped app answer reply")
	}
	return asks.ChoiceDecision{Outcome: asks.ChoiceDecisionOutcomeAnswered}, nil
}

func (c *appOutputChoice) ReconcileAnswerContext(_ context.Context, ref asks.RequestRef, option string) (asks.ChoiceDecision, error) {
	if ref != c.q.RequestRef || c.answer != 1 {
		return asks.ChoiceDecision{}, errors.New("invalid app answer reconciliation")
	}
	c.reconcile++
	if c.winner == nil {
		c.record(option, "requesting-app")
	}
	return asks.ChoiceDecision{Outcome: asks.ChoiceDecisionOutcomeAnswered}, nil
}

func (c *appOutputChoice) AcknowledgeContext(_ context.Context, ref asks.RequestRef, answer asks.ChoiceAnswer) (asks.ChoiceAcknowledgement, error) {
	if ref != c.q.RequestRef || c.winner == nil || answer != *c.winner || !c.observedWinner {
		return asks.ChoiceAcknowledgement{}, errors.New("acknowledgement did not retain the observed winner")
	}
	c.ack++
	return asks.ChoiceAcknowledgement{Outcome: asks.ChoiceAckOutcomeAcknowledged}, nil
}

func TestAppOutputChoiceUsesRetainedWinner(t *testing.T) {
	for _, mode := range []string{"replace", "operator-wins", "lost-before", "lost-after"} {
		t.Run(mode, func(t *testing.T) {
			where := filepath.Join(t.TempDir(), "weights.bin")
			old, payload := []byte("existing output"), []byte("verified result")
			if err := os.WriteFile(where, old, 0o600); err != nil {
				t.Fatal(err)
			}
			q, err := outputChoice(context.Background(), where)
			if err != nil {
				t.Fatal(err)
			}
			c := &appOutputChoice{q: q}
			if mode == "lost-before" {
				c.lost = "before"
			} else if mode == "lost-after" {
				c.lost = "after"
			}
			input := func(_ context.Context, maximum time.Duration) (string, bool, error) {
				if maximum > 100*time.Millisecond {
					t.Fatal("app input can block observation")
				}
				if mode == "operator-wins" {
					c.record("keep", "operator")
				}
				return "replace", true, nil
			}
			copies := 0
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
			_, err = applyOutputChoice(context.Background(), c, q, where, int64(len(payload)), digest, func(_ context.Context, w io.Writer) (int64, error) {
				if !c.observedWinner {
					t.Fatal("copy preceded retained-answer observation")
				}
				copies++
				n, err := w.Write(payload)
				return int64(n), err
			}, &bytes.Buffer{}, input)
			want, wantCopies, wantReconcile := payload, 1, 0
			if mode == "operator-wins" {
				want, wantCopies = old, 0
			} else if mode == "lost-before" {
				wantReconcile = 1
			}
			got, readErr := os.ReadFile(where)
			if err != nil || readErr != nil || !bytes.Equal(got, want) || copies != wantCopies || c.answer != 1 || c.reconcile != wantReconcile || c.ack != 1 {
				t.Fatalf("winner/effect/retry mismatch: got=%q copies=%d answer=%d reconcile=%d ack=%d err=%v read=%v", got, copies, c.answer, c.reconcile, c.ack, err, readErr)
			}
		})
	}
}

func TestAppOutputChoiceClosedInputKeepsFile(t *testing.T) {
	for _, inputError := range []error{io.EOF, context.Canceled, errors.New("closed stdin")} {
		t.Run(inputError.Error(), func(t *testing.T) {
			where := filepath.Join(t.TempDir(), "weights.bin")
			old := []byte("keep this output")
			if err := os.WriteFile(where, old, 0o600); err != nil {
				t.Fatal(err)
			}
			q, err := outputChoice(context.Background(), where)
			if err != nil {
				t.Fatal(err)
			}
			c := &appOutputChoice{q: q}
			copies := 0
			_, err = applyOutputChoice(context.Background(), c, q, where, 0, "unused", func(context.Context, io.Writer) (int64, error) {
				copies++
				return 0, nil
			}, &bytes.Buffer{}, func(context.Context, time.Duration) (string, bool, error) { return "", false, inputError })
			got, readErr := os.ReadFile(where)
			if !errors.Is(err, inputError) || readErr != nil || !bytes.Equal(got, old) || copies != 0 || c.answer != 0 || c.ack != 0 {
				t.Fatalf("closed input selected or mutated: answer=%d copies=%d ack=%d got=%q err=%v", c.answer, copies, c.ack, got, err)
			}
		})
	}
}

func TestAppOutputChoiceObservesWinnerWhenInputDeadlineCloses(t *testing.T) {
	for _, answered := range []bool{false, true} {
		t.Run(fmt.Sprint(answered), func(t *testing.T) {
			q, err := outputChoice(context.Background(), "weights.bin")
			if err != nil {
				t.Fatal(err)
			}
			q.PendingUntilMs = time.Now().Add(30 * time.Millisecond).UnixMilli()
			c := &appOutputChoice{q: q}
			answer, err := waitOutputChoice(context.Background(), c, q, "weights.bin", &bytes.Buffer{}, func(ctx context.Context, _ time.Duration) (string, bool, error) {
				if answered {
					c.record("keep", "operator")
				}
				<-ctx.Done()
				return "", false, ctx.Err()
			})
			if answered && (err != nil || answer.Option != "keep") || !answered && err == nil || c.answer != 0 || c.observe != 1 {
				t.Fatalf("deadline lost a winner or selected implicitly: %+v observe=%d submissions=%d err=%v", answer, c.observe, c.answer, err)
			}
		})
	}
}
