package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Provider metadata is untrusted input (docs/architecture.md). The desktop
// frontend renders it into a WebView that holds live bindings to this Go
// process, so a session ID or working directory containing markup must never
// reach the DOM as HTML. These sinks are how that would happen.
var unsafeSinks = []string{
	"innerHTML",
	"outerHTML",
	"insertAdjacentHTML",
	"document.write",
}

func frontendSources(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("frontend", "src")
	sources := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sources[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(sources) == 0 {
		t.Fatalf("no frontend sources found under %s", root)
	}
	return sources
}

func TestFrontendNeverAssignsUntrustedMarkup(t *testing.T) {
	for path, source := range frontendSources(t) {
		for _, sink := range unsafeSinks {
			if strings.Contains(source, sink) {
				t.Errorf("%s uses %q; provider metadata must reach the DOM as text, "+
					"not markup (see docs/architecture.md and issue #19)", path, sink)
			}
		}
	}
}

// The status value decides a CSS class. Only a fixed set may do so, otherwise a
// provider-supplied status would inject a class name.
func TestFrontendConstrainsStatusDerivedClasses(t *testing.T) {
	sources := frontendSources(t)
	main, ok := sources[filepath.Join("frontend", "src", "main.js")]
	if !ok {
		t.Fatal("frontend/src/main.js not found")
	}
	if !strings.Contains(main, "function statusPillClass") {
		t.Error("statusPillClass is missing; status must not be interpolated into a class name directly")
	}
	if !strings.Contains(main, `status === "active" || status === "idle"`) {
		t.Error("statusPillClass no longer restricts itself to the known status values")
	}
}

// TestFrontendRendersUntrustedMetadataAsText runs the renderer against hostile
// provider metadata in a minimal DOM. The static check above proves the unsafe
// sinks are absent; this proves the rendered result is actually inert.
func TestFrontendRendersUntrustedMetadataAsText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the frontend render check")
	}
	script := filepath.Join("frontend", "test", "render-untrusted.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("render check failed: %v\n%s", err, output)
	}
}
