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
	// PeerListenWithdrawn says the running peerListen is not a value anybody
	// chose: this node started with allowLan off beside a peer listener it
	// could not serve, so the listener was withdrawn to the default and that
	// default was stored.
	//
	// Absent unless it happened, and absent again from the next start, where
	// the stored default is an ordinary remembered value. It exists because
	// sources cannot say this: the three provenances are a contract a desktop
	// already reads, and "default" is the only honest one of them for a value
	// nobody asked for — but on its own it reads as "nothing is stored", which
	// would have `ah settings` and a settings page print a value that is very
	// much in the database as though it were not.
	PeerListenWithdrawn bool `json:"peerListenWithdrawn,omitempty"`
	// Message is the human sentence a GUI can show verbatim: what a write did,
	// or why the running configuration is not the one that was remembered.
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

// WithPeerListenWithdrawn records that this start withdrew the peer listener it
// had remembered, so the answers can say why peerListen reads as a default.
//
// Its own option rather than a fourth argument to WithNodeSettings, because it
// is a fact about how this start went rather than part of the configuration,
// and a node that did not withdraw anything simply does not pass it.
func WithPeerListenWithdrawn() Option {
	return func(s *Server) {
		s.peerListenWithdrawn = true
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
	// Read, validated and written inside one transaction, so what was
	// validated is what gets stored: two writes arriving together would
	// otherwise each check against the state before the other and commit a
	// combination nobody validated, which the next start refuses.
	// The address the withdrawal took away, kept because the message names it:
	// the owner asked about allowLan and the listener moved, and a sentence
	// that does not say which address was given up leaves them to guess what
	// this node was serving.
	var withdrawnFrom string
	// What this write leaves in effect, which is what a refusal has to be
	// explained in terms of — see explainRefusal.
	var effective nodeconfig.Settings
	var invalid error
	saved, err := s.store.UpdateNodeSettings(r.Context(),
		func(stored nodeconfig.Partial) (nodeconfig.Partial, error) {
			// Validated as a whole, and against what the next start will use
			// rather than against what is running now. These two differ, and
			// the saved one is the configuration this write is part of:
			// withdrawing a declared range while a saved peer listener depends
			// on it is exactly the combination that starts nothing, and
			// checking it against a running loopback listener would wave it
			// through.
			//
			// As a whole, because a peer listener and the ranges that make it
			// private are one decision: a field checked on its own accepts a
			// LAN address while allowLan stays false.
			next, _ := nodeconfig.Resolve(nodeconfig.Partial{}, stored, nodeconfig.DefaultSettings())
			writing, closed := withdrawLANListener(requested, next)
			effective = writing.Apply(next)
			if _, err := effective.Validate(); err != nil {
				invalid = err
				return nodeconfig.Partial{}, err
			}
			withdrawnFrom = ""
			if closed {
				withdrawnFrom = next.PeerListen
			}
			return writing, nil
		})
	if invalid != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", explainRefusal(requested, effective, invalid))
		return
	}
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	message := "saved; these take effect when the node next starts (ah service restart)"
	if withdrawnFrom != "" {
		// The same reason the node's own start-up log gives for the same
		// withdrawal, from the same function, with both addresses named: the
		// one given up and the one now in its place.
		message = "saved; " + nodeconfig.WithdrawalReason(nodeconfig.SettingAllowLAN, withdrawnFrom) +
			"; peerListen went back to " + nodeconfig.DefaultPeerListen +
			". These take effect when the node next starts (ah service restart)"
	}
	writeJSON(w, http.StatusOK, s.settingsView(saved, message))
}

