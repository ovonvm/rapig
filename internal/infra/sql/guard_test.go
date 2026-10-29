package sqlstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestScopedRepositoriesValidateFirst is the AST guard behind hard rule 1 in
// the access package: every repository method that accepts an access.Scope must
// start with `if err := sc.Validate(); err != nil { return ... }`. Without it a
// new query could silently drop the owner_id filter.
func TestScopedRepositoriesValidateFirst(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !hasScopeParam(fn.Type) {
				continue
			}
			checked++
			if len(fn.Body.List) == 0 || !isScopeValidateStatement(fn.Body.List[0]) {
				t.Errorf("%s: %s takes an access.Scope but does not start with sc.Validate()",
					name, fn.Name.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("guard found no scoped repository methods; the check has gone stale")
	}
}

func hasScopeParam(ft *ast.FuncType) bool {
	if ft.Params == nil {
		return false
	}
	for _, field := range ft.Params.List {
		sel, ok := field.Type.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if sel.Sel.Name == "Scope" {
			return true
		}
	}
	return false
}

func isScopeValidateStatement(stmt ast.Stmt) bool {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init == nil {
		return false
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) != 1 {
		return false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "Validate"
}
