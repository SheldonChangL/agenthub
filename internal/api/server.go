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
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

type Server struct {
	store      *registry.Registry
	hub        *hub.Hub
	heartbeats *protocol.HeartbeatBuilder
	node       model.NodeIdentity
}

func NewServer(store *registry.Registry, service *hub.Hub, heartbeats *protocol.HeartbeatBuilder, node model.NodeIdentity) *Server {
	return &Server{store: store, hub: service, heartbeats: heartbeats, node: node}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/discover", s.discover)
	mux.HandleFunc("GET /v1/sessions", s.listSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.getSession)
	mux.HandleFunc("PUT /v1/sessions/{id}/visibility", s.setVisibility)
	mux.HandleFunc("GET /v1/node", s.getNode)
	mux.HandleFunc("GET /v1/heartbeat", s.heartbeat)
	mux.HandleFunc("POST /v1/messages", s.sendMessage)
	mux.HandleFunc("GET /v1/inbox/{id}", s.inbox)
	return securityBoundary(mux)
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
	session, err := s.store.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) setVisibility(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Visibility model.Visibility `json:"visibility"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if err := s.store.SetVisibility(r.Context(), r.PathValue("id"), input.Visibility); err != nil {
		writeRegistryError(w, err)
		return
	}
	session, err := s.store.GetSession(r.Context(), r.PathValue("id"))
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
	message, err := s.store.CreateMessage(r.Context(), model.Message{To: input.To, From: input.From, Body: input.Body})
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
	messages, err := s.store.Inbox(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
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
