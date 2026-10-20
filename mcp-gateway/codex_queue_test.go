package main

import (
	"context"
	"errors"
	watchwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"reflect"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestCodexQueueResumeCapabilityUsesHeldImage(t *testing.T) {
	for _, mode := range []string{"supported", "unsupported", "prefix", "warning", "changed-before", "changed-after"} {
		t.Run(mode, func(t *testing.T) {
			cli := new(codexReceiverTestCLI)
			if mode == "changed-before" {
				cli.err = errors.New("changed")
			}
			calls := 0
			err := checkCodexQueueResume(context.Background(), cli, "owned-directory", func(ctx context.Context, program, directory string, args []string) (codexCLIResult, error) {
				calls++
				if program != cli.Program() || directory != "owned-directory" || !reflect.DeepEqual(args, []string{"queue", "--help"}) {
					t.Fatal(program, directory, args)
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("unbounded help")
				}
				out := codexCLIResult{Output: []byte("Options:\n  --resume\n")}
				switch mode {
				case "unsupported":
					out.Output = []byte("Options:\n  --thread\n")
				case "prefix":
					out.Output = []byte("  --resume-other\n")
				case "warning":
					out.Diagnostic = []byte("warning")
				case "changed-after":
					cli.err = errors.New("changed")
				}
				return out, nil
			})
			if (err == nil) != (mode == "supported") || calls > 1 || mode == "changed-before" && calls != 0 {
				t.Fatal(err, calls)
			}
		})
	}
}

func TestCodexReceiverZeroExitWarningIsUnknownAndConsumed(t *testing.T) {
	b := codexReceiverTestBinding(t)
	p := new(codexReceiverProgress)
	queues := 0
	run := func(context.Context, string, string, []string) (codexCLIResult, error) {
		queues++
		return codexCLIResult{Diagnostic: []byte("resume failed")}, nil
	}
	watch := codexReceiverTestWatch(func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
		return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: "same", PendingUntilUnixMs: time.Now().Add(time.Minute).UnixMilli(), Wake: "0001"}, WatchMetadata: gateway.WatchMetadata{Context: b.context}}, nil
	})
	stop := errors.New("done")
	pause := func(context.Context, time.Duration) error { return stop }
	err := runCodexInboxReceiverProgress(context.Background(), b, watch, run, time.Now, pause, p)
	var uncertain *codexReceiverQueueError
	if !errors.As(err, &uncertain) || queues != 1 || p.submitted != "0001" {
		t.Fatal(err, queues, p)
	}
	err = runCodexInboxReceiverProgress(context.Background(), b, watch, run, time.Now, pause, p)
	if !errors.Is(err, stop) || queues != 1 {
		t.Fatal("uncertain wake replayed", err, queues)
	}
}
