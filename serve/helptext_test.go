package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestNoHelpTextContainsATab is item 4's own test: provider --help had a
// literal tab where every other continuation line uses spaces, misaligning
// the one line it fell on. Every raw ("backtick") string literal in this
// package's own (non-test) source is a usage or help text, so this parses
// every serve/*.go file and refuses a tab in any of them, rather than
// checking one constant by name and missing the next one like it.
func TestNoHelpTextContainsATab(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || !strings.HasPrefix(lit.Value, "`") {
				return true
			}
			if strings.Contains(lit.Value, "\t") {
				pos := fset.Position(lit.Pos())
				t.Errorf("%s:%d: raw string literal contains a tab character; align with spaces", pos.Filename, pos.Line)
			}
			return true
		})
	}
}
