package discovery

import (
	"agenthub.local/agenthub/internal/identity"
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/text/secure/precis"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/nodeconfig"
)

// fakeResolver records what discovery tried to write, so a test can assert on
// the attempt rather than only on the outcome.
type fakeResolver struct {
	trusted    []string
	stored     map[string]string
	writes     int
	trustReads int
	failWith   error
}

func newResolver(trusted ...string) *fakeResolver {
	return &fakeResolver{trusted: trusted, stored: map[string]string{}}
}

func (r *fakeResolver) TrustedNodeIDs(context.Context) ([]string, error) {
	r.trustReads++
	return r.trusted, nil
}

func (r *fakeResolver) SetNodeAddress(_ context.Context, nodeID, address string) error {
	r.writes++
	if r.failWith != nil {
		return r.failWith
	}
	r.stored[nodeID] = address
	return nil
}

func anyAddress(string) error { return nil }

const (
	pairedNode   = "node_paired000000000"
	unpairedNode = "node_stranger0000000"
)

// TestDiscoveryCannotCreateTrust is the property the whole package rests on.
//
// mDNS carries no authentication: anything on the network can claim any node id
// at any address. What makes that survivable is that discovery may only fill in
// an address for a node the owner already paired with. If it could add nodes,
// whatever shouts loudest on the network would decide who this node believes.
func TestDiscoveryCannotCreateTrust(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, anyAddress)

	applied, err := browser.Apply(context.Background(), Announcement{
		NodeID: unpairedNode, Address: "192.0.2.10:7463",
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if applied {
		t.Fatal("an unpaired node's announcement was applied")
	}
	if resolver.writes != 0 {
		t.Fatalf("the registry was written to %d times for an unpaired node", resolver.writes)
	}
}

func TestAPairedNodesAddressIsRecorded(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, anyAddress)

	applied, err := browser.Apply(context.Background(), Announcement{
		NodeID: pairedNode, Address: "192.0.2.10:7463",
	})
	if err != nil || !applied {
		t.Fatalf("Apply() = %v, %v", applied, err)
	}
	if got := resolver.stored[pairedNode]; got != "192.0.2.10:7463" {
		t.Fatalf("stored address = %q", got)
	}
}

// clockedBrowser returns a browser whose time the test controls, so cooldown
// behaviour is asserted rather than waited for.
func clockedBrowser(resolver Resolver, policy AddressPolicy, clock *time.Time) *Browser {
	browser := NewBrowser(resolver, policy)
	browser.now = func() time.Time { return *clock }
	return browser
}

// TestARepeatedAnnouncementIsNotRewritten keeps a peer announcing every few
// seconds from writing to the database every few seconds.
func TestARepeatedAnnouncementIsNotRewritten(t *testing.T) {
	resolver := newResolver(pairedNode)
	clock := time.Now().UTC()
	browser := clockedBrowser(resolver, anyAddress, &clock)
	announcement := Announcement{NodeID: pairedNode, Address: "192.0.2.10:7463"}

	for range 5 {
		if _, err := browser.Apply(context.Background(), announcement); err != nil {
			t.Fatal(err)
		}
	}
	if resolver.writes != 1 {
		t.Fatalf("writes = %d; an unchanged address must be written once", resolver.writes)
	}

	// A genuinely new address gets through once the cooldown has passed.
	clock = clock.Add(addressChangeCooldown + time.Second)
	if _, err := browser.Apply(context.Background(), Announcement{
		NodeID: pairedNode, Address: "192.0.2.11:7463",
	}); err != nil {
		t.Fatal(err)
	}
	if resolver.writes != 2 {
		t.Fatalf("writes = %d; a changed address must be recorded", resolver.writes)
	}
}

