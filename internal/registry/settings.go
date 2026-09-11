package registry

import (
	"context"
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
	rows, err := r.db.QueryContext(ctx, `SELECT key, value FROM node_settings`)
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

	// One transaction: a half-written set is a configuration nobody chose, and
	// the node would start with it after the next restart.
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save node settings: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	now := time.Now().UTC().UnixMilli()
	for _, write := range writes {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO node_settings (key, value, updated_at_ms) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
			write[0], write[1], now); err != nil {
			return fmt.Errorf("save node setting %q: %w", write[0], err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("save node settings: %w", err)
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
