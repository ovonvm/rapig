package httpx

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestAPIHandlersCallAuthorizer is the AST guard behind hard rule 2: every API
// handler that touches a resource must resolve the caller (requireUser) and ask
// the Authorizer. The exempt list is intentionally tiny — login/logout/health
// operate on the caller themselves, /auth/me only reads the caller's own
// session, and /auth/deletion* manage the caller's own account (the request
// path re-authenticates with the password instead of checking a permission).
// Adding an authenticated handler without an authz check fails here.
func TestAPIHandlersCallAuthorizer(t *testing.T) {
	exempt := map[string]bool{
		"handleHealth":             true,
		"handleLogin":              true,
		"handleLogout":             true,
		"handleMe":                 true,
		"handleDeletionStatus":     true,
		"handleRegistrationStatus": true,
		"handleRegister":           true,
		"handleStoragePolicies":    true,
		"handleDeletionRequest":    true,
		"handleDeletionCancel":     true,
	}

	fset := token.NewFileSet()
	serverFile, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	routes := map[string]string{} // handler name -> pattern
	ast.Inspect(serverFile, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		handler, ok := call.Args[1].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		routes[handler.Sel.Name] = strings.Trim(lit.Value, `"`)
		return true
	})
	if len(routes) == 0 {
		t.Fatal("no routes found in server.go; guard has gone stale")
	}

	// Handlers are spread over several files (handlers.go, upload_handlers.go,
	// auth_handlers.go at M1); parse the whole package so a new file cannot
	// silently bypass the guard.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list package files: %v", err)
	}
	bodies := map[string]*ast.FuncDecl{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				bodies[fn.Name.Name] = fn
			}
		}
	}

	checked := 0
	publicChecked := 0
	for handler, pattern := range routes {
		// Patterns look like "GET /api/v1/nodes" or "/".
		parts := strings.Fields(pattern)
		path := parts[len(parts)-1]
		if !strings.HasPrefix(path, "/api/") {
			continue
		}
		if exempt[handler] {
			if _, ok := bodies[handler]; !ok {
				t.Errorf("exempt handler %s is missing from handlers.go", handler)
			}
			continue
		}
		if strings.HasPrefix(path, "/api/v1/public/") {
			// Public share endpoints are anonymous by design: they resolve
			// access through the share token, not through a session principal.
			if _, ok := bodies[handler]; !ok {
				t.Errorf("public handler %s is not defined", handler)
			}
			publicChecked++
			continue
		}
		fn, ok := bodies[handler]
		if !ok {
			t.Errorf("API handler %s is not defined in handlers.go", handler)
			continue
		}
		checked++
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, fn.Body); err != nil {
			t.Fatalf("print %s: %v", handler, err)
		}
		body := buf.String()
		if !strings.Contains(body, "requireUser") {
			t.Errorf("%s (%s) does not call requireUser", handler, pattern)
		}
		if !strings.Contains(body, "s.authz.Require") {
			t.Errorf("%s (%s) does not call s.authz.Require", handler, pattern)
		}
	}
	if checked == 0 {
		t.Fatal("guard checked no API handlers; the check has gone stale")
	}
	if publicChecked == 0 {
		t.Fatal("guard checked no public share handlers; the check has gone stale")
	}
}
