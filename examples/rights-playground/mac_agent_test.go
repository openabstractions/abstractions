package main

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestMacAgentRegistersOnlyOwnNativeNamespace(t *testing.T) {
	name := "rights-playground-unit"
	raw, err := macAgent(name, "/private/demo & pack/openabstractions", "/private/demo & pack/Panel.app/Contents/MacOS/panel", "/private/demo/run/state", "/private/demo/run/out", "/private/demo/run/err")
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, service := range macServices {
		key := "<key>com.openabstractions.isolated." + name + "." + service + "</key><true/>"
		if strings.Count(string(raw), key) != 1 {
			t.Fatal("temporary agent lost an isolated native service", service)
		}
	}
	if strings.Contains(string(raw), "<key>com.openabstractions.runtime-v1</key>") || !bytes.Contains(raw, []byte("demo &amp; pack")) {
		t.Fatal("agent must preserve installed names and escape local paths")
	}
	if _, err := macAgent("installed", "", "", "", "", ""); err == nil {
		t.Fatal("agent accepted a non-playground namespace")
	}
}
