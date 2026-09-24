package nodeconfig

import (
	"fmt"
	"strings"
)

// The node's start-up configuration, and the rules for deciding what it is.
//
// These five settings used to live only in flags, which meant they also lived
// in whatever launchd or systemd unit `ah service install` wrote — a second
// copy of the truth that could only be changed by reinstalling the service.
// They are remembered in the database instead, the way a chosen display name
// already was: a flag given once records itself and applies on every later
// start, and a flag left off means "whatever I said last time".
//
// -listen is deliberately not one of them. The owner's API has no
// authentication and is safe only because it is loopback; a setting that could
// move it is a setting a mistaken write could open. It stays a flag, validated
// on every start.

// Field names. These are the JSON names the owner's API uses and the keys the
// database stores, spelled once so the three cannot drift apart.
const (
	SettingPeerListen     = "peerListen"
	SettingAllowLAN       = "allowLan"
	SettingDiscover       = "discover"
	SettingTreatAsPrivate = "treatAsPrivate"
	SettingAutoWake       = "autoWake"
)

// SettingNames lists every field, in the order a reader should meet them.
var SettingNames = []string{
	SettingPeerListen, SettingAllowLAN, SettingDiscover, SettingTreatAsPrivate, SettingAutoWake,
}

// Where an effective value came from.
const (
	SourceFlag       = "flag"
	SourceRemembered = "remembered"
	SourceDefault    = "default"
)

// DefaultPeerListen is where the peer surface binds when nobody has said
// otherwise: loopback, serving nothing to the network.
const DefaultPeerListen = "127.0.0.1:7463"

// DefaultOwnerListen is the owner's API address. Not a setting — it is here so
// the node and `ah service install` validate the same default.
const DefaultOwnerListen = "127.0.0.1:7462"

// Settings is one complete configuration: every field has a value.
//
// The peer listener is spelled twice. PeerListens is the list of addresses the
// node serves peers on; PeerListen is its first entry, kept because every
// reader written before the list existed — an older window, an older `ah`, a
// script — reads that one field and nothing else. They are one setting, and
// every function in this package moves them together (ADR-005 §1).
type Settings struct {
	PeerListen     string   `json:"peerListen"`
	PeerListens    []string `json:"peerListens"`
	AllowLAN       bool     `json:"allowLan"`
	Discover       bool     `json:"discover"`
	TreatAsPrivate []string `json:"treatAsPrivate"`
	AutoWake       bool     `json:"autoWake"`
}

// DefaultSettings is what a node with an empty database does, which is what
// every build before these settings existed did: loopback only, no discovery,
// no waking, nothing declared private.
func DefaultSettings() Settings {
	return Settings{
		PeerListen: DefaultPeerListen, PeerListens: []string{DefaultPeerListen}, TreatAsPrivate: nil,
	}
}

// Partial is a configuration where each field may be absent.
//
// Absent and false are different facts and both have to be expressible: a
// remembered `allowLan: false` is an answer somebody gave, while no entry at
// all means the default applies. Pointers say that; a plain bool cannot.
type Partial struct {
	PeerListen *string `json:"peerListen,omitempty"`
	// PeerListens is the whole list of peer addresses, replaced as a whole like
	// TreatAsPrivate. PeerListen alone means a list of one — see
	// NormalizePeerListen for why that is the rule and not a convenience.
	PeerListens *[]string `json:"peerListens,omitempty"`
	AllowLAN    *bool     `json:"allowLan,omitempty"`
	Discover    *bool     `json:"discover,omitempty"`
	// TreatAsPrivate is replaced as a whole when present, never appended to:
	// a declaration says which networks the owner believes are private, and
	// half a declaration is not a smaller belief, it is a different one.
	TreatAsPrivate *[]string `json:"treatAsPrivate,omitempty"`
	AutoWake       *bool     `json:"autoWake,omitempty"`
}

// Empty reports whether nothing at all was given.
func (p Partial) Empty() bool {
	return p.PeerListen == nil && p.PeerListens == nil && p.AllowLAN == nil && p.Discover == nil &&
		p.TreatAsPrivate == nil && p.AutoWake == nil
}

