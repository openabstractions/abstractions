package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	facaderuntime "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	logging "github.com/openabstractions/abstraction-logging/go"
)

type completionFixture struct {
	available       bool
	mode            string
	mu              sync.Mutex
	requests        []wire.RequestV2
	frames, cancels int
	waits           []int64
	listeners       []listen.Listener
	wg              sync.WaitGroup
	once            sync.Once
}

func (f *completionFixture) ChatV2Available() bool           { return f.available }
func (f *completionFixture) Serve(ctx context.Context) error { <-ctx.Done(); return nil }
func (f *completionFixture) Close() error {
	var err error
	f.once.Do(func() {
		for _, l := range f.listeners {
			err = errors.Join(err, l.Close())
		}
		f.wg.Wait()
	})
	return err
}
func (f *completionFixture) Start(q wire.RequestV2) (wire.AdmissionV2, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, q)
	if f.mode == "refusal" {
		return wire.AdmissionV2{Outcome: wire.StartOutcomeNoHost, Reason: "router:no_host"}, nil
	}
	if f.mode == "activating" && len(f.requests) == 1 {
		return wire.AdmissionV2{Outcome: wire.StartOutcomeUnavailable, Reason: "activating:fixture"}, nil
	}
	if f.mode == "unavailable" {
		return wire.AdmissionV2{Outcome: wire.StartOutcomeUnavailable, Reason: "admission:unavailable"}, nil
	}
	reason := wire.SelectionReasonExplicitModel
	return wire.AdmissionV2{Outcome: wire.StartOutcomeAccepted, Operation: "operation", Host: "fixture", Model: q.Target.Model, PreferredModel: q.Target.Model, SelectionReason: &reason, RetentionMs: 1000, IdleMs: 1000, RetainedDeltas: 16}, nil
}
func (*completionFixture) StartWithGuarantees(wire.RequestV2, []wire.RequestGuarantee) (wire.AdmissionV2, error) {
	return wire.AdmissionV2{}, errors.New("fixture: StartWithGuarantees is unsupported")
}
func (f *completionFixture) Observe(op string, cursor, _, _, waitMS int64) (wire.DeltaPage, error) {
	f.mu.Lock()
	mode := f.mode
	f.waits = append(f.waits, waitMS)
	f.mu.Unlock()
	if op == "foreign" {
		return wire.DeltaPage{Outcome: wire.PageOutcomeForbidden}, nil
	}
	if op != "operation" {
		return wire.DeltaPage{}, errors.New("wrong operation")
	}
	if (mode == "partial_wait" || mode == "partial_refusal") && cursor == 0 {
		return wire.DeltaPage{Outcome: wire.PageOutcomePage, Next: 1, Deltas: []wire.Delta{{Sequence: 0, Kind: wire.DeltaKindPart, Part: &wire.Part{Kind: wire.PartKindText, Text: "first"}}}}, nil
	}
	if mode == "partial_refusal" {
		return wire.DeltaPage{Outcome: wire.PageOutcomeForbidden, Next: cursor}, nil
	}
	if mode == "partial_resume" {
		return wire.DeltaPage{Outcome: wire.PageOutcomePage, Next: cursor + 1, AtEnd: true, Deltas: []wire.Delta{{Sequence: cursor, Kind: wire.DeltaKindEnd, End: &wire.Reply{Outcome: wire.ReplyOutcomeCompleted, StopReason: wire.StopReasonEnd, Message: wire.Message{Role: wire.RoleAssistant, Parts: []wire.Part{}}}}}}, nil
	}
	if mode == "wait" || mode == "partial_wait" {
		time.Sleep(5 * time.Millisecond)
		return wire.DeltaPage{Outcome: wire.PageOutcomePage, Next: cursor}, nil
	}
	if mode == "gap" {
		return wire.DeltaPage{Outcome: wire.PageOutcomeGap, Next: 4}, nil
	}
	if mode == "bound" {
		return wire.DeltaPage{Outcome: wire.PageOutcomePage, Next: cursor + 1, Deltas: []wire.Delta{{Sequence: cursor, Kind: wire.DeltaKindPart, Part: &wire.Part{Kind: wire.PartKindText, Text: strings.Repeat("x", 64<<10)}}}}, nil
	}
	return wire.DeltaPage{Outcome: wire.PageOutcomePage, Next: 2, AtEnd: true, Deltas: []wire.Delta{
		{Sequence: 0, Kind: wire.DeltaKindPart, Part: &wire.Part{Kind: wire.PartKindText, Text: "fixture reply"}},
		{Sequence: 1, Kind: wire.DeltaKindEnd, End: &wire.Reply{Outcome: wire.ReplyOutcomeCompleted, StopReason: wire.StopReasonEnd, Host: "fixture", Model: "chosen/model", Message: wire.Message{Role: wire.RoleAssistant, Parts: []wire.Part{}}}},
	}}, nil
}
func (f *completionFixture) Cancel(op string) (wire.Cancellation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if op != "operation" {
		return wire.Cancellation{}, errors.New("wrong operation")
	}
	f.cancels++
	return wire.Cancellation{Outcome: wire.CancelOutcomeCancelled}, nil
}

