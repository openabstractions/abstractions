package main

import (
	"strings"
	"testing"
)

func TestServiceRejectsCodecHelperCollision(t *testing.T) {
	for _, name := range []string{"Reader", "Raw", "Refusal"} {
		t.Run(name, func(t *testing.T) {
			_, err := parse(head + strings.Replace(serviceFixture, "service Events", "service "+name, 1))
			if err == nil || !strings.Contains(err.Error(), "name collision") {
				t.Fatalf("got %v; expected helper collision", err)
			}
		})
	}
}
