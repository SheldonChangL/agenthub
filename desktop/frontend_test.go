package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	// Any file under src/: the renderer moved from main.js into app.js when the
	// frontend became importable, and may move again when it is split further.
	// Both checks against the SAME file: the function and the allow-list it
	// carries have to live together, or a comment elsewhere could satisfy one.
	found := false
	for path, source := range sources {
		if !strings.Contains(source, "function statusPillClass") {
			continue
		}
		found = true
		if !strings.Contains(source, `status === "active" || status === "idle"`) {
			t.Errorf("%s: statusPillClass no longer restricts itself to the known status values", path)
		}
	}
	if !found {
		t.Error("statusPillClass is missing; status must not be interpolated into a class name directly")
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

// TestFrontendMCPConfigNamesItsOwnSession drives openMCPConfig and the dialog.
//
// The per-row button it used to be driven through is gone: `ah` covers all four
// MCP tools, the agenthub-watch skill uses `ah`, and a control an owner needs
// once does not belong on every row. The check stayed, because what the snippet
// binds is an agent to a session, and a wrong id in it is invisible afterwards:
// the server starts, the four tools answer, and they answer for somebody else's
// session. A session id from another machine was pasted into a config by hand
// on 2026-09-10, which is what this is still guarding. It also pins that the
// row is down to two actions, so putting the button back is deliberate, and the
// two warnings the snippet cannot carry itself: an MCP config is per-project
// (issue #104), and reading is all it buys until the owner opens the session's
// outbound gate.
func TestFrontendMCPConfigNamesItsOwnSession(t *testing.T) {
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

// TestFrontendRunsEverySessionsFilterCheck covers the filter model behind the
// session table: three groups that OR within and AND across, and counts that
// answer "how many of what you are already looking at".
//
// It has no DOM at all — it imports the module — so it is the cheapest of these
// and the one most likely to be forgotten.
func TestFrontendRunsEverySessionsFilterCheck(t *testing.T) {
	runNodeCheck(t, "sessions-filter.mjs")
}

// TestFrontendShowsSessionTitles covers the SESSION column: a named
// conversation reads as its name, an unnamed one still shows its ID, and the
// ID stays reachable in the tooltip because resuming a session needs it.
func TestFrontendShowsSessionTitles(t *testing.T) {
	runNodeCheck(t, "session-title.mjs")
}

// TestFrontendInboxDrawerAsksTheNodeForOneSession covers the send and wake logs
// beside the inbox. The node filters `/v1/outbound` to one session, and the
// session has to be repeated on every continuation or the second page is the
// node-wide list appended under one session's name.
func TestFrontendInboxDrawerAsksTheNodeForOneSession(t *testing.T) {
	runNodeCheck(t, "inbox-drawer.mjs")
}

// TestFrontendNodeSettingsFormSpeaksTheNodesRules covers the settings page.
//
// Three of its rules cost real damage when they are wrong: the node's answer
// repaints the whole form (a write can change a field nobody sent), an absent
// withdrawal flag means no withdrawal, and a refusal is shown in the node's own
// words. The form also never makes the owner's choice for them — an earlier
// version could not untick allowLan, which put the node's own headline
// behaviour out of reach.
func TestFrontendNodeSettingsFormSpeaksTheNodesRules(t *testing.T) {
	runNodeCheck(t, "node-settings.mjs")
}

// TestFrontendShimSelectDoesNotLie covers the fake <select> the other checks
// run against.
//
// A shim that reports a selected option where a browser reports none lets a
// form bug pass: two settings-form defects that turned LAN access on did
// exactly that before its selectedIndex was corrected. The fake's own
// behaviour is therefore pinned, not left to whichever check leans on it.
func TestFrontendShimSelectDoesNotLie(t *testing.T) {
	runNodeCheck(t, "dom-shim-select.mjs")
}

// runNodeCheck runs one check under frontend/test.
func runNodeCheck(t *testing.T, name string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not installed; skipping %s", name)
	}
	script := filepath.Join("frontend", "test", name)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	output, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, output)
	}
}

