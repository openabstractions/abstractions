package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
)

func nativeBridgeFixture(t *testing.T) (*nativeAsksBridge, <-chan nativeAsksFrame) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	bridge := newNativeAsksBridge(ctx, writer)
	frames := make(chan nativeAsksFrame, 8)
	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), maxNativeAsksLine)
		for scanner.Scan() {
			var frame nativeAsksFrame
			if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), nativeAsksPrefix)), &frame) == nil {
				frames <- frame
			}
		}
	}()
	return bridge, frames
}

func nextNativeFrame(t *testing.T, frames <-chan nativeAsksFrame, kind string) nativeAsksFrame {
	t.Helper()
	select {
	case frame := <-frames:
		if frame.Kind != kind {
			t.Fatalf("wanted %s, got %+v", kind, frame)
		}
		return frame
	case <-time.After(time.Second):
		t.Fatalf("no native %s frame", kind)
		return nativeAsksFrame{}
	}
}

func TestNativeAsksBridgeUsesPublicOperatorAndRejectsInterruptedAnswer(t *testing.T) {
	book, operator := trayTestBook(t)
	id := ask(t, book, "native-reply", "requester.exe", "abstraction.model/lookup", "hf")
	page, err := operator.ListQuestionsContext(context.Background(), "", 64)
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("question: %+v %v", page, err)
	}
	bridge, frames := nativeBridgeFixture(t)
	bridge.session(true)
	presenter := &questionPresenter{session: bridge, surface: bridge,
		operator: func(context.Context) (presenterOperator, error) { return operator, nil },
		account:  probe.Self().Account, rules: func() grants.RuleOperator { return &fakeRules{} }, budget: time.Second}
	done := make(chan presentResult, 1)
	go func() { done <- presenter.present(context.Background(), page.Records[0]) }()
	shown := nextNativeFrame(t, frames, "question")
	if shown.Prompt == nil || !strings.Contains(shown.Prompt.Details, id) {
		t.Fatalf("missing public question: %+v", shown)
	}
	bridge.session(false)
	_ = nextNativeFrame(t, frames, "dismiss")
	bridge.session(true)
	if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: shown.ID, Option: "allow"}, nil); err != nil {
		t.Fatal(err)
	}
	if result := <-done; result.Outcome != presentDeferred {
		t.Fatalf("interrupted: %+v", result)
	}
	attributedCurrent, err := operator.ReadAttributedQuestionContext(context.Background(), id)
	current := attributedCurrent.Question
	if err != nil || current.Record == nil || current.Record.Option != "" {
		t.Fatalf("interrupted answer landed: %+v %v", current, err)
	}
	go func() { done <- presenter.present(context.Background(), page.Records[0]) }()
	reshown := nextNativeFrame(t, frames, "question")
	// A late answer from the interrupted dialog cannot answer its replacement.
	if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: shown.ID, Option: "allow"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		t.Fatalf("old reply completed replacement: %+v", result)
	default:
	}
	if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: reshown.ID, Option: "refuse"}, nil); err != nil {
		t.Fatal(err)
	}
	if result := <-done; result.Outcome != presentAnswered || result.Answered.Decision.Outcome != askclient.OperatorDecisionOutcomeAnswered {
		t.Fatalf("answer: %+v", result)
	}
	attributedCurrent, err = operator.ReadAttributedQuestionContext(context.Background(), id)
	current = attributedCurrent.Question
	if err != nil || current.Record == nil || current.Record.Option != "refuse" {
		t.Fatalf("public decision missing: %+v %v", current, err)
	}
}

func TestNativeAsksBridgeMakesHiddenControlsVisibleAndKeepsExactAnswer(t *testing.T) {
	bridge, frames := nativeBridgeFixture(t)
	bridge.session(true)
	const option = "answer\u202eexact"
	done := make(chan string, 1)
	go func() {
		answer, err := bridge.Ask(trayQuestionPresentation{Instruction: "app\u202ename", Details: "program\u202epath\x00",
			Choices: []trayQuestionChoice{{Label: "choose\u202eanswer", Option: option}}, Default: option})
		if err != nil {
			done <- err.Error()
		} else {
			done <- answer
		}
	}()
	frame := nextNativeFrame(t, frames, "question")
	if frame.Prompt.Instruction != `app\u202Ename` || frame.Prompt.Details != `program\u202Epath\u0000` || frame.Prompt.Choices[0].Label != `choose\u202Eanswer` || frame.Prompt.Choices[0].Option != option || frame.Prompt.Default != option {
		t.Fatalf("display should expose controls while keeping the service's answer token: %+v", frame.Prompt)
	}
	if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: frame.ID, Option: option}, nil); err != nil {
		t.Fatal(err)
	}
	if answer := <-done; answer != option {
		t.Fatalf("answer token changed: %q", answer)
	}
}

