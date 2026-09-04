package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"agenthub.local/agenthub/internal/model"
)

// TrustedNode is a peer this owner has decided to believe.
//
// Trust is about identity only: it says this node ID belongs to this key. It
// grants no access to any session — that is an audience decision, made per
// session, so pairing a machine never publishes anything.
type TrustedNode struct {
	NodeID      string    `json:"nodeId"`
	DisplayName string    `json:"displayName"`
	Platform    string    `json:"platform"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
	PairedAt    time.Time `json:"pairedAt"`
	LastSeenAt  time.Time `json:"lastSeenAt,omitzero"`
	// Address is where this node reaches the peer, as host:port. It is empty
	// until something supplies one, and an empty address simply means "nothing
	// to deliver to" — it is not a trust decision. Trust says who a node is;
	// the address says where it currently answers, which is the part that
	// changes when a laptop moves between networks.
	Address string `json:"address,omitempty"`
}

func (r *Registry) migrateTrust(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS trusted_nodes (
    node_id TEXT PRIMARY KEY CHECK (length(node_id) BETWEEN 16 AND 128 AND node_id NOT GLOB '*[^!-~]*'),
    display_name TEXT NOT NULL,
    platform TEXT NOT NULL,
    public_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    paired_at_ms INTEGER NOT NULL,
    last_seen_at_ms INTEGER NOT NULL DEFAULT 0
);
`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate trust store: %w", err)
	}
	return r.addTrustAddressColumn(ctx)
}

// addTrustAddressColumn brings a database paired by an earlier build up to
// holding peer addresses. The default is empty, which delivers to nobody: an
// upgrade must not invent a destination for a node the owner paired when no
// address existed.
func (r *Registry) addTrustAddressColumn(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('trusted_nodes')`)
	if err != nil {
		return fmt.Errorf("read trusted_nodes columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan trusted_nodes column: %w", err)
		}
		if name == "address" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read trusted_nodes columns: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`ALTER TABLE trusted_nodes ADD COLUMN address TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add trusted_nodes address column: %w", err)
	}
	return nil
}

