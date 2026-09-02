package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// TestFrontendRendersHostilePeerMetadataAsText covers the network view, whose
// input is strictly less trustworthy than the local table's.
//
// Peer session metadata arrives from another machine. It is authenticated — the
// signature is verified and the envelope must name this node — but
// authenticated is not benign: a paired peer that has itself been compromised,
// or whose provider files were tampered with, sends signed hostile strings. The
// same rule therefore applies, and the check also pins that an offline peer's
// last snapshot is not rendered as the current one.
func TestFrontendRendersHostilePeerMetadataAsText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the network view render check")
	}
	script := filepath.Join("frontend", "test", "render-hostile-peer.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("network view render check failed: %v\n%s", err, output)
	}
}

// Every element the frontend looks up by a literal id must exist in the page.
//
// el() is document.getElementById, which returns null for an id that is not
// there. Most of these lookups run at module top level, where assigning to a
// property of null throws and abandons the rest of the module - including the
// load() call on the last line. The window then renders its static placeholder
// text forever and never asks the node for anything, which looks like the node
// being unreachable rather than like a broken build.
//
// This is not hypothetical: index.html was missing btn-audience while main.js
// wired a click handler to it, so the desktop app silently displayed no
// sessions at all while every test here passed.
func TestEveryElementLookupHasAnElement(t *testing.T) {
	markup, err := os.ReadFile(filepath.Join("frontend", "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	present := map[string]bool{}
	for _, match := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(string(markup), -1) {
		present[match[1]] = true
	}
	if len(present) == 0 {
		t.Fatal("index.html declares no ids; this test would pass vacuously")
	}

	lookup := regexp.MustCompile(`\bel\(\s*"([^"]+)"\s*\)`)
	found := 0
	for path, source := range frontendSources(t) {
		for _, match := range lookup.FindAllStringSubmatch(source, -1) {
			found++
			if !present[match[1]] {
				t.Errorf("%s looks up el(%q), which index.html does not define; "+
					"at module scope this throws and stops the frontend from ever loading", path, match[1])
			}
		}
	}
	if found == 0 {
		t.Fatal("no el(\"...\") lookups found; this test would pass vacuously")
	}
}
