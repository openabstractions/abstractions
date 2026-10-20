package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	codexAttachThread = "01a10a6d-d9f4-7c61-9e33-4fe29defa7c6"
	codexOtherThread  = "01a18f6b-dab3-72a1-9b80-bf15c18a939f"
)

// codexAttachFixtureMain is an inert stand-in for the Codex CLI. It answers
// the sign-in status, a two-thread app-server listing, and records the
// delivery arguments; it holds no model, account or vendor settings.
func codexAttachFixtureMain() bool {
	if os.Getenv("OA_CODEX_ATTACH_FIXTURE") != "1" || len(os.Args) < 2 {
		return false
	}
	switch {
	case len(os.Args) == 2 && os.Args[1] == "--version":
		_, _ = io.WriteString(os.Stdout, "codex-cli 0.166.0")
	case len(os.Args) == 3 && os.Args[1] == "login" && os.Args[2] == "status":
		_, _ = io.WriteString(os.Stdout, os.Getenv("OA_CODEX_ATTACH_LOGIN"))
	case len(os.Args) == 2 && os.Args[1] == "app-server":
		codexAppServerFixture(os.Stdin, os.Stdout)
	case len(os.Args) == 3 && os.Args[1] == "queue" && os.Args[2] == "--help":
		if os.Getenv("OA_CODEX_ATTACH_NO_RESUME") == "1" {
			_, _ = io.WriteString(os.Stdout, "Options:\n  --thread\n")
		} else {
			_, _ = io.WriteString(os.Stdout, "Options:\n  --resume\n")
		}
	case os.Args[1] == "queue" || os.Args[1] == "exec":
		if want := os.Getenv("OA_CODEX_ATTACH_CWD"); os.Args[1] == "exec" && want != "" {
			// A headless resume runs in the thread's own folder.
			if cwd, err := os.Getwd(); err != nil || !strings.EqualFold(filepath.Clean(cwd), filepath.Clean(want)) {
				os.Exit(6)
			}
		}
		raw, _ := json.Marshal(os.Args[1:])
		if os.WriteFile(os.Getenv("OA_CODEX_ATTACH_MARKER"), raw, 0o600) != nil {
			os.Exit(4)
		}
		if os.Args[1] == "queue" {
			_, _ = io.WriteString(os.Stderr, os.Getenv("OA_CODEX_ATTACH_QUEUE_WARNING"))
		}
	default:
		os.Exit(4)
	}
	return true
}

// codexAppServerFixture answers initialize and thread/list in the public
// app-server shape and exits at end of input.
func codexAppServerFixture(in io.Reader, out io.Writer) {
	lines := bufio.NewScanner(in)
	initialized := false
	for lines.Scan() {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(lines.Bytes(), &request) != nil {
			os.Exit(4)
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"userAgent": "fixture"}
		case "initialized":
			initialized = true
			continue
		case "thread/list":
			if !initialized {
				os.Exit(4)
			}
			current := attachFixtureFolder("work", "current")
			if cwd := os.Getenv("OA_CODEX_ATTACH_CWD"); cwd != "" {
				current = cwd
			}
			result = map[string]any{"data": []map[string]any{
				{"id": codexOtherThread, "cwd": attachFixtureFolder("work", "older"), "updatedAt": 1793300000, "preview": "never read"},
				{"id": codexAttachThread, "cwd": current, "updatedAt": 1793400000},
				{"id": "not-a-thread", "cwd": attachFixtureFolder("x"), "updatedAt": 1},
			}, "nextCursor": nil}
		default:
			os.Exit(4)
		}
		_, _ = io.WriteString(out, `{"jsonrpc":"2.0","method":"notice","params":{}}`+"\n")
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
		_, _ = out.Write(append(raw, '\n'))
	}
}

// Discovery speaks the app-server's own protocol, keeps well-formed threads
// newest first, labels each by its folder name only and reads no content.
func TestCodexAttachThreadsFromAppServer(t *testing.T) {
	threads, err := codexAttachThreads(context.Background(), func(_ context.Context, input *os.File, out io.Writer) error {
		codexAppServerFixture(input, out)
		return nil
	})
	if err != nil || len(threads) != 2 {
		t.Fatal(threads, err)
	}
	if threads[0].Session != codexAttachThread || threads[0].Folder != "current" || threads[0].Updated != 1793400000000 || threads[1].Session != codexOtherThread {
		t.Fatalf("%+v", threads)
	}
	if _, err := codexAttachThreads(context.Background(), func(context.Context, *os.File, io.Writer) error {
		return errors.New("app-server failed to start")
	}); agentSessionReason(err) != "session_discovery_unavailable" {
		t.Fatal(err)
	}
	// A server that reads input and never answers ends at the caller's budget.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := codexAttachThreads(ctx, func(_ context.Context, input *os.File, _ io.Writer) error {
		_, _ = io.Copy(io.Discard, input)
		return nil
	}); agentSessionReason(err) != "session_discovery_unavailable" {
		t.Fatal("silent server held discovery", err)
	}
}
