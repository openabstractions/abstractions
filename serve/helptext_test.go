package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestNoHelpTextContainsATab is item 4's own test: provider --help had a
// literal tab where every other continuation line uses spaces, misaligning
// the one line it fell on. Parse every source file and classify actual usage
// literals and named help/usage fragments. Structured package templates have
// their own byte-parity receiver.
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
		for _, lit := range helpLiterals(file) {
			for _, problem := range helpLiteralProblems(lit) {
				pos := fset.Position(lit.Pos())
				t.Errorf("%s:%d: %s", pos.Filename, pos.Line, problem)
			}
		}
	}
}

func helpLiterals(file *ast.File) []*ast.BasicLit {
	marked := map[*ast.BasicLit]bool{}
	mark := func(node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok {
				marked[lit] = true
			}
			return true
		})
	}
	helps := func(name string) bool {
		name = strings.ToLower(name)
		return strings.Contains(name, "usage") || strings.Contains(name, "help")
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ValueSpec:
			for _, name := range n.Names {
				if helps(name.Name) {
					mark(n)
					break
				}
			}
		case *ast.FuncDecl:
			if n.Body != nil && helps(n.Name.Name) {
				mark(n.Body)
			}
		}
		return true
	})
	var out []*ast.BasicLit
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !strings.HasPrefix(lit.Value, "`") {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err == nil && (marked[lit] || strings.HasPrefix(strings.TrimSpace(text), "Usage:")) {
			out = append(out, lit)
		}
		return true
	})
	return out
}

func helpLiteralProblems(lit *ast.BasicLit) []string {
	var out []string
	if strings.Contains(lit.Value, "\t") {
		out = append(out, "help text contains a tab character; align with spaces")
	}
	// An installed user has the published contract or website.
	if strings.Contains(lit.Value, "openabstractions-flat/") {
		out = append(out, "help text names a private source path; link the published contract or website")
	}
	return out
}

func TestHelpLiteralClassificationPreservesHelpFailuresAndTemplates(t *testing.T) {
	source := "package main\nconst commandUsage = `Usage: app\topenabstractions-flat/private`\nconst usageTail = `\tcontinuation`\nfunc commandUsageFor(){_ = `Usage: nested\tvalue`}\nconst unrelated = `Usage: unnamed\tvalue`\nconst runtimeLoginTemplate = `" + runtimeLoginTemplate + "`\nconst panelLoginTemplate = `" + panelLoginTemplate + "`\n"
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	literals := helpLiterals(file)
	if len(literals) != 4 {
		t.Fatalf("classified %d help literals", len(literals))
	}
	problems := 0
	for _, lit := range literals {
		problems += len(helpLiteralProblems(lit))
	}
	if problems != 5 {
		t.Fatalf("lost help tab/private-path failures: %d", problems)
	}
}
