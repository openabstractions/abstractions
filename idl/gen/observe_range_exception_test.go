package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveRangeExceptionsAreExactAndLive(t *testing.T) {
	for _, tc := range []struct{ name, namespace, wire, method, bound, want string }{
		{"legacy", "abstraction.config", "abstraction.config/observer@1", "Observe", "", ""},
		{"declared", "example.watch", "example.watch/watch@1", "Observe", `range="0..120000"`, ""},
		{"unlisted", "example.watch", "example.watch/watch@1", "Observe", "", "requires a declared range"},
		{"range gained", "abstraction.config", "abstraction.config/observer@1", "Observe", `range="0..30000"`, "stale observation range exception"},
		{"wire gone", "abstraction.config", "example.watch/watch@1", "Observe", `range="0..30000"`, "stale observation range exception"},
		{"method gone", "abstraction.config", "abstraction.config/observer@1", "Read", `range="0..30000"`, "stale observation range exception"},
		{"metadata gone", "abstraction.config", "abstraction.config/observer@1", "Observe", "", "stale observation range exception"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound := ""
			if tc.bound != "" {
				bound = "(" + tc.bound + ")"
			}
			header := strings.Replace(constraintHead, "  10: bad_binary       (stage = \"structure\")\n", "", 1)
			if bound == "" {
				header = strings.Replace(header, "  11: bad_value        (stage = \"structure\")\n", "", 1)
				header = strings.Replace(header, "  12: trailing_bytes", "  10: trailing_bytes", 1)
			} else {
				header = strings.Replace(header, "  11: bad_value", "  10: bad_value", 1)
				header = strings.Replace(header, "  12: trailing_bytes", "  11: trailing_bytes", 1)
			}
			observation := `(observe="cursor")`
			if tc.name == "metadata gone" {
				observation = ""
			}
			fixture := "namespace go " + tc.namespace + "\n" + header + `struct Page {1:required string next}(document="true",unknown_fields="refuse")
service Watch {Page ` + tc.method + `(1:string cursor,2:i64 wait_ms` + bound + `)` + observation + `}(wire_name="` + tc.wire + `")`
			dir := t.TempDir()
			path := filepath.Join(dir, "watch.thrift")
			writeNamespaceFile(t, dir, "watch.thrift", fixture)
			var report bytes.Buffer
			err := run([]string{path, filepath.Join(dir, "out"), "go"}, &report)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v, want %s", err, tc.want)
				}
				// Projection must not hide an invalid or stale unfiltered owner.
				err = run([]string{path, filepath.Join(dir, "selected"), "go", "-only=Page"}, &report)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("projected got %v, want %s", err, tc.want)
				}
				err = run([]string{path, filepath.Join(dir, "relocated"), "go", "--namespace=go=example.relocated"}, &report)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("output placement hid owning definition: got %v, want %s", err, tc.want)
				}
			}
			if tc.name == "declared" {
				d := mustParse(t, fixture)
				for _, be := range backends {
					if !contains([]string{"go", "python", "javascript", "rust", "cpp"}, be.lang) {
						continue
					}
					body := mustEmit(t, be, d)
					if !strings.Contains(body, "120000") || strings.Contains(body, "30000") {
						t.Fatal(be.lang, "did not derive 0..120000")
					}
				}
			}
		})
	}
}

func TestObserveRangeValidationPrecedesServiceProjection(t *testing.T) {
	only := []string{"ConfigReader", "RunOverrides", "Snapshot", "Origins", "Origin", "reader_error_codes"}
	var report bytes.Buffer
	if err := run([]string{productionFile(t, "abstraction-config/config.thrift"), t.TempDir(), "go", "-only=" + strings.Join(only, ","), "--go-import=rights=github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"}, &report); err != nil {
		t.Fatal("legitimate config service projection looked like API removal:", err)
	}
	for _, name := range []string{"abstraction-rights/rights.thrift", "abstraction-job/acceptance.thrift"} {
		d, err := loadDefinition(productionFile(t, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateObserveRanges(d); err != nil {
			t.Fatal("unrelated definition required config exception:", err)
		}
	}
}
