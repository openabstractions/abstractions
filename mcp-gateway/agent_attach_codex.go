package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/client"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// codexDesktopRunning is the production check that the person's Codex Desktop
// runs; while it does, it owns its conversations and OA queues into them.
var codexDesktopRunning = nativecli.CodexDesktopRunning

// codexAppServerRun starts the selected CLI's `app-server` with input as its
// standard input and out as its standard output, and returns when it exits.
type codexAppServerRun func(ctx context.Context, input *os.File, out io.Writer) error

// codexAttachThreads lists existing Codex conversations through the vendor's
// app-server `thread/list`. It sends initialize, initialized and one bounded
// list request, then closes input. No conversation content is requested.
func codexSummaryRPC(ctx context.Context, run codexAppServerRun, method string, params map[string]any) (json.RawMessage, error) {
	unavailable := sessionRefusal("session_discovery_unavailable")
	input, feed, err := os.Pipe()
	if err != nil {
		return nil, unavailable
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := run(ctx, input, writer)
		writer.CloseWithError(err)
		done <- err
	}()
	// Cancellation ends input and unblocks a pending read, so a server that
	// never answers cannot hold discovery past its budget.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			feed.Close()
			reader.CloseWithError(ctx.Err())
		case <-stop:
		}
	}()
	finish := func(result json.RawMessage, err error) (json.RawMessage, error) {
		// End of input ends the server; drain its output until it exits so
		// the supervised copy completes, then join it.
		feed.Close()
		_, _ = io.Copy(io.Discard, reader)
		runErr := <-done
		input.Close()
		if err == nil && runErr != nil {
			err = unavailable
		}
		return result, err
	}
	lines := bufio.NewScanner(reader)
	lines.Buffer(make([]byte, 64<<10), 4<<20)
	send := func(message map[string]any) error {
		message["jsonrpc"] = "2.0"
		raw, err := json.Marshal(message)
		if err != nil {
			return err
		}
		_, err = feed.Write(append(raw, '\n'))
		return err
	}
	await := func(id int) (json.RawMessage, error) {
		for lines.Scan() {
			var reply struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(lines.Bytes(), &reply) != nil || reply.ID == nil || *reply.ID != id {
				continue // notifications and unrelated lines
			}
			if len(reply.Error) != 0 || len(reply.Result) == 0 {
				return nil, unavailable
			}
			return reply.Result, nil
		}
		return nil, unavailable
	}
	if send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "openabstractions", "title": "OpenAbstractions", "version": "0.8"}}}) != nil {
		return finish(nil, unavailable)
	}
	if _, err := await(1); err != nil {
		return finish(nil, err)
	}
	if send(map[string]any{"method": "initialized"}) != nil || send(map[string]any{"id": 2, "method": method, "params": params}) != nil {
		return finish(nil, unavailable)
	}
	raw, err := await(2)
	if err != nil {
		return finish(nil, err)
	}
	return finish(raw, nil)
}

func codexAttachThreads(ctx context.Context, run codexAppServerRun) ([]agentAttachSession, error) {
	raw, err := codexSummaryRPC(ctx, run, "thread/list", map[string]any{"limit": agentAttachSessions})
	if err != nil {
		return nil, err
	}
	var page struct {
		Data []struct {
			ID        string  `json:"id"`
			Cwd       string  `json:"cwd"`
			UpdatedAt int64   `json:"updatedAt"`
			Name      *string `json:"name"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &page) != nil {
		return nil, sessionRefusal("session_discovery_unavailable")
	}
	var found []agentAttachSession
	for _, thread := range page.Data {
		if !codexQueueThread.MatchString(thread.ID) || thread.UpdatedAt <= 0 {
			continue
		}
		updated := thread.UpdatedAt
		if updated < 1e12 { // the protocol reports seconds
			updated *= 1000
		}
		folder := filepath.Base(filepath.Clean(thread.Cwd))
		if thread.Cwd == "" || strings.IndexFunc(folder, unicode.IsControl) >= 0 {
			folder = ""
		}
		cwd := ""
		if filepath.IsAbs(thread.Cwd) && strings.IndexFunc(thread.Cwd, unicode.IsControl) < 0 {
			cwd = filepath.Clean(thread.Cwd)
		}
		title := ""
		if thread.Name != nil {
			title = hostChatTitle(*thread.Name)
		}
		found = append(found, agentAttachSession{Agent: "codex", Session: thread.ID, Folder: folder, Title: title, Updated: updated, cwd: cwd})
		if len(found) == agentAttachSessions {
			break
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Updated > found[j].Updated })
	return found, nil
}

// Only the vendor's explicit summary name supplies a title. Missing or
// oversized names remain unnamed; paths and prompt excerpts supply no fallback.
func hostChatTitle(value string) string {
	title, _ := client.NormalizeChatTitle(value)
	return title
}

// codexSubscriptionSignIn runs the documented status command, which performs
// no login. It accepts the installed program's ChatGPT sign-in, never an
// API-key backend.
func codexSubscriptionSignIn(ctx context.Context, p startprofile.Profile, selected agentStartCLI, environment []string) error {
	var status agentStartOutput
	status.max = 64 << 10
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	status.cancel = cancel
	err := runOwnedAgentStart(call, selected.Program(), p.Folder, []string{"login", "status"}, environment, &status, p.MemoryBytes)
	cancel()
	if err != nil || !strings.Contains(string(status.data), "Logged in using ChatGPT") {
		return errors.New("subscription_signin_unavailable")
	}
	return nil
}
