package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Directory unlock must remain one owner even in readers: it also drains the
// pending Asks retirements captured by a previously committed directory edit.
func directoryRawUnlocks(sources map[string]string) ([]token.Position, error) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	fields := map[string]map[string]string{}
	returns := map[string]string{}
	var typeName func(ast.Expr) string
	typeName = func(e ast.Expr) string {
		switch v := e.(type) {
		case *ast.Ident:
			return v.Name
		case *ast.StarExpr:
			return typeName(v.X)
		}
		return ""
	}
	for name, source := range sources {
		f, err := parser.ParseFile(fset, name, source, 0)
		if err != nil {
			return nil, err
		}
		files[name] = f
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
				returns[fn.Name.Name] = typeName(fn.Type.Results.List[0].Type)
			}
			group, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range group.Specs {
				named, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				body, ok := named.Type.(*ast.StructType)
				if !ok {
					continue
				}
				members := map[string]string{}
				for _, field := range body.Fields.List {
					for _, n := range field.Names {
						members[n.Name] = typeName(field.Type)
					}
				}
				fields[named.Name.Name] = members
			}
		}
	}
	var bad []token.Position
	for name, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			types := map[*ast.Object]string{}
			bind := func(list *ast.FieldList) {
				if list != nil {
					for _, f := range list.List {
						for _, n := range f.Names {
							types[n.Obj] = typeName(f.Type)
						}
					}
				}
			}
			bind(fn.Recv)
			bind(fn.Type.Params)
			var infer func(ast.Expr) string
			infer = func(e ast.Expr) string {
				switch v := e.(type) {
				case *ast.Ident:
					return types[v.Obj]
				case *ast.SelectorExpr:
					return fields[infer(v.X)][v.Sel.Name]
				case *ast.UnaryExpr:
					return infer(v.X)
				case *ast.ParenExpr:
					return infer(v.X)
				case *ast.CompositeLit:
					return typeName(v.Type)
				case *ast.CallExpr:
					if id, ok := v.Fun.(*ast.Ident); ok {
						if id.Name == "new" && len(v.Args) == 1 {
							return typeName(v.Args[0])
						}
						return returns[id.Name]
					}
				}
				return ""
			}
			// Iterate aliases so declaration order inside closures cannot hide a raw
			// directory release. Objects preserve lexical shadowing of local names.
			for pass := 0; pass < 4; pass++ {
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					switch n := node.(type) {
					case *ast.FuncLit:
						bind(n.Type.Params)
					case *ast.ValueSpec:
						for i, id := range n.Names {
							if n.Type != nil {
								types[id.Obj] = typeName(n.Type)
							} else if i < len(n.Values) {
								types[id.Obj] = infer(n.Values[i])
							}
						}
					case *ast.AssignStmt:
						for i, left := range n.Lhs {
							if id, ok := left.(*ast.Ident); ok && i < len(n.Rhs) {
								if t := infer(n.Rhs[i]); t != "" {
									types[id.Obj] = t
								}
							}
						}
					}
					return true
				})
			}
			owner := filepath.Base(name) == "applications.go" && fn.Recv != nil && typeName(fn.Recv.List[0].Type) == "applicationDirectory" && (fn.Name.Name == "lock" || fn.Name.Name == "unlock")
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				method, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || method.Sel.Name != "Unlock" {
					return true
				}
				mu, ok := method.X.(*ast.SelectorExpr)
				if ok && mu.Sel.Name == "mu" && infer(mu.X) == "applicationDirectory" && !owner {
					bad = append(bad, fset.Position(call.Pos()))
				}
				return true
			})
		}
	}
	return bad, nil
}

func TestApplicationDirectoryUnlockHasOneSourceOwner(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source unavailable")
	}
	root := filepath.Dir(source)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sources[entry.Name()] = string(raw)
	}
	bad, err := directoryRawUnlocks(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("applicationDirectory raw mutex release bypasses d.unlock: %v", bad)
	}
}

func TestApplicationDirectoryUnlockGuardRejectsNewReadersAndAliases(t *testing.T) {
	fixtures := map[string]string{
		"reader":   "func read(d *applicationDirectory){d.mu.Unlock()}",
		"receiver": "func(d *applicationDirectory) read(){d.mu.Unlock()}",
		"alias":    "func read(d *applicationDirectory){alias:=d;alias.mu.Unlock()}",
		"field":    "type holder struct{directory *applicationDirectory};func read(h *holder){h.directory.mu.Unlock()}",
	}
	for name, body := range fixtures {
		t.Run(name, func(t *testing.T) {
			bad, err := directoryRawUnlocks(map[string]string{"reader.go": "package main\n" + body})
			if err != nil || len(bad) != 1 {
				t.Fatal(bad, err)
			}
		})
	}
	allowed := map[string]string{"applications.go": "package main;func(d *applicationDirectory) lock(){d.mu.Unlock()};func(d *applicationDirectory) unlock(){d.mu.Unlock()}", "other.go": "package main;type other struct{};func read(d *other){d.mu.Unlock()}"}
	if bad, err := directoryRawUnlocks(allowed); err != nil || len(bad) != 0 {
		t.Fatal(bad, err)
	}
}
