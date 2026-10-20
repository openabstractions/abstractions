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
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	asksbook "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	asks "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

func choicesRuntime(t *testing.T) (*asks.ChoiceClient, *asks.Operator) {
	t.Helper()
	book, err := asksbook.LoadApplicationBook(filepath.Join(t.TempDir(), "choices.json"))
	if err != nil {
		t.Fatal(err)
	}
	serviceRuntime(t, func(o *host.Options) {
		o.QuestionBook, o.QuestionEndpoint = book, o.JobEndpoint+"-asks"
		// This test executable is the sole enrolled operator for its runtime.
		o.QuestionOperator = func(ctx context.Context, peer *identity.Peer) error {
			process, err := peer.Process.AtLeast(listen.Program.Process)
			if err != nil || process.PID != os.Getpid() {
				return asksservice.ErrOperatorForbidden
			}
			return ctx.Err()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := machine().ResolveAsksChoices(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	op, err := machine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	return c, op
}

// This is the existing CLI through real facade/job/choice services. Choosing an
// output action never changes the already completed job or transfers it again.
func TestExistingOutputChoicesPreserveCompletedJob(t *testing.T) {
	c, op := choicesRuntime(t)
	payload := bytes.Repeat([]byte("verified weights\n"), 4096)
	var transfers atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transfers.Add(1)
		http.ServeContent(w, r, "weights.bin", time.Time{}, bytes.NewReader(payload))
	}))
	defer srv.Close()
	dir := t.TempDir()
	source := srv.URL + "/weights.bin"
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	args := []string{"--out", dir, source, strconv.Itoa(len(payload)), digest}
	var original bytes.Buffer
	if err := run(args, &original); err != nil {
		t.Fatal(err)
	}
	operation := submittedOperation(original.String())
	if operation == "" {
		t.Fatal(original.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobs, err := machine().ResolveJobs(ctx, facade.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	window, err := jobs.GetHistoryWindow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref := api.RequestIdentity{Key: fmt.Sprintf("pretend-lemonade-%x", sha256.Sum256([]byte(source+"\n"+strconv.Itoa(len(payload))+"\n"+digest))), HistoryEpoch: window.HistoryEpoch}
	keys := map[string]bool{}
	for _, option := range []string{"replace", "keep", "cancel", "dismiss"} {
		t.Run(option, func(t *testing.T) {
			where := filepath.Join(dir, "weights.bin")
			old := []byte("existing application output for " + option)
			if err := os.WriteFile(where, old, 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- run(append([]string{"--ask-before-replace", "--wait-output-choice"}, args...), &out) }()
			var pending asks.ChoiceRecord
			for pending.ID == "" {
				page, err := op.ListChoicesContext(ctx, "", 64)
				if err != nil {
					t.Fatal(err)
				}
				for _, record := range page.Records {
					if record.State == asks.ChoiceStatePending {
						pending = record
					}
				}
				if pending.ID == "" {
					if err := retryChoice(ctx); err != nil {
						t.Fatal("app did not ask", err)
					}
				}
			}
			if keys[pending.Request.RequestRef.Key] {
				t.Fatal("new invocation reused the previous local intent")
			}
			keys[pending.Request.RequestRef.Key] = true
			var decision asks.ChoiceDecision
			if option == "dismiss" {
				decision, err = op.DismissChoiceContext(ctx, pending.ID)
			} else {
				decision, err = op.AnswerChoiceContext(ctx, pending.ID, option)
			}
			if err != nil || decision.Record == nil {
				t.Fatal(decision, err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%v\n%s", err, out.String())
				}
			case <-ctx.Done():
				t.Fatal("app failed to finish")
			}
			want := old
			if option == "replace" {
				want = payload
			}
			got, err := os.ReadFile(where)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("local output changed incorrectly: %q %v", got, err)
			}
			observed, err := jobs.ObserveWork(ctx, ref)
			if err != nil || observed.Snapshot == nil || observed.Snapshot.State != api.WorkStateComplete || submittedOperation(out.String()) != operation || transfers.Load() != 1 {
				t.Fatalf("completed download changed or repeated: %+v transfers=%d output=%s error=%v", observed, transfers.Load(), out.String(), err)
			}
			gone, err := c.ObserveContext(ctx, pending.Request.RequestRef, 0)
			if err != nil || gone.Outcome != asks.ChoiceOutcomeGone {
				t.Fatalf("local decision was not acknowledged: %+v %v", gone, err)
			}
		})
	}
}

func submittedOperation(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "submitted ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "submitted "))
		}
	}
	return ""
}

func TestOptionalChoiceCreatesMissingOutputWithoutChoiceService(t *testing.T) {
	serviceRuntime(t) // This runtime deliberately has no choice service.
	payload := []byte("first output")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "first.bin", time.Time{}, bytes.NewReader(payload))
	}))
	defer srv.Close()
	dir := t.TempDir()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	args := []string{"--ask-before-replace", "--out", dir, srv.URL + "/first.bin", strconv.Itoa(len(payload)), digest}
	if err := run(args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	where := filepath.Join(dir, "first.bin")
	if !matchesOutput(where, int64(len(payload)), digest) {
		t.Fatal("first output was not delivered")
	}
	old := []byte("existing file")
	if err := os.WriteFile(where, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(args, &bytes.Buffer{}); err == nil {
		t.Fatal("existing output was overwritten without a choice service")
	}
	got, err := os.ReadFile(where)
	if err != nil || !bytes.Equal(got, old) {
		t.Fatal("missing choice service damaged old output", err)
	}
}

// Replies are dropped after the real service durably processed them. Calls
// still cross the published client and verified native fixture transport.
type lostChoiceReplies struct {
	c                 *asks.ChoiceClient
	op                *asks.Operator
	q                 asks.ChoiceRequest
	ask, observe, ack int
	where, digest     string
	size              int64
}

func (l *lostChoiceReplies) AskContext(ctx context.Context, q asks.ChoiceRequest) (asks.ChoiceObservation, error) {
	if !reflect.DeepEqual(q, l.q) {
		return asks.ChoiceObservation{}, errors.New("request changed during admission retry")
	}
	l.ask++
	r, err := l.c.AskContext(ctx, q)
	if err == nil && l.ask == 1 {
		return asks.ChoiceObservation{}, errors.New("test dropped admitted reply")
	}
	return r, err
}
func (l *lostChoiceReplies) ObserveContext(ctx context.Context, ref asks.RequestRef, waitMS int64) (asks.ChoiceObservation, error) {
	if ref != l.q.RequestRef {
		return asks.ChoiceObservation{}, errors.New("request changed during observation retry")
	}
	l.observe++
	if l.observe == 1 {
		r, err := l.c.ObserveContext(ctx, ref, 0)
		if err != nil {
			return r, err
		}
		if _, err := l.op.AnswerChoiceContext(ctx, r.ID, "replace"); err != nil {
			return asks.ChoiceObservation{}, err
		}
	}
	r, err := l.c.ObserveContext(ctx, ref, waitMS)
	if err == nil && l.observe == 1 {
		return asks.ChoiceObservation{}, errors.New("test dropped recorded answer reply")
	}
	return r, err
}
func (l *lostChoiceReplies) AcknowledgeContext(ctx context.Context, ref asks.RequestRef, answer asks.ChoiceAnswer) (asks.ChoiceAcknowledgement, error) {
	if ref != l.q.RequestRef || !matchesOutput(l.where, l.size, l.digest) {
		return asks.ChoiceAcknowledgement{}, errors.New("ack preceded durable local effect or changed reference")
	}
	l.ack++
	r, err := l.c.AcknowledgeContext(ctx, ref, answer)
	if err == nil && l.ack == 1 {
		return asks.ChoiceAcknowledgement{}, errors.New("test dropped completed ack reply")
	}
	return r, err
}

func TestLostChoiceRepliesRetainIntentAndCopyOnce(t *testing.T) {
	c, op := choicesRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	where := filepath.Join(t.TempDir(), "weights.bin")
	q, err := outputChoice(ctx, where)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(where, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := []byte("same completed job result")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	lost := &lostChoiceReplies{c: c, op: op, q: q, where: where, digest: digest, size: int64(len(payload))}
	copies := 0
	message, err := applyOutputChoice(ctx, lost, q, where, int64(len(payload)), digest, func(_ context.Context, w io.Writer) (int64, error) {
		copies++
		n, err := w.Write(payload)
		return int64(n), err
	}, &bytes.Buffer{}, nil)
	if err != nil || message != "download delivered" || lost.ask != 2 || lost.observe != 2 || lost.ack != 2 || copies != 1 {
		t.Fatalf("lost replies repeated or changed intent/effect: ask=%d observe=%d ack=%d copies=%d message=%s err=%v", lost.ask, lost.observe, lost.ack, copies, message, err)
	}
	observed, err := c.ObserveContext(ctx, q.RequestRef, 0)
	if err != nil || observed.Outcome != asks.ChoiceOutcomeGone {
		t.Fatal(observed, err)
	}
}

func TestChoiceExpiryAndRecordedAnswer(t *testing.T) {
	c, op := choicesRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	where := filepath.Join(t.TempDir(), "weights.bin")
	old := []byte("unchanged old output")
	if err := os.WriteFile(where, old, 0o600); err != nil {
		t.Fatal(err)
	}
	q, err := outputChoice(ctx, where)
	if err != nil {
		t.Fatal(err)
	}
	q.PendingUntilMs = time.Now().Add(50 * time.Millisecond).UnixMilli()
	copies := 0
	copyResult := func(context.Context, io.Writer) (int64, error) { copies++; return 0, nil }
	if _, err := applyOutputChoice(ctx, c, q, where, 0, "unused", copyResult, &bytes.Buffer{}, nil); err == nil {
		t.Fatal("unanswered expired choice succeeded")
	}
	if got, err := os.ReadFile(where); err != nil || !bytes.Equal(got, old) || copies != 0 {
		t.Fatal("expiry changed output", err, copies)
	}
	q, err = outputChoice(ctx, where)
	if err != nil {
		t.Fatal(err)
	}
	// The share and the answer must land inside the pending window, which then
	// passes. A 200 ms window was missed by the ask and answer at 72% CPU
	// (2026-10-07); two seconds holds them, and the test waits for the
	// deadline itself.
	q.PendingUntilMs = time.Now().Add(2 * time.Second).UnixMilli()
	// The app shares its output choice; the earlier admission is the same intent.
	r, err := c.ShareContext(ctx, q, outputAssistantOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.AnswerChoiceContext(ctx, r.ID, "keep"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(q.PendingUntilMs)) + 10*time.Millisecond)
	if message, err := applyOutputChoice(ctx, c, q, where, 0, "unused", copyResult, &bytes.Buffer{}, nil); err != nil || message != "download kept existing output" || copies != 0 {
		t.Fatalf("recorded answer lost at pending deadline: %s %v", message, err)
	}
}

func TestStagedReplacementFailureAndUncertainCompletion(t *testing.T) {
	payload := []byte("verified completed result")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	for _, mode := range []string{"copy-error", "mismatch", "expired-before-replace", "unsupported-rename", "lost-rename-reply"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			where := filepath.Join(dir, "weights.bin")
			old := []byte("intact old output")
			if err := os.WriteFile(where, old, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			copyResult := func(_ context.Context, w io.Writer) (int64, error) {
				body := payload
				if mode == "mismatch" {
					body = []byte("wrong result")
				}
				n, err := w.Write(body)
				if mode == "copy-error" {
					err = errors.New("read interrupted")
				}
				if mode == "expired-before-replace" {
					cancel()
				}
				return int64(n), err
			}
			install := func(tmp, where string) error {
				if mode == "unsupported-rename" {
					return errors.New("replacement unsupported")
				}
				if err := replaceSibling(tmp, where); err != nil {
					return err
				}
				return errors.New("replacement finished but reply lost")
			}
			err := replaceOutput(ctx, where, int64(len(payload)), digest, copyResult, install)
			want := old
			if mode == "lost-rename-reply" {
				want = payload
				if err != nil {
					t.Fatal("matching finished replacement did not reconcile", err)
				}
			} else if err == nil {
				t.Fatal("failed replacement reported success")
			}
			got, readErr := os.ReadFile(where)
			entries, dirErr := os.ReadDir(dir)
			if readErr != nil || !bytes.Equal(got, want) || dirErr != nil || len(entries) != 1 {
				t.Fatalf("target damaged or staging leaked: %q entries=%v read=%v dir=%v", got, entries, readErr, dirErr)
			}
		})
	}
}

type terminalChoice struct {
	observation asks.ChoiceObservation
	ask, ack    int
	firstAck    *asks.ChoiceAnswer
	expireAck   bool
}

func (c *terminalChoice) AskContext(context.Context, asks.ChoiceRequest) (asks.ChoiceObservation, error) {
	c.ask++
	return c.observation, nil
}
func (c *terminalChoice) ObserveContext(context.Context, asks.RequestRef, int64) (asks.ChoiceObservation, error) {
	return asks.ChoiceObservation{}, errors.New("unexpected observation after terminal choice")
}
func (c *terminalChoice) AcknowledgeContext(_ context.Context, _ asks.RequestRef, answer asks.ChoiceAnswer) (asks.ChoiceAcknowledgement, error) {
	c.ack++
	if c.firstAck == nil {
		copy := answer
		c.firstAck = &copy
	} else if answer != *c.firstAck {
		return asks.ChoiceAcknowledgement{}, errors.New("acknowledgement changed answer")
	}
	if c.expireAck {
		if c.ack == 1 {
			return asks.ChoiceAcknowledgement{}, errors.New("test dropped ack reply")
		}
		return asks.ChoiceAcknowledgement{Outcome: asks.ChoiceAckOutcomeExpired}, nil
	}
	return asks.ChoiceAcknowledgement{Outcome: asks.ChoiceAckOutcomeAcknowledged}, nil
}

func TestUnknownAndInvalidChoicesDoNotCopyOrAcknowledge(t *testing.T) {
	for _, mode := range []string{"unknown", "gone", "expired", "unoffered", "replay-expired"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			where := filepath.Join(t.TempDir(), "weights.bin")
			q, err := outputChoice(ctx, where)
			if err != nil {
				t.Fatal(err)
			}
			answer := &asks.ChoiceAnswer{ID: "record", Option: "replace", AnsweredMs: time.Now().UnixMilli(), AnsweredBy: "test-operator"}
			c := &terminalChoice{observation: asks.ChoiceObservation{Outcome: asks.ChoiceOutcomeAnswered, ID: answer.ID, Answer: answer}}
			switch mode {
			case "unknown":
				c.observation = asks.ChoiceObservation{Outcome: asks.ChoiceOutcomeUnknown}
			case "gone":
				c.observation = asks.ChoiceObservation{Outcome: asks.ChoiceOutcomeGone}
			case "expired":
				c.observation = asks.ChoiceObservation{Outcome: asks.ChoiceOutcomeExpired}
			case "unoffered":
				answer.Option = "delete"
			case "replay-expired":
				q.RequestRef.ReplayUntilMs = time.Now().Add(-time.Second).UnixMilli()
			}
			old := []byte("keep this file")
			if err := os.WriteFile(where, old, 0o600); err != nil {
				t.Fatal(err)
			}
			copies := 0
			if _, err := applyOutputChoice(ctx, c, q, where, 0, "unused", func(context.Context, io.Writer) (int64, error) { copies++; return 0, nil }, &bytes.Buffer{}, nil); err == nil {
				t.Fatal("unsafe decision succeeded")
			}
			got, err := os.ReadFile(where)
			if err != nil || !bytes.Equal(got, old) || copies != 0 || c.ack != 0 || c.ask != 1 {
				t.Fatalf("unsafe decision mutated/recreated intent: copy=%d ack=%d ask=%d got=%q err=%v", copies, c.ack, c.ask, got, err)
			}
		})
	}
}

