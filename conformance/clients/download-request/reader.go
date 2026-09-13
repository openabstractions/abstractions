package main

import (
	"bytes"
	"fmt"
	api "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 2 {
		panic("output directory required")
	}
	for _, name := range []string{"http.json", "empty.json", "strings.json"} {
		raw, err := os.ReadFile(filepath.Join(os.Args[1], name))
		if err != nil {
			panic(err)
		}
		v, err := api.Decode(raw)
		if err != nil {
			panic(err)
		}
		switch name {
		case "http.json":
			if v.Artifact.Digest != "sha256:0123456789abcdef" || v.Artifact.Size != 123456789 || len(v.Sources) != 1 || v.Sources[0].Scheme != "https" || v.Sources[0].Locator != "https://example.invalid/archive?x=1&y=2" {
				panic("HTTP fields changed")
			}
		case "empty.json":
			if v.Artifact.Digest != "" || v.Artifact.Size != 0 || len(v.Sources) != 0 {
				panic("empty defaults changed")
			}
		case "strings.json":
			if v.Artifact.Size != 9223372036854775807 || len(v.Sources) != 2 || v.Sources[0].Scheme != "codec" || v.Sources[0].Locator != "quote\" slash\\ line\n nul\x00🌍" || v.Sources[1].Locator != "https://example.invalid/second" {
				panic(fmt.Sprintf("string/integer fields changed: %#v", v))
			}
		}
		if !bytes.Equal(api.Encode(v), raw) {
			panic("canonical Go/C++ encoding differs")
		}
	}
	for _, raw := range []string{`{"artifact":{},"sources":[],"provider_path":"/tmp/escape"}`, `{"artifact":{}}`, `{"artifact":{},"sources":[],"sources":[]}`, `{"artifact":{"size":9223372036854775808},"sources":[]}`} {
		if _, err := api.Decode([]byte(raw)); err == nil {
			panic("invalid request accepted: " + raw)
		}
	}
	fmt.Println("PASS: three installed C++ payloads decoded with exact fields and canonical bytes; four invalid payloads refused")
}
