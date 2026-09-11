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
func TestFrontendEveryElementLookupHasAnElement(t *testing.T) {
	markup, err := os.ReadFile(filepath.Join("frontend", "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	present := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?:^|[\s<])id="([^"]+)"`).FindAllStringSubmatch(string(markup), -1) {
		present[match[1]] = true
	}
	if len(present) == 0 {
		t.Fatal("index.html declares no ids; this test would pass vacuously")
	}

	// Double-quoted literals only. el() over a variable (main.js passes an array
	// of ids in one place) is not covered; those ids are exercised by the render
	// tests instead.
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

// TestFrontendRendersHostileCandidateMetadataAsText covers the pairing panel,
// whose input is the least trustworthy in the app.
//
// A peer's session metadata at least arrives authenticated: the signature is
// verified and the envelope must name this node. A pairing candidate arrives on
// a multicast group anyone on the segment can write to, unsigned, from a
// machine this owner has no relationship with. Every field is whatever the
// sender typed. The check also pins that the panel never presents a claim as a
// fact, and that clicking a row cannot pre-confirm a fingerprint nobody
// compared.
func TestFrontendRendersHostileCandidateMetadataAsText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the pairing panel render check")
	}
	script := filepath.Join("frontend", "test", "render-hostile-candidate.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("pairing panel render check failed: %v\n%s", err, output)
	}
}

// A warning with no style that applies to it is read as body text, and a
// container with no overflow rule clips whatever an attacker can make long.
//
// Both of those shipped: `.stale` was scoped to `.nodedetail`, so the pairing
// panel's "this is the absence of a fact, not a fact" notices rendered
// identically to its statements; `.pill.bad` did not exist at all, so the flag
// marking a contested candidate — the only place impersonation is visible from
// this side — looked like a neutral pill; and the sidebar had no scroll, so 64
// candidate rows (a state anyone on the segment can force) pushed the pairing
// button and the full-list warning out of reach.
//
// A static check, so it cannot prove a colour is legible. What it does prove is
// that a rule exists to be applied, which is what was missing.
func TestFrontendStylesTheThingsThatCarryAWarning(t *testing.T) {
	stylesheet, err := os.ReadFile(filepath.Join("frontend", "src", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(stylesheet)

	for what, selector := range map[string]string{
		// Unscoped, so the pairing panel's notices get it too. A leading `.` at
		// the start of a rule is what distinguishes it from `.nodedetail .stale`.
		"a notice explaining an absence": "\n.stale {",
		"a contested or duplicate flag":  "\n.pill.bad {",
		// The only thing between a hostile message body and someone acting on
		// it. Styled like body text, it is read as body text.
		"the data-not-instruction warning": "\n.modal-card .warning {",
		// The name this node broadcasts, set apart from the prose around it.
		// Scoped to the inbox, as it was, the pairing note's span rendered
		// identically to the sentence it sat in — the class was there and did
		// nothing, and a test asserting only the class was green.
		"a string somebody chose, inside prose": "\n.claimed {",
		"that string against the muted note":    "\n#pairing-note .claimed {",
		// A peer with no address is skipped in delivery without a word, while
		// the sender's `ah send` still answers `queued`. This is the only place
		// that failure is visible before it happens, so it must not be styled
		// like the muted notes it sits among.
		"a paired node with nowhere to deliver to": "\n.noaddress {",
		// The key the peer has to type into its own dialog. Rendered in the
		// body font it wraps mid-group and is retyped wrong, which is how a
		// trailing "=" was lost on 2026-09-10.
		"this node's own public key": "\n.keyvalue {",
	} {
		if !strings.Contains(css, selector) {
			t.Errorf("no rule for %s: style.css has no %q, so it renders like ordinary text",
				what, strings.TrimSpace(selector))
		}
	}

	// And the markup still asks for it. The stylesheet having a rule proves
	// nothing if the element that carries the warning stopped using the class —
	// that mutation was green.
	markup, err := os.ReadFile(filepath.Join("frontend", "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(markup), `<p class="warning">`) {
		t.Error("no element carries the warning class, so the prompt-injection warning " +
			"renders as body text whatever the stylesheet says")
	}

	// The scrolling containers, each with the property that makes it scroll.
	// Without these the sidebar clips, and what it clips is the button the
	// owner needs and the warning that explains what they are looking at.
	for _, required := range []struct{ selector, property string }{
		{".nodelist {", "overflow-y"},
		{"#candidate-rows {", "max-height"},
		// A message body is 32KB of whatever a sender chose. One of newlines
		// renders over a hundred thousand pixels tall, which is every message
		// after it made unreachable.
		{"#inbox-body {", "max-height"},
		{".inboxrow .inboxbody {", "max-height"},
	} {
		start := strings.Index(css, required.selector)
		if start < 0 {
			t.Errorf("style.css has no %q rule", required.selector)
			continue
		}
		block := css[start:]
		if end := strings.Index(block, "}"); end > 0 {
			block = block[:end]
		}
		if !strings.Contains(block, required.property) {
			t.Errorf("%s does not set %s, so a long candidate list clips instead of scrolling",
				required.selector, required.property)
		}
	}
	// #candidate-rows needs a scroll of its own as well, which it gets from a
	// grouped selector, so look for it anywhere.
	if !strings.Contains(css, "#candidate-rows") {
		t.Error("style.css never mentions #candidate-rows")
	}
}

// TestFrontendPairingPanelSurvivesTheSequences drives the whole module —
// wiring, polls and handlers — through the orderings a render-only check cannot
// reach.
//
// The other render checks slice the source at the wiring marker, so
// loadPairing, both intervals and the button handlers were executed by nothing.
// Three defects lived in exactly that gap: a stale poll overwriting a fresher
// one, an expired window counting 0:00 until the next read, and a countdown
// tick rebuilding the candidate rows — which replaces the row an owner is about
// to click.
func TestFrontendPairingPanelSurvivesTheSequences(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the pairing lifecycle check")
	}
	script := filepath.Join("frontend", "test", "pairing-lifecycle.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("pairing lifecycle check failed: %v\n%s", err, output)
	}
}

// TestFrontendRendersHostileInboxMessagesAsText covers the inbox, whose message
// bodies are the most attacker-controlled text the app shows.
//
// Candidate metadata at least describes a machine and is bounded by PRECIS. A
// message body is up to 32KB of whatever the sender chose, written to be read
// by a person, and the sender need only be a node this owner once paired with —
// a peer that has since been compromised sends signed hostile strings. The
// check also pins that the four states stay apart: a failed read, an empty
// inbox, a full one, and messages.
func TestFrontendRendersHostileInboxMessagesAsText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the inbox render check")
	}
	script := filepath.Join("frontend", "test", "render-hostile-inbox.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("inbox render check failed: %v\n%s", err, output)
	}
}

// TestFrontendRecordsDialogShowsWhereMessagesWentAndWhatTheyWoke covers the
// dialog added for the two failures this window could not see.
//
// A message queued for a peer with no address sits pending for ever while
// `ah send` answers "queued", and a wake refused by a rate limit leaves nothing
// behind but a row in a trail only the CLI could read — both were walked into
// on 2026-09-10. Everything the dialog shows is a string this machine did not
// write, so the check pins that none of it becomes markup or a class name, that
// a refusal does not read like a delivery, and that the two empty lists say
// different things.
func TestFrontendRecordsDialogShowsWhereMessagesWentAndWhatTheyWoke(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the records dialog check")
	}
	script := filepath.Join("frontend", "test", "records.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("records dialog check failed: %v\n%s", err, output)
	}
}

// TestFrontendAudienceDialogStartsEveryFlagOff drives the dialog itself.
//
// It applies to whatever is selected and reads its values straight from the
// boxes, so a box left ticked from the last time it was opened is a setting
// about to be applied to a different set of sessions. That was survivable
// while the flags governed only what could be read; one of them now starts a
// turn in an agent with nobody watching, and inheriting that from a previous
// dialog is not a thing anyone would choose on purpose.
func TestFrontendAudienceDialogStartsEveryFlagOff(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the audience dialog check")
	}
	script := filepath.Join("frontend", "test", "audience-dialog.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("audience dialog check failed: %v\n%s", err, output)
	}
}

// TestFrontendMainListRefreshesAndSurvivesAFailedRead covers the one state the
// window cannot recover from on its own: showing nothing.
//
// The session table and the node list used to load once at startup and then
// only when someone pressed refresh, and a read that could not reach the node
// copied its emptiness into state. A node that was mid-rescan when the window
// opened therefore left it claiming zero sessions and no paired node — observed
// on 2026-09-10 against a node serving 1083 sessions — with nothing to correct
// it until a person noticed and clicked.
func TestFrontendMainListRefreshesAndSurvivesAFailedRead(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the main list refresh check")
	}
	script := filepath.Join("frontend", "test", "list-refresh.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("main list refresh check failed: %v\n%s", err, output)
	}
}

// TestFrontendMCPConfigButtonCarriesItsOwnRowsSession drives the per-row button
// that hands over a `.mcp.json`.
//
// What the snippet binds is an agent to a session, and a wrong id in it is
// invisible afterwards: the server starts, the four tools answer, and they
// answer for somebody else's session. That is the mistake the button exists to
// prevent — a session id from another machine was pasted into a config by hand
// on 2026-09-10 — so the path from the row to the call is followed with more
// than one row on screen. The check also pins the two warnings the snippet
// cannot carry itself: an MCP config is per-project (issue #104), and reading
// is all it buys until the owner opens the session's outbound gate.
func TestFrontendMCPConfigButtonCarriesItsOwnRowsSession(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the MCP config check")
	}
	script := filepath.Join("frontend", "test", "mcp-config.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("MCP config check failed: %v\n%s", err, output)
	}
}

// TestFrontendPairingIsCompleteFromInsideTheWindow drives the three things a
// first real pairing needed and this window did not have (issues #63, #110).
//
// Each was found by pairing two machines by hand on 2026-09-10, and none of
// them was visible from inside the app. The public key the peer has to type was
// obtainable only by running `ah node` in a terminal — the node answers with it
// and main.js never read the field, while the README pointed at a "node line"
// that showed no such thing. Trust is recorded per machine, and nothing said so
// when one side was done and the other still answered `No paired nodes`. And a
// peer with no recorded address is skipped in delivery without a word while the
// sender's `ah send` still says `queued`, with no button and no subcommand to
// record one.
func TestFrontendPairingIsCompleteFromInsideTheWindow(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the pairing completeness check")
	}
	script := filepath.Join("frontend", "test", "pairing-completeness.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("pairing completeness check failed: %v\n%s", err, output)
	}
}
