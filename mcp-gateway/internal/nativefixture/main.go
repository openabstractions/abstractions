// nativefixture is a test-only MCP stdio gateway whose OA runtime endpoint is
// explicit. Building this package to distinct paths gives integration tests
// distinct native Program principals without touching an installed runtime.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

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
	phase := func(name string) {
		//unchecked: diagnostic-only stderr; stdout belongs to the MCP transport
		fmt.Fprintf(os.Stderr, "nativefixture %s %s\n", time.Now().UTC().Format(time.RFC3339Nano), name)
	}
	phase("entry")
	endpoint := flag.String("runtime", "", "isolated OA fixture runtime endpoint")
	state := flag.String("state-dir", "", "absolute gateway state directory")
	flag.Parse()
	if *endpoint == "" || *state == "" || flag.NArg() != 0 {
		//unchecked: last diagnostic before os.Exit in a test fixture; a failed write to stderr has no fallback destination
		fmt.Fprintln(os.Stderr, "nativefixture: runtime and state-dir are required")
		os.Exit(2)
	}
	phase("NewOA begin")
	backend, err := gateway.NewOA(facade.New(*endpoint), *state)
	if err != nil {
		//unchecked: last diagnostic before os.Exit in a test fixture; a failed write to stderr has no fallback destination
		fmt.Fprintln(os.Stderr, "nativefixture:", err)
		os.Exit(1)
	}
	phase("NewOA ready")
	var served gateway.Backend = backend
	if os.Getenv("OA_MCP_FIXTURE_DROP_AFTER_SUBMIT") == "1" {
		served = dropAfterSubmit{Backend: backend}
	}
	phase("NewServer begin")
	server := gateway.NewServer(served)
	phase("NewServer ready")
	phase("MCPServe begin")
	if err := server.Run(context.Background(), &mcp.StdioTransport{MaxLineLength: 1 << 20}); err != nil {
		//unchecked: last diagnostic before os.Exit in a test fixture; a failed write to stderr has no fallback destination
		fmt.Fprintln(os.Stderr, "nativefixture:", err)
		os.Exit(1)
	}
}
