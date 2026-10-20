package main

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requireNativeModelHostRegistration(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("Registry v3 native ModelHost registration requires Windows held image custody")
	}
}

// The models host tests measure the declaration logic with stand-in
// programs on every platform, so they run with the platform limit lifted;
// TestModelsHostSaysNativeProvidersAreUnavailableOnMacOS checks the limit.
func init() { nativeProviderLimit = "" }

func TestModelsHostRefusesLinuxNativeRegistrationBeforeEffects(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("checks the native Linux command")
	}
	for _, options := range [][]string{nil, {
		"--endpoint", filepath.Join(t.TempDir(), "absent.sock"),
		"--program", filepath.Join(t.TempDir(), "absent-modelhostd"), "--engine", "/absent/llama-server",
	}} {
		var output, diagnostics bytes.Buffer
		args := append([]string{"host", "store/object"}, options...)
		err := modelsCommand(args, &output, &diagnostics)
		assertExit(t, err, exitRefusedCall, "models host on Linux")
		if !strings.Contains(err.Error(), linuxModelHostRegistrationLimit) || output.Len() != 0 || diagnostics.Len() != 0 {
			t.Fatalf("models host on Linux: %v output=%q diagnostics=%q", err, output.String(), diagnostics.String())
		}
	}
}

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
