package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestStdioLinesValidateAndNormalizeWholeLine(t *testing.T) {
	const ping = `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	for _, test := range []struct {
		name, line, want string
	}{
		{"space", ping + " ", ping + "\n"},
		{"tab", ping + "\t", ping + "\n"},
		{"two values", ping + " " + ping, ""},
		{"garbage", ping + "garbage", ""},
		{"batch", "[" + ping + "," + ping + "]", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := copyJSONRPCLines(strings.NewReader(test.line+"\n"+ping+"\n"), &out, nil); err != nil {
				t.Fatal(err)
			}
			if got, want := out.String(), test.want+ping+"\n"; got != want {
				t.Fatalf("forwarded %q, want %q", got, want)
			}
		})
	}
}
