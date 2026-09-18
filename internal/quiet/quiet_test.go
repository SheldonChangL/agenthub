package quiet

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every short-lived command this module runs must be built here, or the next
// tasklist/schtasks/ps call added in a hurry brings the console flash back on
// Windows. Spawning the node itself is the one exception: those files detach
// a long-lived process and set their own creation flags.
func TestEveryShortLivedCommandIsBuiltQuietly(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := map[string]bool{
		filepath.Join("internal", "quiet", "quiet.go"):           true,
		filepath.Join("internal", "service", "spawn_windows.go"): true,
		filepath.Join("internal", "service", "spawn_other.go"):   true,
		filepath.Join("internal", "codexapp", "supervisor.go"):   true, // long-lived app-server child, its own lifecycle
	}
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "desktop" || name == "frontend" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(src, []byte("exec.Command")) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if allowed[rel] {
			return nil
		}
		// Parse rather than grep so a mention in a comment does not count.
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "os/exec" {
				offenders = append(offenders, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("these files build commands with os/exec directly; use quiet.Command so no console window opens on Windows:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
