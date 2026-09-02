package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"agenthub.local/agenthub/internal/address"
)

// ErrNotVisible marks an address this caller may not see.
//
// It is deliberately the same answer for "no such session", "that node is not
// paired" and "that session exists but its owner did not authorise this node".
// Distinguishing them would turn this tool into a way to find out what another
// machine is running, and what its owner has chosen to keep private, without
// ever being authorised to see it.
var ErrNotVisible = fmt.Errorf("unknown node or session")

// visible returns every session this caller may see.
//
// Local sessions come from the node's own list. Remote sessions come from
// presence and nowhere else: the node applied each peer's audience when it
// accepted that peer's heartbeat, so presence already IS the authorised view.
// Reading the registry for remote sessions would be a second implementation of
// that filter, and the two would eventually disagree.
func (s *server) visible(ctx context.Context) ([]Session, error) {
	local, err := s.client.LocalSessions(ctx)
	if err != nil {
		return nil, err
	}
	peers, err := s.client.Peers(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(local))
	sessions = append(sessions, local...)
	for _, peer := range peers {
		sessions = append(sessions, peer.Sessions...)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, nil
}

// find resolves one address within what this caller may see.
func (s *server) find(ctx context.Context, raw string) (Session, error) {
	parsed, err := address.ParseAddress(strings.TrimSpace(raw), s.nodeID)
	if err != nil {
		return Session{}, err
	}
	wanted := parsed.SessionID
	if !parsed.Local() {
		wanted = address.QualifiedID(parsed.NodeID, parsed.SessionID)
	}
	sessions, err := s.visible(ctx)
	if err != nil {
		return Session{}, err
	}
	for _, session := range sessions {
		if session.ID == wanted {
			return session, nil
		}
	}
	return Session{}, ErrNotVisible
}

// filter applies the optional narrowing agent_list accepts.
func filter(sessions []Session, provider, status, node string) []Session {
	out := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if provider != "" && !strings.EqualFold(session.Provider, provider) {
			continue
		}
		if status != "" && !strings.EqualFold(session.Status, status) {
			continue
		}
		if node != "" && session.Node != node {
			continue
		}
		out = append(out, session)
	}
	return out
}
