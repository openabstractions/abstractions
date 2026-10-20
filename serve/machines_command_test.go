package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMachinesCommandPrivateInputAndExplicitScopes(t *testing.T) {
	var out, diag bytes.Buffer
	if err := machinesCommand([]string{"--help"}, strings.NewReader(""), &out, &diag); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"stdin", "--request", "--offer", "until revoked"} {
		if !strings.Contains(out.String(), word) {
			t.Fatalf("help missing %q", word)
		}
	}
	const dummy = "PAIR_INPUT_DUMMY_INVALID_7f490c"
	out.Reset()
	diag.Reset()
	err := machinesCommand([]string{"enter", "--request", "none", "--offer", "none", "--binding-name", "peer"}, strings.NewReader(dummy+"\n"), &out, &diag)
	if err == nil {
		t.Fatal("invalid invitation reached a runtime")
	}
	if strings.Contains(out.String()+diag.String()+err.Error(), dummy) {
		t.Fatal("private stdin value escaped into output or error")
	}
	out.Reset()
	diag.Reset()
	if err = machinesCommand([]string{"offer", "--binding-name", "peer"}, strings.NewReader(""), &out, &diag); err == nil {
		t.Fatal("missing explicit directions selected a granting default")
	}
}
