//go:build !windows && !darwin

package main

// Native process birth is unavailable here; do not substitute wall-clock time.
func agentDiagnosticBirth() string { return "" }

// Without a native birth a reused PID cannot be told apart; prove nothing.
func agentProcessGone(int, string) bool { return false }
