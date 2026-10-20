package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
)

func TestMessagePublicationDescriptorFence(t *testing.T) {
	const childPath = "OA_TEST_MESSAGE_PUBLICATION_DESCRIPTOR"
	if path := os.Getenv(childPath); path != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := cas.ChangeLimitContext(ctx, path, 1024, func([]byte) ([]byte, error) {
			return []byte("{}"), nil
		})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("exclusive change under publication: %v", err)
		}
		fmt.Println("writer-blocked")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		if err := cas.ChangeLimit(path, 1024, func([]byte) ([]byte, error) { return []byte("{}"), nil }); err != nil {
			t.Fatal(err)
		}
		return
	}

	path := filepath.Join(t.TempDir(), "descriptors.json")
	facts := messagePublicationFacts{descriptor: path, raw: []messagePartyInput{{path: path, limit: 1024}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMessagePublicationDescriptorFence$")
	cmd.Env = append(os.Environ(), childPath+"="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	published, err := facts.publish(ctx, func() error {
		release, err := cas.LockContext(ctx, path, true)
		if err != nil {
			return err
		}
		release()
		if err := cmd.Start(); err != nil {
			return err
		}
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			return err
		}
		if line != "writer-blocked\n" {
			return fmt.Errorf("unexpected writer result: %q", line)
		}
		return nil
	})
	if !published || err != nil {
		cancel()
		if cmd.Process != nil {
			_ = cmd.Wait()
		}
		t.Fatalf("publication: %t, %v", published, err)
	}
	if _, err = fmt.Fprintln(stdin, "released"); err != nil {
		t.Error(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	published, err = facts.publish(ctx, func() error {
		t.Error("published using changed descriptors")
		return nil
	})
	if published || err != nil {
		t.Fatalf("stale publication: %t, %v", published, err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	published, err = facts.publish(ctx, func() error {
		t.Error("treated a present empty descriptor as absent")
		return nil
	})
	if published || err != nil {
		t.Fatalf("empty descriptor publication: %t, %v", published, err)
	}
}