// Apply overlays the fields that are present onto a complete configuration.
func (p Partial) Apply(base Settings) Settings {
	if list, ok := p.PeerListenList(); ok {
		base.PeerListen, base.PeerListens = list[0], list
	}
	if p.AllowLAN != nil {
		base.AllowLAN = *p.AllowLAN
	}
	if p.Discover != nil {
		base.Discover = *p.Discover
	}
	if p.TreatAsPrivate != nil {
		base.TreatAsPrivate = append([]string(nil), (*p.TreatAsPrivate)...)
	}
	if p.AutoWake != nil {
		base.AutoWake = *p.AutoWake
	}
	return base
}

// Overlay returns the fields of p, with those of newer replacing them.
func (p Partial) Overlay(newer Partial) Partial {
	// Both halves from newer, never one from each: a newer scalar laid over an
	// older list would leave the list naming addresses the scalar just closed.
	if list, ok := newer.PeerListenList(); ok {
		first := list[0]
		p.PeerListen, p.PeerListens = &first, &list
	}
	if newer.AllowLAN != nil {
		p.AllowLAN = newer.AllowLAN
	}
	if newer.Discover != nil {
		p.Discover = newer.Discover
	}
	if newer.TreatAsPrivate != nil {
		p.TreatAsPrivate = newer.TreatAsPrivate
	}
	if newer.AutoWake != nil {
		p.AutoWake = newer.AutoWake
	}
	return p
}

// Resolve decides the configuration this start will run with, and says where
// each value came from.
//
// A flag given now wins and is what the caller then records; a flag left off
// falls back to what was remembered, and only then to the default. The
// provenance is returned rather than logged here because it has two readers:
// the start-up log, and the owner's API, which a desktop uses to show whether
// -allow-lan is on because somebody typed it just now or because this machine
// has been remembering it since March.
func Resolve(given, remembered Partial, defaults Settings) (Settings, map[string]string) {
	sources := make(map[string]string, len(SettingNames))
	settings := remembered.Apply(defaults)
	settings = given.Apply(settings)
	for _, field := range SettingNames {
		switch {
		case givenHas(given, field):
			sources[field] = SourceFlag
		case givenHas(remembered, field):
			sources[field] = SourceRemembered
		default:
			sources[field] = SourceDefault
		}
	}
	return settings, sources
}

// givenHas reports whether one named field is present in a Partial.
func givenHas(p Partial, field string) bool {
	switch field {
	case SettingPeerListen:
		return p.PeerListen != nil || p.PeerListens != nil
	case SettingAllowLAN:
		return p.AllowLAN != nil
	case SettingDiscover:
		return p.Discover != nil
	case SettingTreatAsPrivate:
		return p.TreatAsPrivate != nil
	case SettingAutoWake:
		return p.AutoWake != nil
	}
	return false
}

// Validate applies the node's own start-up rules to a complete configuration
// and returns the private ranges it declares.
//
// One function so the answer the API gives when the owner saves a setting is
// the answer the node will give when it next starts. Two copies of this would
// let a desktop accept an address that then crashes the service on every
// restart, with the reason only in a log nobody is watching.
func (s Settings) Validate() (PrivateRanges, error) {
	ranges, err := ParsePrivateRanges(s.TreatAsPrivate)
	if err != nil {
		return nil, err
	}
	list := s.PeerListens
	if len(list) == 0 {
		// A configuration built before the list existed, or by a caller that
		// only knows the scalar: a list of one, which is what it always meant.
		list = []string{s.PeerListen}
	}
	if list[0] != s.PeerListen {
		return nil, fmt.Errorf("peer listener: peerListen %q is not the first of peerListens %q; "+
			"they are one setting, and peerListen is always the first entry", s.PeerListen, list)
	}
	if err := ValidatePeerListens(list, s.AllowLAN, ranges); err != nil {
		return nil, fmt.Errorf("peer listener: %w", err)
	}
	return ranges, nil
}