// withdrawLANListener takes the peer listener off the network when the allowLan
// this write leaves in effect is off and nothing else was said about it.
//
// Without this, closing the switch is refused on exactly the nodes that need
// it closed: a saved peerListen of 192.168.1.10:7463 makes {"allowLan": false}
// an invalid configuration, and the refusal tells the owner to pass -allow-lan
// — the flag they are trying to turn off. The two fields are one decision, and
// when they conflict the safe direction is the only one: stop serving the
// network. Widening is never done here; a LAN address still has to be asked
// for.
//
// Both fields are written in the same transaction, so no start can see the
// half of this that serves a network address with allowLan off.
func withdrawLANListener(requested nodeconfig.Partial, next nodeconfig.Settings) (nodeconfig.Partial, bool) {
	// The switch this decision is about is the one that will be in effect after
	// this write, not the one this write happens to mention. A request that
	// says nothing about allowLan inherits the saved answer, and if that answer
	// is already "off" beside a saved LAN listener, then this write is landing
	// on a configuration the next start cannot run — the same configuration the
	// start-up path silently withdraws. Reading `requested.AllowLAN` here
	// instead made the two routes disagree: the PUT refused what the start
	// quietly repaired.
	allowLAN := next.AllowLAN
	if requested.AllowLAN != nil {
		allowLAN = *requested.AllowLAN
	}
	if allowLAN {
		return requested, false
	}
	// The rule itself lives in nodeconfig, because the node's own start-up
	// applies the same one: see nodeconfig.WithdrawPeerListen.
	address, withdrawn := nodeconfig.WithdrawPeerListen(
		allowLAN, requested.PeerListen != nil, next.PeerListen)
	if !withdrawn {
		return requested, false
	}
	requested.PeerListen = &address
	return requested, true
}

// explainRefusal answers the caller in the terms of the configuration this
// write would leave behind.
//
// The validator speaks to a command line: its refusal of a LAN listener ends
// "pass -allow-lan to serve paired peers on this network" — a flag, and on the
// write that closes that very switch it reads as the API contradicting the
// request. A caller who sent a LAN address needs to be told the two fields are
// one decision, and which one to send differently.
//
// Judged on `effective` — allowLan as it stands after this write — rather than
// on what the request happened to mention, which is the same value the
// withdrawal is decided on. A request that names only a LAN peerListen against
// a stored `allowLan: false` says nothing about the switch and is refused by
// it; explained from the request alone, that refusal fell back to the flag
// wording about a switch the caller never touched. The validator's own message
// is kept after the colon: it is the node's, and it names the address.
func explainRefusal(requested nodeconfig.Partial, effective nodeconfig.Settings, err error) string {
	if !effective.AllowLAN && requested.PeerListen != nil &&
		nodeconfig.ValidateLoopback(*requested.PeerListen) != nil {
		return "allowLan is off, so peerListen has to be a loopback address, and it was sent as " +
			*requested.PeerListen + "; to serve that address, send allowLan true in the same write: " +
			err.Error()
	}
	return err.Error()
}

// withdrawalStands reports whether what this start withdrew is still what the
// database holds.
//
// The withdrawal is a fact about this process, but the sentence it justifies is
// about the stored configuration — "…and 127.0.0.1:7463 was stored in its
// place" — and that stays true only until somebody writes. An owner who turns
// allowLan back on and saves a LAN listener has undone the withdrawal, in this
// same process, through this same endpoint; going on saying the default was
// stored would contradict the `saved` block of the very response carrying it.
//
// So the fact expires with its subject: either of the two halves moving away
// from what the withdrawal left behind ends it, and the boolean goes with the
// sentence, because a desktop rendering its own wording from the flag would
// print the same stale claim.
func (s *Server) withdrawalStands(next nodeconfig.Settings) bool {
	return s.peerListenWithdrawn && !next.AllowLAN && next.PeerListen == nodeconfig.DefaultPeerListen
}

// withdrawnAtStart is the sentence a reader gets when nothing else was said.
//
// Only on a read: after a write, the message is about the write, and the
// boolean beside it still carries this fact for anything that wants to render
// it itself.
func withdrawnAtStart() string {
	return "peerListen reads as a default because it was withdrawn at start-up: allowLan is off, so the " +
		"address this node had remembered could not be served, and " + nodeconfig.DefaultPeerListen +
		" was stored in its place"
}

// settingsView renders the running configuration beside the saved one.
func (s *Server) settingsView(saved nodeconfig.Partial, message string) settingsResponse {
	// What the next start will resolve to: no flags but --db, the saved values,
	// then the defaults. The same rule the node applies, so this is a
	// prediction of the node's own behaviour rather than a second opinion.
	next, _ := nodeconfig.Resolve(nodeconfig.Partial{}, saved, nodeconfig.DefaultSettings())
	running := s.settings.settings
	withdrawn := s.withdrawalStands(next)
	if message == "" && withdrawn {
		message = withdrawnAtStart()
	}
	return settingsResponse{
		Settings:            withRanges(running),
		Sources:             s.settings.sources,
		Saved:               withRanges(next),
		RestartRequired:     !sameSettings(running, next),
		PeerListenWithdrawn: withdrawn,
		Message:             message,
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
