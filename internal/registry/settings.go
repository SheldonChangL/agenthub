package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"agenthub.local/agenthub/internal/nodeconfig"
)

// The node's remembered start-up settings.
//
// Key/value rather than one row of columns, because absence is the point: a
// key that is not there means nobody has ever said, and the node's own default
// applies, while a key holding "false" is an answer somebody gave. A wide row
// would have to invent a third state per column to tell those apart, and the
// migration for a database created by an earlier build would have to guess
// which one every existing installation meant.
//
// An unrecognised key is left alone rather than deleted. A database is opened
// by whichever build is installed, and a downgrade must not throw away a
// setting the newer build was remembering.

func (r *Registry) migrateNodeSettings(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS node_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at_ms INTEGER NOT NULL
);`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate node settings: %w", err)
	}
	return nil
}

// GetNodeSettings reads what this node has been told to remember.
//
// A database that has never been written to answers with everything absent,
// which is how a node upgraded from an earlier build keeps the behaviour it
// had: the defaults are the flag defaults those builds used.
func (r *Registry) GetNodeSettings(ctx context.Context) (nodeconfig.Partial, error) {
	return readNodeSettings(ctx, r.db)
}

// querier is whatever can run the read: the database, or a transaction that
// will also do the write. (The write side is the package's existing execer.)
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func readNodeSettings(ctx context.Context, db querier) (nodeconfig.Partial, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM node_settings`)
	if err != nil {
		return nodeconfig.Partial{}, fmt.Errorf("read node settings: %w", err)
	}
	defer rows.Close()
	var settings nodeconfig.Partial
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nodeconfig.Partial{}, fmt.Errorf("read node settings: %w", err)
		}
		switch key {
		case nodeconfig.SettingPeerListen:
			stored := value
			settings.PeerListen = &stored
		case nodeconfig.SettingAllowLAN:
			settings.AllowLAN = parseStoredBool(value)
		case nodeconfig.SettingDiscover:
			settings.Discover = parseStoredBool(value)
		case nodeconfig.SettingAutoWake:
			settings.AutoWake = parseStoredBool(value)
		case nodeconfig.SettingTreatAsPrivate:
			var ranges []string
			if err := json.Unmarshal([]byte(value), &ranges); err != nil {
				return nodeconfig.Partial{}, fmt.Errorf(
					"read node settings: %s is not a list of CIDR blocks: %w", key, err)
			}
			settings.TreatAsPrivate = &ranges
		}
	}
	if err := rows.Err(); err != nil {
		return nodeconfig.Partial{}, fmt.Errorf("read node settings: %w", err)
	}
	return settings, nil
}

// SaveNodeSettings records the fields that are present and leaves the rest
// alone, so a caller changing one switch does not pin the other four.
func (r *Registry) SaveNodeSettings(ctx context.Context, settings nodeconfig.Partial) error {
	r.settingsWrite.Lock()
	defer r.settingsWrite.Unlock()
	// One transaction: a half-written set is a configuration nobody chose, and
	// the node would start with it after the next restart.
	return r.inSettingsTransaction(ctx, func(transaction *sql.Tx) error {
		return writeNodeSettings(ctx, transaction, settings)
	})
}

// UpdateNodeSettings reads, decides and writes without letting go in between.
//
// The decision is made inside the transaction because it is made *about* what
// is stored: decide is handed the current settings and returns the fields to
// write, so a validation that passed cannot be committed against a
// configuration it never saw. Two concurrent writes on the owner's API — one
// closing allowLan, one moving the peer listener — could otherwise each
// validate against the state before the other and store a combination nobody
// validated, which the next start would refuse.
//
// The mutex is not redundant with the transaction: it keeps this process's own
// writers in line so they queue rather than collide, and only one process is
// meant to hold the database at a time.
func (r *Registry) UpdateNodeSettings(ctx context.Context,
	decide func(stored nodeconfig.Partial) (nodeconfig.Partial, error)) (nodeconfig.Partial, error) {
	r.settingsWrite.Lock()
	defer r.settingsWrite.Unlock()
	var saved nodeconfig.Partial
	err := r.inSettingsTransaction(ctx, func(transaction *sql.Tx) error {
		stored, err := readNodeSettings(ctx, transaction)
		if err != nil {
			return err
		}
		settings, err := decide(stored)
		if err != nil {
			return err
		}
		if err := writeNodeSettings(ctx, transaction, settings); err != nil {
			return err
		}
		saved, err = readNodeSettings(ctx, transaction)
		return err
	})
	if err != nil {
		return nodeconfig.Partial{}, err
	}
	return saved, nil
}

// inSettingsTransaction runs body in a transaction and commits if it returns.
func (r *Registry) inSettingsTransaction(ctx context.Context, body func(*sql.Tx) error) error {
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save node settings: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := body(transaction); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("save node settings: %w", err)
	}
	return nil
}

func writeNodeSettings(ctx context.Context, db execer, settings nodeconfig.Partial) error {
	if settings.Empty() {
		return nil
	}
	writes := make([][2]string, 0, len(nodeconfig.SettingNames))
	if settings.PeerListen != nil {
		writes = append(writes, [2]string{nodeconfig.SettingPeerListen, *settings.PeerListen})
	}
	for _, pair := range []struct {
		key   string
		value *bool
	}{
		{nodeconfig.SettingAllowLAN, settings.AllowLAN},
		{nodeconfig.SettingDiscover, settings.Discover},
		{nodeconfig.SettingAutoWake, settings.AutoWake},
	} {
		if pair.value != nil {
			writes = append(writes, [2]string{pair.key, strconv.FormatBool(*pair.value)})
		}
	}
	if settings.TreatAsPrivate != nil {
		ranges := *settings.TreatAsPrivate
		if ranges == nil {
			ranges = []string{}
		}
		encoded, err := json.Marshal(ranges)
		if err != nil {
			return fmt.Errorf("save node settings: %w", err)
		}
		writes = append(writes, [2]string{nodeconfig.SettingTreatAsPrivate, string(encoded)})
	}

	now := time.Now().UTC().UnixMilli()
	for _, write := range writes {
		if _, err := db.ExecContext(ctx, `
INSERT INTO node_settings (key, value, updated_at_ms) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
			write[0], write[1], now); err != nil {
			return fmt.Errorf("save node setting %q: %w", write[0], err)
		}
	}
	return nil
}

// parseStoredBool reads a stored switch. A value this build cannot read is
// treated as absent rather than as false: "I do not understand what was
// remembered" and "the owner turned it off" are different, and only one of
// them should quietly move the node's configuration.
func parseStoredBool(value string) *bool {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return nil
	}
	return &parsed
}
