package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTerminalOutputInputReadsExplicitSelection(t *testing.T) {
	for _, text := range []string{"Replace\n", "k\n", "Cancel\n", "invalid\nkeep\n", "replx\bace\n"} {
		t.Run(text, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "input")
			if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			in, err := os.Open(file)
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			option, ready, err := terminalOutputInput(in, &bytes.Buffer{})(context.Background(), 100*time.Millisecond)
			want := "replace"
			if text == "k\n" || text == "invalid\nkeep\n" {
				want = "keep"
			} else if text == "Cancel\n" {
				want = "cancel"
			}
			if err != nil || !ready || option != want {
				t.Fatalf("selection = %q ready=%t error=%v", option, ready, err)
			}
		})
	}
}

func TestTerminalOutputInputIdleCancelledAndClosed(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	input := terminalOutputInput(reader, &bytes.Buffer{})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	option, ready, err := input(ctx, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || option != "" || ready || time.Since(start) > time.Second {
		t.Fatalf("idle input did not cancel promptly: %q %t %v", option, ready, err)
	}
	if _, err := writer.Write([]byte("replace")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	option, ready, err = input(context.Background(), 100*time.Millisecond)
	if !errors.Is(err, io.EOF) || option != "" || ready {
		t.Fatalf("partial choice at EOF selected an option: %q %t %v", option, ready, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := input(context.Background(), time.Second); err == nil || ready {
		t.Fatal("closed input did not fail")
	}
}
