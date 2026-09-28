package main

import (
	"bytes"
	"errors"
	"testing"
)

type failedAvailabilityWriter struct{}

func (failedAvailabilityWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestReportWebView2Availability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available bool
		code      int
		output    string
	}{
		{"available", true, 0, "WebView2: available (native rendering untested)\n"},
		{"missing", false, 3, "WebView2: unavailable (browser fallback)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			calls := 0
			code := reportWebView2Availability(&out, func() bool {
				calls++
				return tc.available
			})
			if calls != 1 || code != tc.code || out.String() != tc.output {
				t.Fatalf("calls=%d code=%d output=%q; want 1, %d, %q", calls, code, out.String(), tc.code, tc.output)
			}
		})
	}
}

func TestReportWebView2AvailabilityWriteFailure(t *testing.T) {
	for _, available := range []bool{true, false} {
		if code := reportWebView2Availability(failedAvailabilityWriter{}, func() bool { return available }); code != 1 {
			t.Errorf("available=%t: exit %d, want write failure 1", available, code)
		}
	}
}
