package protocol

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"agenthub.local/agenthub/internal/address"
	"agenthub.local/agenthub/internal/model"
)

// MaxAcceptedTTL bounds how far ahead a peer may declare its snapshot valid.
//
// A peer chooses its own expiry, and nothing refuses one far in the future — so
// a single heartbeat saying it is good until the year 3000 leaves that peer
// permanently online, with whatever it last claimed, after it has been switched
// off, sold, or cleaned up. Presence would stop meaning "recently heard from".
//
// Generous against the 30-second interval a node actually publishes at, so a
// slow link or a paused peer is not punished for it.
const MaxAcceptedTTL = 10 * time.Minute

// ClampExpiry returns the expiry this node will honour for a peer's snapshot.
func ClampExpiry(declared, now time.Time) time.Time {
	ceiling := now.Add(MaxAcceptedTTL)
	if declared.After(ceiling) {
		return ceiling
	}
	return declared
}

// MaxCWDLength bounds a working directory a peer reports.
//
// This bounds volume, not intent: a sentence fits in 512 bytes, and no length
// limit can stop one. What it stops is a field being used to carry a page. The
// sending side applies the same bound (Summarize drops an over-long directory
// rather than exporting it), so this refuses only what a peer running different
// code would send.
//
// PATH_MAX is 1024 on macOS and 4096 on Linux, so a legal path can exceed this
// and its session is still exported — without the directory.
const MaxCWDLength = 512

// MaxProviderSessionIDLength bounds the half of an id a peer chooses.
//
// Claude and Codex both use UUIDs, which are 36 characters. The same number the
// store enforces on write, aliased rather than repeated: two constants that
// must agree are one that eventually will not, and the gap between them is a
// session this node stores happily and every peer refuses.
const MaxProviderSessionIDLength = model.MaxProviderSessionIDLength

// MaxClockSkew is how far ahead a peer's reported times may be.
//
// Not a security bound — it exists so that a value no clock explains cannot
// become a sort order. Wide enough that a genuinely skewed peer is not punished.
const MaxClockSkew = 24 * time.Hour

// MaxStatusSourceLength matches the published schema. Every value this build
// produces is under 30 bytes.
const MaxStatusSourceLength = 64

// MaxSummarySessions bounds how many sessions one snapshot may describe.
//
// A peer publishing more than this has either an unusual installation or an
// intent this node need not accommodate; either way the answer is the same.
const MaxSummarySessions = 500

// ValidateIncomingPayload checks a heartbeat payload's contents before it is
// stored.
//
// The envelope proves who sent a snapshot. It says nothing about what is inside
// it, and what is inside reaches an agent's reasoning through agent_list with no
// notice and no attribution — a quieter channel than the inbox, and one the MCP
// server's own instructions describe as authorised metadata.
//
// So the fields are constrained to what they are for. A status must be a status;
// a working directory must be a path and not a paragraph; a session id must be
// an id. None of this stops a peer lying about its own state — that is its
// prerogative and the ADR says so — but it stops the fields being used as a
// place to write to the reader.
//
// Applied at the receiving edge rather than at each reader: the desktop app and
// the CLI read the same rows, and a check in one of them protects only that one.
func ValidateIncomingPayload(senderNodeID string, payload HeartbeatPayload) error {
	if len(payload.Sessions) > MaxSummarySessions {
		return fmt.Errorf("snapshot describes %d sessions, over the %d limit",
			len(payload.Sessions), MaxSummarySessions)
	}
	seen := make(map[string]struct{}, len(payload.Sessions))
	for i, summary := range payload.Sessions {
		if err := validateIncomingSummary(senderNodeID, summary); err != nil {
			return fmt.Errorf("session %d: %w", i, err)
		}
		if _, duplicate := seen[summary.ID]; duplicate {
			// Not truncated because it is bounded by construction: the row has
			// just passed validateIncomingSummary, so the id is a node id and a
			// provider session id, each within its limit.
			return fmt.Errorf("session %d: %q appears twice", i, summary.ID)
		}
		seen[summary.ID] = struct{}{}
	}
	return nil
}

