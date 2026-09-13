package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"strconv"
	"strings"
)

// goExportAliases derives compatibility exports from the generated Go AST.
// Methods follow aliased type identity; functions keep their real signatures.
func goExportAliases(source, target string) (string, error) {
	set := gotoken.NewFileSet()
	file, err := goparser.ParseFile(set, "generated.go", source, 0)
	if err != nil {
		return "", err
	}
	var declarations strings.Builder
	imports := map[string]string{}
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = path
	}
	used := map[string]bool{}
	render := func(node ast.Node) string {
		ast.Inspect(node, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && imports[id.Name] != "" {
					used[id.Name] = true
				}
			}
			return true
		})
		var b bytes.Buffer
		_ = format.Node(&b, set, node)
		return b.String()
	}
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if !spec.Name.IsExported() {
						continue
					}
					if spec.TypeParams != nil {
						return "", fmt.Errorf("Go compatibility aliases: generic type unsupported")
					}
					fmt.Fprintf(&declarations, "type %s = canonical.%s\n", spec.Name.Name, spec.Name.Name)
				case *ast.ValueSpec:
					for i, name := range spec.Names {
						if name.IsExported() && decl.Tok == gotoken.VAR {
							kind := spec.Type
							if kind == nil && i < len(spec.Values) {
								if literal, ok := spec.Values[i].(*ast.CompositeLit); ok {
									kind = literal.Type
								}
							}
							shared := false
							switch kind := kind.(type) {
							case *ast.ArrayType:
								shared = kind.Len == nil
							case *ast.MapType:
								shared = true
							}
							if !shared {
								return "", fmt.Errorf("Go compatibility aliases: mutable variable %s is not a slice/map", name.Name)
							}
						}
						if name.IsExported() {
							fmt.Fprintf(&declarations, "%s %s = canonical.%s\n", decl.Tok.String(), name.Name, name.Name)
						}
					}
				}
			}
		case *ast.FuncDecl:
			if decl.Recv != nil || !decl.Name.IsExported() {
				continue
			}
			if decl.Type.TypeParams != nil {
				return "", fmt.Errorf("Go compatibility aliases: generic function unsupported")
			}
			args := []string{}
			for _, field := range decl.Type.Params.List {
				if len(field.Names) == 0 {
					field.Names = []*ast.Ident{ast.NewIdent(fmt.Sprintf("arg%d", len(args)))}
				}
				for _, name := range field.Names {
					args = append(args, name.Name)
				}
				if _, ok := field.Type.(*ast.Ellipsis); ok {
					args[len(args)-1] += "..."
				}
			}
			signature := strings.TrimPrefix(render(decl.Type), "func")
			statement := ""
			if decl.Type.Results != nil && len(decl.Type.Results.List) > 0 {
				statement = "return "
			}
			fmt.Fprintf(&declarations, "func %s%s { %scanonical.%s(%s) }\n", decl.Name.Name, signature, statement, decl.Name.Name, strings.Join(args, ","))
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "package %s\nimport canonical %q\n", file.Name.Name, target)
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if used[name] {
			fmt.Fprintf(&out, "import %s %q\n", name, path)
		}
	}
	out.WriteString("// Exported data initially shares canonical values; variable reassignment is package-local.\n")
	out.WriteString(declarations.String())
	result, err := format.Source([]byte(out.String()))
	return string(result), err
}
