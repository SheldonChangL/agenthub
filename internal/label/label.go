package label

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/secure/precis"
	"golang.org/x/text/unicode/norm"
)

// MaxLength bounds each human-readable label that crosses the network.
//
// These reach a person's screen, and every one of them is chosen by whoever
// sent the packet — on a multicast group anyone on the network can write to.
// The presence path learned this the hard way (#76): an unbounded field is a
// place to write to the reader, not a label. A DNS TXT string cannot exceed 255
// bytes anyway; this is smaller because a display name that does not fit on a
// line is not a display name.
const MaxLength = 64

// Announceable returns the form of a name this node would actually
// announce, or an error saying why there is none.
//
// Normalise rather than refuse. Printable is a normaliser as much as a
// judge — NFD becomes NFC, a variation selector is dropped, runs of spaces
// collapse — and refusing everything that is not already its own output turns
// ordinary names into dead ends. macOS hands out NFD, and this whole change is
// about macOS: "café mac" typed there was refused with a message naming the
// same eight visible characters back, because the two differ only in bytes. The
// emoji case had no exit at all — the suggested form of "☕️" is one the macOS
// picker cannot produce.
//
// So the announceable form is what gets stored. What the owner sees in their
// own UI is then the string on the wire, which is the property that mattered;
// insisting they type it was never part of it.
//
// The bound is the announcement's, not the trust store's larger one. A peer's
// name is a label this node received and displays; this one is a label this
// node transmits, and the announcement drops any field Printable refuses
// — silently, because a TXT record has nowhere to report an error to.
func Announceable(name string) (string, error) {
	if name == "" {
		return "", errors.New("display name is required")
	}
	clean := Printable(name)
	if clean != "" {
		return clean, nil
	}
	// Two different failures, and a person can act on only one of them. Saying
	// "is 10 bytes, and must be at most 64" to someone whose name was refused
	// for a zero-width character reads as nonsense.
	if len(name) > MaxLength {
		return "", fmt.Errorf("display name %q is %d bytes; an announcement carries at most %d",
			name, len(name), MaxLength)
	}
	return "", fmt.Errorf(
		"display name %q cannot be announced: it has to contain characters that render, "+
			"and no control, invisible or direction-changing ones", name)
}

// Printable keeps a human-readable label only if it is one, and returns
// the normalised form.
//
// It lives in a package of its own, below both, because both sides of the
// exchange need the same rule: a peer's label is judged by it on the way
// in, and this node's own name has to satisfy it on the way out or the
// announcement carries no name at all.
//
// The rules are PRECIS Nickname (RFC 8266), which is the standard answer to
// exactly this question: what may a human-readable identifier chosen by someone
// else contain. Hand-rolling it went wrong twice here. unicode.IsGraphic admits
// U+FFFD, so arbitrary bytes read as printable. A deny list of invisible code
// points cannot be finished, and worse, the obvious hand-written rules delete
// real names: a cap on combining marks refuses Tibetan and pointed Hebrew, and
// refusing every zero-width character refuses Persian, which needs U+200C to
// spell ordinary words.
//
// PRECIS also normalises rather than merely judging: NBSP and the ideographic
// space become an ordinary space, NFD becomes NFC, runs of spaces collapse, and
// leading and trailing spaces go. That matters as much as the rejection, since
// "laptop" and "laptop " are two rows that look like one.
//
// It admits U+2800, the braille blank, which renders as nothing — so that one
// is still refused here.
//
// Two of its refusals are worked around because they cost real names. Variation
// selectors are Default_Ignorable and therefore disallowed, which is how ❤️, ☕️
// and every keycap are written — and, for CJK, which glyph of a character to
// draw; the selector is dropped and the character kept. A ZWJ between
// pictographs violates a contextual rule, so a name that fails only for that is
// retried without the emoji joiners — the ones after a virama stay, because in
// Devanagari a joiner there is spelling and removing it writes a different
// conjunct. Tag-sequence flags are still refused, and accepted as a limitation.
//
// What this cannot do is stop a name that merely looks like another. A Cyrillic
// а in "lаptop" is a different letter, and no normalisation makes it the same
// one; mixed-script detection would, and is not done here. The fingerprint
// comparison in the handshake is what separates two rows that read alike.
func Printable(value string) string {
	if len(value) == 0 || len(value) > MaxLength {
		return ""
	}
	// Before anything else: strings.Map below replaces an invalid byte with
	// U+FFFD, which PRECIS then accepts because it is a symbol. Checking here
	// keeps arbitrary bytes from being laundered into a valid label.
	if !utf8.ValidString(value) {
		return ""
	}
	// Variation selectors are Default_Ignorable, so PRECIS refuses them — and
	// they are how a phone or a Mac writes ❤️, ☕️, ⚠️ and every keycap. Dropping
	// the selector keeps the character, which is what the person typed.
	value = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Variation_Selector, r) {
			return -1
		}
		return r
	}, value)
	clean, err := precis.Nickname.String(value)
	if err != nil {
		// A ZWJ sequence — 👨‍💻 — violates a contextual rule. Joining is
		// presentation, so the sequence is retried without it rather than the
		// whole name being dropped. ZWNJ is left alone: in Persian it is not
		// presentation, it is spelling.
		if !strings.ContainsRune(value, '\u200d') {
			return ""
		}
		clean, err = precis.Nickname.String(stripEmojiJoiners(value))
		if err != nil {
			return ""
		}
	}
	if clean == "" {
		return ""
	}
	// A name of nothing but combining marks passes PRECIS and renders as the
	// same empty row the braille blank was refused for.
	if !hasBase(clean) {
		return ""
	}
	// Normalisation can lengthen a string, so the bound is applied to what will
	// actually be stored and shown.
	if len(clean) > MaxLength {
		return ""
	}
	for _, r := range clean {
		if r == brailleBlank {
			return ""
		}
	}
	return clean
}

// stripEmojiJoiners removes the zero-width joiners that only join emoji.
//
// A ZWJ after a virama is not presentation: in Devanagari it selects the
// half-form, and removing it spells a different conjunct. A ZWJ between
// pictographs is presentation, and PRECIS refuses it under a contextual rule,
// so that one goes and the name survives.
func stripEmojiJoiners(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	var previous rune
	for _, r := range value {
		if r == '\u200d' && !isVirama(previous) {
			continue
		}
		out.WriteRune(r)
		previous = r
	}
	return out.String()
}

// isVirama reports a combining mark with canonical combining class 9, which is
// what makes a following joiner part of the spelling rather than of the
// rendering.
func isVirama(r rune) bool {
	return norm.NFD.PropertiesString(string(r)).CCC() == viramaCombiningClass
}

const viramaCombiningClass = 9

// hasBase reports whether a value has anything for its marks to attach to.
// Combining marks alone are a row with no visible name.
func hasBase(value string) bool {
	for _, r := range value {
		if !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Me, r) && !unicode.Is(unicode.Mc, r) {
			return true
		}
	}
	return false
}

// brailleBlank renders as nothing and PRECIS admits it: it is a symbol, so it
// is neither a space nor a control character by any classification. A name made
// of these looks empty, or looks exactly like the row above it.
const brailleBlank = '\u2800'
