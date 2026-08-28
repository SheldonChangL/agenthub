package protocol_test

import (
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
)

func exportable() model.Session {
	return model.Session{
		ID:                "claude:abc",
		Provider:          model.ProviderClaude,
		ProviderSessionID: "abc",
		Management:        model.Unmanaged,
		Visibility:        model.VisibilityPublic,
		Audience:          model.Audience{Mode: model.AudienceAllPaired, ExportCWD: true},
		Status:            model.StatusIdle,
		StatusSource:      "metadata_process_heuristic",
		LastSeenAt:        time.Now().UTC(),
	}
}

// The projection must copy the owner's decision, never assert it.
//
// An earlier version wrote Visibility as the constant "public". A private
// session passed to it produced a summary stamped public that satisfied the
// schema and every test in this package, so the only real defence was the SQL
// filter in one caller.
func TestSummarizeRefusesUnpublishedSessions(t *testing.T) {
	for _, visibility := range []model.Visibility{model.VisibilityPrivate, "", "PUBLIC", "unknown"} {
		session := exportable()
		session.Visibility = visibility

		summary, err := protocol.Summarize("node_0123456789abcdef0123", session)
		if err == nil {
			t.Errorf("Summarize() accepted visibility %q and produced %+v", visibility, summary)
			continue
		}
		if summary != (protocol.SessionSummary{}) {
			t.Errorf("Summarize() returned a populated summary alongside an error for visibility %q", visibility)
		}
	}
}

// With the guard in place only a published session reaches the field copy, so
// copying and writing the constant are indistinguishable from outside: the
// guard is the defence, and the copy is there so the guard staying is not the
// only thing between us and the old bug. This test pins the exported shape.
func TestSummarizeProducesTheExportShape(t *testing.T) {
	summary, err := protocol.Summarize("node_0123456789abcdef0123", exportable())
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if summary.Visibility != string(model.VisibilityPublic) {
		t.Errorf("Visibility = %q, want public", summary.Visibility)
	}
	if summary.ID != "node_0123456789abcdef0123/claude:abc" {
		t.Errorf("ID = %q, want a qualified address", summary.ID)
	}
}

// The schema requires a status source, so a session without one must be caught
// here rather than producing an envelope no conforming consumer accepts.
func TestSummarizeRequiresStatusSourceAndNode(t *testing.T) {
	noSource := exportable()
	noSource.StatusSource = ""
	if _, err := protocol.Summarize("node_0123456789abcdef0123", noSource); err == nil {
		t.Error("Summarize() accepted a session with no status source")
	}
	if _, err := protocol.Summarize("", exportable()); err == nil {
		t.Error("Summarize() accepted an empty node identity")
	}
}

func TestQualifiedAddressRoundTrip(t *testing.T) {
	qualified := protocol.QualifiedID("node_abc", "claude:xyz")
	nodeID, sessionID, ok := protocol.SplitQualifiedID(qualified)
	if !ok || nodeID != "node_abc" || sessionID != "claude:xyz" {
		t.Fatalf("SplitQualifiedID(%q) = %q, %q, %v", qualified, nodeID, sessionID, ok)
	}

	// A bare local ID is not a qualified address and must not be guessed at.
	if _, _, ok := protocol.SplitQualifiedID("claude:xyz"); ok {
		t.Error("SplitQualifiedID accepted an unqualified id")
	}
	for _, malformed := range []string{"", "/", "node_only/", "/claude:x"} {
		if _, _, ok := protocol.SplitQualifiedID(malformed); ok {
			t.Errorf("SplitQualifiedID(%q) reported success", malformed)
		}
	}

	// The left-most separator wins, so a session ID cannot impersonate a node.
	nodeID, sessionID, ok = protocol.SplitQualifiedID("evil/node_b/claude:x")
	if !ok || nodeID != "evil" || sessionID != "node_b/claude:x" {
		t.Errorf("SplitQualifiedID resolved a spoofed address to %q, %q", nodeID, sessionID)
	}
	if strings.HasPrefix(sessionID, "claude:") {
		t.Error("a session id containing a separator was able to select a different node")
	}
}

// A database written before the registry rejected separators must not produce
// an address that a peer splits somewhere else.
func TestSummarizeRejectsSeparatorsFromLegacyRows(t *testing.T) {
	legacy := exportable()
	legacy.ID = "claude:evil/node_b"
	legacy.ProviderSessionID = "evil/node_b"
	if summary, err := protocol.Summarize("node_0123456789abcdef0123", legacy); err == nil {
		t.Errorf("Summarize() accepted a legacy row with a separator: %+v", summary)
	}
	if summary, err := protocol.Summarize("node_a/node_b", exportable()); err == nil {
		t.Errorf("Summarize() accepted a node id with a separator: %+v", summary)
	}
}

// The working directory names the account and the project, so it travels only
// when the owner asked for it.
func TestSummarizeOmitsWorkingDirectoryUnlessExported(t *testing.T) {
	session := exportable()
	session.CWD = "/Users/someone/Projects/secret-product"

	session.Audience.ExportCWD = false
	withheld, err := protocol.Summarize("node_0123456789abcdef0123", session)
	if err != nil {
		t.Fatal(err)
	}
	if withheld.CWD != "" {
		t.Errorf("CWD = %q; the owner did not opt in", withheld.CWD)
	}

	session.Audience.ExportCWD = true
	shared, err := protocol.Summarize("node_0123456789abcdef0123", session)
	if err != nil {
		t.Fatal(err)
	}
	if shared.CWD != session.CWD {
		t.Errorf("CWD = %q, want the session's directory", shared.CWD)
	}
}

// An audience that reaches nobody must not produce an export summary, whatever
// the derived visibility says.
func TestSummarizeRefusesAudiencesThatReachNobody(t *testing.T) {
	for name, audience := range map[string]model.Audience{
		"none":            {Mode: model.AudienceNone},
		"empty selection": {Mode: model.AudienceSelected},
		"unset":           {},
	} {
		t.Run(name, func(t *testing.T) {
			session := exportable()
			session.Audience = audience
			if summary, err := protocol.Summarize("node_0123456789abcdef0123", session); err == nil {
				t.Errorf("Summarize() produced %+v", summary)
			}
		})
	}
}
