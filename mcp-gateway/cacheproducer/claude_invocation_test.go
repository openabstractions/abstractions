package cacheproducer

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// The gateway command reports a wrapped vendor failure through this one
// owner: the closed tokens alone, with the whole chain kept as cause.
func TestClaudeInvocationDiagnosticWordsTheFailureAlone(t *testing.T) {
	secret := errors.New("secret vendor output")
	wrapped := fmt.Errorf("apply: %w", ClaudeInvocationFailure(context.Background(), "install", true, secret))
	err := ClaudeInvocationDiagnostic(wrapped)
	var invocation *ClaudeInvocationError
	if !errors.As(err, &invocation) || invocation.Operation() != "install" || invocation.Reason() != "unavailable" || !invocation.Mutating() {
		t.Fatalf("diagnostic = %#v", err)
	}
	if err.Error() != "native install failed: unavailable; outcome unknown; no automatic retry" || !errors.Is(err, secret) {
		t.Fatalf("diagnostic = %v", err)
	}
	other := errors.New("not a vendor call")
	if ClaudeInvocationDiagnostic(other) != other || ClaudeInvocationDiagnostic(nil) != nil {
		t.Fatal("a non-invocation error changed")
	}
}
