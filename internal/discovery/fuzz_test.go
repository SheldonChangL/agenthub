package discovery

import (
	"net/netip"
	"testing"
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
		[]netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("2001:db8::1")})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
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
		}
	})
}