// TestFlappingAddressesCannotAmplifyWrites covers the attack the cooldown
// exists for: alternating between two acceptable addresses defeats the
// unchanged-address check, so without a cooldown every packet is a write and
// the peer stays pointed somewhere it is not.
func TestFlappingAddressesCannotAmplifyWrites(t *testing.T) {
	resolver := newResolver(pairedNode)
	clock := time.Now().UTC()
	browser := clockedBrowser(resolver, anyAddress, &clock)

	for i := range 100 {
		address := "192.0.2.10:7463"
		if i%2 == 1 {
			address = "192.0.2.11:7463"
		}
		if _, err := browser.Apply(context.Background(), Announcement{
			NodeID: pairedNode, Address: address,
		}); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(time.Second)
	}
	// 100 packets over 100 simulated seconds, with a 30s cooldown: a handful of
	// writes, not one per packet.
	if resolver.writes > 5 {
		t.Fatalf("writes = %d for 100 flapping announcements; the cooldown is not bounding them",
			resolver.writes)
	}
	if resolver.writes == 0 {
		t.Fatal("no write at all; the cooldown is refusing the first announcement too")
	}
}

// TestOneTrustReadPerPacket pins that the trust store is read once for a whole
// packet, not once per announcement. One datagram can carry hundreds of address
// records, and a full table read per record is a read amplifier for anyone able
// to send multicast.
func TestOneTrustReadPerPacket(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, anyAddress)

	announcements := make([]Announcement, 0, 200)
	for i := range 200 {
		announcements = append(announcements, Announcement{
			NodeID:  unpairedNode,
			Address: "192.0.2." + strconv.Itoa(i%250+1) + ":7463",
		})
	}
	if _, err := browser.ApplyAll(context.Background(), announcements); err != nil {
		t.Fatal(err)
	}
	if resolver.trustReads != 1 {
		t.Fatalf("trust store read %d times for one packet of %d announcements",
			resolver.trustReads, len(announcements))
	}
}

// TestAnAddressTheTransportWouldRefuseIsNotStored keeps discovery and delivery
// applying the same rule, so a located peer is one that can actually receive.
func TestAnAddressTheTransportWouldRefuseIsNotStored(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, nodeconfig.ValidateLoopback)

	applied, err := browser.Apply(context.Background(), Announcement{
		NodeID: pairedNode, Address: "192.0.2.10:7463",
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied || resolver.writes != 0 {
		t.Fatalf("a routable address was stored while the policy refuses it (applied=%v writes=%d)",
			applied, resolver.writes)
	}
}

func TestEmptyAnnouncementsAreIgnored(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, anyAddress)
	for _, announcement := range []Announcement{
		{NodeID: "", Address: "192.0.2.10:7463"},
		{NodeID: pairedNode, Address: ""},
		{},
	} {
		if applied, err := browser.Apply(context.Background(), announcement); err != nil || applied {
			t.Fatalf("Apply(%#v) = %v, %v", announcement, applied, err)
		}
	}
	if resolver.writes != 0 {
		t.Fatalf("writes = %d", resolver.writes)
	}
}

func TestAStoreFailureIsReported(t *testing.T) {
	resolver := newResolver(pairedNode)
	resolver.failWith = errors.New("database is closed")
	browser := NewBrowser(resolver, anyAddress)

	if _, err := browser.Apply(context.Background(), Announcement{
		NodeID: pairedNode, Address: "192.0.2.10:7463",
	}); err == nil {
		t.Fatal("a failed write was reported as success")
	}
}