// TestFrontendEveryNodeCheckIsRunByGo is the reason the list above cannot drift.
//
// CI runs the frontend checks only through these wrappers — there is no `npm
// test` step in the workflow — so a check added under frontend/test and not
// named here is a check that never runs anywhere but a developer's machine.
// That already happened: two checks merged with the GUI redesign and were
// invisible to CI until this test was written.
func TestFrontendEveryNodeCheckIsRunByGo(t *testing.T) {
	source, err := os.ReadFile("frontend_test.go")
	if err != nil {
		t.Fatalf("read frontend_test.go: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join("frontend", "test"))
	if err != nil {
		t.Fatalf("read frontend/test: %v", err)
	}
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".mjs" {
			continue
		}
		// dom-shim.mjs is the fake DOM the checks import, not a check itself.
		// Its behaviour is covered by dom-shim-select.mjs.
		if name == "dom-shim.mjs" {
			continue
		}
		found++
		if !strings.Contains(string(source), strconv.Quote(name)) {
			t.Errorf("frontend/test/%s is never run by go test, so CI never runs it; "+
				"add a wrapper that calls runNodeCheck(t, %q)", name, name)
		}
	}
	if found == 0 {
		t.Fatal("no frontend checks found; this test would pass vacuously")
	}
}

// TestFrontendKeepsTheRowActionsReachable pins the two mechanisms that make the
// last column usable, both of which were missing when the app was first run on
// a real desktop (issue #153).
//
// The column was 214px wide for 241px of buttons, so the third one — copy the
// resume command — was clipped by the cell's own overflow at EVERY window
// width, not only narrow ones; and the table could not scroll horizontally,
// because `width: 100%` with no `min-width` means it is always exactly as wide
// as its container however much it has to fit. Neither is visible from reading
// the markup, which is why they are checked here by number.
func TestFrontendKeepsTheRowActionsReachable(t *testing.T) {
	stylesheet, err := os.ReadFile(filepath.Join("frontend", "src", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(stylesheet)

	// The content works out to 154px: two buttons (收件匣 63 + resume 67), one
	// 4px gap, and 10px of cell padding each side. Arithmetic, not a fresh
	// measurement — the per-button widths are the ones measured when there were
	// three of them (241px total), and this layout has not been measured since.
	// The floor leaves room for a font that renders the labels wider than the
	// machine those buttons were measured on.
	const actionsFloor = 160
	width := regexp.MustCompile(`col\.c-actions \{ width: (\d+)px; \}`).FindStringSubmatch(css)
	if width == nil {
		t.Fatal("style.css no longer sets a width for col.c-actions; the row actions have no reserved space")
	}
	pixels, err := strconv.Atoi(width[1])
	if err != nil {
		t.Fatalf("parse col.c-actions width %q: %v", width[1], err)
	}
	if pixels < actionsFloor {
		t.Errorf("col.c-actions is %dpx, want at least %dpx: the two row actions need 154px and the cell "+
			"clips what does not fit, at every window width", pixels, actionsFloor)
	}

	// Without a min-width the table is always exactly as wide as the card, so a
	// narrow window squeezes columns instead of letting .tablescroll scroll.
	table := regexp.MustCompile(`\ntable \{[^}]*\}`).FindString(css)
	if table == "" {
		t.Fatal("style.css has no table rule")
	}
	if !strings.Contains(table, "min-width") {
		t.Error("the table rule sets no min-width, so a narrow window compresses the columns " +
			"instead of scrolling and the last one is cut off with no way to reach it")
	}

	// And the actions stay put while the rest scrolls under them.
	actions := regexp.MustCompile(`\n\.col-actions \{[^}]*\}`).FindString(css)
	if !strings.Contains(actions, "position: sticky") {
		t.Error(".col-actions is no longer sticky, so the row actions scroll out of reach on a narrow window")
	}
}

// TestFrontendLeavesRoomForTheMacWindowButtons covers the other half of #153.
//
// desktop/main.go asks for mac.TitleBarHiddenInset(), which draws the traffic
// lights over the top-left of the page. Without a left inset they sit on top of
// the app's name and the node line beneath it. The inset is keyed off a class
// the window sets from HostPlatform(), so both halves are checked.
func TestFrontendLeavesRoomForTheMacWindowButtons(t *testing.T) {
	stylesheet, err := os.ReadFile(filepath.Join("frontend", "src", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	if !strings.Contains(string(stylesheet), "body.mac .titlebar") {
		t.Error("style.css has no body.mac .titlebar rule, so the macOS window buttons cover the app's name")
	}

	window, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(window), "TitleBarHiddenInset") {
		t.Skip("the window no longer insets its title bar; the rule above is then unnecessary")
	}

	var joined strings.Builder
	for _, source := range frontendSources(t) {
		joined.WriteString(source)
	}
	if !strings.Contains(joined.String(), "HostPlatform") {
		t.Error("no frontend source asks for the host platform, so the macOS inset is never applied")
	}
}

// TestFrontendKeepsTheWorkingDirectoryReadable pins the one thing that makes
// that column worth its width.
//
// The column answers "which project is this session in", and the answer is at
// the END of the path. Clipped the ordinary way it showed 「/Us…」 — the same
// eleven characters for every row on the machine. It is laid out right-to-left
// so the head is what goes, with the path itself isolated in a <bdi> so its
// own slashes do not reorder.
func TestFrontendKeepsTheWorkingDirectoryReadable(t *testing.T) {
	stylesheet, err := os.ReadFile(filepath.Join("frontend", "src", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(stylesheet)
	rule := regexp.MustCompile(`\ntd\.cwd \{[^}]*\}`).FindString(css)
	if rule == "" {
		t.Fatal("style.css has no td.cwd rule, so the working directory is clipped from the end")
	}
	if !strings.Contains(rule, "direction: rtl") {
		t.Error("td.cwd is no longer laid out right-to-left, so a path too long for the column " +
			"loses its tail — the project name — and every row reads the same")
	}
	if !strings.Contains(css, "td.cwd bdi") {
		t.Error("style.css no longer isolates the path's own direction; the slashes will reorder")
	}

	var joined strings.Builder
	for _, source := range frontendSources(t) {
		joined.WriteString(source)
	}
	if !strings.Contains(joined.String(), `element("bdi"`) {
		t.Error("no frontend source wraps the working directory in a <bdi>, so the right-to-left cell " +
			"reorders the path itself")
	}
}

// TestFrontendBackdropRainIsOptIn covers #156.
func TestFrontendBackdropRainIsOptIn(t *testing.T) {
	runNodeCheck(t, "backdrop-switches.mjs")
}

// TestFrontendMotionToggleStartsUnchecked keeps the markup agreeing with the
// state it is supposed to show.
//
// renderSettings writes the switch from state on every paint, so a stray
// checked attribute would only be visible for the first frame — long enough to
// read as the rain being on, and easy to leave behind by accident.
func TestFrontendMotionToggleStartsUnchecked(t *testing.T) {
	markup, err := os.ReadFile(filepath.Join("frontend", "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	for _, line := range strings.Split(string(markup), "\n") {
		if !strings.Contains(line, `id="toggle-motion"`) {
			continue
		}
		if strings.Contains(line, "checked") {
			t.Errorf("the 數字雨 switch is checked in the markup; the rain costs a whole core on an "+
				"Intel HD 520 and starts off (#156): %s", strings.TrimSpace(line))
		}
		return
	}
	t.Error(`no toggle-motion switch in index.html`)
}

// TestFrontendDoesNotBlurOverMovingPixels keeps blur off the backdrop (#156).
//
// Sixteen panels carried backdrop-filter over the rain, so a frame of moving
// background was resampled sixteen times. Unlike the rain itself — 101.7% of a
// core with it running against 2.4% with it off, measured on an Intel HD 520 —
// the blur was never measured on its own, so this guard rests on the reasoning
// and not on a number. It costs nothing to keep: the panels only ever needed to
// look translucent, which a colour does for free.
func TestFrontendDoesNotBlurOverMovingPixels(t *testing.T) {
	stylesheet, err := os.ReadFile(filepath.Join("frontend", "src", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	// The declaration, not the word: the rule that removed them explains itself
	// in a comment that names what it removed.
	if count := strings.Count(string(stylesheet), "backdrop-filter:"); count > 0 {
		t.Errorf("style.css has %d backdrop-filter rules; each one resamples the moving backdrop "+
			"every frame, which is what made the window unusable on an Intel HD 520 (#156)", count)
	}
}
