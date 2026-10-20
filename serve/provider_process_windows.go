package main

import (
	"context"
	"os"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
)

const providerForceTimeout = 5 * time.Second

// startProviderChild starts program with env as its whole environment and
// output as its standard output and error; a nil output leaves both
// unconnected.
func startProviderChild(program string, args, env []string, output *os.File) (providerChild, error) {
	return supervision.Start(context.Background(), supervision.Options{
		Executable: program, Args: args, Env: env, ForceTimeout: providerForceTimeout, Stdout: output, Stderr: output,
	})
}
