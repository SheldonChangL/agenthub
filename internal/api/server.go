package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/hub"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
	"agenthub.local/agenthub/internal/transport"
)

// maxBatchSessions bounds one batch so a single request cannot hold a write
// transaction open across an unbounded list.
const maxBatchSessions = 500

type Server struct {
	store      *registry.Registry
	hub        *hub.Hub
	heartbeats *protocol.HeartbeatBuilder
	node       model.NodeIdentity
	// deliveryPolicy is the same rule the publisher applies, so an address the
	// owner can save is an address that will actually be delivered to.
	deliveryPolicy func(string) error
	// peerLimiter throttles the peer surface by source address.
	peerLimiter *rateLimiter
}

// Option adjusts a Server at construction.
type Option func(*Server)

// WithDeliveryPolicy makes the API accept exactly the addresses the publisher
// will deliver to. The default is loopback only, matching a node that has not
// been told to serve peers on a network.
func WithDeliveryPolicy(policy func(string) error) Option {
	return func(s *Server) { s.deliveryPolicy = policy }
}

func NewServer(store *registry.Registry, service *hub.Hub, heartbeats *protocol.HeartbeatBuilder, node model.NodeIdentity, options ...Option) *Server {
	server := &Server{
		store: store, hub: service, heartbeats: heartbeats, node: node,
		deliveryPolicy: transport.LoopbackOnly,
		peerLimiter:    newRateLimiter(),
	}
	for _, option := range options {
		option(server)
	}
	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/discover", s.discover)
	mux.HandleFunc("GET /v1/sessions", s.listSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.getSession)
	mux.HandleFunc("PUT /v1/sessions/{id}/visibility", s.setVisibility)
	mux.HandleFunc("GET /v1/sessions/{id}/audience", s.getAudience)
	mux.HandleFunc("PUT /v1/sessions/{id}/audience", s.setAudience)
	mux.HandleFunc("POST /v1/sessions/audience", s.setAudienceBatch)
	mux.HandleFunc("GET /v1/node", s.getNode)
	mux.HandleFunc("GET /v1/nodes", s.listNodes)
	mux.HandleFunc("POST /v1/nodes", s.trustNode)
	mux.HandleFunc("DELETE /v1/nodes/{id}", s.revokeNode)
	mux.HandleFunc("PUT /v1/nodes/{id}/address", s.setNodeAddress)
	// GET /v1/heartbeat is the owner's preview of what this node would publish.
	// The peer-facing POST /v1/heartbeat and POST /v1/challenge deliberately do
	// not appear here: they live only on PeerHandler, so the management port has
	// no peer ingress at all rather than ingress nobody is expected to use.
	mux.HandleFunc("GET /v1/heartbeat", s.heartbeat)
	mux.HandleFunc("GET /v1/peers", s.listPeers)
	mux.HandleFunc("POST /v1/messages", s.sendMessage)
	mux.HandleFunc("GET /v1/inbox/{id}", s.inbox)
	mux.HandleFunc("DELETE /v1/inbox/{id}", s.clearInbox)
	mux.HandleFunc("DELETE /v1/inbox/{id}/{messageId}", s.deleteMessage)
	mux.HandleFunc("GET /v1/outbound/{id}", s.outboundStatus)
	return securityBoundary(mux)
}

