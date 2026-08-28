package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	return mux
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
		writeError(w, http.StatusInternalServerError, "DISCOVERY_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListSessions(r.Context(), registry.ListOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "REGISTRY_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
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
		writeError(w, http.StatusInternalServerError, "HEARTBEAT_FAILED", err.Error())
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
		writeError(w, http.StatusBadRequest, "MESSAGE_REJECTED", err.Error())
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
		writeError(w, http.StatusInternalServerError, "REGISTRY_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

func decodeJSON(r *http.Request, destination any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeRegistryError(w http.ResponseWriter, err error) {
	if errors.Is(err, registry.ErrNotFound) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	if strings.Contains(err.Error(), "invalid") {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "REGISTRY_ERROR", err.Error())
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
