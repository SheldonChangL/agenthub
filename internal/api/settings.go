package api

import (
	"net/http"
	"slices"

	"agenthub.local/agenthub/internal/nodeconfig"
)

// The node's start-up settings, on the owner's surface.
//
// Two facts, deliberately both published. `settings` is what this process is
// running with right now; `saved` is what the next start will use. They differ
// the moment anything is written here, because every one of these settings is
// tied to a listener or an announcer built once at startup — there is no hot
// reload, and pretending otherwise would leave a desktop showing a node as
// LAN-visible while it is still serving loopback.
//
// No authentication, like every other endpoint on this listener: it is
// loopback-only, and whoever can reach it can already restart the process.

// settingsResponse is what both endpoints answer with.
type settingsResponse struct {
	// Settings is the configuration in effect in this process.
	Settings nodeconfig.Settings `json:"settings"`
	// Sources says where each effective value came from: "flag" (this command
	// line), "remembered" (saved earlier) or "default".
	Sources map[string]string `json:"sources"`
	// Saved is what the node will use when it next starts.
	Saved nodeconfig.Settings `json:"saved"`
	// RestartRequired says the two differ, so something written here is not
	// live yet.
	RestartRequired bool `json:"restartRequired"`
	// Message is the human sentence a GUI can show verbatim after a write.
	Message string `json:"message,omitempty"`
}

// effectiveSettings is what main resolved for this process.
type effectiveSettings struct {
	settings nodeconfig.Settings
	sources  map[string]string
}

// WithNodeSettings publishes the configuration this node started with.
//
// Given by main rather than read from the database, because the two can
// disagree: a flag on this start overrides what was stored, and an owner
// looking at a settings page has to be told which of the two they are seeing.
func WithNodeSettings(settings nodeconfig.Settings, sources map[string]string) Option {
	return func(s *Server) {
		s.settings = &effectiveSettings{settings: settings, sources: sources}
	}
}

func (s *Server) settingsUnavailable(w http.ResponseWriter) bool {
	if s.settings != nil {
		return false
	}
	// Refused rather than answered from the database alone: an answer that
	// could not say what this process is actually running with would be read
	// as "these are live", which is the one thing it would not know.
	writeError(w, http.StatusConflict, "SETTINGS_UNAVAILABLE",
		"this node was built without its start-up settings, so it cannot say what is in effect")
	return true
}

func (s *Server) getNodeSettings(w http.ResponseWriter, r *http.Request) {
	if s.settingsUnavailable(w) {
		return
	}
	saved, err := s.store.GetNodeSettings(r.Context())
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(saved, ""))
}

func (s *Server) setNodeSettings(w http.ResponseWriter, r *http.Request) {
	if s.settingsUnavailable(w) {
		return
	}
	var requested nodeconfig.Partial
	if err := decodeJSON(r, &requested); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if requested.Empty() {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST",
			"no settings given; send at least one of peerListen, allowLan, discover, treatAsPrivate, autoWake")
		return
	}
	stored, err := s.store.GetNodeSettings(r.Context())
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	// Validated as a whole, and against what the next start will use rather
	// than against what is running now. These two differ, and the saved one is
	// the configuration this write is part of: withdrawing a declared range
	// while a saved peer listener depends on it is exactly the combination
	// that starts nothing, and checking it against a running loopback listener
	// would wave it through.
	//
	// As a whole, because a peer listener and the ranges that make it private
	// are one decision: a field checked on its own accepts a LAN address while
	// allowLan stays false.
	next, _ := nodeconfig.Resolve(nodeconfig.Partial{}, stored, nodeconfig.DefaultSettings())
	if _, err := requested.Apply(next).Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if err := s.store.SaveNodeSettings(r.Context(), requested); err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	saved, err := s.store.GetNodeSettings(r.Context())
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(saved,
		"saved; these take effect when the node next starts (ah service restart)"))
}

// settingsView renders the running configuration beside the saved one.
func (s *Server) settingsView(saved nodeconfig.Partial, message string) settingsResponse {
	// What the next start will resolve to: no flags but --db, the saved values,
	// then the defaults. The same rule the node applies, so this is a
	// prediction of the node's own behaviour rather than a second opinion.
	next, _ := nodeconfig.Resolve(nodeconfig.Partial{}, saved, nodeconfig.DefaultSettings())
	running := s.settings.settings
	return settingsResponse{
		Settings:        withRanges(running),
		Sources:         s.settings.sources,
		Saved:           withRanges(next),
		RestartRequired: !sameSettings(running, next),
		Message:         message,
	}
}

// withRanges makes an empty declaration serialise as [] rather than null, so a
// reader never has to treat "no ranges" as two different values.
func withRanges(settings nodeconfig.Settings) nodeconfig.Settings {
	if settings.TreatAsPrivate == nil {
		settings.TreatAsPrivate = []string{}
	}
	return settings
}

func sameSettings(a, b nodeconfig.Settings) bool {
	return a.PeerListen == b.PeerListen && a.AllowLAN == b.AllowLAN &&
		a.Discover == b.Discover && a.AutoWake == b.AutoWake &&
		slices.Equal(a.TreatAsPrivate, b.TreatAsPrivate)
}
