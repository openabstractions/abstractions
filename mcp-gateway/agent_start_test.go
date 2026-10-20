package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixtureStartRecipe(run func(context.Context, string, io.Writer, uint64) error) agentStartResolver {
	return func(context.Context, string, string) (*agentStartRecipe, error) {
		return &agentStartRecipe{profile: "fixture", context: "queue-1", folder: tFixtureFolder, timeout: time.Second, outputBytes: 64, memoryBytes: 64 << 20, approved: true, check: func(context.Context) error { return nil }, run: run}, nil
	}
}

const tFixtureFolder = "fixture-owned-workspace"

func TestAgentStartFixedPointerAndCapturedResult(t *testing.T) {
	r := runAgentStart(context.Background(), "fixture", "queue-1", fixtureStartRecipe(func(ctx context.Context, prompt string, out io.Writer, memory uint64) error {
		if prompt != agentStartPrompt("queue-1") || memory != 64<<20 {
			t.Fatal(prompt, memory)
		}
		_, err := io.WriteString(out, `{"result":"done"}`)
		return err
	}))
	if r.Outcome != "exited" || r.Metadata == nil || r.Metadata.StdoutBytes == 0 || r.Capture != "incomplete" {
		t.Fatal(r)
	}
}

func TestAgentStartRefusesUnapprovedBeforeExecution(t *testing.T) {
	called := false
	resolver := fixtureStartRecipe(func(context.Context, string, io.Writer, uint64) error { called = true; return nil })
	r := runAgentStart(context.Background(), "fixture", "queue-1", func(ctx context.Context, p, c string) (*agentStartRecipe, error) {
		r, e := resolver(ctx, p, c)
		r.approved = false
		return r, e
	})
	if called || r.Outcome != "refused" {
		t.Fatal(r, called)
	}
	if r := runAgentStart(context.Background(), "fixture", "queue-1", registeredAgentStartResolver(nil)); r.Outcome != "refused" {
		t.Fatal(r)
	}
}

func TestAgentStartOutputAndTimeBounds(t *testing.T) {
	r := runAgentStart(context.Background(), "fixture", "queue-1", fixtureStartRecipe(func(ctx context.Context, _ string, w io.Writer, _ uint64) error {
		_, e := io.WriteString(w, strings.Repeat("x", 65))
		return e
	}))
	if r.Reason != "output_limit" || r.Metadata == nil || !r.Metadata.Truncated {
		t.Fatal(r)
	}
	resolver := fixtureStartRecipe(func(ctx context.Context, _ string, _ io.Writer, _ uint64) error { <-ctx.Done(); return ctx.Err() })
	r = runAgentStart(context.Background(), "fixture", "queue-1", func(ctx context.Context, p, c string) (*agentStartRecipe, error) {
		r, e := resolver(ctx, p, c)
		r.timeout = time.Millisecond
		return r, e
	})
	if r.Reason != "time_limit" {
		t.Fatal(r)
	}
}

func TestAgentStartCommandRejectsArbitraryArguments(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if code := agentStartCommand([]string{"--profile", "fixture", "--context", "queue-1", "peer body"}, &out, &diagnostic, nil); code != 2 || out.Len() != 0 {
		t.Fatal(code, out.String())
	}
	if code := agentStartCommand([]string{"--profile", "fixture", "--context", "queue-1"}, &out, &diagnostic, registeredAgentStartResolver(nil)); code != 2 {
		t.Fatal(code)
	}
}

func TestAgentRecipeContextRemainsRoutingLabel(t *testing.T) {
	if c, e := agentContext("recipe-context"); e != nil || c != "recipe-context" {
		t.Fatal(c, e)
	}
	if _, e := agentContext("bad context"); e == nil {
		t.Fatal("invalid context accepted")
	}
	a, e := agentContext("")
	if e != nil {
		t.Fatal(e)
	}
	b, e := agentContext("")
	if e != nil || a == b || !strings.HasPrefix(a, "native-") {
		t.Fatal(a, b, e)
	}
}

func TestAgentStartOwnedProcessCapture(t *testing.T) {
	if os.Getenv("OA_AGENT_START_FIXTURE") == "capture" {
		_, _ = io.WriteString(os.Stdout, "fixture result")
		os.Exit(0)
	}
	if runtime.GOOS != "windows" {
		t.Skip("owned process supervision currently Windows-only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	err = runOwnedAgentStart(ctx, exe, t.TempDir(), []string{"-test.run=^TestAgentStartOwnedProcessCapture$"}, append(os.Environ(), "OA_AGENT_START_FIXTURE=capture"), &out, 256<<20)
	if err != nil || out.String() != "fixture result" {
		t.Fatal(err, out.String())
	}
}

func TestAgentStartOwnedProcessDeadline(t *testing.T) {
	if os.Getenv("OA_AGENT_START_FIXTURE") == "wait" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if runtime.GOOS != "windows" {
		t.Skip("owned process supervision currently Windows-only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	if err := runOwnedAgentStart(ctx, exe, t.TempDir(), []string{"-test.run=^TestAgentStartOwnedProcessDeadline$"}, append(os.Environ(), "OA_AGENT_START_FIXTURE=wait"), &out, 256<<20); err == nil {
		t.Fatal("deadline did not end owned process")
	}
}