// PeerHandler serves only what another node is allowed to reach.
//
// This is a separate surface from Handler, not a subset enforced by a check
// inside it, because the two answer to different people. Handler is the
// owner's: it changes who may see a session, revokes a peer, sends messages.
// PeerHandler is a stranger's, and a stranger must not be one routing rule away
// from PUT /v1/sessions/{id}/audience.
//
// Keeping them apart is what makes widening the peer listener a bounded
// decision. If both lived on one mux, opening a port for heartbeats would also
// open every management endpoint, and the only thing standing between a peer
// and the owner's controls would be that nobody had written the request.
func (s *Server) PeerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/challenge", s.answerChallenge)
	mux.HandleFunc("POST /v1/heartbeat", s.receiveHeartbeat)
	// The peer's message endpoint shares a path with the owner's, on a
	// different mux and with a different handler. The owner's takes a JSON
	// body and decides where the message goes; this one takes a signed
	// envelope from a paired node and queues it for a local session.
	mux.HandleFunc("POST /v1/messages", s.receiveMessage)
	// Rate limiting wraps only this surface. Every endpoint here answers an
	// unauthenticated caller — /v1/challenge signs on request, and both refuse
	// before knowing who is asking — so a throttle is the only thing bounding
	// what one host can cost. The owner API is loopback-only and belongs to
	// somebody who can already restart the process.
	return limitPeers(s.peerLimiter, securityBoundary(mux))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "discovery is not configured")
		return
	}
	result, err := s.hub.Discover(r.Context())
	if err != nil {
		writeInternalError(w, "DISCOVERY_FAILED", "discovery failed", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	page, err := positiveQueryInt(r, "page", 1, 1_000_000)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	pageSize, err := positiveQueryInt(r, "pageSize", 50, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	totalItems, err := s.store.CountSessions(r.Context(), false)
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	sessions, err := s.store.ListSessions(r.Context(), registry.ListOptions{Limit: pageSize, Offset: (page - 1) * pageSize})
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	totalPages := 0
	if totalItems > 0 {
		totalPages = (totalItems + pageSize - 1) / pageSize
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":   sessions,
		"pagination": map[string]int{"page": page, "pageSize": pageSize, "totalItems": totalItems, "totalPages": totalPages},
	})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id, ok := s.localSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	session, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// localSession resolves an address a caller wrote, in either form, to a session
// on this node.
//
// It writes the response and returns false when the address is malformed or
// names another node, so every session-addressed handler answers the same way.
func (s *Server) localSession(w http.ResponseWriter, raw string) (string, bool) {
	sessionID, err := protocol.ResolveLocal(raw, s.node.ID)
	switch {
	case err == nil:
		return sessionID, true
	case errors.Is(err, protocol.ErrUnknownNode):
		// The address is well formed; this node simply cannot reach it. Saying
		// so is more useful than reporting a bad request, and remote routing
		// does not exist yet.
		writeError(w, http.StatusNotFound, "UNKNOWN_NODE", err.Error())
	default:
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	}
	return "", false
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.TrustedNodes(r.Context())
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// trustNode records a peer whose fingerprint the owner has already compared.
//
// The comparison happens outside this call, on two screens. The API cannot do
// it and must not pretend to: it takes the fingerprint the caller saw and
// refuses if it does not match the key being trusted, so a mistyped or
// substituted key cannot slip through as verified.
func (s *Server) trustNode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		NodeID               string `json:"nodeId"`
		DisplayName          string `json:"displayName"`
		Platform             string `json:"platform"`
		PublicKey            string `json:"publicKey"`
		ConfirmedFingerprint string `json:"confirmedFingerprint"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if input.NodeID == s.node.ID {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "a node cannot pair with itself")
		return
	}

	public, err := identity.DecodePublicKey(input.PublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "public key is not a valid Ed25519 key")
		return
	}
	derived := identity.Fingerprint(public)
	if !fingerprintsMatch(derived, input.ConfirmedFingerprint) {
		// The caller believes it verified a different key than it sent.
		writeError(w, http.StatusBadRequest, "FINGERPRINT_MISMATCH",
			"the confirmed fingerprint does not belong to the supplied key")
		return
	}

	node := registry.TrustedNode{
		NodeID:      input.NodeID,
		DisplayName: input.DisplayName,
		Platform:    input.Platform,
		PublicKey:   identity.EncodePublicKey(public),
		Fingerprint: derived,
	}
	if err := s.store.TrustNode(r.Context(), node); err != nil {
		writeRegistryError(w, err)
		return
	}
	stored, err := s.store.TrustedNode(r.Context(), input.NodeID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

// fingerprintsMatch compares what a person read off two screens. Spacing and
// case are presentation, so they must not decide whether a pairing succeeds.
func fingerprintsMatch(derived, confirmed string) bool {
	normalize := func(value string) string {
		return strings.ToUpper(strings.Join(strings.Fields(value), ""))
	}
	return normalize(derived) == normalize(confirmed)
}

func (s *Server) revokeNode(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeNode(r.Context(), r.PathValue("id")); err != nil {
		writeRegistryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getAudience(w http.ResponseWriter, r *http.Request) {
	id, ok := s.localSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	audience, err := s.store.GetAudience(r.Context(), id)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, audience)
}

// audienceInput is decoded with DisallowUnknownFields, so a caller that means
// to grant nodes but misspells the field is refused rather than silently
// publishing to nobody — or, worse, to everyone.
type audienceInput struct {
	Mode           model.AudienceMode `json:"mode"`
	Nodes          []string           `json:"nodes"`
	ExportCWD      bool               `json:"exportCwd"`
	AcceptMessages bool               `json:"acceptMessages"`
}

func (i audienceInput) audience() model.Audience {
	return model.Audience{
		Mode:           i.Mode,
		Nodes:          i.Nodes,
		ExportCWD:      i.ExportCWD,
		AcceptMessages: i.AcceptMessages,
	}
}

func (s *Server) setAudience(w http.ResponseWriter, r *http.Request) {
	var input audienceInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	id, ok := s.localSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.store.SetAudience(r.Context(), id, input.audience()); err != nil {
		writeRegistryError(w, err)
		return
	}
	session, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// setAudienceBatch applies one policy to many sessions.
//
// It reports per-session outcomes instead of stopping at the first failure: a
// caller that asked to unpublish twenty sessions needs to know which nineteen
// succeeded, and a partial result reported as a total failure invites a retry
// that changes nothing.
func (s *Server) setAudienceBatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs      []string      `json:"ids"`
		Audience audienceInput `json:"audience"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if len(input.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "ids must contain at least one session")
		return
	}
	if len(input.IDs) > maxBatchSessions {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST",
			fmt.Sprintf("ids must contain at most %d sessions", maxBatchSessions))
		return
	}
	// Validate once before touching anything: an invalid policy should change
	// no session at all rather than the first few.
	if !model.ValidAudienceMode(input.Audience.Mode) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "mode must be none, all_paired or selected")
		return
	}

	audience := input.Audience.audience()
	type outcome struct {
		ID    string `json:"id"`
		Error string `json:"error,omitempty"`
	}
	results := make([]outcome, 0, len(input.IDs))
	changed := 0
	for _, raw := range input.IDs {
		id, err := protocol.ResolveLocal(raw, s.node.ID)
		if err != nil {
			results = append(results, outcome{ID: raw, Error: err.Error()})
			continue
		}
		if err := s.store.SetAudience(r.Context(), id, audience); err != nil {
			if errors.Is(err, registry.ErrInvalidSession) || errors.Is(err, registry.ErrNotFound) {
				results = append(results, outcome{ID: id, Error: err.Error()})
				continue
			}
			// The store is unavailable rather than the request being wrong;
			// continuing would report every remaining session as rejected.
			writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
			return
		}
		changed++
		results = append(results, outcome{ID: raw})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"changed": changed,
		"failed":  len(results) - changed,
		"results": results,
	})
}

