package setup

import (
	"errors"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go-core/redact/redacttest"
)

// A failed provider verb's stderr reaches the panel through redact.Line:
// every row of the bypass table answers as the owner answers it, and an
// empty stderr falls back to the run error.
func TestLemonadeFailureRedactsTheProvidersWords(t *testing.T) {
	for _, c := range redacttest.Cases {
		if got := lemonadeFailure(c.In, errors.New("exit status 1")).Reason; got != c.Want {
			t.Errorf("lemonadeFailure(%q).Reason = %q, want %q", c.In, got, c.Want)
		}
	}
	got := lemonadeFailure("first Bearer fixtureSECRET\nsecond line", errors.New("exit status 1"))
	if strings.Contains(got.Error(), "fixtureSECRET") || got.Code != 1 {
		t.Fatalf("lemonadeFailure = %+v", got)
	}
	if got := lemonadeFailure(" \n", errors.New("exit status 7")); got.Reason != "exit status 7" {
		t.Fatalf("an empty stderr gave %q", got.Reason)
	}
}