// TestAnnouncementsSurviveTheWire builds a real packet and parses it back,
// so the two halves are checked against each other rather than against a
// hand-written fixture that could drift from both.
func TestAnnouncementsSurviveTheWire(t *testing.T) {
	packet, err := buildAnnouncement(pairedNode, "agenthub-test", 7463, []netip.Addr{
		netip.MustParseAddr("192.0.2.10"),
		netip.MustParseAddr("2001:db8::1"),
	}, Offer{})
	if err != nil {
		t.Fatalf("buildAnnouncement(, Offer{}) error = %v", err)
	}

	announcements := ParseAnnouncements(packet)
	if len(announcements) != 2 {
		t.Fatalf("announcements = %#v; want one per address", announcements)
	}
	addresses := map[string]bool{}
	for _, announcement := range announcements {
		if announcement.NodeID != pairedNode {
			t.Errorf("node id = %q; want the announced id", announcement.NodeID)
		}
		addresses[announcement.Address] = true
	}
	for _, want := range []string{"192.0.2.10:7463", "[2001:db8::1]:7463"} {
		if !addresses[want] {
			t.Errorf("missing %q in %v", want, addresses)
		}
	}
}

// TestHostilePacketsProduceNothing feeds the parser the shapes an attacker
// controls. None may panic, and none may yield an announcement.
func TestHostilePacketsProduceNothing(t *testing.T) {
	valid, err := buildAnnouncement(pairedNode, "agenthub-test", 7463,
		[]netip.Addr{netip.MustParseAddr("192.0.2.10")}, Offer{})
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"empty":                        {},
		"one byte":                     {0x00},
		"header only":                  valid[:12],
		"truncated mid-record":         valid[:len(valid)-5],
		"truncated by one byte":        valid[:len(valid)-1],
		"random bytes":                 []byte("this is not a DNS packet at all, not even close"),
		"all zeroes":                   make([]byte, 512),
		"all ones":                     bytesRepeat(0xff, 512),
		"header claiming many answers": append(append([]byte{}, 0x00, 0x00, 0x84, 0x00, 0x00, 0x00, 0xff, 0xff, 0x00, 0x00, 0x00, 0x00), make([]byte, 4)...),
	}
	for name, packet := range cases {
		t.Run(name, func(t *testing.T) {
			// The assertion is that this returns rather than panics or hangs.
			got := ParseAnnouncements(packet)
			for _, announcement := range got {
				if announcement.NodeID != "" && announcement.Address != "" {
					t.Fatalf("a hostile packet produced a usable announcement: %#v", announcement)
				}
			}
		})
	}
}

// TestAPacketWithoutANodeIDYieldsNothing covers a well-formed service record
// that simply does not identify itself: another protocol on the same group.
func TestAPacketWithoutANodeIDYieldsNothing(t *testing.T) {
	packet, err := buildAnnouncement("", "agenthub-test", 7463,
		[]netip.Addr{netip.MustParseAddr("192.0.2.10")}, Offer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseAnnouncements(packet); len(got) != 0 {
		t.Fatalf("announcements = %#v; a record with no node id must yield nothing", got)
	}
}

func TestBuildAnnouncementRefusesAnImpossiblePort(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 1 << 20} {
		if _, err := buildAnnouncement(pairedNode, "agenthub-test", port, nil, Offer{}); err == nil {
			t.Errorf("buildAnnouncement(port=%d, Offer{}) succeeded", port)
		}
	}
}

func bytesRepeat(value byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = value
	}
	return out
}

// A node offering to pair carries three more claims. They have to survive a
// real packet, because the alternative is a candidate list that is empty for a
// reason nobody can see.
func TestAnOfferSurvivesTheWire(t *testing.T) {
	packet, err := buildAnnouncement("node_offering0000000", "agenthub-test", 7463,
		[]netip.Addr{netip.MustParseAddr("192.168.1.42")},
		Offer{
			DisplayName: "sheldon's laptop",
			Platform:    "darwin/arm64",
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
		})
	if err != nil {
		t.Fatalf("buildAnnouncement() error = %v", err)
	}
	announcements := ParseAnnouncements(packet)
	if len(announcements) != 1 {
		t.Fatalf("announcements = %+v, want one", announcements)
	}
	got := announcements[0]
	if !got.Offering() {
		t.Error("an announcement with a fingerprint does not report itself as an offer")
	}
	for field, pair := range map[string][2]string{
		"node id":      {got.NodeID, "node_offering0000000"},
		"display name": {got.DisplayName, "sheldon's laptop"},
		"platform":     {got.Platform, "darwin/arm64"},
		"fingerprint":  {got.Fingerprint, "1223 03EA 5E96 543A 2DD8 BFEA"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", field, pair[0], pair[1])
		}
	}
}

