package gateway

import (
	"errors"
	"iter"
	"strings"
	"testing"

	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
)

func TestFoldCompletionPreservesNormalAndRefusedReplies(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		stream := func(yield func(inferenceclient.Delta, error) bool) {
			for _, delta := range []inferenceclient.Delta{
				{Kind: inferenceclient.DeltaKindPart, Index: 0, Part: &inferenceclient.Part{Kind: inferenceclient.PartKindText, Text: "hel"}},
				{Kind: inferenceclient.DeltaKindPart, Index: 0, Part: &inferenceclient.Part{Kind: inferenceclient.PartKindText, Text: "lo"}},
				{Kind: inferenceclient.DeltaKindEnd, End: &inferenceclient.Reply{Outcome: inferenceclient.ReplyOutcomeCompleted, StopReason: inferenceclient.StopReasonEnd, Host: "local", Model: "fixture"}},
			} {
				if !yield(delta, nil) {
					return
				}
			}
		}
		reply, err := foldCompletion(iter.Seq2[inferenceclient.Delta, error](stream))
		if err != nil {
			t.Fatal(err)
		}
		if reply.Outcome != inferenceclient.ReplyOutcomeCompleted || len(reply.Message.Parts) != 1 || reply.Message.Parts[0].Text != "hello" {
			t.Fatalf("unexpected folded reply: %+v", reply)
		}
	})

	t.Run("refused", func(t *testing.T) {
		want := &inferenceclient.PageRefusal{Outcome: inferenceclient.PageOutcomeForbidden}
		_, err := foldCompletion(func(yield func(inferenceclient.Delta, error) bool) { yield(inferenceclient.Delta{}, want) })
		if !errors.Is(err, want) {
			t.Fatalf("typed refusal lost: %T %v", err, err)
		}

		reply, err := foldCompletion(func(yield func(inferenceclient.Delta, error) bool) {
			yield(inferenceclient.Delta{Kind: inferenceclient.DeltaKindEnd, End: &inferenceclient.Reply{Outcome: inferenceclient.ReplyOutcomeNotPermitted, Reason: "rights:execute"}}, nil)
		})
		if err != nil || reply.Outcome != inferenceclient.ReplyOutcomeNotPermitted || reply.Reason != "rights:execute" {
			t.Fatalf("terminal refusal changed: reply=%+v err=%v", reply, err)
		}
	})
}

func TestFoldCompletionStopsAndCancelsOversizedIteratorEarly(t *testing.T) {
	chunk := strings.Repeat("x", 64<<10)
	produced, cancelled := 0, false
	stream := func(yield func(inferenceclient.Delta, error) bool) {
		// Chat.Stream uses this same iterator-exit edge to send Cancel for an
		// accepted operation that has not delivered its terminal delta.
		defer func() { cancelled = true }()
		for i := 0; i < 1000; i++ {
			produced++
			if !yield(inferenceclient.Delta{Kind: inferenceclient.DeltaKindPart, Index: 0, Part: &inferenceclient.Part{Kind: inferenceclient.PartKindText, Text: chunk}}, nil) {
				return
			}
		}
	}
	_, err := foldCompletion(iter.Seq2[inferenceclient.Delta, error](stream))
	if err == nil || !strings.Contains(err.Error(), "262144-byte bound") {
		t.Fatalf("expected content bound, got %v", err)
	}
	if produced != 5 || !cancelled {
		t.Fatalf("consumer did not terminate iterator at bound: produced=%d cancelled=%v", produced, cancelled)
	}
}

func TestFoldCompletionBoundsAllRetainedFields(t *testing.T) {
	tests := []struct {
		name   string
		stream iter.Seq2[inferenceclient.Delta, error]
		want   string
	}{
		{
			name: "arguments",
			stream: func(yield func(inferenceclient.Delta, error) bool) {
				yield(inferenceclient.Delta{Kind: inferenceclient.DeltaKindPart, Index: 0, Part: &inferenceclient.Part{Kind: inferenceclient.PartKindToolCall, Arguments: strings.Repeat("x", maxCompletionBytes+1)}}, nil)
			},
			want: "262144-byte bound",
		},
		{
			name: "part metadata",
			stream: func(yield func(inferenceclient.Delta, error) bool) {
				yield(inferenceclient.Delta{Kind: inferenceclient.DeltaKindPart, Index: 0, Part: &inferenceclient.Part{Kind: inferenceclient.PartKindToolCall, Name: strings.Repeat("x", maxCompletionMetadata+1)}}, nil)
			},
			want: "metadata exceeds",
		},
		{
			name: "part indices",
			stream: func(yield func(inferenceclient.Delta, error) bool) {
				for i := 0; i <= maxCompletionParts; i++ {
					if !yield(inferenceclient.Delta{Kind: inferenceclient.DeltaKindPart, Index: int64(i), Part: &inferenceclient.Part{Kind: inferenceclient.PartKindText}}, nil) {
						return
					}
				}
			},
			want: "256-part bound",
		},
		{
			name: "terminal metadata",
			stream: func(yield func(inferenceclient.Delta, error) bool) {
				yield(inferenceclient.Delta{Kind: inferenceclient.DeltaKindEnd, End: &inferenceclient.Reply{Outcome: inferenceclient.ReplyOutcomeRefused, Reason: strings.Repeat("x", maxCompletionMetadata+1)}}, nil)
			},
			want: "metadata exceeds",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := foldCompletion(tc.stream)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}
