//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

var replyTimeoutPipes atomic.Int64

// Owner's PC, 2026-11-12 (R36): the runtime took longer than the native
// call's budget to answer Prepare and InboxesList, and the tools answered an
// opaque reply_lost. Over a real pipe whose server reads the request and
// never answers, the tools name the timeout, Prepare says nothing was sent
// and why, and the server log gets one line naming the cause.
func TestMessagesUnansweredNativeCallNamesTimeout(t *testing.T) {
	endpoint := fmt.Sprintf(`\\.\pipe\oa-reply-timeout-%d-%d`, os.Getpid(), replyTimeoutPipes.Add(1))
	listener, err := listen.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				// Hold the request unanswered until the client gives up.
				_, _ = io.Copy(io.Discard, conn)
				_ = conn.Close()
			}()
		}
	}()
	var logged bytes.Buffer
	prior := messagesCauseLog
	messagesCauseLog = &logged
	defer func() { messagesCauseLog = prior }()
	b := &messagesBackend{application: "oa-claude-integration", instance: "instance", contextName: "native-source", status: new(messagesInboxStatus), native: func(ctx context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(listen.FrameClient{Endpoint: endpoint}.WithDefaults(500*time.Millisecond, 1<<20).WithContext(ctx)), nil
	}}
	prepared, err := b.MessagesPrepare(context.Background(), gateway.MessagesPrepareInput{Receiver: "OpenCode", Body: "hi", Kind: "note"})
	if err != nil || prepared.Outcome != "unknown" || prepared.Reason != messageReplyTimeout || !strings.Contains(prepared.Guidance, "did not answer within 35 seconds") || !strings.Contains(prepared.Guidance, "Nothing was sent") {
		t.Fatal("prepare", prepared, err)
	}
	listed, err := b.MessagesInboxesList(context.Background(), gateway.MessagesListInput{})
	if err != nil || listed.Outcome != wire.OutcomeUnknown || listed.Reason != messageReplyTimeout {
		t.Fatal("listing", listed, err)
	}
	if lines := strings.Count(logged.String(), "OA_MESSAGES Prepare reply_timeout: "); lines != 1 {
		t.Fatalf("log %q", logged.String())
	}
	if messageVersionSkew(messageReplyTimeout) || messageVersionSkew("reply_lost") || !messageVersionSkew("reply_refused_field") || !messageVersionSkew("dispatch_unknown_method") {
		t.Fatal("version skew classification")
	}
}
