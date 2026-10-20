//go:build windows

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// serveHostedLocalChat is J-CLOUD-LOSS's local model: a native chat@1 leaf,
// declared to the runtime like any local model host, whose own router
// serves one model through an OpenAI-compatible server on loopback inside
// this process. It answers "local reply from <model>".
func serveHostedLocalChat(endpoint, runtimeProgram, model string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	reply := "local reply from " + model
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{{"id": model, "object": "model"}}})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &body)
		writeChatReply(w, body.Stream, model, reply)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	rt := router.New(router.OpenAICompatible("journey-local", "http://"+ln.Addr().String(), "/v1", []string{model}))
	rt.Survey()
	provider, err := inference.New(inference.Config{Router: rt,
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "applied"
		}})
	if err != nil {
		return err
	}
	defer provider.Close()
	leaf, err := service.ListenDesignatedLeaf(endpoint, provider, inference.ExecutionLocal, runtimeProgram)
	if err != nil {
		return err
	}
	leaf.Residents = func() []string { return []string{model} }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := leaf.Serve(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