// The public key must not travel. A key on a multicast group is a key an
// attacker can replace, and the fingerprint is only useful next to the key that
// arrives in the handshake.
func TestAnOfferCarriesNoPublicKey(t *testing.T) {
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded := identity.EncodePublicKey(public)
	packet, err := buildAnnouncement("node_offering0000000", "agenthub-test", 7463,
		[]netip.Addr{netip.MustParseAddr("192.168.1.42")},
		Offer{DisplayName: "laptop", Platform: "darwin/arm64", Fingerprint: identity.Fingerprint(public)})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(packet, []byte(encoded)) {
		t.Error("the announcement carries the public key")
	}
	if bytes.Contains(packet, public) {
		t.Error("the announcement carries the raw public key bytes")
	}
	// The fingerprint is there, and it is the one identity produces.
	if got := ParseAnnouncements(packet)[0].Fingerprint; got != identity.Fingerprint(public) {
		t.Errorf("fingerprint = %q, want %q", got, identity.Fingerprint(public))
	}
}

// Every field here reaches a person's screen and every one is chosen by whoever
// sent the packet. A newline in a display name is what turns one row of a
// candidate list into two, and the second row is the sender's.
func TestAHostileOfferCannotWriteToTheReader(t *testing.T) {
	for name, offer := range map[string]Offer{
		"a newline in the name": {
			DisplayName: "laptop\n\nnode_evil000000000  trusted",
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
		},
		"a line separator": {
			DisplayName: "laptop attacker",
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
		},
		"a right-to-left override": {
			DisplayName: "laptop‮gnp.exe",
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
		},
		"a name longer than the bound": {
			DisplayName: strings.Repeat("a", MaxCandidateFieldLength+1),
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
		},
	} {
		t.Run(name, func(t *testing.T) {
			packet, err := buildAnnouncement("node_hostile00000000", "agenthub-test", 7463,
				[]netip.Addr{netip.MustParseAddr("192.168.1.42")}, offer)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range ParseAnnouncements(packet) {
				for field, value := range map[string]string{
					"display name": got.DisplayName,
					"platform":     got.Platform,
					"fingerprint":  got.Fingerprint,
				} {
					if strings.ContainsAny(value, "\n\r  ‮") {
						t.Errorf("%s carries a character that moves the cursor: %q", field, value)
					}
					if len(value) > MaxCandidateFieldLength {
						t.Errorf("%s is %d bytes, over the %d bound", field, len(value), MaxCandidateFieldLength)
					}
				}
			}
		})
	}
}

// The parser refuses a fingerprint that is not one, rather than carrying it as
// text that looks like one. Checked through a packet this code did not build,
// which is where such a value comes from.
func TestAFingerprintThatIsNotOneIsNotAnOffer(t *testing.T) {
	for name, value := range map[string]string{
		"a letter O for a zero": "1223 O3EA 5E96 543A 2DD8 BFEA",
		"prose":                 "trust me",
		"too short":             "1223 03EA",
		"too long":              "1223 03EA 5E96 543A 2DD8 BFEA 0000",
	} {
		t.Run(name, func(t *testing.T) {
			packet := txtPacket(t, []string{"node=node_x000000000000", "fp=" + value, "name=laptop"})
			for _, got := range ParseAnnouncements(packet) {
				if got.Offering() {
					t.Errorf("a packet with fingerprint %q reads as an offer: %+v", value, got)
				}
				if got.Fingerprint != "" {
					t.Errorf("fingerprint = %q, want it dropped", got.Fingerprint)
				}
			}
		})
	}
	// And a real one, written the other way, parses to the canonical form.
	packet := txtPacket(t, []string{"node=node_x000000000000", "fp=122303ea5e96543a2dd8bfea"})
	got := ParseAnnouncements(packet)
	if len(got) != 1 || !got[0].Offering() {
		t.Fatalf("announcements = %+v", got)
	}
	if got[0].Fingerprint != "1223 03EA 5E96 543A 2DD8 BFEA" {
		t.Errorf("fingerprint = %q, want the canonical form", got[0].Fingerprint)
	}
}