func TestNativeAsksBridgeKeepsTrustedPermissionWarning(t *testing.T) {
	bridge, frames := nativeBridgeFixture(t)
	bridge.session(true)
	done := make(chan error, 1)
	go func() {
		_, err := bridge.Ask(trayQuestionPresentation{Content: "All saved app secrets.", Warning: "Future secrets\u202e are included.", PermissionContent: "Read\u202e secret value.",
			Choices: []trayQuestionChoice{{Label: "Refuse", Option: "refuse"}}, Default: "refuse"})
		done <- err
	}()
	frame := nextNativeFrame(t, frames, "question")
	if frame.Prompt.Warning != `Future secrets\u202E are included.` || frame.Prompt.PermissionContent != `Read\u202E secret value.` || frame.Prompt.Default != "refuse" {
		t.Fatalf("trusted caution or refusing default lost: %+v", frame.Prompt)
	}
	if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: frame.ID, Option: "refuse"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNativeAsksBridgeDismissesChangedQuestionAndReportsErrors(t *testing.T) {
	for _, mode := range []string{"stale", "failed", "close", "invalid", "channel"} {
		t.Run(mode, func(t *testing.T) {
			bridge, frames := nativeBridgeFixture(t)
			bridge.session(true)
			record := pendingFixture()
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
			presenter := testPresenter(bridge, bridge, operator, &fakeRules{})
			presenter.watchEvery = time.Millisecond
			done := make(chan presentResult, 1)
			go func() { done <- presenter.present(context.Background(), record) }()
			shown := nextNativeFrame(t, frames, "question")
			switch mode {
			case "stale":
				operator.set()
				_ = nextNativeFrame(t, frames, "dismiss")
			case "failed":
				if err := bridge.accept(nativeAsksReply{Kind: "failed", ID: shown.ID, Error: "AppKit fixture failure"}, nil); err != nil {
					t.Fatal(err)
				}
			case "close":
				if err := bridge.accept(nativeAsksReply{Kind: "close", ID: shown.ID}, nil); err != nil {
					t.Fatal(err)
				}
			case "invalid":
				if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: shown.ID, Option: "not offered"}, nil); err != nil {
					t.Fatal(err)
				}
			case "channel":
				bridge.close(io.EOF)
				_ = nextNativeFrame(t, frames, "dismiss")
			}
			result := <-done
			want := map[string]presentOutcome{"stale": presentStale, "failed": presentFailed, "close": presentClosed, "invalid": presentStale, "channel": presentDeferred}[mode]
			if result.Outcome != want || len(operator.sent()) != 0 {
				t.Fatalf("result=%+v answers=%v", result, operator.sent())
			}
		})
	}
}

func TestNativeAsksBridgeFailsClosedWithoutSessionAndOnMalformedChannel(t *testing.T) {
	bridge := newNativeAsksBridge(context.Background(), io.Discard)
	if _, err := bridge.Ask(trayQuestionPresentation{}); !errors.Is(err, errPromptNotShown) {
		t.Fatal(err)
	}
	bridge.session(true)
	if _, err := bridge.Ask(trayQuestionPresentation{Details: strings.Repeat("x", maxNativeAsksLine)}); err == nil {
		t.Fatal("an oversized exact presentation was silently truncated or accepted")
	}
	for _, input := range []string{
		`{"kind":"session"}`, `{"kind":"session","unlocked":true,"option":"allow"}`,
		`{"kind":"session","unlocked":true,"unknown":1}`, `{"kind":"answer","id":"1"}`,
		`{"kind":"close","id":"1"} {}`, strings.Repeat("x", maxNativeAsksLine+1),
	} {
		if err := bridge.read(strings.NewReader(input+"\n"), func(string) error { t.Fatal("unexpected Panel open"); return nil }); err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("accepted malformed reply %q: %v", input[:min(len(input), 80)], err)
		}
	}
	opened := ""
	if err := bridge.read(strings.NewReader("{\"kind\":\"open-panel\",\"page\":\"questions\"}\n"), func(page string) error { opened = page; return nil }); !errors.Is(err, io.EOF) || opened != "questions" {
		t.Fatalf("open=%q error=%v", opened, err)
	}
}
