package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type pkg struct {
	ImportPath string
	Dir        string
	Export     string
	GoFiles    []string
	Standard   bool
}

func list(dir string) []pkg {
	cmd := exec.Command("go", "list", "-e", "-export", "-deps",
		"-json=ImportPath,Dir,Export,GoFiles,Standard", "./...")
	cmd.Dir = dir
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if len(out) == 0 {
		fmt.Fprintf(os.Stderr, "unchecked: go list in %s: %v\n%s", dir, err, errb.String())
		return nil
	}
	var ps []pkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return ps
		}
		ps = append(ps, p)
	}
}

var errFace = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

func isErr(t types.Type) bool {
	if t == nil || t == types.Typ[types.Invalid] {
		return false
	}
	return types.Implements(t, errFace)
}

func carriesErr(t types.Type) bool {
	tup, ok := t.(*types.Tuple)
	if !ok {
		return isErr(t)
	}
	for i := range tup.Len() {
		if isErr(tup.At(i).Type()) {
			return true
		}
	}
	return false
}

func shortPkg(p *types.Package) string { return p.Name() }

func callee(info *types.Info, call *ast.CallExpr) string {
	fun := ast.Unparen(call.Fun)
	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun = ast.Unparen(f.X)
	case *ast.IndexListExpr:
		fun = ast.Unparen(f.X)
	}
	var id *ast.Ident
	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	}
	fn, _ := info.Uses[id].(*types.Func)
	if fn == nil {
		return types.ExprString(fun)
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return types.TypeString(sig.Recv().Type(), shortPkg) + "." + fn.Name()
	}
	if fn.Pkg() != nil {
		return fn.Pkg().Name() + "." + fn.Name()
	}
	return fn.Name()
}

func isBlank(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "_"
}

func call(e ast.Expr) *ast.CallExpr {
	c, _ := ast.Unparen(e).(*ast.CallExpr)
	return c
}

func findings(fset *token.FileSet, imp types.Importer, cwd string, p pkg, out map[string]bool) {
	var files []*ast.File
	for _, name := range p.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, 0)
		if err == nil {
			files = append(files, f)
		}
	}
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	conf := types.Config{Importer: imp, Error: func(error) {}}
	conf.Check(p.ImportPath, fset, files, info)

	record := func(c *ast.CallExpr) {
		pos := fset.Position(c.Lparen)
		where, err := filepath.Rel(cwd, pos.Filename)
		if err != nil {
			where = pos.Filename
		}
		out[fmt.Sprintf("%s\t%s:%d", callee(info, c), filepath.ToSlash(where), pos.Line)] = true
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.ExprStmt:
				if c := call(s.X); c != nil && carriesErr(info.TypeOf(c)) {
					record(c)
				}
			case *ast.AssignStmt:
				if len(s.Rhs) == 1 && len(s.Lhs) > 1 {
					c := call(s.Rhs[0])
					tup, ok := info.TypeOf(s.Rhs[0]).(*types.Tuple)
					if c == nil || !ok {
						return true
					}
					for i, l := range s.Lhs {
						if isBlank(l) && i < tup.Len() && isErr(tup.At(i).Type()) {
							record(c)
							return true
						}
					}
					return true
				}
				for i, l := range s.Lhs {
					if !isBlank(l) || i >= len(s.Rhs) {
						continue
					}
					if c := call(s.Rhs[i]); c != nil && carriesErr(info.TypeOf(c)) {
						record(c)
					}
				}
			}
			return true
		})
	}
}

func within(dir, root string) bool {
	r, err := filepath.Rel(root, dir)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: unchecked <module dir>...")
		os.Exit(2)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "unchecked:", err)
		os.Exit(2)
	}
	fset := token.NewFileSet()
	export := map[string]string{}
	seen := map[string]bool{}
	var own []pkg
	for _, d := range os.Args[1:] {
		root, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		for _, p := range list(root) {
			if p.Export != "" {
				export[p.ImportPath] = p.Export
			}
			if p.Standard || seen[p.ImportPath] || len(p.GoFiles) == 0 || !within(p.Dir, root) {
				continue
			}
			seen[p.ImportPath] = true
			own = append(own, p)
		}
	}
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		f, ok := export[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(f)
	})
	out := map[string]bool{}
	for _, p := range own {
		findings(fset, imp, cwd, p, out)
	}
	lines := make([]string, 0, len(out))
	for l := range out {
		lines = append(lines, l)
	}
	sort.Strings(lines)
	fmt.Fprintf(os.Stderr, "%d packages\n", len(own))
	for _, l := range lines {
		fmt.Println(l)
	}
	if len(own) == 0 {
		os.Exit(1)
	}
}
