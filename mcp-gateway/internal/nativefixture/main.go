// nativefixture is a test-only MCP stdio gateway whose OA runtime endpoint is
// explicit. Building this package to distinct paths gives integration tests
// distinct native Program principals without touching an installed runtime.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type dropAfterSubmit struct{ gateway.Backend }

func (b dropAfterSubmit) Submit(ctx context.Context, in gateway.JobSubmitInput) (gateway.JobSubmitOutput, error) {
	out, err := b.Backend.Submit(ctx, in)
	if os.Getenv("OA_MCP_FIXTURE_DROP_AFTER_SUBMIT") == "1" {
		os.Exit(86)
	}
	return out, err
}

func main() {
	endpoint := flag.String("runtime", "", "isolated OA fixture runtime endpoint")
	state := flag.String("state-dir", "", "absolute gateway state directory")
	flag.Parse()
	if *endpoint == "" || *state == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "nativefixture: runtime and state-dir are required")
		os.Exit(2)
	}
	backend, err := gateway.NewOA(facade.New(*endpoint), *state)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nativefixture:", err)
		os.Exit(1)
	}
	var served gateway.Backend = backend
	if os.Getenv("OA_MCP_FIXTURE_DROP_AFTER_SUBMIT") == "1" {
		served = dropAfterSubmit{Backend: backend}
	}
	if err := gateway.NewServer(served).Run(context.Background(), &mcp.StdioTransport{MaxLineLength: 1 << 20}); err != nil {
		fmt.Fprintln(os.Stderr, "nativefixture:", err)
		os.Exit(1)
	}
}
