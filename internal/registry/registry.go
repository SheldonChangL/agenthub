package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/model"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Registry struct {
	db *sql.DB
}

type ListOptions struct {
	PublicOnly bool
	Limit      int
	Offset     int
}

func Open(ctx context.Context, path string) (*Registry, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Registry{db: db}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (r *Registry) Close() error {
	return r.db.Close()
}

func (r *Registry) migrate(ctx context.Context) error {
	const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider IN ('claude', 'codex')),
    provider_session_id TEXT NOT NULL CHECK (instr(provider_session_id, '/') = 0),
    management TEXT NOT NULL CHECK (management IN ('managed', 'unmanaged')),
    audience_mode TEXT NOT NULL DEFAULT 'none' CHECK (audience_mode IN ('none', 'all_paired', 'selected')),
    export_cwd INTEGER NOT NULL DEFAULT 0 CHECK (export_cwd IN (0, 1)),
    accept_messages INTEGER NOT NULL DEFAULT 0 CHECK (accept_messages IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('active', 'idle', 'inactive', 'unknown')),
    status_source TEXT NOT NULL,
    cwd TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    metadata_path TEXT NOT NULL DEFAULT '',
    last_seen_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    UNIQUE(provider, provider_session_id)
);
CREATE TABLE IF NOT EXISTS session_audience (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL CHECK (
        length(node_id) BETWEEN 1 AND 128
        AND node_id NOT GLOB '*[^ -~]*'
    ),
    granted_at_ms INTEGER NOT NULL,
    PRIMARY KEY (session_id, node_id)
);
CREATE INDEX IF NOT EXISTS idx_session_audience_node
    ON session_audience(node_id, session_id);
CREATE TABLE IF NOT EXISTS node_identity (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    id TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    platform TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    sender_id TEXT NOT NULL DEFAULT '',
    recipient_id TEXT NOT NULL REFERENCES sessions(id),
    body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 32768),
    created_at_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_recipient_created
    ON messages(recipient_id, created_at_ms ASC, id ASC);
`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate registry: %w", err)
	}
	if err := r.addSessionPolicyColumns(ctx); err != nil {
		return err
	}
	// Indexes come last: a database created by an earlier build only gains the
	// audience columns in the step above.
	const indexes = `
CREATE INDEX IF NOT EXISTS idx_sessions_audience_updated
    ON sessions(audience_mode, updated_at_ms DESC, id);
`
	if _, err := r.db.ExecContext(ctx, indexes); err != nil {
		return fmt.Errorf("create registry indexes: %w", err)
	}
	return nil
}

// addSessionPolicyColumns brings a database created by an earlier build up to
// the audience model.
//
// Every column defaults to the closed value, which is also the decision in
// ADR-001: a session marked public before any peer could exist was never
// consent to reach one, so an upgraded database publishes nothing until the
// owner chooses again.
func (r *Registry) addSessionPolicyColumns(ctx context.Context) error {
	columns := []struct{ name, definition string }{
		{"audience_mode", "TEXT NOT NULL DEFAULT 'none' CHECK (audience_mode IN ('none', 'all_paired', 'selected'))"},
		{"export_cwd", "INTEGER NOT NULL DEFAULT 0 CHECK (export_cwd IN (0, 1))"},
		{"accept_messages", "INTEGER NOT NULL DEFAULT 0 CHECK (accept_messages IN (0, 1))"},
	}

	existing := map[string]bool{}
	rows, err := r.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('sessions')`)
	if err != nil {
		return fmt.Errorf("read sessions columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan sessions column: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read sessions columns: %w", err)
	}

	for _, column := range columns {
		if existing[column.name] {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE sessions ADD COLUMN %s %s", column.name, column.definition)
		if _, err := r.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("add sessions column %q: %w", column.name, err)
		}
	}
	return nil
}

func (r *Registry) GetNodeIdentity(ctx context.Context) (model.NodeIdentity, error) {
	var identity model.NodeIdentity
	var createdMS int64
	err := r.db.QueryRowContext(ctx, `SELECT id, display_name, platform, created_at_ms FROM node_identity WHERE singleton = 1`).Scan(
		&identity.ID, &identity.DisplayName, &identity.Platform, &createdMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.NodeIdentity{}, ErrNotFound
	}
	if err != nil {
		return model.NodeIdentity{}, fmt.Errorf("get node identity: %w", err)
	}
	identity.CreatedAt = time.UnixMilli(createdMS).UTC()
	return identity, nil
}

