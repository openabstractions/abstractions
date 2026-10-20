package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// twinPrograms creates two program files whose names Unicode case folding
// treats as one and NTFS keeps apart: link.exe and lin<KELVIN SIGN>.exe, or
// xs.exe and x<LONG S>.exe.
func twinPrograms(t *testing.T) [][2]string {
	t.Helper()
	dir := t.TempDir()
	var out [][2]string
	for _, pair := range [][2]string{{"link", "linK"}, {"xs", "xſ"}} {
		real, twin := filepath.Join(dir, pair[0]+".exe"), filepath.Join(dir, pair[1]+".exe")
		for _, p := range []string{real, twin} {
			if err := os.WriteFile(p, []byte(p), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, [2]string{real, twin})
	}
	return out
}

// The content indexes name a writer by its program. A twin program, holding
// the same profile rights, reads neither a request input nor a generated
// output the real program wrote.
func TestContentIndexesRefuseATwinProgram(t *testing.T) {
	for _, pair := range twinPrograms(t) {
		content, rights, base := inputContent(t)
		writer := inference.Subject{Account: base.Account, Program: pair[0]}
		twin := inference.Subject{Account: base.Account, Program: pair[1]}

		digest := digestOf(editInputPNG)
		grantInput(t, rights, writer, inference.InputImage, contentWriteAction, contentReadAction)
		grantInput(t, rights, twin, inference.InputImage, contentWriteAction, contentReadAction)
		if outcome := content.write(context.Background(), writer, digest, editInputPNG); outcome != inference.ContentResolved {
			t.Fatalf("input write by %q: %v", writer.Program, outcome)
		}
		if _, outcome := content.read(imageRequest(), twin, digest, int64(len(editInputPNG))); outcome != inference.ContentForbidden {
			t.Fatalf("%q read the request input %q wrote: %v", twin.Program, writer.Program, outcome)
		}

		data := []byte("generated output of " + filepath.Base(pair[0]))
		output := digestOf(data)
		for _, s := range []inference.Subject{writer, twin} {
			for _, action := range []string{contentWriteAction, contentReadAction} {
				if err := rights.policy.Set(rwire.Subject{Account: s.Account, Program: s.Program}, action, "output:image", true); err != nil {
					t.Fatal(err)
				}
			}
		}
		commit, outcome := content.prepareWrite(context.Background(), writer, "image", "image/png", int64(len(data)))
		if outcome != inference.ContentResolved {
			t.Fatalf("output prepare by %q: %v", writer.Program, outcome)
		}
		if outcome := commit(context.Background(), output, data); outcome != inference.ContentResolved {
			t.Fatalf("output commit by %q: %v", writer.Program, outcome)
		}
		if _, outcome := content.read(context.Background(), writer, output, int64(len(data))); outcome != inference.ContentResolved {
			t.Fatalf("%q read its own output: %v", writer.Program, outcome)
		}
		if _, outcome := content.read(context.Background(), twin, output, int64(len(data))); outcome != inference.ContentForbidden {
			t.Fatalf("%q read the output %q generated: %v", twin.Program, writer.Program, outcome)
		}
	}
}