func TestExpiredAcknowledgementDoesNotRepeatFinishedCopy(t *testing.T) {
	ctx := context.Background()
	where := filepath.Join(t.TempDir(), "weights.bin")
	q, err := outputChoice(ctx, where)
	if err != nil {
		t.Fatal(err)
	}
	answer := &asks.ChoiceAnswer{ID: "record", Option: "replace", AnsweredMs: time.Now().UnixMilli(), AnsweredBy: "test-operator"}
	c := &terminalChoice{observation: asks.ChoiceObservation{Outcome: asks.ChoiceOutcomeAnswered, ID: answer.ID, Answer: answer}, expireAck: true}
	if err := os.WriteFile(where, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := []byte("finished once")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	copies := 0
	message, err := applyOutputChoice(ctx, c, q, where, int64(len(payload)), digest, func(_ context.Context, w io.Writer) (int64, error) {
		copies++
		n, err := w.Write(payload)
		return int64(n), err
	}, &bytes.Buffer{}, nil)
	if err == nil || !strings.Contains(err.Error(), "acknowledgement expired") || message != "download delivered" || copies != 1 || c.ask != 1 || c.ack != 2 || !matchesOutput(where, int64(len(payload)), digest) {
		t.Fatalf("expired acknowledgement repeated local effect: copies=%d ask=%d ack=%d message=%s err=%v", copies, c.ask, c.ack, message, err)
	}
}

func TestOutputChoiceNamesTwoDestinationsAndKeepsPrivatePath(t *testing.T) {
	ctx := context.Background()
	first := filepath.Join("models-one", "weights.bin")
	second := filepath.Join("models-two", "weights.bin")
	a, err := outputChoice(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := outputChoice(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if a.Title == b.Title || !strings.Contains(a.Title, "weights.bin") || !strings.Contains(a.Title, "models-one") || !strings.Contains(b.Title, "models-two") || !reflect.DeepEqual(a.Options, b.Options) {
		t.Fatalf("destination context or stable options missing: %q %q", a.Title, b.Title)
	}
	// Presentation remains metadata: the exact private path is reported with
	// the pending ID, and its quoted text is never turned back into a path.
	c, _ := choicesRuntime(t)
	private := filepath.Join(t.TempDir(), "private output.bin")
	q, err := outputChoice(ctx, private)
	if err != nil {
		t.Fatal(err)
	}
	// The wait ends once it has reported the pending choice: a fixed 75 ms
	// ran out before the ask was admitted at 72% CPU (2026-10-07).
	pending, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	if _, err := waitOutputChoice(pending, c, q, private, cancelOnLine{&out, "output choice ", cancel}, nil); err == nil {
		t.Fatal("unanswered choice finished")
	}
	if !strings.Contains(out.String(), "output choice ") || !strings.Contains(out.String(), strconv.QuoteToGraphic(private)) {
		t.Fatalf("pending inspection lacks full private destination: %s", out.String())
	}
}

// cancelOnLine writes to w and calls cancel once the output holds marker.
type cancelOnLine struct {
	w      *bytes.Buffer
	marker string
	cancel context.CancelFunc
}

func (c cancelOnLine) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if strings.Contains(c.w.String(), c.marker) {
		c.cancel()
	}
	return n, err
}

func TestOutputChoiceTitleBoundsAndSafeFilenamePresentation(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"Pārbaude.bin", "line\nname.bin", "paragraph\u2029name.bin", "tab\tname.bin", strings.Repeat("ā", 60)} {
		where := filepath.Join(strings.Repeat("parent", 30), name)
		q, err := outputChoice(ctx, where)
		if err != nil || !asksbook.ValidChoiceRequest(q) || utf8.RuneCountInString(q.Title) > 80 || !strings.Contains(q.Title, strconv.QuoteToGraphic(name)) {
			t.Fatalf("safe Unicode filename lost or bounds violated: %q title=%q err=%v", name, q.Title, err)
		}
	}
	where := filepath.Join("models", strings.Repeat("ā", 63))
	if _, err := outputChoice(ctx, where); err == nil || !strings.Contains(err.Error(), "full output filename") {
		t.Fatal("unpresentable full filename was silently shortened", err)
	}
}