// Describe renders one setting and where its value came from, for the
// start-up log.
//
// Printed for every setting, every start, because -allow-lan is the only
// switch in this set that lets anything leave the machine and a remembered
// one is otherwise invisible: nothing on the command line would mention it.
func Describe(settings Settings, sources map[string]string) []string {
	values := map[string]string{
		SettingPeerListen:     strings.Join(settings.peerListenList(), ", "),
		SettingAllowLAN:       fmt.Sprintf("%t", settings.AllowLAN),
		SettingDiscover:       fmt.Sprintf("%t", settings.Discover),
		SettingTreatAsPrivate: strings.Join(settings.TreatAsPrivate, ", "),
		SettingAutoWake:       fmt.Sprintf("%t", settings.AutoWake),
	}
	if values[SettingTreatAsPrivate] == "" {
		values[SettingTreatAsPrivate] = "none"
	}
	lines := make([]string, 0, len(SettingNames))
	for _, field := range SettingNames {
		source := sources[field]
		if source == "" {
			source = SourceDefault
		}
		lines = append(lines, fmt.Sprintf("%s = %s (%s)", FlagName(field), values[field], source))
	}
	return lines
}

// FlagName is the command-line spelling of a setting, which is what an owner
// reading a log or an error has to type to change it.
func FlagName(field string) string {
	var out strings.Builder
	for _, r := range field {
		if r >= 'A' && r <= 'Z' {
			out.WriteByte('-')
			out.WriteRune(r + ('a' - 'A'))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// WithdrawPeerListens decides whether peer listeners have to come off the
// network because allowLan is off, and says which addresses are left.
//
// peerListens is the list that would otherwise be in effect; peerListenNamed
// says whether this request or this command line said anything about it.
// Every entry that is not loopback is dropped; what is left is kept in order,
// and a list left empty is the loopback default. The second result says
// whether anything was dropped, and the third names what was.
//
// The two fields are one decision, and Validate refuses a configuration that
// serves a LAN address with allowLan off. That refusal is right when the owner
// typed both halves in the same breath — they gave contradictory instructions
// and only they can say which one they meant. It is wrong in every other case,
// and wrong in the worst way: a node that remembered 192.168.1.10:7463 and is
// then started with -allow-lan=false refuses, the supervisor restarts it, and
// it refuses again, forever. The owner's API never comes up, so the
// `ah settings` rescue runs behind a listener the dead node is not serving.
//
// So when nothing at hand names the listener, the safe direction is the only
// one: stop serving the network. Widening is never done here — a LAN address
// still has to be asked for.
//
// One function because the owner's PUT and the node's own start-up have to
// answer this identically. The API predicting one thing and the node doing
// another is how a desktop comes to show a setting that does not survive a
// restart.
func WithdrawPeerListens(allowLAN, peerListenNamed bool, peerListens []string) ([]string, bool, []string) {
	if allowLAN || peerListenNamed {
		return nil, false, nil
	}
	kept := make([]string, 0, len(peerListens))
	var dropped []string
	for _, address := range peerListens {
		// A loopback listener is already off the network, whatever its port,
		// and an owner who chose that port did not ask for it to move.
		if ValidateLoopback(address) == nil {
			kept = append(kept, address)
			continue
		}
		dropped = append(dropped, address)
	}
	if len(dropped) == 0 {
		return nil, false, nil
	}
	if len(kept) == 0 {
		kept = []string{DefaultPeerListen}
	}
	return kept, true, dropped
}

// WithdrawalReason says why a remembered peer listener was not kept, spelled
// once so the node's start-up log and the owner's API cannot drift apart.
//
// It says only what is true of every address WithdrawPeerListen takes away.
// Not "a LAN address": the same rule withdraws one that does not parse at all,
// which is the right call — an unusable listener must not be bound and must
// not refuse every start forever — and a sentence that called it LAN would be
// false on exactly the node whose database was hand-edited.
//
// allowLANName is how the switch is spelled to this reader: the flag
// (allow-lan) in a log read beside a command line, the JSON field (allowLan)
// in an API answer. The rest is one string, in one place.
func WithdrawalReason(allowLANName string, withdrawn ...string) string {
	if len(withdrawn) == 1 {
		return fmt.Sprintf("%s is off, so the remembered peer listener %q is not one this node can serve",
			allowLANName, withdrawn[0])
	}
	quoted := make([]string, len(withdrawn))
	for index, address := range withdrawn {
		quoted[index] = fmt.Sprintf("%q", address)
	}
	return fmt.Sprintf("%s is off, so the remembered peer listeners %s are not ones this node can serve",
		allowLANName, strings.Join(quoted, ", "))
}
