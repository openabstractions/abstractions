package main

import (
	"go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"strconv"
	"strings"
	"testing"
)

func TestProtocolOperationsEmitStringConstants(t *testing.T) {
	s := jobDefinition(t)
	// A new definition member must produce a constant without a backend edit.
	s.Proto.Operations = append(s.Proto.Operations, Operation{Name: "future_action"})
	source := genGo(s)
	f, err := goparser.ParseFile(gotoken.NewFileSet(), "generated.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	constants := map[string]string{}
	for _, declaration := range f.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != gotoken.CONST {
			continue
		}
		for _, item := range group.Specs {
			v := item.(*ast.ValueSpec)
			for i, name := range v.Names {
				if !strings.HasPrefix(name.Name, "Op") {
					continue
				}
				if v.Type != nil {
					t.Fatal("operation became a typed enum", name.Name)
				}
				literal, ok := v.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != gotoken.STRING {
					t.Fatal("operation ceased being a string", name.Name)
				}
				constants[name.Name], err = strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if len(constants) != len(s.Proto.Operations) {
		t.Fatal(constants)
	}
	for _, operation := range s.Proto.Operations {
		if constants["Op"+exported(operation.Name)] != operation.Name {
			t.Fatal("operation missing/wrong", operation.Name, constants)
		}
	}
}
