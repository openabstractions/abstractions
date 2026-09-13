package main

import (
	"context"
	"errors"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStartHelperProcess(t *testing.T) {
	if os.Getenv("OA_START_HELPER") == "" {
		return
	}
	if os.Getenv("OA_START_HELPER") == "sleep" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	n, _ := strconv.Atoi(os.Getenv("OA_START_HELPER"))
	os.Exit(n)
}
func TestStartLifecycle(t *testing.T) {
	for _, mode := range []string{"already", "activate", "refused", "failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			probes := 0
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			refusal := errors.New("refused")
			h := startHooks{guard: func() error {
				if mode == "refused" {
					return refusal
				}
				return nil
			}, activate: func(ctx context.Context) error {
				calls++
				if mode == "failure" {
					return refusal
				}
				return nil
			}, ready: func(ctx context.Context) (bool, error) {
				probes++
				return mode == "already" || mode == "activate" && probes > 1, nil
			}}
			err := startInstalled(ctx, h)
			switch mode {
			case "already":
				if err != nil || calls != 0 {
					t.Fatal(err, calls)
				}
			case "activate":
				if err != nil || calls != 1 {
					t.Fatal(err, calls)
				}
			case "refused":
				if !errors.Is(err, refusal) || calls != 0 || probes != 0 {
					t.Fatal(err, calls, probes)
				}
			case "failure":
				if !errors.Is(err, refusal) || probes != 1 {
					t.Fatal(err, probes)
				}
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
					t.Fatal(err, calls)
				}
			}
		})
	}
}
func TestStartCommandCancellation(t *testing.T) {
	t.Setenv("OA_START_HELPER", "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err := startCommand(ctx, os.Args[0], "-test.run=^TestStartHelperProcess$")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func exitForStartTest(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartHelperProcess$")
	cmd.Env = append(os.Environ(), "OA_START_HELPER="+strconv.Itoa(code))
	err := cmd.Run()
	if err == nil {
		t.Fatal("missing fixture error")
	}
	return err
}
func TestStartFlagsDoNotActivate(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--timeout=0"}, {"unexpected"}} {
		if runtimeStart(args, io.Discard, io.Discard) == nil {
			t.Fatal(args)
		}
	}
}

func TestStartReadyRequiresAllRuntimeContracts(t *testing.T) {
	proven := statusTransportProvesProgram(t)
	all := [][2]string{{"abstraction.logging", "abstraction.logging/sink@1"}, {"abstraction.config", "abstraction.config/reader@1"}, {"abstraction.job", "abstraction.job/acceptance@1"}, {"abstraction.job", "abstraction.job/operations@1"}, {"abstraction.config", "abstraction.config/editor@1"}}
	for _, count := range []int{2, 3, 4, 5} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			options, _ := isolatedRuntime(t)
			var candidates []resolution.Candidate
			for _, item := range all[:count] {
				candidates = append(candidates, resolution.Candidate{Ready: true, Reference: wire.ServiceReference{Provider: "fixture", Capability: item[0], Contract: item[1], Transport: "oa-framed-local@1", Endpoint: options.endpoint, Scope: "local"}})
			}
			catalog, err := resolution.New(candidates)
			if err != nil {
				t.Fatal(err)
			}
			host, err := resolution.Listen(options.endpoint, catalog, func(*identity.Peer, wire.ServiceReference) bool { return true })
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- host.Serve(ctx) }()
			defer func() {
				cancel()
				host.Close()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("fixture cleanup timed out")
				}
			}()
			ready, err := startReady(ctx, options.endpoint)
			if (count == 3 || count == 4) && proven {
				if err == nil || !strings.Contains(err.Error(), "incompatible") {
					t.Fatalf("missing runtime contract refusal: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if ready != (count == 5 && proven) {
				t.Fatalf("%d contracts ready=%v Program proof=%v", count, ready, proven)
			}
		})
	}
}
