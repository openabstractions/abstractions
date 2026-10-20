package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestLibrarySearchIsRestrictedFirst is FAC-H1: main restricts library
// search before it parses anything or runs any other statement.
func TestLibrarySearchIsRestrictedFirst(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" || fn.Recv != nil {
			continue
		}
		first, ok := fn.Body.List[0].(*ast.IfStmt)
		if !ok {
			t.Fatal("main's first statement is not the library-search check")
		}
		assign, ok := first.Init.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			t.Fatal("main's first statement does not call restrictLibrarySearch")
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if ident, isIdent := call.Fun.(*ast.Ident); !ok || !isIdent || ident.Name != "restrictLibrarySearch" {
			t.Fatal("main's first statement does not call restrictLibrarySearch")
		}
		return
	}
	t.Fatal("no main function")
}
