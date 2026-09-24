package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// MaxNodeAddresses bounds how many addresses one trusted node is known by,
// preferred and alternates together (ADR-005 §4). A dead alternate costs a
// connection attempt in every delivery round, so the bound is what keeps a
// peer from making this node spend its whole budget on addresses that answer
// nothing.
const MaxNodeAddresses = 4

// readAlternates decodes the stored alternates and drops what cannot be one:
// an empty entry, a repeat, or the preferred address itself. The last is not
// hypothetical. An older build writes address and leaves alternate_addresses
// alone, so after a downgrade and an upgrade the preferred address it wrote
// can also be sitting in the list.
//
// A value that does not decode reads as no alternates rather than as an
// error: the preferred address is still good, and refusing to list the node
// would stop delivery over a column that only ever adds a second chance.
func readAlternates(preferred, stored string) []string {
	var decoded []string
	if err := json.Unmarshal([]byte(stored), &decoded); err != nil {
		return nil
	}
	return cleanAlternates(preferred, decoded)
}

// cleanAlternates removes empties, repeats and the preferred address, and
// keeps the order and at most MaxNodeAddresses-1 entries.
func cleanAlternates(preferred string, candidates []string) []string {
	seen := map[string]bool{"": true, preferred: true}
	var kept []string
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		kept = append(kept, candidate)
		if len(kept) == MaxNodeAddresses-1 {
			break
		}
	}
	return kept
}

// updateAddresses rewrites one node's addresses from what it holds now, in one
// transaction, so two writers cannot each read the old set and have the second
// write discard the first one's change.
func (r *Registry) updateAddresses(ctx context.Context, nodeID string,
	change func(preferred string, alternates []string) (string, []string)) error {
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin address update for %q: %w", nodeID, err)
	}
	defer func() { _ = transaction.Rollback() }()

	var preferred, stored string
	err = transaction.QueryRowContext(ctx,
		`SELECT address, alternate_addresses FROM trusted_nodes WHERE node_id = ?`, nodeID).
		Scan(&preferred, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("node %q: %w", nodeID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("read addresses for %q: %w", nodeID, err)
	}
	nextPreferred, nextAlternates := change(preferred, readAlternates(preferred, stored))
	nextAlternates = cleanAlternates(nextPreferred, nextAlternates)
	if nextPreferred == "" {
		// No preferred address means no address. Alternates alone would be a
		// peer that is located and unlocated at once, and an older build reads
		// only the preferred column.
		nextAlternates = nil
	}
	encoded, err := json.Marshal(nonNil(nextAlternates))
	if err != nil {
		return fmt.Errorf("encode addresses for %q: %w", nodeID, err)
	}
	if _, err := transaction.ExecContext(ctx,
		`UPDATE trusted_nodes SET address = ?, alternate_addresses = ? WHERE node_id = ?`,
		nextPreferred, string(encoded), nodeID); err != nil {
		return fmt.Errorf("set addresses for %q: %w", nodeID, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit address update for %q: %w", nodeID, err)
	}
	return nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// SetNodeAddress records where a trusted peer currently answers.
//
// It never creates a row, for the same reason MarkNodeSeen does not: learning
// an address is not a trust decision. Anything that discovers addresses —
// an owner typing one, or mDNS filling it in — is an untrusted input source, and a
// discovery that could add rows here would let whatever is shouting on the
// network decide who this node believes in.
//
// The address becomes the preferred one. The address it replaces is not
// forgotten but becomes the first alternate (ADR-005 §4): a machine that
// announced its Wi-Fi address has not stopped answering on its cable. The
// oldest alternate goes when that would make more than MaxNodeAddresses.
//
// An empty address is allowed and means "I no longer know where this peer is",
// which clears the alternates too and stops delivery without touching trust.
func (r *Registry) SetNodeAddress(ctx context.Context, nodeID, address string) error {
	return r.updateAddresses(ctx, nodeID, func(preferred string, alternates []string) (string, []string) {
		if address == "" {
			return "", nil
		}
		return address, append([]string{preferred}, alternates...)
	})
}

// SetNodeAddresses replaces every address a trusted peer is known by. The
// first is the preferred one and the rest are alternates, in the order given.
//
// Each address must be host:port and pass policy — the delivery policy, so
// that nothing is stored that the publisher would refuse to dial and the owner
// is not left looking at a configured peer that never receives anything.
// Repeats are dropped; more than MaxNodeAddresses distinct addresses is
// refused rather than cut, because which ones to keep is the owner's choice.
// An empty list clears them all, as an empty SetNodeAddress does.
func (r *Registry) SetNodeAddresses(ctx context.Context, nodeID string, addresses []string,
	policy func(address string) error) error {
	if policy == nil {
		return fmt.Errorf("%w: no delivery policy to check the addresses against", ErrInvalidSession)
	}
	distinct := make([]string, 0, len(addresses))
	seen := map[string]bool{}
	for _, address := range addresses {
		address = strings.TrimSpace(address)
		if address == "" {
			return fmt.Errorf("%w: an empty address; give host:port", ErrInvalidSession)
		}
		if seen[address] {
			continue
		}
		seen[address] = true
		if _, _, err := net.SplitHostPort(address); err != nil {
			return fmt.Errorf("%w: address %q must be host:port", ErrInvalidSession, address)
		}
		if err := policy(address); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidSession, err)
		}
		distinct = append(distinct, address)
	}
	if len(distinct) > MaxNodeAddresses {
		return fmt.Errorf("%w: %d addresses given; a node is known by at most %d",
			ErrInvalidSession, len(distinct), MaxNodeAddresses)
	}
	return r.updateAddresses(ctx, nodeID, func(string, []string) (string, []string) {
		if len(distinct) == 0 {
			return "", nil
		}
		return distinct[0], distinct[1:]
	})
}

// PromoteNodeAddress makes an alternate the preferred address, because it is
// the one that just answered; the preferred address it replaces becomes the
// first alternate.
//
// Only an address the node is already known by moves. The dial that found it
// working started from a set read earlier, and if the owner has replaced the
// set since, the promotion must not put back an address they removed. An
// address that is already preferred, or no longer known, changes nothing.
func (r *Registry) PromoteNodeAddress(ctx context.Context, nodeID, address string) error {
	return r.updateAddresses(ctx, nodeID, func(preferred string, alternates []string) (string, []string) {
		if address == "" || address == preferred {
			return preferred, alternates
		}
		for index, alternate := range alternates {
			if alternate != address {
				continue
			}
			rest := make([]string, 0, len(alternates))
			rest = append(rest, preferred)
			rest = append(rest, alternates[:index]...)
			rest = append(rest, alternates[index+1:]...)
			return address, rest
		}
		return preferred, alternates
	})
}