// A node whose own fingerprint is not a label must not announce at all.
//
// Dropping it would announce this node's name and platform in a packet that
// reads as "not offering": it would believe it was discoverable for pairing,
// nobody would list it, and it would have leaked two labels for nothing.
func TestAnUnannounceableFingerprintIsRefusedNotDropped(t *testing.T) {
	for name, fingerprint := range map[string]string{
		"prose with a newline": "trust me\nthis is fine",
		"over the bound":       strings.Repeat("A", MaxCandidateFieldLength+1),
		"invisible":            "\u00a0\u3000",
		"invalid utf-8":        "\xff\xfe",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildAnnouncement("node_offering0000000", "agenthub-test", 7463,
				[]netip.Addr{netip.MustParseAddr("192.168.1.42")},
				Offer{DisplayName: "laptop", Platform: "linux/amd64", Fingerprint: fingerprint})
			if err == nil {
				t.Fatal("announced anyway")
			}
			if !strings.Contains(err.Error(), "fingerprint") {
				t.Errorf("the refusal does not say what was wrong: %v", err)
			}
		})
	}
}

// A label has to be one, and two labels that look the same have to compare the
// same. PRECIS Nickname is the rule; what matters here is which inputs it
// refuses, which it repairs, and which it leaves alone.
func TestALabelIsNormalisedOrRefused(t *testing.T) {
	refused := map[string]string{
		"a braille blank":             "⠀",
		"a braille blank inside":      "lap⠀top",
		"a hangul filler":             "ㅤ",
		"a zero-width space":          "lap\u200btop",
		"a right-to-left override":    "laptop\u202egnp.exe",
		"a combining grapheme joiner": "lap\u034ftop",
		"a soft hyphen":               "lap\u00adtop",
		"a newline":                   "lap\ntop",
		"invalid utf-8":               "lap\xff\xfetop",
		"nothing but spaces":          "     ",
		// PRECIS accepts these; they render as the same empty row the
		// braille blank was refused for.
		"nothing but combining marks": "\u0301\u0301\u0301",
		"one combining mark":          "\u0301",
		// Mn is not the only mark category: Mc is a spacing mark and Me an
		// enclosing one, and a name of either is as empty as a name of Mn.
		"nothing but a spacing mark":    "\u0903",
		"nothing but an enclosing mark": "\u20e3",
		"empty":                         "",
	}
	for name, value := range refused {
		t.Run("refused: "+name, func(t *testing.T) {
			if got := printableField(value); got != "" {
				t.Errorf("printableField(%q) = %q, want it dropped", value, got)
			}
		})
	}

	// Repaired rather than refused: these are how one name is written two ways,
	// and normalising them is what stops "laptop" and "laptop " being two rows
	// that look like one.
	repaired := map[string][2]string{
		"a non-breaking space": {"lap\u00a0top", "lap top"},
		"an ideographic space": {"lap\u3000top", "lap top"},
		"a trailing space":     {"laptop ", "laptop"},
		"a leading space":      {" laptop", "laptop"},
		"a double space":       {"lap  top", "lap top"},
		"decomposed":           {"cafe\u0301", "café"},
		// The selector is presentation. Refusing it refuses ❤️ and every
		// keycap, which is how a phone and a Mac write them.
		"a variation selector": {"laptop\ufe0e", "laptop"},
		"an emoji with one":    {"heart \u2764\ufe0f", "heart \u2764"},
		"a ZWJ sequence":       {"dev \U0001f468\u200d\U0001f4bb", "dev \U0001f468\U0001f4bb"},
	}
	for name, pair := range repaired {
		t.Run("repaired: "+name, func(t *testing.T) {
			if got := printableField(pair[0]); got != pair[1] {
				t.Errorf("printableField(%q) = %q, want %q", pair[0], got, pair[1])
			}
		})
	}

	// Names people have, in the scripts they use. Persian needs U+200C to spell
	// ordinary words and Tibetan stacks combining marks by design — a
	// hand-written rule against zero-width characters or mark stacks refuses
	// both, which is why this is PRECIS and not a hand-written rule.
	kept := []string{
		"sheldon's laptop", "build-server-2", "café", "雪登的筆電",
		"laptop 💻", "MacBook Pro (16-inch)", "linux/amd64",
		"لپ\u200cتاپ", "བསྒྲུབས", "שֶּׁ", "Ноутбук", "노트북",
	}
	for _, value := range kept {
		if got := printableField(value); got != value {
			t.Errorf("printableField(%q) = %q; a legitimate label was changed or dropped", value, got)
		}
	}
}

