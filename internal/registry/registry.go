package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agenthub.local/agenthub/internal/model"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Registry struct {
	db *sql.DB
}

type ListOptions struct {
	PublicOnly bool
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
    provider_session_id TEXT NOT NULL,
    management TEXT NOT NULL CHECK (management IN ('managed', 'unmanaged')),
    visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'public')),
    status TEXT NOT NULL CHECK (status IN ('active', 'idle', 'inactive', 'unknown')),
    status_source TEXT NOT NULL,
    cwd TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    metadata_path TEXT NOT NULL DEFAULT '',
    last_seen_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    UNIQUE(provider, provider_session_id)
);
CREATE INDEX IF NOT EXISTS idx_sessions_visibility_updated
    ON sessions(visibility, updated_at_ms DESC, id);
`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate registry: %w", err)
	}
	return nil
}

func (r *Registry) UpsertSession(ctx context.Context, session model.Session) (model.Session, error) {
	if err := validateSession(session); err != nil {
		return model.Session{}, err
	}
	const query = `
INSERT INTO sessions (
    id, provider, provider_session_id, management, visibility, status,
    status_source, cwd, source, metadata_path, last_seen_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, 'private', ?, ?, ?, ?, ?, ?, ?)
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
SELECT id, provider, provider_session_id, management, visibility, status,
       status_source, cwd, source, metadata_path, last_seen_at_ms, updated_at_ms
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
SELECT id, provider, provider_session_id, management, visibility, status,
       status_source, cwd, source, metadata_path, last_seen_at_ms, updated_at_ms
FROM sessions`
	if options.PublicOnly {
		query += ` WHERE visibility = 'public'`
	}
	query += ` ORDER BY updated_at_ms DESC, id ASC`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []model.Session
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

func (r *Registry) SetVisibility(ctx context.Context, id string, visibility model.Visibility) error {
	if visibility != model.VisibilityPrivate && visibility != model.VisibilityPublic {
		return fmt.Errorf("invalid visibility %q", visibility)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE sessions SET visibility = ?, updated_at_ms = ? WHERE id = ?`, visibility, time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("set visibility for %q: %w", id, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read visibility update result: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("session %q: %w", id, ErrNotFound)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner) (model.Session, error) {
	var session model.Session
	var lastSeenMS, updatedMS int64
	err := row.Scan(
		&session.ID, &session.Provider, &session.ProviderSessionID, &session.Management,
		&session.Visibility, &session.Status, &session.StatusSource, &session.CWD,
		&session.Source, &session.MetadataPath, &lastSeenMS, &updatedMS,
	)
	if err != nil {
		return model.Session{}, err
	}
	session.LastSeenAt = time.UnixMilli(lastSeenMS).UTC()
	session.UpdatedAt = time.UnixMilli(updatedMS).UTC()
	return session, nil
}

func validateSession(session model.Session) error {
	if session.ID == "" || session.ProviderSessionID == "" {
		return errors.New("session id and provider session id are required")
	}
	if session.ID != model.SessionID(session.Provider, session.ProviderSessionID) {
		return fmt.Errorf("session id %q does not match provider identity", session.ID)
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
