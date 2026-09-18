package quiet

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every command this module runs must be built here, or the next tasklist,
// schtasks or ps call added in a hurry brings the console flash back on
// Windows. The two spawn files are the exception: they start the node itself,
// detached, and set their own creation flags.
func TestEveryCommandIsBuiltQuietly(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := map[string]bool{
		filepath.Join("internal", "quiet", "quiet.go"):           true,
		filepath.Join("internal", "service", "spawn_windows.go"): true,
		filepath.Join("internal", "service", "spawn_other.go"):   true,
	}
	offenders := rawExecCalls(t, root, allowed, func(name string) bool {
		return name == ".git" || name == "node_modules" || name == "desktop" || name == "frontend"
	})
	if len(offenders) > 0 {
		t.Fatalf("these call os/exec directly; build the command with quiet.Command so no console window opens on Windows:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// rawExecCalls returns "file:line" for every exec.Command / exec.CommandContext
// call in a non-test Go file under root, minus the allowed files. It parses
// rather than greps, so a mention in a comment or a bare *exec.Cmd type does
// not count.
func rawExecCalls(t *testing.T, root string, allowed map[string]bool, skipDir func(string) bool) []string {
	t.Helper()
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if allowed[rel] {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "exec" && strings.HasPrefix(sel.Sel.Name, "Command") {
				offenders = append(offenders, rel+":"+fset.Position(call.Pos()).String()[len(path)+1:])
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return offenders
}
