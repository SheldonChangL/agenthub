package label

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Printable is a normaliser as much as a judge, and callers depend on both
// halves: the announcement stores what it returns, and this node's own name is
// stored in that form so what an owner reads is what the network sees.
func TestPrintableNormalisesRatherThanOnlyRefusing(t *testing.T) {
	for typed, want := range map[string]string{
		"café mac":         "café mac",   // NFD, which is what macOS hands out
		"☕️ mac":            "☕ mac",      // the variation selector an emoji picker adds
		"my  mac":           "my mac",     // a run of spaces
		"laptop mac":        "laptop mac", // a non-breaking space
		"　mac":              "mac",        // an ideographic space, then trimmed
		"Sheldon's MacBook": "Sheldon's MacBook",
		"辦公室的 MacBook Pro":  "辦公室的 MacBook Pro",
		"lab-top":           "lab-top",
	} {
		if got := Printable(typed); got != want {
			t.Errorf("Printable(%q) = %q, want %q", typed, got, want)
		}
	}
}

// Idempotence is load-bearing, not incidental. Both callers store what Printable
// returns and then hand that back to it on the next start; a value that changed
// on the second pass would be rewritten on every boot, and — while the rule was
// equality rather than normalisation — refused as its own suggested form.
func TestPrintableIsIdempotent(t *testing.T) {
	for _, value := range []string{
		"café mac", "☕️ mac", "my  mac", "laptop mac",
		"Sheldon's MacBook", "辦公室的 MacBook Pro", "न्‍न",
		"\U0001f468‍\U0001f4bb desk", "Ⅻ mac", "ＭacBook", "㍿ mac",
		strings.Repeat("三", 21), strings.Repeat("a", MaxLength),
	} {
		once := Printable(value)
		if once == "" {
			continue
		}
		if twice := Printable(once); twice != once {
			t.Errorf("Printable(%q) = %q, and again = %q; the second pass changed it",
				value, once, twice)
		}
	}
}

// What Printable refuses, and why each one is refused rather than shown.
func TestPrintableRefusesWhatWouldNotReachAReader(t *testing.T) {
	for what, value := range map[string]string{
		"empty":                   "",
		"over the bound":          strings.Repeat("a", MaxLength+1),
		"one CJK character over":  strings.Repeat("三", 22),
		"only spaces":             "   ",
		"invalid UTF-8":           "\xff\xfe",
		"a control character":     "lab\x07top",
		"a newline":               "a\r\nb",
		"an ANSI escape":          "\x1b[31mred",
		"zero-width spaces":       "​​​",
		"a braille blank":         "⠀⠀",
		"right-to-left override":  "safe‮gnp.exe",
		"a left-to-right mark":    "‎",
		"an isolate":              "⁦nested⁩",
		"only combining marks":    "́̂",
		"a zero-width non-joiner": "mac‌book",
	} {
		if got := Printable(value); got != "" {
			t.Errorf("Printable(%s = %q) = %q, want it refused", what, value, got)
		}
	}
}

// Everything Printable returns is something an announcement can carry, whatever
// it was given. Normalisation can lengthen a string, so the bound has to be
// applied to the output and not only to the input.
func TestPrintableAlwaysReturnsSomethingAnnounceable(t *testing.T) {
	// A name that fits going in and not coming out. Without one the test was
	// vacuous for the property it is named after: every long fixture was
	// refused by the input check and skipped, and deleting the output bound
	// left this package green.
	if grown := Printable(strings.Repeat("㍿", 6)); grown != "" {
		t.Errorf("Printable(%d bytes that normalise to %d) = %q, want it refused on the way out",
			len(strings.Repeat("㍿", 6)), len("株式会社")*6, grown)
	}
	if fits := Printable(strings.Repeat("㍿", 5)); fits == "" {
		t.Error("the pair is not discriminating: the shorter of the two was refused as well")
	}

	for _, value := range []string{
		// The point of this test, and it needs an input that survives the check
		// on the way in and crosses the bound on the way out. ㍿ is three bytes
		// and normalises to 株式会社, twelve: six of them is 18 in and 72 out.
		// Five is 15 in and 60 out and is accepted, which is what makes the
		// pair discriminating rather than the whole range being refused early.
		strings.Repeat("㍿", 6), strings.Repeat("㍿", 5),
		strings.Repeat("½", 8), strings.Repeat("Ⅻ", 7),
		strings.Repeat("㍿", 30), strings.Repeat("½", 40), strings.Repeat("Ⅻ", 30),
		strings.Repeat("a", MaxLength), strings.Repeat("三", 21),
		"\U0001f468‍\U0001f4bb\U0001f468‍\U0001f4bb", "café mac",
	} {
		got := Printable(value)
		if got == "" {
			continue
		}
		if len(got) > MaxLength {
			t.Errorf("Printable(%q) = %d bytes, over the %d an announcement carries",
				value, len(got), MaxLength)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Printable(%q) = %q, which is not valid UTF-8", value, got)
		}
	}
}

// Announceable's two failures have different remedies, so they have to read
// differently. Every non-length refusal used to report a length.
func TestAnnounceableSaysWhichFailureItIs(t *testing.T) {
	if _, err := Announceable(""); err == nil {
		t.Error("an empty name was accepted")
	}

	long := strings.Repeat("三", 22)
	_, err := Announceable(long)
	if err == nil {
		t.Fatalf("a %d-byte name was accepted", len(long))
	}
	if !strings.Contains(err.Error(), "66 bytes") || !strings.Contains(err.Error(), "64") {
		t.Errorf("a name refused for its length does not say so: %v", err)
	}

	_, err = Announceable("mac‌book")
	if err == nil {
		t.Fatal("a name that renders as nothing was accepted")
	}
	if strings.Contains(err.Error(), "bytes") {
		t.Errorf("a name refused for what is in it complains about its length: %v", err)
	}

	// And an acceptable name comes back in the form that goes out.
	got, err := Announceable("café  mac")
	if err != nil {
		t.Fatalf("Announceable() error = %v", err)
	}
	if got != "café mac" {
		t.Errorf("Announceable() = %q, want the announced form", got)
	}
}