func (r *Registry) SaveNodeIdentity(ctx context.Context, identity model.NodeIdentity) error {
	if identity.ID == "" || identity.DisplayName == "" || identity.Platform == "" || identity.CreatedAt.IsZero() {
		return errors.New("complete node identity is required")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO node_identity (singleton, id, display_name, platform, created_at_ms)
VALUES (1, ?, ?, ?, ?)
ON CONFLICT(singleton) DO NOTHING`,
		identity.ID, identity.DisplayName, identity.Platform, identity.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("save node identity: %w", err)
	}
	return nil
}

func (r *Registry) CreateMessage(ctx context.Context, message model.Message) (model.Message, error) {
	if strings.TrimSpace(message.To) == "" {
		return model.Message{}, fmt.Errorf("%w: message recipient is required", ErrInvalidSession)
	}
	if strings.TrimSpace(message.Body) == "" || len(message.Body) > 32768 {
		return model.Message{}, fmt.Errorf("%w: message body must contain 1 to 32768 bytes", ErrInvalidSession)
	}
	destination, err := r.GetSession(ctx, message.To)
	if err != nil {
		return model.Message{}, err
	}
	// A session accepts messages only when its owner said so. Refusing here
	// keeps an unwanted queue from building up for a session nobody intends to
	// deliver to.
	if !destination.Audience.AcceptMessages {
		return model.Message{}, fmt.Errorf(
			"%w: session %q does not accept messages", ErrInvalidSession, message.To)
	}
	if message.ID == "" {
		var err error
		message.ID, err = id.New("msg_")
		if err != nil {
			return model.Message{}, err
		}
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO messages (id, sender_id, recipient_id, body, created_at_ms)
VALUES (?, ?, ?, ?, ?)`, message.ID, message.From, message.To, message.Body, message.CreatedAt.UTC().UnixMilli()); err != nil {
		return model.Message{}, fmt.Errorf("create message: %w", err)
	}
	return message, nil
}

