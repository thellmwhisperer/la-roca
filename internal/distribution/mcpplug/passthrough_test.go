package mcpplug_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Behaviour that lives in an MCP handler is behaviour no shell, hook or script
// can reach. This contract is pinned by reading the handlers
// themselves, because a comment saying "no logic here" has never stopped
// anybody.
//
// The rule has two halves and both are structural:
//
//  1. Every handler's body is exactly one return statement.
//  2. That statement makes exactly one call into the service.
//
// A handler that needs an `if` needs it in the service, where the other surface
// can reach it too.
const handlersFile = "handlers.go"

func TestEveryHandlerIsOneCallIntoTheService(t *testing.T) {
	file, fset := parseHandlers(t)

	handlers := 0
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		handlers++
		t.Run(function.Name.Name, func(t *testing.T) {
			if len(function.Body.List) != 1 {
				t.Fatalf("the body has %d statements: a handler is one return "+
					"into the service, and whatever else it needs belongs in the service",
					len(function.Body.List))
			}
			if _, ok := function.Body.List[0].(*ast.ReturnStmt); !ok {
				t.Fatalf("the body is not a return at %s",
					fset.Position(function.Body.List[0].Pos()))
			}
			if calls := serviceCalls(function.Body); calls != 1 {
				t.Errorf("%d calls into the service, want exactly 1", calls)
			}
		})
	}
	if handlers < 5 {
		t.Errorf("%d handlers read, want the five tools of the decided surface", handlers)
	}
}

// The other half of the same law: a handler cannot decide anything. Control
// flow in this file is a capability the shell cannot reach.
func TestNoHandlerDecidesAnything(t *testing.T) {
	file, fset := parseHandlers(t)

	ast.Inspect(file, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.AssignStmt, *ast.DeclStmt:
			t.Errorf("%s: a handler that decides is behaviour the shell cannot reach; "+
				"move it into the service", fset.Position(node.Pos()))
		}
		return true
	})
}

// serviceCalls counts the calls whose receiver is the service the plug holds.
func serviceCalls(body *ast.BlockStmt) int {
	calls := 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if inner, ok := selector.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "svc" {
			calls++
		}
		return true
	})
	return calls
}

func parseHandlers(t *testing.T) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, handlersFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("read %s: %v", handlersFile, err)
	}
	if !strings.HasSuffix(fset.Position(file.Pos()).Filename, handlersFile) {
		t.Fatalf("I did not read %s", handlersFile)
	}
	return file, fset
}
