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
// Long enough for any real path, short enough that the field cannot carry
// prose. A peer that wants to say more than this about a directory is not
// describing a directory.
const MaxCWDLength = 512

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
		return fmt.Errorf("id %q is not <node-id>/<provider>:<id>", summary.ID)
	}
	if nodeID != senderNodeID {
		return fmt.Errorf("id %q names another node", summary.ID)
	}
	if err := address.ValidateLocalSessionID(sessionID); err != nil {
		return fmt.Errorf("id %q: %w", summary.ID, err)
	}
	provider, _, _ := strings.Cut(sessionID, ":")
	if summary.Provider != provider {
		return fmt.Errorf("provider %q disagrees with the id %q", summary.Provider, summary.ID)
	}
	if !model.ValidLifecycleStatus(model.LifecycleStatus(summary.Status)) {
		return fmt.Errorf("status %q is not a status", summary.Status)
	}
	switch model.Management(summary.Management) {
	case model.Managed, model.Unmanaged:
	default:
		return fmt.Errorf("management %q is neither managed nor unmanaged", summary.Management)
	}
	if summary.Visibility != string(model.VisibilityPublic) {
		// A summary only exists because its owner published the session, so
		// anything else is a snapshot that disagrees with itself.
		return fmt.Errorf("visibility %q, but only a published session is exported", summary.Visibility)
	}
	if err := validateReportedCWD(summary.CWD); err != nil {
		return err
	}
	if len(summary.StatusSource) > 128 {
		return fmt.Errorf("statusSource is %d bytes, over the 128 limit", len(summary.StatusSource))
	}
	return printableOnly("statusSource", summary.StatusSource)
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

// printableOnly refuses control characters.
//
// A newline in a path is what turns a field into two lines on a reader's screen,
// and the second line is the attacker's.
func printableOnly(field, value string) error {
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	return nil
}
