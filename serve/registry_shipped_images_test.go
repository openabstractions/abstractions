package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"strings"
	"testing"
)

func TestShippedProviderRecordProducerInterop(t *testing.T) {
	encoded := os.Getenv("OA_SHIPPED_PROVIDER_IMAGES_TEST_RECORD")
	if encoded == "" {
		t.Skip("producer-owned exact record fixture not supplied")
	}
	got, err := parseShippedProviderImages(encoded)
	console := sha256.Sum256([]byte("final provider console bytes"))
	unicode := sha256.Sum256([]byte("final provider unicode bytes"))
	want := map[string]string{"InventoryD.exe": hex.EncodeToString(console[:]), "nested/é&line\u2028\u2029.exe": hex.EncodeToString(unicode[:])}
	if err != nil || !maps.Equal(got, want) {
		t.Fatal("producer record differs from byte-exact runtime fixture", got, err)
	}
}

func shippedTestRecord(images map[string]string) string {
	raw, _ := json.Marshal(map[string]any{"version": 1, "images": images})
	return base64.StdEncoding.EncodeToString(raw)
}

func TestShippedProviderRecordFailsClosed(t *testing.T) {
	valid := shippedTestRecord(map[string]string{"nested/provider.exe": strings.Repeat("a", 64)})
	if got, err := parseShippedProviderImages(valid); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	for _, name := range []string{"nested/é&provider.exe", "nested/line\u2028separator\u2029.exe"} {
		raw := `{"images":{"` + name + `":"` + strings.Repeat("a", 64) + `"},"version":1}`
		if _, err := parseShippedProviderImages(base64.StdEncoding.EncodeToString([]byte(raw))); err != nil {
			t.Fatal("canonical UTF-8 rejected", err)
		}
	}
	if got, err := parseShippedProviderImages(""); got != nil || err != nil {
		t.Fatal(got, err)
	}
	for _, raw := range []string{
		`{"images":{"a/../provider.exe":"` + strings.Repeat("a", 64) + `"},"version":1}`,
		`{"images":{"a\\provider.exe":"` + strings.Repeat("a", 64) + `"},"version":1}`,
		`{"images":{"provider.exe:stream":"` + strings.Repeat("a", 64) + `"},"version":1}`,
		`{"images":{"../provider.exe":"` + strings.Repeat("a", 64) + `"},"version":1}`,
		`{"images":{"/provider.exe":"` + strings.Repeat("a", 64) + `"},"version":1}`,
		`{"images":{"a//b.exe":"` + strings.Repeat("a", 64) + `"},"version":1}`,
		`{"images":{"provider.exe":"` + strings.Repeat("A", 64) + `"},"version":1}`,
		`{"images":{},"version":2}`,
		`{"images":{},"version":1,"version":1}`,
		`{"version":1,"images":{"provider.exe":"` + strings.Repeat("a", 64) + `"}}`,
	} {
		if got, err := parseShippedProviderImages(base64.StdEncoding.EncodeToString([]byte(raw))); err == nil {
			t.Fatalf("accepted malformed record %s: %v", raw, got)
		}
	}
}