func (s *Server) setVisibility(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Visibility model.Visibility `json:"visibility"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	id, ok := s.localSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.store.SetVisibility(r.Context(), id, input.Visibility); err != nil {
		writeRegistryError(w, err)
		return
	}
	session, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) getNode(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.node)
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	heartbeat, err := s.heartbeats.Build(r.Context(), time.Now().UTC())
	if err != nil {
		// The builder refuses to export anything unpublished, and says which
		// session and why. That detail identifies a private session, so it
		// belongs in the operator's log, not in a response body on the one
		// endpoint intended to face a network.
		writeInternalError(w, "HEARTBEAT_FAILED", "could not build heartbeat", err)
		return
	}
	writeJSON(w, http.StatusOK, heartbeat)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		To   string `json:"to"`
		From string `json:"from"`
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	destination, err := protocol.ParseAddress(input.To, s.node.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	// The sender label is stored and shown. Validating it now means it cannot
	// later be a free-text field that a remote sender fills in with anything.
	from := ""
	if input.From != "" {
		address, err := protocol.ParseAddress(input.From, s.node.ID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "from: "+err.Error())
			return
		}
		from = input.From
		if address.Local() {
			from = address.SessionID
		}
	}
	if !destination.Local() {
		s.queueForPeer(w, r, destination, from, input.Body)
		return
	}
	to := destination.SessionID
	// The destination node is recorded explicitly. Today it is always this
	// node — localSession refuses anything else — but the row must say where
	// the message was addressed rather than leaving it to be inferred from the
	// absence of a prefix, which stops being readable once routing exists.
	message, err := s.store.CreateMessage(r.Context(), model.Message{
		To: to, From: from, DestinationNodeID: s.node.ID, Body: input.Body,
	})
	if err != nil {
		// CreateMessage resolves the destination session, so a store failure
		// reaches here as readily as a bad request. Classifying by sentinel
		// keeps a database error from being reported as the caller's fault and
		// from carrying store detail into the response.
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, message)
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	recipient, ok := s.localSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	messages, err := s.store.Inbox(r.Context(), recipient, limit)
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	// How full the inbox is travels with its contents. The bound defers
	// incoming messages rather than destroying them, so a session that is
	// filling up is something the owner can act on — but only if they can see
	// it before senders start backing up.
	held, err := s.store.CountInbox(r.Context(), recipient)
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": messages,
		"held":     held,
		"capacity": registry.MaxInboxMessages,
		"full":     held >= registry.MaxInboxMessages,
	})
}

// deleteMessage removes one message the owner has finished with.
//
// The inbox is bounded, so there has to be a way to clear it: a bound with no
// release is a session that stops receiving the first time it fills. Deletion
// is explicit rather than inferred from reading, because nothing here tracks
// what has been read and guessing would throw away things the owner was not
// done with.
func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	recipient, ok := s.localSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.store.DeleteMessage(r.Context(), recipient, r.PathValue("messageId")); err != nil {
		writeRegistryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// clearInbox empties one session's inbox.
func (s *Server) clearInbox(w http.ResponseWriter, r *http.Request) {
	recipient, ok := s.localSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	removed, err := s.store.ClearInbox(r.Context(), recipient)
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func positiveQueryInt(r *http.Request, name string, defaultValue, maximum int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", name, maximum)
	}
	return parsed, nil
}

func securityBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
			writeError(w, http.StatusForbidden, "FORBIDDEN_ORIGIN", "browser origin is not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeJSON(r *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		// encoding/json names the destination Go type and field, which tells a
		// caller about this program rather than about their request.
		return errors.New("request body is not valid JSON for this endpoint")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeRegistryError(w http.ResponseWriter, err error) {
	if errors.Is(err, registry.ErrNotFound) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}
	if errors.Is(err, registry.ErrInvalidSession) {
		// Validation messages describe the caller's own input, so they are safe
		// to return and useful to have. Matching on a sentinel rather than on
		// the word "invalid" matters: a driver error such as "invalid syntax"
		// would otherwise be echoed back with the column and stored value.
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
}

// writeInternalError keeps server-side detail on the server.
//
// Internal errors here name absolute provider paths, the account's home
// directory, and private session IDs. The local API is loopback-only today, but
// it is the same listener a LAN mode would expose, and an error body is a poor
// place to learn what a machine is working on.
func writeInternalError(w http.ResponseWriter, code, message string, err error) {
	log.Printf("%s: %v", code, err)
	writeError(w, http.StatusInternalServerError, code, message)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