func (r *Registry) Inbox(ctx context.Context, recipientID string, limit int) ([]model.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, sender_id, recipient_id, body, created_at_ms
FROM messages WHERE recipient_id = ?
ORDER BY created_at_ms ASC, id ASC LIMIT ?`, recipientID, limit)
	if err != nil {
		return nil, fmt.Errorf("read inbox: %w", err)
	}
	defer rows.Close()
	messages := make([]model.Message, 0)
	for rows.Next() {
		var message model.Message
		var createdMS int64
		if err := rows.Scan(&message.ID, &message.From, &message.To, &message.Body, &createdMS); err != nil {
			return nil, fmt.Errorf("scan inbox message: %w", err)
		}
		message.CreatedAt = time.UnixMilli(createdMS).UTC()
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read inbox rows: %w", err)
	}
	return messages, nil
}

func (r *Registry) UpsertSession(ctx context.Context, session model.Session) (model.Session, error) {
	if err := validateSession(session); err != nil {
		return model.Session{}, err
	}
	const query = `
INSERT INTO sessions (
    id, provider, provider_session_id, management, status,
    status_source, cwd, source, metadata_path, last_seen_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    provider = excluded.provider,
    provider_session_id = excluded.provider_session_id,
    management = excluded.management,
    status = excluded.status,
    status_source = excluded.status_source,
    cwd = excluded.cwd,
    source = excluded.source,
    metadata_path = excluded.metadata_path,
    last_seen_at_ms = excluded.last_seen_at_ms,
    updated_at_ms = excluded.updated_at_ms
`
	_, err := r.db.ExecContext(ctx, query,
		session.ID, session.Provider, session.ProviderSessionID, session.Management,
		session.Status, session.StatusSource, session.CWD, session.Source,
		session.MetadataPath, session.LastSeenAt.UTC().UnixMilli(), session.UpdatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return model.Session{}, fmt.Errorf("upsert session %q: %w", session.ID, err)
	}
	return r.GetSession(ctx, session.ID)
}

func (r *Registry) GetSession(ctx context.Context, id string) (model.Session, error) {
	const query = `
SELECT id, provider, provider_session_id, management, status,
       status_source, cwd, source, metadata_path, last_seen_at_ms, updated_at_ms,
       audience_mode, export_cwd, accept_messages,
       (audience_mode = 'all_paired'
        OR (audience_mode = 'selected'
            AND EXISTS (SELECT 1 FROM session_audience WHERE session_id = sessions.id))) AS published,
       COALESCE((SELECT group_concat(node_id, char(31)) FROM session_audience
                 WHERE session_id = sessions.id), '') AS grants
FROM sessions WHERE id = ?`
	session, err := scanSession(r.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, fmt.Errorf("session %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.Session{}, fmt.Errorf("get session %q: %w", id, err)
	}
	return session, nil
}

func (r *Registry) ListSessions(ctx context.Context, options ListOptions) ([]model.Session, error) {
	query := `
SELECT id, provider, provider_session_id, management, status,
       status_source, cwd, source, metadata_path, last_seen_at_ms, updated_at_ms,
       audience_mode, export_cwd, accept_messages,
       (audience_mode = 'all_paired'
        OR (audience_mode = 'selected'
            AND EXISTS (SELECT 1 FROM session_audience WHERE session_id = sessions.id))) AS published,
       COALESCE((SELECT group_concat(node_id, char(31)) FROM session_audience
                 WHERE session_id = sessions.id), '') AS grants
FROM sessions`
	if options.PublicOnly {
		query += ` ` + publishedPredicate
	}
	query += ` ORDER BY updated_at_ms DESC, id ASC`
	var args []any
	if options.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, options.Limit, max(options.Offset, 0))
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]model.Session, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions rows: %w", err)
	}
	return sessions, nil
}

func (r *Registry) CountSessions(ctx context.Context, publicOnly bool) (int, error) {
	query := `SELECT COUNT(*) FROM sessions`
	if publicOnly {
		query += ` ` + publishedPredicate
	}
	var count int
	if err := r.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("count sessions: %w", err)
	}
	return count, nil
}

// SetVisibility keeps the pre-audience call working: publishing means the
// explicit "all paired nodes" choice, and unpublishing means nobody.
//
// It is a convenience over SetAudience, not a second way to store the decision.
func (r *Registry) SetVisibility(ctx context.Context, id string, visibility model.Visibility) error {
	switch visibility {
	case model.VisibilityPublic:
		// The export flags stay closed. Publishing through the old call says
		// who may see the session, not how much of it; the working directory
		// names the account and the project and needs its own opt-in.
		return r.SetAudience(ctx, id, model.Audience{Mode: model.AudienceAllPaired})
	case model.VisibilityPrivate:
		return r.SetAudience(ctx, id, model.Audience{Mode: model.AudienceNone})
	default:
		return fmt.Errorf("%w: visibility %q is not private or public", ErrInvalidSession, visibility)
	}
}

// SetAudience replaces a session's export policy.
//
// The whole policy is written at once, inside a transaction: a mode and its
// grants that disagree would be a policy nobody chose, and the window where
// they disagree is exactly when a heartbeat could read it.
func (r *Registry) SetAudience(ctx context.Context, id string, audience model.Audience) error {
	if !model.ValidAudienceMode(audience.Mode) {
		return fmt.Errorf("%w: audience mode %q is not none, all_paired or selected", ErrInvalidSession, audience.Mode)
	}
	nodes := make([]string, 0, len(audience.Nodes))
	seen := map[string]bool{}
	for _, node := range audience.Nodes {
		if node == "" || len(node) > 128 {
			return fmt.Errorf("%w: node id %q is empty or too long", ErrInvalidSession, node)
		}
		if strings.ContainsFunc(node, unicode.IsControl) {
			return fmt.Errorf("%w: node id %q contains a control character", ErrInvalidSession, node)
		}
		if seen[node] {
			continue
		}
		seen[node] = true
		nodes = append(nodes, node)
	}
	if audience.Mode != model.AudienceSelected && len(nodes) > 0 {
		return fmt.Errorf("%w: audience mode %q does not take a node list", ErrInvalidSession, audience.Mode)
	}

	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audience update: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	result, err := transaction.ExecContext(ctx,
		`UPDATE sessions SET audience_mode = ?, export_cwd = ?, accept_messages = ?, updated_at_ms = ? WHERE id = ?`,
		audience.Mode, boolToInt(audience.ExportCWD), boolToInt(audience.AcceptMessages),
		time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("set audience for %q: %w", id, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read audience update result: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("session %q: %w", id, ErrNotFound)
	}

	// Grants are replaced rather than merged. Leaving a stale row would keep a
	// node authorized after the owner removed it.
	if _, err := transaction.ExecContext(ctx, `DELETE FROM session_audience WHERE session_id = ?`, id); err != nil {
		return fmt.Errorf("clear audience grants for %q: %w", id, err)
	}
	now := time.Now().UTC().UnixMilli()
	for _, node := range nodes {
		if _, err := transaction.ExecContext(ctx,
			`INSERT INTO session_audience (session_id, node_id, granted_at_ms) VALUES (?, ?, ?)`,
			id, node, now); err != nil {
			return fmt.Errorf("grant %q access to %q: %w", node, id, err)
		}
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit audience update: %w", err)
	}
	return nil
}

// GetAudience reads a session's export policy including its grants.
func (r *Registry) GetAudience(ctx context.Context, id string) (model.Audience, error) {
	var audience model.Audience
	var exportCWD, acceptMessages int
	err := r.db.QueryRowContext(ctx,
		`SELECT audience_mode, export_cwd, accept_messages FROM sessions WHERE id = ?`, id).
		Scan(&audience.Mode, &exportCWD, &acceptMessages)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Audience{}, fmt.Errorf("session %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.Audience{}, fmt.Errorf("get audience for %q: %w", id, err)
	}
	audience.ExportCWD = exportCWD == 1
	audience.AcceptMessages = acceptMessages == 1

	rows, err := r.db.QueryContext(ctx,
		`SELECT node_id FROM session_audience WHERE session_id = ? ORDER BY node_id`, id)
	if err != nil {
		return model.Audience{}, fmt.Errorf("list audience grants for %q: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var node string
		if err := rows.Scan(&node); err != nil {
			return model.Audience{}, fmt.Errorf("scan audience grant: %w", err)
		}
		audience.Nodes = append(audience.Nodes, node)
	}
	if err := rows.Err(); err != nil {
		return model.Audience{}, fmt.Errorf("read audience grants for %q: %w", id, err)
	}
	return audience, nil
}

// grantSeparator joins node IDs inside one SQL value. Node IDs reject control
// characters, so it cannot appear inside one.
const grantSeparator = "\x1f"

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type rowScanner interface {
	Scan(dest ...any) error
}

// publishedPredicate selects sessions that leave this host at all. "selected"
// with no grants publishes to nobody, so it must not count as published.
const publishedPredicate = `WHERE (
    audience_mode = 'all_paired'
    OR (audience_mode = 'selected'
        AND EXISTS (SELECT 1 FROM session_audience WHERE session_id = sessions.id))
)`

func scanSession(row rowScanner) (model.Session, error) {
	var session model.Session
	var lastSeenMS, updatedMS int64
	var exportCWD, acceptMessages, published int
	var grants string
	err := row.Scan(
		&session.ID, &session.Provider, &session.ProviderSessionID, &session.Management,
		&session.Status, &session.StatusSource, &session.CWD,
		&session.Source, &session.MetadataPath, &lastSeenMS, &updatedMS,
		&session.Audience.Mode, &exportCWD, &acceptMessages, &published, &grants,
	)
	if err != nil {
		return model.Session{}, err
	}
	session.Audience.ExportCWD = exportCWD == 1
	session.Audience.AcceptMessages = acceptMessages == 1
	session.LastSeenAt = time.UnixMilli(lastSeenMS).UTC()
	session.UpdatedAt = time.UnixMilli(updatedMS).UTC()
	// Visibility is derived, never stored: one source of truth for "who may
	// see this" avoids the two disagreeing.
	// Grants come back with the row. Loading them separately would leave
	// Audience.PublishesTo answering "no" for a selected session read from a
	// listing, so the same policy would mean different things depending on how
	// it was fetched.
	if grants != "" {
		session.Audience.Nodes = strings.Split(grants, grantSeparator)
	}
	session.Visibility = model.VisibilityPrivate
	if published == 1 {
		session.Visibility = model.VisibilityPublic
	}
	return session, nil
}

// ErrInvalidSession marks a session the store will never accept, whatever the
// caller does. Callers use it to tell "this record is unusable" apart from
// "the database is unavailable", which need opposite responses.
var ErrInvalidSession = errors.New("invalid session")

func validateSession(session model.Session) error {
	if err := validateSessionFields(session); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSession, err)
	}
	return nil
}

func validateSessionFields(session model.Session) error {
	if session.ID == "" || session.ProviderSessionID == "" {
		return errors.New("session id and provider session id are required")
	}
	if session.ID != model.SessionID(session.Provider, session.ProviderSessionID) {
		return fmt.Errorf("session id %q does not match provider identity", session.ID)
	}
	if err := model.ValidateProviderSessionID(session.ProviderSessionID); err != nil {
		return err
	}
	if session.Provider != model.ProviderClaude && session.Provider != model.ProviderCodex {
		return fmt.Errorf("invalid provider %q", session.Provider)
	}
	if session.Management != model.Managed && session.Management != model.Unmanaged {
		return fmt.Errorf("invalid management %q", session.Management)
	}
	if session.Status != model.StatusActive && session.Status != model.StatusIdle && session.Status != model.StatusInactive && session.Status != model.StatusUnknown {
		return fmt.Errorf("invalid status %q", session.Status)
	}
	if session.LastSeenAt.IsZero() || session.UpdatedAt.IsZero() {
		return errors.New("last seen and updated timestamps are required")
	}
	return nil
}
