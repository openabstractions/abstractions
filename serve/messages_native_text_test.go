package main

import (
	"strings"
	"testing"
)

func TestMessagesPrivateTextBoundsAndRawProjection(t *testing.T) {
	for _, line := range []string{"a\nb", "a\rb", "a\u2028b", "a\u2029b"} {
		if nativeMessageText(line, "body", "key") {
			t.Fatal("multiline subject admitted", line)
		}
	}
	if !nativeMessageText(strings.Repeat("🦊", 80), "", "key") || nativeMessageText(strings.Repeat("🦊", 81), "", "key") {
		t.Fatal("scalar/UTF-8 subject boundary")
	}
	if !nativeMessageText(strings.Repeat("é", 80), strings.Repeat("é", 4096), "key") || nativeMessageText(strings.Repeat("é", 81), "", "key") || nativeMessageText("", strings.Repeat("é", 4096)+"x", "key") || nativeMessageText("", "", "") || nativeMessageText("", "", strings.Repeat("é", 65)) || nativeMessageText("\xff", "", "key") {
		t.Fatal("raw UTF-8 bounds")
	}
	if nativeMessageStatus(strings.Repeat("\n", 201)) || !nativeMessageStatus(strings.Repeat("é", 200)) {
		t.Fatal("raw status before sanitizing")
	}
	raw := "C\nlaude\u202e\u2066 Code\u2028\x00"
	if nativeMessageHeader(raw) != "Claude Code" || raw == nativeMessageHeader(raw) {
		t.Fatal("header projection")
	}
	if nativeMessageHeader("é") == nativeMessageHeader("e\u0301") {
		t.Fatal("normalized raw spelling")
	}
}
