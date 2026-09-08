package discovery

import (
	"net/netip"
	"testing"
	"unicode/utf8"
)

// FuzzParseAnnouncements guards the one place this package reads bytes an
// attacker chose. mDNS is UDP on a group anyone can write to, so every packet
// reaching the parser is hostile input by default.
//
// The property is deliberately weak — no panic, no hang, and nothing usable
// invented out of malformed bytes — because that is what the parser actually
// promises. It is not asserted that garbage yields no announcements at all: a
// packet can be well-formed DNS carrying a service record from something else
// entirely, and dropping it is the caller's job, not the parser's.
func FuzzParseAnnouncements(f *testing.F) {
	valid, err := buildAnnouncement("node_paired000000000", "agenthub-seed", 7463,
		[]netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("2001:db8::1")}, Offer{})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	// An offering seed as well: the fields a candidate carries are parsed by
	// the same code path and are the ones an attacker chooses.
	offeringSeed, err := buildAnnouncement("node_offering0000000", "agenthub-seed", 7463,
		[]netip.Addr{netip.MustParseAddr("192.168.1.42")},
		Offer{DisplayName: "laptop", Platform: "linux/amd64", Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA"})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(offeringSeed)
	f.Add(valid[:len(valid)/2])
	f.Add(valid[:12])
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add(make([]byte, 512))
	// A header claiming far more answers than the packet contains, which is the
	// shape that makes a naive parser read past the end.
	f.Add([]byte{0x00, 0x00, 0x84, 0x00, 0x00, 0x00, 0xff, 0xff, 0x00, 0x00, 0x00, 0x00})

	f.Fuzz(func(t *testing.T, packet []byte) {
		for _, announcement := range ParseAnnouncements(packet) {
			// Anything returned must be structurally usable; a half-formed
			// announcement would be applied against the trust store later.
			if announcement.NodeID == "" {
				t.Fatalf("parser returned an announcement with no node id: %#v", announcement)
			}
			if announcement.Address == "" {
				t.Fatalf("parser returned an announcement with no address: %#v", announcement)
			}
			if _, err := netip.ParseAddrPort(announcement.Address); err != nil {
				t.Fatalf("parser returned an unusable address %q: %v", announcement.Address, err)
			}
			// The three candidate fields reach a person's screen, so whatever
			// the packet said, what comes out has to be a label: valid UTF-8,
			// within the bound, and nothing that moves a cursor or renders as
			// nothing.
			for field, value := range map[string]string{
				"display name": announcement.DisplayName,
				"platform":     announcement.Platform,
				"fingerprint":  announcement.Fingerprint,
			} {
				if value == "" {
					continue
				}
				if len(value) > MaxCandidateFieldLength {
					t.Fatalf("%s is %d bytes, over the %d bound: %q", field, len(value), MaxCandidateFieldLength, value)
				}
				if !utf8.ValidString(value) {
					t.Fatalf("%s is not valid UTF-8: %q", field, value)
				}
				// Whatever the packet said, what comes out is what PRECIS
				// Nickname produces: idempotent under the profile, and with no
				// braille blank, which PRECIS itself admits.
				if again := printableField(value); again != value {
					t.Fatalf("%s is not already normalised: %q became %q", field, value, again)
				}
				for _, r := range value {
					if r == '\u2800' {
						t.Fatalf("%s carries a braille blank: %q", field, value)
					}
				}
			}
		}
	})
}
