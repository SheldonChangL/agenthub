package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// This window runs ah.exe on every panel refresh. On Windows a console child
// of a console-less parent gets a window of its own, so every exec.Command
// here must be wrapped in quietly(...). The two nodeprocess spawn files are
// the exception: they start the node detached and set their own flags.
func TestEveryCommandIsBuiltQuietly(t *testing.T) {
	allowed := map[string]bool{
		"nodeprocess_windows.go": true,
		"nodeprocess_unix.go":    true,
	}
	var offenders []string
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		// Every node goes on the stack, so when an exec.Command* call is
		// reached its direct parent is the top of the stack; quietly(...) is
		// that parent exactly when the call is its argument.
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			if call, ok := n.(*ast.CallExpr); ok && isExecCommand(call) {
				parent, _ := stack[len(stack)-1].(*ast.CallExpr)
				if parent == nil || !isIdentCall(parent, "quietly") {
					offenders = append(offenders, name+":"+fset.Position(call.Pos()).String()[len(name)+1:])
				}
			}
			stack = append(stack, n)
			return true
		})
	}
	if len(offenders) > 0 {
		t.Fatalf("these exec.Command calls are not wrapped in quietly(...), so they would open a console window on Windows:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

func isExecCommand(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "exec" && strings.HasPrefix(sel.Sel.Name, "Command")
}

func isIdentCall(call *ast.CallExpr, name string) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == name
}
