// Command oa-journey-chat is the journey suite's stand-in model host
// (release/testing/journeys.py, J-MODELS). It serves the native chat@1 leaf a
// model host serves and states one loaded model on Describe, the way the
// model host's llama.cpp residency reaches the router. It runs no model.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	inference "github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

func main() {
	endpoint := flag.String("endpoint", "", "native provider endpoint the runtime names")
	runtimeProgram := flag.String("runtime", "", "absolute path of the OA runtime executable that may call this host")
	loaded := flag.String("loaded", "journey-chat-small", "comma-separated model names this host reports as loaded")
	flag.Parse()
	if *endpoint == "" || *runtimeProgram == "" {
		fmt.Fprintln(os.Stderr, "oa-journey-chat: -endpoint and -runtime are required")
		os.Exit(2)
	}
	provider, err := inference.New(inference.Config{Router: router.New(),
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "applied"
		}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "oa-journey-chat:", err)
		os.Exit(1)
	}
	leaf, err := service.ListenDesignatedLeaf(*endpoint, provider, inference.ExecutionLocal, *runtimeProgram)
	if err != nil {
		fmt.Fprintln(os.Stderr, "oa-journey-chat:", err)
		os.Exit(1)
	}
	names := strings.Split(*loaded, ",")
	leaf.Residents = func() []string { return append([]string(nil), names...) }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = leaf.Serve(ctx)
	provider.Close()
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "oa-journey-chat:", err)
		os.Exit(1)
	}
}
