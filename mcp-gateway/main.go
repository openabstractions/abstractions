package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func commandFlags(output io.Writer, defaultState string) (*flag.FlagSet, *string) {
	flags := flag.NewFlagSet("openabstractions-mcp", flag.ContinueOnError)
	flags.SetOutput(output)
	state := flags.String("state-dir", defaultState, "absolute directory for durable opaque job handles")
	flags.Usage = func() {
		fmt.Fprintln(output, `usage: openabstractions-mcp [--state-dir ABSOLUTE_PATH]

Local stdio MCP server. It exposes this fixed tool inventory:
  oa_applications_list       read visible application instances and contexts
  oa_models_list             read servable model families
  oa_inference_complete      run one bounded text inference call
  oa_inference_job_submit    submit durable image or video inference
  oa_inference_job_status    read one durable inference handle
  oa_inference_job_cancel    explicitly request cancellation of one handle

Authority belongs to the OS-bound OpenAbstractions identity of this gateway
executable. MCP clientInfo, request metadata and parent process IDs never grant
or select OA rights. Sessions using the same executable share that configured
integration principal. Applications and models are read-only; completion,
submission and cancellation can cause provider or job effects.

Supported MCP protocol versions: 2026-07-28, 2025-11-25.

options:`)
		flags.PrintDefaults()
	}
	return flags, state
}

func main() {
	config, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "openabstractions-mcp:", err)
		os.Exit(1)
	}
	flags, state := commandFlags(os.Stderr, filepath.Join(config, "OpenAbstractions", "mcp-gateway"))
	if err := flags.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return
		}
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "openabstractions-mcp: unexpected arguments")
		os.Exit(2)
	}
	backend, err := gateway.NewOA(facade.Discover(), *state)
	if err != nil {
		fmt.Fprintln(os.Stderr, "openabstractions-mcp:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := gateway.NewServer(backend).Run(ctx, &mcp.StdioTransport{MaxLineLength: 1 << 20}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "openabstractions-mcp:", err)
		os.Exit(1)
	}
}