func validateIncomingSummary(senderNodeID string, summary SessionSummary) error {
	// A peer describes its own sessions and no one else's. Checked here as well
	// as in every reader, because this is where it can be checked once.
	nodeID, sessionID, qualified := address.SplitQualifiedID(summary.ID)
	if !qualified {
		return fmt.Errorf("id %q is not <node-id>/<provider>:<id>", truncateForError(summary.ID))
	}
	if nodeID != senderNodeID {
		return fmt.Errorf("id %q names another node", truncateForError(summary.ID))
	}
	if err := address.ValidateLocalSessionID(sessionID); err != nil {
		// The inner error carries the id it was given, so it is not wrapped:
		// wrapping would put the untruncated value back into the message the
		// truncation above exists to bound.
		return fmt.Errorf("id %q is not a session address", truncateForError(summary.ID))
	}
	// The provider session id is an address, not a label. Unconstrained it was
	// the widest channel here — the first field of every agent_list row, and
	// large enough to hold a page of text. Real ones are UUIDs.
	_, providerSessionID, _ := strings.Cut(sessionID, ":")
	if len(providerSessionID) > MaxProviderSessionIDLength {
		return fmt.Errorf("id is %d bytes, over the %d limit",
			len(providerSessionID), MaxProviderSessionIDLength)
	}
	for _, r := range providerSessionID {
		if r < '!' || r > '~' {
			return fmt.Errorf("id %q has a character outside printable ASCII", truncateForError(summary.ID))
		}
	}
	provider, _, _ := strings.Cut(sessionID, ":")
	if summary.Provider != provider {
		return fmt.Errorf("provider %q disagrees with the id %q",
			truncateForError(summary.Provider), truncateForError(summary.ID))
	}
	if !model.ValidLifecycleStatus(model.LifecycleStatus(summary.Status)) {
		return fmt.Errorf("status %q is not a status", truncateForError(summary.Status))
	}
	switch model.Management(summary.Management) {
	case model.Managed, model.Unmanaged:
	default:
		return fmt.Errorf("management %q is neither managed nor unmanaged",
			truncateForError(summary.Management))
	}
	if summary.Visibility != string(model.VisibilityPublic) {
		// A summary only exists because its owner published the session, so
		// anything else is a snapshot that disagrees with itself.
		return fmt.Errorf("visibility %q, but only a published session is exported",
			truncateForError(summary.Visibility))
	}
	if err := validateReportedCWD(summary.CWD); err != nil {
		return err
	}
	if summary.StatusSource == "" {
		return fmt.Errorf("statusSource is empty; the schema and the sending side both require one")
	}
	if len(summary.StatusSource) > MaxStatusSourceLength {
		return fmt.Errorf("statusSource is %d bytes, over the %d limit",
			len(summary.StatusSource), MaxStatusSourceLength)
	}
	if err := printableOnly("statusSource", summary.StatusSource); err != nil {
		return err
	}
	// A time that is not a time. It reaches a reader through agent_list and the
	// desktop, where "last seen in the year 9999" is a sort order nobody chose.
	//
	// Deliberately wide. A wrong lastSeenAt is a display problem, not a security
	// one, and the refusal is of the whole snapshot — so a peer whose clock runs
	// twenty minutes fast should not vanish from its owner's view over it. Only
	// values no clock error explains are refused.
	if summary.LastSeenAt.After(time.Now().UTC().Add(MaxClockSkew)) {
		return fmt.Errorf("lastSeenAt %s is further ahead than any clock error explains", summary.LastSeenAt)
	}
	return nil
}

func validateReportedCWD(cwd string) error {
	if cwd == "" {
		return nil
	}
	if len(cwd) > MaxCWDLength {
		return fmt.Errorf("cwd is %d bytes, over the %d limit", len(cwd), MaxCWDLength)
	}
	return printableOnly("cwd", cwd)
}

// printableOnly refuses anything that is not a graphic character.
//
// A newline in a path is what turns a field into two lines on a reader's screen,
// and the second line is the attacker's. unicode.IsControl alone misses that:
// U+2028 LINE SEPARATOR and U+2029 are category Zl/Zp, not Cc, and render as
// line breaks in plenty of contexts. U+202E RIGHT-TO-LEFT OVERRIDE is Cf and
// reorders whatever follows it. IsGraphic keeps letters, marks, numbers,
// punctuation, symbols and ordinary spaces, and refuses the rest.
func printableOnly(field, value string) error {
	for _, r := range value {
		if !unicode.IsGraphic(r) {
			return fmt.Errorf("%s contains a character that is not printable", field)
		}
	}
	return nil
}

// truncateForError bounds peer-controlled text on its way into an error or a
// log line. A refusal is logged, and a megabyte id would otherwise be a
// megabyte log line, on demand.
func truncateForError(value string) string {
	// Counted in place rather than converted to []rune, which would allocate
	// four bytes per input byte to keep eighty characters of a megabyte.
	count := 0
	for i := range value {
		if count == 80 {
			return value[:i] + "…"
		}
		count++
	}
	return value
}