// Normalisation can make a value longer — U+3231 becomes three characters — so
// the bound has to be applied to what will be stored and shown, not to what
// arrived.
func TestTheBoundAppliesToTheNormalisedValue(t *testing.T) {
	// 30 bytes in, 50 out: under the bound before, over it after.
	// 21 characters at 3 bytes in, 3 characters at 5 bytes out: 63 bytes
	// becomes 105, so it is under the bound before normalisation and over it
	// after.
	compact := strings.Repeat("㈱", 21)
	if len(compact) > MaxCandidateFieldLength {
		t.Fatalf("the input is already over the bound at %d bytes; pick a shorter one", len(compact))
	}
	expanded, err := precis.Nickname.String(compact)
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded) <= MaxCandidateFieldLength {
		t.Fatalf("normalisation produced %d bytes; this test needs an input that grows past %d",
			len(expanded), MaxCandidateFieldLength)
	}
	if got := printableField(compact); got != "" {
		t.Errorf("printableField() = %q (%d bytes), over the %d bound after normalisation",
			got, len(got), MaxCandidateFieldLength)
	}
}

// Two spellings of one label have to compare equal, or an impersonator simply
// picks the other spelling.
func TestLabelsCompareByMeaningNotBytes(t *testing.T) {
	for name, pair := range map[string][2]string{
		"case":        {"Laptop", "laptop"},
		"composition": {"café", "cafe\u0301"},
		// macOS writes the machine name with U+2019; an impersonator would
		// send the ASCII one, and the two must not be different names.
		"a typographic apostrophe":          {"sheldon\u2019s laptop", "sheldon's laptop"},
		"a modifier apostrophe":             {"sheldon\u02bcs laptop", "sheldon's laptop"},
		"a hyphen":                          {"build\u2010server", "build-server"},
		"a non-breaking hyphen figure dash": {"build\u2012server", "build-server"},
		"an en dash":                        {"build\u2013server", "build-server"},
		"an em dash":                        {"build\u2014server", "build-server"},
		"a left single quote":               {"sheldon\u2018s laptop", "sheldon's laptop"},
		"a non-breaking hyphen":             {"build\u2011server", "build-server"},
		"a minus sign":                      {"build\u2212server", "build-server"},
	} {
		t.Run(name, func(t *testing.T) {
			if fieldKey(pair[0]) != fieldKey(pair[1]) {
				t.Errorf("%q and %q compare differently", pair[0], pair[1])
			}
		})
	}
	// A fingerprint is canonical by the time it is stored — the parser produces
	// one form — so comparison is equality. What that relies on is the parser
	// refusing everything else, which internal/identity tests.
	canonical, err := identity.ParseFingerprint("1223 03ea 5e96 543a 2dd8 bfea")
	if err != nil {
		t.Fatal(err)
	}
	spaceless, err := identity.ParseFingerprint("122303EA5E96543A2DD8BFEA")
	if err != nil {
		t.Fatal(err)
	}
	if canonical != spaceless {
		t.Errorf("two spellings of one fingerprint parsed differently: %q and %q", canonical, spaceless)
	}
	// And two genuinely different labels stay different.
	if fieldKey("laptop") == fieldKey("desktop") {
		t.Error("different names compare equal")
	}
}

