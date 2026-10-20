package main

import (
	"bytes"
	"strings"
	"testing"
)

// The models host tests measure the declaration logic with stand-in
// programs on every platform, so they run with the platform limit lifted;
// TestModelsHostSaysNativeProvidersAreUnavailableOnMacOS checks the limit.
func init() { nativeProviderLimit = "" }

// On macOS "models host" refuses before it reads a flag or reaches a runtime,
// naming why: no native provider can start there in 0.4.0.
func TestModelsHostSaysNativeProvidersAreUnavailableOnMacOS(t *testing.T) {
	if nativeProviderLimitOn("windows") != "" || nativeProviderLimitOn("linux") != "" {
		t.Fatal("a platform that runs native providers names a limit")
	}
	saved := nativeProviderLimit
	t.Cleanup(func() { nativeProviderLimit = saved })
	nativeProviderLimit = nativeProviderLimitOn("darwin")
	var output, diagnostics bytes.Buffer
	err := modelsCommand([]string{"host", "store/object", "--engine", "/absolute/llama-server"}, &output, &diagnostics)
	assertExit(t, err, exitRefusedCall, "models host on macOS")
	if !strings.Contains(err.Error(), "native providers are not available on macOS in 0.4.0: their caller check needs an XPC endpoint") || output.Len() != 0 {
		t.Fatalf("models host on macOS: %v output=%q", err, output.String())
	}
	if err := modelsCommand([]string{"host", "--help"}, &output, &diagnostics); err != nil {
		t.Fatalf("help on macOS: %v", err)
	}
}
