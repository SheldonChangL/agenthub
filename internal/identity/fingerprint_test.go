package identity_test

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/identity"
)

// A fingerprint is what a person compares between two machines, so every way of
// writing one has to read as the same fingerprint — and anything that is not one
// has to be refused rather than carried as text that looks like one.
func TestAFingerprintIsHexadecimalOrItIsNothing(t *testing.T) {
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	canonical := identity.Fingerprint(public)
	if _, err := identity.ParseFingerprint(canonical); err != nil {
		t.Fatalf("the fingerprint this package produces does not parse: %v", err)
	}

	// Every spelling of one fingerprint is that fingerprint.
	for name, spelling := range map[string]string{
		"as produced":  canonical,
		"lower case":   strings.ToLower(canonical),
		"no spaces":    strings.ReplaceAll(canonical, " ", ""),
		"extra spaces": strings.ReplaceAll(canonical, " ", "  "),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := identity.ParseFingerprint(spelling)
			if err != nil {
				t.Fatalf("ParseFingerprint(%q) = %v", spelling, err)
			}
			if got != canonical {
				t.Errorf("ParseFingerprint(%q) = %q, want %q", spelling, got, canonical)
			}
		})
	}

	// And anything that is not a fingerprint is refused. The letter O is the
	// one that matters: it renders like a zero, so free text would let two rows
	// claim what looks like one key.
	for name, value := range map[string]string{
		"a letter O for a zero": "1223 O3EA 5E96 543A 2DD8 BFEA",
		"a letter l for a one":  "l223 03EA 5E96 543A 2DD8 BFEA",
		"prose":                 "trust me, this is fine",
		"too short":             "1223 03EA 5E96",
		"too long":              canonical + " 0000",
		"empty":                 "",
		"a newline":             "1223 03EA\n5E96 543A 2DD8 BFEA",
		// The boundaries of the hexadecimal range, either side.
		"a G":                  "1223 G3EA 5E96 543A 2DD8 BFEA",
		"a g":                  "1223 g3EA 5E96 543A 2DD8 BFEA",
		"a slash":              "1223 /3EA 5E96 543A 2DD8 BFEA",
		"a colon":              "1223 :3EA 5E96 543A 2DD8 BFEA",
		"an at sign":           "1223 @3EA 5E96 543A 2DD8 BFEA",
		"a backtick":           "1223 `3EA 5E96 543A 2DD8 BFEA",
		"a tab for a space":    "1223\t03EA 5E96 543A 2DD8 BFEA",
		"a non-breaking space": "1223\u00a003EA 5E96 543A 2DD8 BFEA",
		"a fullwidth digit":    "1223 ０3EA 5E96 543A 2DD8 BFEA",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := identity.ParseFingerprint(value); err == nil {
				t.Errorf("ParseFingerprint(%q) = %q, want an error", value, got)
			}
		})
	}
}