// A packet claiming to offer, built by something other than this code: the
// parser has to bound it the same way, because that is where hostile input
// arrives.
func TestHostileFieldsAreDroppedOnParse(t *testing.T) {
	for name, txt := range map[string][]string{
		// 240, not 300: a DNS character string cannot exceed 255, so the
		// protocol bounds this before we do. MaxCandidateFieldLength is the
		// tighter bound, and it is the one under test.
		"an over-long name":        {"node=node_x000000000000", "name=" + strings.Repeat("b", 240)},
		"a name with a newline":    {"node=node_x000000000000", "name=one\ntwo"},
		"a fingerprint with a tab": {"node=node_x000000000000", "fp=AAAA\tBBBB"},
	} {
		t.Run(name, func(t *testing.T) {
			packet := txtPacket(t, txt)
			for _, got := range ParseAnnouncements(packet) {
				if strings.ContainsAny(got.DisplayName+got.Platform+got.Fingerprint, "\n\r\t") {
					t.Errorf("a control character survived parsing: %+v", got)
				}
				if len(got.DisplayName) > MaxCandidateFieldLength {
					t.Errorf("an over-long name survived parsing: %d bytes", len(got.DisplayName))
				}
			}
		})
	}
}

// txtPacket builds a packet with arbitrary TXT entries, which buildAnnouncement
// will not do — the point is to be the sender this code does not control.
func txtPacket(t *testing.T, txt []string) []byte {
	t.Helper()
	instanceName, err := dnsmessage.NewName("agenthub-hostile." + serviceName)
	if err != nil {
		t.Fatal(err)
	}
	hostName, err := dnsmessage.NewName("agenthub-hostile.local.")
	if err != nil {
		t.Fatal(err)
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	builder.EnableCompression()
	if err := builder.StartAnswers(); err != nil {
		t.Fatal(err)
	}
	header := dnsmessage.ResourceHeader{Name: instanceName, Class: dnsmessage.ClassINET, TTL: 120}
	if err := builder.SRVResource(header, dnsmessage.SRVResource{Port: 7463, Target: hostName}); err != nil {
		t.Fatal(err)
	}
	if err := builder.TXTResource(header, dnsmessage.TXTResource{TXT: txt}); err != nil {
		t.Fatal(err)
	}
	addressHeader := dnsmessage.ResourceHeader{Name: hostName, Class: dnsmessage.ClassINET, TTL: 120}
	if err := builder.AResource(addressHeader, dnsmessage.AResource{A: [4]byte{192, 168, 1, 42}}); err != nil {
		t.Fatal(err)
	}
	packet, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

// A node not in pairing mode announces exactly what it announced before any of
// this existed: its id, so peers that already know it can find its address.
func TestANodeNotOfferingAnnouncesNothingExtra(t *testing.T) {
	packet, err := buildAnnouncement("node_quiet0000000000", "agenthub-test", 7463,
		[]netip.Addr{netip.MustParseAddr("192.168.1.42")}, Offer{})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"name=", "platform=", "fp="} {
		if bytes.Contains(packet, []byte(marker)) {
			t.Errorf("a node that is not offering announced %q", marker)
		}
	}
	got := ParseAnnouncements(packet)
	if len(got) != 1 || got[0].Offering() {
		t.Errorf("announcements = %+v; a quiet node must not read as an offer", got)
	}
}
