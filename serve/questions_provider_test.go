package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestPendingQuestionsShowsBothProviderTargetsAndWithholdsCLIAllow(t *testing.T) {
	p, ok := providerPermission(rwire.Subject{Account: "account", Program: appProgram("requester")}, "deepseek")
	if !ok {
		t.Fatal("permission")
	}
	record := askswire.RecordMetadata{ID: "fixture", Key: asks.FirstUseKey, Text: "provider request", Options: []string{"allow", "never", "refuse"}}
	permissions := map[string]asks.PermissionRequest{record.ID: p}
	var text bytes.Buffer
	if err := printPendingQuestions(&text, []askswire.RecordMetadata{record}, nil, false, permissions); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"credential:deepseek", "host:deepseek", "native permission prompt"} {
		if !strings.Contains(text.String(), part) {
			t.Fatal(text.String(), part)
		}
	}
	if strings.Contains(text.String(), "allow|never") {
		t.Fatal("CLI offered setup approval", text.String())
	}
	if err := answerableQuestion([]askswire.RecordMetadata{record}, record.ID, "allow"); err == nil {
		t.Fatal("CLI accepted native provider Allow")
	}
	var raw bytes.Buffer
	if err := printPendingQuestions(&raw, []askswire.RecordMetadata{record}, nil, true, permissions); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Permissions map[string]asks.PermissionRequest `json:"permissions"`
	}
	if err := json.Unmarshal(raw.Bytes(), &got); err != nil || len(got.Permissions[record.ID].Targets) != 2 {
		t.Fatal(raw.String(), err)
	}
}