// TrustNode records a peer's verified identity.
//
// Re-pairing a node that is already trusted with a different key is refused:
// silently accepting a new key is how a machine gets impersonated, and the
// owner must revoke first so the decision is deliberate.
func (r *Registry) TrustNode(ctx context.Context, node TrustedNode) error {
	if err := validateTrustedNode(node); err != nil {
		return err
	}

	if node.PairedAt.IsZero() {
		node.PairedAt = time.Now().UTC()
	}

	// The check and the write share a transaction. Reading first and writing
	// after lets two concurrent pairings both report success, and the loser
	// would be told its key was accepted when the stored key is the winner's.
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin trust update: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	var storedKey string
	err = transaction.QueryRowContext(ctx,
		`SELECT public_key FROM trusted_nodes WHERE node_id = ?`, node.NodeID).Scan(&storedKey)
	switch {
	case err == nil:
		if storedKey != node.PublicKey {
			return fmt.Errorf(
				"%w: node %q is already trusted with a different key; revoke it first",
				ErrInvalidSession, node.NodeID)
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return fmt.Errorf("read trusted node %q: %w", node.NodeID, err)
	}

	_, err = transaction.ExecContext(ctx, `
INSERT INTO trusted_nodes (node_id, display_name, platform, public_key, fingerprint, paired_at_ms, last_seen_at_ms)
VALUES (?, ?, ?, ?, ?, ?, 0)
ON CONFLICT(node_id) DO UPDATE SET
    display_name = excluded.display_name,
    platform = excluded.platform`,
		node.NodeID, node.DisplayName, node.Platform, node.PublicKey, node.Fingerprint,
		node.PairedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("trust node %q: %w", node.NodeID, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit trust update: %w", err)
	}
	return nil
}

// RevokeNode withdraws trust, removes every session grant the node held, and
// discards the presence snapshot it last sent.
//
// All three happen in one transaction. A node that is no longer trusted must
// not keep grants that would take effect again if it were paired a second
// time, and the last view it published must not stay on screen: trust is what
// made that view admissible, so withdrawing trust withdraws the view with it.
func (r *Registry) RevokeNode(ctx context.Context, nodeID string) error {
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin revoke: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	// Grants go first, and go whether or not a trust row exists. Returning
	// early on a missing row would leave a grant nobody can reach: revoking
	// says "not found" while the grant waits for the node to be paired.
	if _, err := transaction.ExecContext(ctx, `DELETE FROM session_audience WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("drop grants for %q: %w", nodeID, err)
	}
	// The peer's last snapshot goes with the grants, and for the same reason:
	// it is data this owner accepted only because the node was trusted.
	if _, err := transaction.ExecContext(ctx, `DELETE FROM peer_snapshots WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("drop peer snapshot for %q: %w", nodeID, err)
	}
	result, err := transaction.ExecContext(ctx, `DELETE FROM trusted_nodes WHERE node_id = ?`, nodeID)
	if err != nil {
		return fmt.Errorf("revoke node %q: %w", nodeID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read revoke result: %w", err)
	}
	if count == 0 {
		// Still commit: the grants above are gone, which is the part that
		// controls access.
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit revoke: %w", err)
		}
		return fmt.Errorf("node %q: %w", nodeID, ErrNotFound)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit revoke: %w", err)
	}
	return nil
}

func (r *Registry) TrustedNode(ctx context.Context, nodeID string) (TrustedNode, error) {
	var node TrustedNode
	var pairedMS, lastSeenMS int64
	err := r.db.QueryRowContext(ctx, `
SELECT node_id, display_name, platform, public_key, fingerprint, paired_at_ms, last_seen_at_ms, address
FROM trusted_nodes WHERE node_id = ?`, nodeID).
		Scan(&node.NodeID, &node.DisplayName, &node.Platform, &node.PublicKey, &node.Fingerprint,
			&pairedMS, &lastSeenMS, &node.Address)
	if errors.Is(err, sql.ErrNoRows) {
		return TrustedNode{}, fmt.Errorf("node %q: %w", nodeID, ErrNotFound)
	}
	if err != nil {
		return TrustedNode{}, fmt.Errorf("get trusted node %q: %w", nodeID, err)
	}
	node.PairedAt = time.UnixMilli(pairedMS).UTC()
	if lastSeenMS > 0 {
		node.LastSeenAt = time.UnixMilli(lastSeenMS).UTC()
	}
	return node, nil
}

func (r *Registry) TrustedNodes(ctx context.Context) ([]TrustedNode, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT node_id, display_name, platform, public_key, fingerprint, paired_at_ms, last_seen_at_ms, address
FROM trusted_nodes ORDER BY display_name, node_id`)
	if err != nil {
		return nil, fmt.Errorf("list trusted nodes: %w", err)
	}
	defer rows.Close()

	nodes := make([]TrustedNode, 0)
	for rows.Next() {
		var node TrustedNode
		var pairedMS, lastSeenMS int64
		if err := rows.Scan(&node.NodeID, &node.DisplayName, &node.Platform, &node.PublicKey,
			&node.Fingerprint, &pairedMS, &lastSeenMS, &node.Address); err != nil {
			return nil, fmt.Errorf("scan trusted node: %w", err)
		}
		node.PairedAt = time.UnixMilli(pairedMS).UTC()
		if lastSeenMS > 0 {
			node.LastSeenAt = time.UnixMilli(lastSeenMS).UTC()
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read trusted nodes: %w", err)
	}
	return nodes, nil
}

// SetNodeAddress records where a trusted peer currently answers.
//
// It never creates a row, for the same reason MarkNodeSeen does not: learning
// an address is not a trust decision. Anything that discovers addresses —
// an owner typing one, or mDNS filling it in — is an untrusted input source, and a
// discovery that could add rows here would let whatever is shouting on the
// network decide who this node believes in.
//
// An empty address is allowed and means "I no longer know where this peer is",
// which stops delivery without touching trust.
func (r *Registry) SetNodeAddress(ctx context.Context, nodeID, address string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE trusted_nodes SET address = ? WHERE node_id = ?`, address, nodeID)
	if err != nil {
		return fmt.Errorf("set address for %q: %w", nodeID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read address update for %q: %w", nodeID, err)
	}
	if count == 0 {
		return fmt.Errorf("node %q: %w", nodeID, ErrNotFound)
	}
	return nil
}

// TrustedNodeIDs returns just the ids of paired nodes.
//
// Discovery uses it to answer one question — is this announcement about someone
// we paired with — without being handed keys and addresses it has no business
// reading.
func (r *Registry) TrustedNodeIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT node_id FROM trusted_nodes ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("list trusted node ids: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan trusted node id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read trusted node ids: %w", err)
	}
	return ids, nil
}

// MarkNodeSeen records contact with a peer. It never creates a row: an unknown
// node making contact must not become trusted by making contact.
func (r *Registry) MarkNodeSeen(ctx context.Context, nodeID string, at time.Time) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE trusted_nodes SET last_seen_at_ms = ? WHERE node_id = ?`, at.UTC().UnixMilli(), nodeID)
	if err != nil {
		return fmt.Errorf("mark node %q seen: %w", nodeID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read mark seen result: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("node %q: %w", nodeID, ErrNotFound)
	}
	return nil
}

func validateTrustedNode(node TrustedNode) error {
	// One rule for node identifiers, shared with address parsing. Two rules
	// that disagree is how " node_x" gets trusted while "node_x" is the name
	// every other surface shows.
	if err := model.ValidateNodeID(node.NodeID); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSession, err)
	}
	if node.PublicKey == "" || node.Fingerprint == "" {
		return fmt.Errorf("%w: node %q has no key material", ErrInvalidSession, node.NodeID)
	}
	if node.DisplayName == "" || len(node.DisplayName) > 128 {
		return fmt.Errorf("%w: node %q has no usable display name", ErrInvalidSession, node.NodeID)
	}
	return nil
}