type completionSink struct{}

func (completionSink) Write(logging.Record) error { return nil }

var completionEndpoints atomic.Uint64

func completionEndpoint() string {
	return listen.Endpoint(fmt.Sprintf("gateway-chat1715-%d-%d", os.Getpid(), completionEndpoints.Add(1)))
}
func startCompletionFixture(t *testing.T, available bool, mode string) (*OA, *completionFixture) {
	t.Helper()
	f := &completionFixture{available: available, mode: mode}
	local, remote := completionEndpoint(), completionEndpoint()
	for _, endpoint := range []string{local, remote} {
		l, err := listen.Listen(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		f.listeners = append(f.listeners, l)
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				f.wg.Add(1)
				go func() {
					defer f.wg.Done()
					defer conn.Close()
					timer := time.AfterFunc(5*time.Second, func() { _ = conn.Close() })
					defer timer.Stop()
					frame, err := listen.ReadFrameFrom(conn, 1<<20)
					if err != nil {
						return
					}
					f.mu.Lock()
					f.frames++
					f.mu.Unlock()
					reply, err := (&wire.ChatV2Dispatcher{Handler: f}).ExchangeFrame(frame)
					if err == nil {
						_ = listen.WriteFrameTo(conn, reply, 1<<20)
					}
				}()
			}
		}()
	}
	runtimeEndpoint := completionEndpoint()
	h, err := facaderuntime.Listen(facaderuntime.Options{Endpoint: runtimeEndpoint, LogEndpoint: completionEndpoint(), ConfigEndpoint: completionEndpoint(), ConfigWithoutMachine: true, Sink: completionSink{}, Inference: f, InferenceEndpoint: local, InferenceRemoteEndpoint: remote})
	if runtime.GOOS == "darwin" {
		closeErr := f.Close()
		if h != nil {
			closeErr = errors.Join(closeErr, h.Close())
		}
		if h != nil || !errors.Is(err, identity.ErrNotProven) || closeErr != nil {
			t.Fatalf("Darwin Unix completion fixture must refuse Program proof: %v (close: %v)", err, closeErr)
		}
		t.Skip("native completion cases require a Program-capable endpoint")
	}
	if err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := h.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("fixture runtime did not join")
		}
		_ = f.Close()
	})
	oa, err := NewOA(facade.NewUnverified(runtimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return oa, f
}
func TestCompleteUsesExplicitChatV2TargetAndCredential(t *testing.T) {
	for _, hosting := range []string{"local", "hosted"} {
		t.Run(hosting, func(t *testing.T) {
			oa, f := startCompletionFixture(t, true, "")
			input := CompleteInput{Model: "chosen/model", Prompt: "prompt", Hosting: hosting}
			if hosting == "hosted" {
				input.Credential = "named"
			}
			got, err := oa.Complete(context.Background(), input)
			if err != nil || got.Text != "fixture reply" || got.Outcome != "completed" {
				t.Fatalf("%+v, %v", got, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.requests) != 1 || f.cancels != 0 {
				t.Fatalf("requests %d cancel %d", len(f.requests), f.cancels)
			}
			q := f.requests[0]
			want := wire.RequestGuaranteeLocalOnly
			if hosting == "hosted" {
				want = wire.RequestGuaranteeHostedAllowed
			}
			if q.Target.Model != "chosen/model" || q.Target.SelectionPolicy != "" || q.Content.Credential != input.Credential || len(q.Content.Guarantees) != 1 || q.Content.Guarantees[0] != want || q.Content.Options == nil || q.Content.Options.MaxOutput != defaultMaxOutput || len(q.Content.Messages) != 1 || q.Content.Messages[0].Parts[0].Text != "prompt" {
				t.Fatalf("forwarded request: %+v", q)
			}
		})
	}
}
func TestCompleteWaitsThroughNativeColdActivation(t *testing.T) {
	for _, tc := range []struct {
		name, hosting, mode, outcome, reason string
		wait                                 time.Duration
		starts                               int
	}{
		{name: "ready on next Start", hosting: "local", mode: "activating", wait: 55 * time.Second, outcome: "completed", starts: 2},
		{name: "no reply room", hosting: "local", mode: "activating", wait: time.Millisecond, outcome: "unavailable", reason: "activating:fixture", starts: 1},
		{name: "hosted", hosting: "hosted", mode: "activating", wait: 55 * time.Second, outcome: "unavailable", reason: "activating:fixture", starts: 1},
		{name: "uncertain admission", hosting: "local", mode: "unavailable", wait: 55 * time.Second, outcome: "unavailable", reason: "admission:unavailable", starts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, f := startCompletionFixture(t, true, tc.mode)
			in := CompleteInput{Model: "chosen/model", Prompt: "prompt", Hosting: tc.hosting}
			if tc.hosting == "hosted" {
				in.Credential = "named"
			}
			out, err := o.Complete(withCallWait(t.Context(), tc.wait), in)
			f.mu.Lock()
			defer f.mu.Unlock()
			if err != nil || out.Outcome != tc.outcome || out.Reason != tc.reason || len(f.requests) != tc.starts || f.cancels != 0 {
				t.Fatalf("completion %+v, error %v, Starts %d, Cancels %d", out, err, len(f.requests), f.cancels)
			}
			if tc.starts == 2 && (out.OperationID != "operation" || out.Text != "fixture reply" || !reflect.DeepEqual(f.requests[0], f.requests[1])) {
				t.Fatalf("retried completion %+v, requests %+v", out, f.requests)
			}
		})
	}
}

func TestCompleteChatV2RefusalCancellationAndNoFallback(t *testing.T) {
	for _, mode := range []string{"refusal", "gap", "bound", "unsupported-runtime"} {
		t.Run(mode, func(t *testing.T) {
			oa, f := startCompletionFixture(t, mode != "unsupported-runtime", mode)
			got, err := oa.Complete(context.Background(), CompleteInput{Model: "chosen/model", Prompt: "prompt", Hosting: "local", MaxOutput: 8})
			f.mu.Lock()
			defer f.mu.Unlock()
			switch mode {
			case "refusal":
				if err != nil || got.Outcome != "no_host" || got.Reason != "router:no_host" || len(f.requests) != 1 || f.cancels != 0 {
					t.Fatalf("%+v error %v requests %d cancels %d", got, err, len(f.requests), f.cancels)
				}
			case "unsupported-runtime":
				if err == nil || len(f.requests) != 0 || f.frames != 0 {
					t.Fatalf("legacy fallback: %+v error %v frames %d", got, err, f.frames)
				}
			case "gap":
				if err == nil || len(f.requests) != 1 || f.cancels != 0 {
					t.Fatalf("%+v error %v requests %d cancels %d", got, err, len(f.requests), f.cancels)
				}
			default:
				if err == nil || len(f.requests) != 1 || f.cancels != 1 {
					t.Fatalf("%+v error %v requests %d cancels %d", got, err, len(f.requests), f.cancels)
				}
			}
		})
	}
}
