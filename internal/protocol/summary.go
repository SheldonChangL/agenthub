package protocol

import (
	"fmt"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/address"
	"agenthub.local/agenthub/internal/model"
)

// SessionSummary is the only session shape that may leave the owner's host.
//
// It is deliberately a separate type from model.Session rather than a subset
// tag set on it: a new owner-local field must not become remotely visible just
// because someone added it to the registry. Every field here is listed on
// purpose, and docs/broker-protocol.schema.json rejects anything else.
//
// Never add: provider session ID as a standalone field, provider source,
// metadata path, internal update time, or any transcript or prompt content.
type SessionSummary struct {
	ID           string    `json:"id"`
	Provider     string    `json:"provider"`
	Status       string    `json:"status"`
	StatusSource string    `json:"statusSource"`
	Management   string    `json:"management"`
	Visibility   string    `json:"visibility"`
	CWD          string    `json:"cwd,omitempty"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
}

// Summarize projects an owner-local session into the export view.
//
// It copies field by field on purpose. A struct conversion or an embedded
// model.Session would silently widen the export view the next time the local
// model grows a field.
//
// It refuses any session the owner has not published. The registry query is
// the primary boundary; this is a second one that does not depend on the
// caller having asked the right question. Writing the visibility as a constant
// here would make the projection fail open: a caller that passed a private
// session would get a summary stamped "public" that satisfies every schema and
// test we have.
func exportedCWD(session model.Session) string {
	if !session.Audience.ExportCWD {
		return ""
	}
	// Bounded by the same rules a receiver applies, and dropped rather than
	// truncated when it does not fit.
	//
	// A receiver refuses the whole snapshot over one bad field, so exporting a
	// directory this build knows will be refused would take the session — and
	// every other session in that heartbeat — off every peer's view, for a path
	// that happens to be long or to contain a tab. PATH_MAX allows both. The
	// owner loses a directory they opted into exporting; they do not lose the
	// session.
	if len(session.CWD) > MaxCWDLength || printableOnly("cwd", session.CWD) != nil {
		return ""
	}
	return session.CWD
}

func Summarize(nodeID string, session model.Session) (SessionSummary, error) {
	if !session.Audience.PublishesToAnyone() {
		return SessionSummary{}, fmt.Errorf(
			"refusing to export session %q with audience %q", session.ID, session.Audience.Mode)
	}
	if session.Visibility != model.VisibilityPublic {
		return SessionSummary{}, fmt.Errorf(
			"refusing to export session %q with visibility %q", session.ID, session.Visibility)
	}
	if nodeID == "" {
		return SessionSummary{}, fmt.Errorf("refusing to export session %q without a node identity", session.ID)
	}
	// statusSource is required by the published schema; an empty one would
	// produce an envelope no conforming consumer accepts.
	if session.StatusSource == "" {
		return SessionSummary{}, fmt.Errorf("session %q has no status source", session.ID)
	}
	// The registry rejects a separator on write, but a database written by an
	// older build predates that rule. The export layer does not get to assume
	// the store is well formed: a separator here would move the boundary
	// between node and session in the address a peer parses.
	if strings.Contains(session.ID, model.SessionIDSeparator) ||
		strings.Contains(nodeID, model.SessionIDSeparator) {
		return SessionSummary{}, fmt.Errorf("session %q or node %q contains an address separator", session.ID, nodeID)
	}
	// A time no clock explains would cost the whole snapshot at the far end,
	// because a receiver refuses a snapshot over one bad row. Clamped rather
	// than refused: the session is still worth exporting, and the wrong value is
	// only ever a display detail.
	lastSeen := session.LastSeenAt.UTC()
	if ceiling := time.Now().UTC().Add(MaxClockSkew); lastSeen.After(ceiling) {
		lastSeen = ceiling
	}
	return SessionSummary{
		ID:           address.QualifiedID(nodeID, session.ID),
		Provider:     string(session.Provider),
		Status:       string(session.Status),
		StatusSource: session.StatusSource,
		Management:   string(session.Management),
		Visibility:   string(session.Visibility),
		// The working directory names the account and the project, so it
		// travels only when the owner asked for it.
		CWD:        exportedCWD(session),
		LastSeenAt: lastSeen,
	}, nil
}

// ExportableSessionID applies the receiver's id rules on the sending side.
//
// One definition, used both ways, so the two cannot drift into a state where
// this node sends what its peers refuse. The caller decides what to do about a
// session that fails it: the builder leaves it out, because an id cannot be
// dropped the way a working directory can — it is what the session is.
func ExportableSessionID(sessionID string) error {
	if err := address.ValidateLocalSessionID(sessionID); err != nil {
		return err
	}
	_, providerSessionID, _ := strings.Cut(sessionID, ":")
	if len(providerSessionID) > MaxProviderSessionIDLength {
		return fmt.Errorf("id is %d bytes, over the %d a peer accepts",
			len(providerSessionID), MaxProviderSessionIDLength)
	}
	for _, r := range providerSessionID {
		if r < '!' || r > '~' {
			return fmt.Errorf("id has a character outside printable ASCII")
		}
	}
	return nil
}
