//go:build windows

package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestOpenCodeAdmissionCloseRetriesRestoration(t *testing.T) {
	for _, stopFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop-success", true: "stop-error"}[stopFails], func(t *testing.T) {
			var events []string
			stopErr := errors.New("listener close failed")
			restoreErr := errors.New("query restoration failed")
			attempts := 0
			close := closeSessionAdmission(func() error {
				events = append(events, "stop", "join")
				if stopFails {
					return stopErr
				}
				return nil
			}, func() error {
				events = append(events, "restore")
				attempts++
				if attempts == 1 {
					return restoreErr
				}
				return nil
			}, func() { events = append(events, "release") })
			if err := close(); !errors.Is(err, restoreErr) || errors.Is(err, stopErr) != stopFails {
				t.Fatalf("first close: %v", err)
			}
			for i := 0; i < 2; i++ {
				if err := close(); errors.Is(err, restoreErr) || errors.Is(err, stopErr) != stopFails || (err != nil && !stopFails) {
					t.Fatalf("retry/idempotent close: %v", err)
				}
			}
			if want := []string{"stop", "join", "restore", "release", "restore"}; !reflect.DeepEqual(events, want) {
				t.Fatalf("close events %v want %v", events, want)
			}
		})
	}
}

func TestOpenCodeAdmissionSuccessfulCloseIsIdempotent(t *testing.T) {
	var events []string
	close := closeSessionAdmission(func() error { events = append(events, "stop", "join"); return nil },
		func() error { events = append(events, "restore"); return nil },
		func() { events = append(events, "release") })
	for i := 0; i < 3; i++ {
		if err := close(); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"stop", "join", "restore", "release"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("close events %v want %v", events, want)
	}
}
