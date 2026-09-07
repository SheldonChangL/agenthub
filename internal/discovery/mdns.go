// Package discovery finds the addresses of paired peers on the local network.
//
// Everything this package produces is untrusted. Anything on the network can
// send a multicast packet claiming any node id at any address, and nothing here
// can tell a genuine announcement from a forged one — mDNS carries no
// authentication and this package adds none.
//
// That is survivable only because of what discovery is allowed to do. It can
// fill in the address of a node the owner already paired with, and nothing
// else: it cannot create trust, and a wrong address cannot leak anything,
// because delivery pins TLS to the key recorded when pairing and a forger does
// not hold it. The worst a hostile announcement achieves is a delivery that
// fails.
//
// The ordering matters and is not an accident: address discovery was written
// after that pin existed, because before it a forged announcement would have
// redirected a peer's session metadata to whoever sent it.
package discovery

import (
	"agenthub.local/agenthub/internal/identity"
	"context"
	"errors"
	"fmt"
	"golang.org/x/text/secure/precis"
	"golang.org/x/text/unicode/norm"
	"log"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// serviceName is the mDNS service AgentHub nodes announce themselves under.
	serviceName = "_agenthub._tcp.local."

	// multicastAddressV4 and multicastAddressV6 are the standard mDNS groups.
	multicastAddressV4 = "224.0.0.251:5353"
	multicastAddressV6 = "[ff02::fb]:5353"

	// maxPacket bounds one datagram. mDNS is UDP, so this is what an attacker
	// can make this node parse per packet.
	maxPacket = 9000

	// TXT record keys. A node announcing itself as a pairing candidate carries
	// the last three as well; a node merely being findable by peers it has
	// already paired with carries only the id.
	nodeIDKey      = "node="
	displayNameKey = "name="
	platformKey    = "platform="
	fingerprintKey = "fp="

	// MaxCandidateFieldLength bounds each human-readable field an announcement
	// carries.
	//
	// These reach a person's screen in the candidate list, and every one of them
	// is chosen by whoever sent the packet — on a multicast group anyone on the
	// network can write to. The presence path learned this the hard way (#76):
	// an unbounded field is a place to write to the reader, not a label. A DNS
	// TXT string cannot exceed 255 bytes anyway; this is smaller because a
	// display name that does not fit on a line is not a display name.
	MaxCandidateFieldLength = 64
)

// Announcement is one peer's claim about where it can be reached.
//
// It is a claim, not a fact. The name of this type is deliberate: nothing about
// an announcement has been verified when it is produced.
type Announcement struct {
	NodeID  string
	Address string

	// The three fields below appear only when the sender announced itself as a
	// pairing candidate. Every one of them is the sender's claim about itself.
	//
	// Fingerprint especially: it is what a person compares out of band, and a
	// sender is free to put someone else's here. It is a hint for finding the
	// right row in a list, never evidence. What makes pairing safe is comparing
	// the fingerprint of the key that actually arrives in the handshake, on
	// both machines — see #62.
	DisplayName string
	Platform    string
	Fingerprint string
}

// Candidate reports that a node is offering to pair.
//
// Separate from Announcement because the two are answers to different
// questions: an Announcement says where a node claims to be, and the browser
// uses it only for nodes already paired. A Candidate is an unpaired node
// asking to be found, which is information for a person rather than for the
// address book.
type Candidate struct {
	NodeID      string
	Address     string
	DisplayName string
	Platform    string
	Fingerprint string
	// FirstSeen and LastSeen bound how long a candidate stays on screen after
	// its owner closes pairing mode, since nothing announces a withdrawal.
	FirstSeen time.Time
	LastSeen  time.Time
	// Duplicate marks a row whose display name or announced fingerprint is
	// shared with another row. Two candidates claiming one fingerprint means at
	// least one is lying, and a person needs to see that rather than pick.
	//
	// It compares names by meaning, not bytes — case, composition, and the
	// apostrophe and dash variants a real machine name contains — but it cannot
	// catch a name built from another script's lookalike letters. That is what
	// the handshake's fingerprint comparison is for.
	Duplicate bool
	// Contested marks a row that something has since announced different
	// details for, under the same node id. Either the node moved or someone is
	// impersonating it, and nothing here can tell which — but pairing with a
	// row in this state means comparing the fingerprint especially carefully,
	// so it must reach the person rather than be resolved by a rule.
	//
	// Once set it stays set for the row's life: a rule that cleared it after
	// quiet would be defeated by one packet every eighty-nine seconds. And it
	// is advisory — an attacker can set it on any row for the cost of one
	// packet, so a reader must treat it as "compare carefully", never as a
	// reason to refuse. It is also family-scoped: a claim arriving over the
	// address family a row is not pinned to is not flagged, because at this
	// layer that is indistinguishable from the same node being dual-stack.
	Contested bool
}

// Offering reports whether an announcement was made by a node asking to pair.
//
// A node that is merely findable — the case discovery already served — carries
// its id and nothing else.
func (a Announcement) Offering() bool {
	return a.Fingerprint != ""
}

// Resolver applies announcements to the trust store.
type Resolver interface {
	// TrustedNodeIDs returns the nodes this owner has paired with. Discovery
	// never adds to this set; it only fills in addresses for what is already
	// in it.
	TrustedNodeIDs(ctx context.Context) ([]string, error)
	// SetNodeAddress records where a trusted peer answers.
	SetNodeAddress(ctx context.Context, nodeID, address string) error
}

// AddressPolicy decides whether a discovered address may be stored.
//
// Discovery is gated by the same rule delivery is. Storing an address the
// publisher would refuse to use leaves an owner looking at a located peer that
// never receives anything, with the reason in a log line.
type AddressPolicy func(address string) error

// addressChangeCooldown is the shortest interval between two recorded address
// changes for one peer.
//
// Without it, an attacker who knows a paired node's id can alternate between
// two acceptable addresses to defeat the unchanged-address check, writing to
// the database on every packet and keeping that peer pointed somewhere it is
// not. A peer that genuinely moves network waits this long to be found again,
// which is a small price for making the flap harmless.
const addressChangeCooldown = 30 * time.Second

// Browser listens for peer announcements and records the ones that name a
// node this owner has already paired with.
type Browser struct {
	resolver Resolver
	policy   AddressPolicy
	now      func() time.Time
	// applied remembers the last address stored for each node, so a peer
	// re-announcing an unchanged address does not write on every packet.
	applied map[string]string
	// changedAt remembers when each node's address last moved, so alternating
	// announcements cannot turn the dedup above into a write amplifier.
	changedAt map[string]time.Time
}

func NewBrowser(resolver Resolver, policy AddressPolicy) *Browser {
	return &Browser{
		resolver:  resolver,
		policy:    policy,
		now:       func() time.Time { return time.Now().UTC() },
		applied:   map[string]string{},
		changedAt: map[string]time.Time{},
	}
}

// ApplyAll records every announcement in one packet.
//
// The trust store is read once per packet rather than once per announcement.
// One 9000-byte datagram can carry hundreds of address records, and a full
// table read per record hands anyone who can send multicast a way to drive
// database reads at packet rate.
func (b *Browser) ApplyAll(ctx context.Context, announcements []Announcement) (int, error) {
	if len(announcements) == 0 {
		return 0, nil
	}
	ids, err := b.resolver.TrustedNodeIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("read trusted nodes: %w", err)
	}
	trusted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trusted[id] = struct{}{}
	}

	applied := 0
	for _, announcement := range announcements {
		ok, err := b.apply(ctx, trusted, announcement)
		if err != nil {
			return applied, err
		}
		if ok {
			applied++
		}
	}
	return applied, nil
}

// Apply records a single announcement. It reads the trust store itself, so
// ApplyAll is what the packet path uses.
func (b *Browser) Apply(ctx context.Context, announcement Announcement) (bool, error) {
	applied, err := b.ApplyAll(ctx, []Announcement{announcement})
	return applied == 1, err
}

// apply records an announcement if, and only if, it names an already-paired
// node and carries an address this build will deliver to.
//
// The paired check is the entire trust boundary of this package. An unpaired
// node id is dropped without a trace of it reaching the registry — SetNodeAddress
// would refuse to create a row for it anyway, and relying on that would be
// asking the storage layer to enforce a rule this layer is responsible for.
func (b *Browser) apply(ctx context.Context, trusted map[string]struct{}, announcement Announcement) (bool, error) {
	if announcement.NodeID == "" || announcement.Address == "" {
		return false, nil
	}
	if _, paired := trusted[announcement.NodeID]; !paired {
		// Not an error and not worth logging at volume: on a shared network,
		// most announcements are from nodes this owner has nothing to do with.
		return false, nil
	}
	if err := b.policy(announcement.Address); err != nil {
		log.Printf("discovery ignored %q at %q: %v", announcement.NodeID, announcement.Address, err)
		return false, nil
	}
	if b.applied[announcement.NodeID] == announcement.Address {
		return false, nil
	}
	if last, seen := b.changedAt[announcement.NodeID]; seen && b.now().Sub(last) < addressChangeCooldown {
		// The address is moving faster than a real peer moves networks.
		return false, nil
	}
	if err := b.resolver.SetNodeAddress(ctx, announcement.NodeID, announcement.Address); err != nil {
		return false, fmt.Errorf("record address for %q: %w", announcement.NodeID, err)
	}
	b.applied[announcement.NodeID] = announcement.Address
	b.changedAt[announcement.NodeID] = b.now()
	return true, nil
}

// ParseAnnouncements extracts every peer claim from one mDNS packet.
//
// It answers with what the packet said, having verified nothing. Parsing is
// done with golang.org/x/net/dns/dnsmessage rather than by hand: this is
// attacker-controlled input arriving on a UDP socket, and a hand-rolled DNS
// parser is a supply of memory-safety bugs nobody needs.
//
// A malformed packet yields no announcements and no error. There is no useful
// distinction between "someone sent us garbage" and "someone sent us a packet
// for a different protocol" on a multicast group anyone can write to, and
// treating either as a failure would let a single sender make discovery look
// broken.
func ParseAnnouncements(packet []byte) []Announcement {
	var parser dnsmessage.Parser
	if _, err := parser.Start(packet); err != nil {
		return nil
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return nil
	}

	// A record set arrives in pieces: SRV gives the port and target host, TXT
	// gives the node id, A and AAAA give the address. They are collected first
	// and matched afterwards, because the order within a packet is not fixed.
	type service struct {
		target string
		port   uint16
	}
	services := map[string]service{}
	nodeIDs := map[string]string{}
	displayNames := map[string]string{}
	platforms := map[string]string{}
	fingerprints := map[string]string{}
	addresses := map[string][]netip.Addr{}

	collect := func(header dnsmessage.ResourceHeader, body dnsmessage.ResourceBody) {
		name := strings.ToLower(header.Name.String())
		switch resource := body.(type) {
		case *dnsmessage.SRVResource:
			services[name] = service{target: strings.ToLower(resource.Target.String()), port: resource.Port}
		case *dnsmessage.TXTResource:
			for _, entry := range resource.TXT {
				// Each value is bounded and stripped of anything that is not
				// printable before it is kept. A field that fails either is
				// dropped rather than truncated: a name cut off mid-way is a
				// different name, and showing one would be worse than showing
				// none.
				if after, found := strings.CutPrefix(entry, nodeIDKey); found {
					nodeIDs[name] = after
				} else if after, found := strings.CutPrefix(entry, displayNameKey); found {
					displayNames[name] = printableField(after)
				} else if after, found := strings.CutPrefix(entry, platformKey); found {
					platforms[name] = printableField(after)
				} else if after, found := strings.CutPrefix(entry, fingerprintKey); found {
					// Parsed, not sanitised. A fingerprint is twelve bytes of a
					// digest; anything else is a string that looks like one.
					if canonical, err := identity.ParseFingerprint(after); err == nil {
						fingerprints[name] = canonical
					}
				}
			}
		case *dnsmessage.AResource:
			addresses[name] = append(addresses[name], netip.AddrFrom4(resource.A))
		case *dnsmessage.AAAAResource:
			addresses[name] = append(addresses[name], netip.AddrFrom16(resource.AAAA))
		}
	}

	// Answers and additionals both carry the records that matter: a responder
	// commonly puts SRV and TXT in answers and the address records in
	// additionals. Sections are read in order because the parser is a cursor.
	//
	// A malformed record stops parsing entirely rather than skipping to the
	// next section: once a section fails mid-way the cursor's position is not
	// known, and continuing would be reading whatever happens to follow.
	for _, next := range []func() (dnsmessage.Resource, error){
		parser.Answer, parser.Authority, parser.Additional,
	} {
		sectionFailed := false
		for {
			resource, err := next()
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			if err != nil {
				sectionFailed = true
				break
			}
			collect(resource.Header, resource.Body)
		}
		if sectionFailed {
			break
		}
	}

	announcements := make([]Announcement, 0, len(services))
	for name, svc := range services {
		nodeID, ok := nodeIDs[name]
		if !ok || nodeID == "" {
			continue
		}
		for _, addr := range addresses[svc.target] {
			if !addr.IsValid() {
				continue
			}
			announcements = append(announcements, Announcement{
				NodeID:      nodeID,
				Address:     net.JoinHostPort(addr.Unmap().String(), strconv.Itoa(int(svc.port))),
				DisplayName: displayNames[name],
				Platform:    platforms[name],
				Fingerprint: fingerprints[name],
			})
		}
	}
	return announcements
}

// printableField keeps a peer-supplied label only if it is one, and returns the
// normalised form.
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
func printableField(value string) string {
	if len(value) == 0 || len(value) > MaxCandidateFieldLength {
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
	if len(clean) > MaxCandidateFieldLength {
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

// fieldKey is what two labels are compared by when deciding whether one row is
// impersonating another.
//
// Byte equality is not it: "café" composed and decomposed are different bytes,
// and so are "Laptop" and "laptop". PRECIS supplies the comparison that goes
// with the profile.
func fieldKey(value string) string {
	// Typographic variants first: macOS puts U+2019 in "Sheldon's MacBook", and
	// an impersonator would send the ASCII apostrophe. Same for the dashes.
	folded := strings.Map(func(r rune) rune {
		switch r {
		case '\u2018', '\u2019', '\u02bc':
			return '\''
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2212':
			return '-'
		}
		return r
	}, value)
	key, err := precis.Nickname.CompareKey(folded)
	if err != nil {
		return folded
	}
	return key
}

// MulticastGroupV4 is the standard IPv4 mDNS group.
func MulticastGroupV4() string { return multicastAddressV4 }

// MulticastGroupV6 is the standard IPv6 mDNS group.
func MulticastGroupV6() string { return multicastAddressV6 }

// Listen joins the mDNS group and applies announcements until ctx is done.
// Handler adapts a Browser to Listen: it records the addresses of peers this
// owner has already paired with and ignores everything else.
func (b *Browser) Handler() PacketHandler {
	return func(ctx context.Context, _ netip.Addr, announcements []Announcement) {
		if _, err := b.ApplyAll(ctx, announcements); err != nil {
			log.Printf("discovery could not apply announcements: %v", err)
		}
	}
}

// PacketHandler is given every packet this node receives on the group, with the
// address it came from.
//
// The source is passed rather than dropped because two of the checks that make
// discovery safe need it: an announced address has to be one the announcing host
// is actually answering at, and without the source a single host can fill a
// candidate list with entries that all resolve to itself.
type PacketHandler func(ctx context.Context, source netip.Addr, announcements []Announcement)

// Listen joins the group and hands every packet to each handler.
//
// More than one handler because a packet is two different things at once: an
// address for a peer this owner has already paired with, and an offer from one
// they have not. Parsed once and given to both, rather than parsed twice or
// routed by guessing which it is.
func Listen(ctx context.Context, group string, handlers ...PacketHandler) error {
	address, err := net.ResolveUDPAddr("udp", group)
	if err != nil {
		return fmt.Errorf("resolve mDNS group %q: %w", group, err)
	}
	connection, err := net.ListenMulticastUDP("udp", nil, address)
	if err != nil {
		return fmt.Errorf("join mDNS group %q: %w", group, err)
	}
	defer func() { _ = connection.Close() }()
	// The closer must not outlive this function. Without the done channel it
	// stays parked on ctx.Done() after a read error returns, which is a leaked
	// goroutine per Listen call.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()

	buffer := make([]byte, maxPacket)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		read, from, err := connection.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read mDNS packet: %w", err)
		}
		announcements := ParseAnnouncements(buffer[:read])
		if len(announcements) == 0 {
			continue
		}
		source := from.Addr()
		for _, handle := range handlers {
			handle(ctx, source, announcements)
		}
	}
}

// Announce writes this node's own service record to the group.
// Offer is what a node says about itself while pairing mode is open.
//
// Fingerprint travels; the public key does not. A key on a multicast group is a
// key an attacker can replace, and a fingerprint is useless to them for the
// same reason it is useful here: it only means something next to the key that
// arrives in the handshake, compared on both machines by a person.
type Offer struct {
	DisplayName string
	Platform    string
	Fingerprint string
}

// Announce writes this node's own service record to the group, saying only where
// it is. A node that has not opened pairing mode announces exactly this.
func Announce(ctx context.Context, group, nodeID, instance string, port int, addresses []netip.Addr) error {
	return AnnounceOffering(ctx, group, nodeID, instance, port, addresses, Offer{})
}

// AnnounceOffering announces this node, and — when the offer is non-empty —
// says it is willing to pair.
func AnnounceOffering(ctx context.Context, group, nodeID, instance string, port int, addresses []netip.Addr, offer Offer) error {
	packet, err := buildAnnouncement(nodeID, instance, port, addresses, offer)
	if err != nil {
		return err
	}
	target, err := net.ResolveUDPAddr("udp", group)
	if err != nil {
		return fmt.Errorf("resolve mDNS group %q: %w", group, err)
	}
	connection, err := net.DialUDP("udp", nil, target)
	if err != nil {
		return fmt.Errorf("dial mDNS group %q: %w", group, err)
	}
	defer func() { _ = connection.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetWriteDeadline(deadline)
	} else {
		_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
	}
	if _, err := connection.Write(packet); err != nil {
		return fmt.Errorf("write announcement: %w", err)
	}
	return nil
}

func buildAnnouncement(nodeID, instance string, port int, addresses []netip.Addr, offer Offer) ([]byte, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("port %d is outside the representable range", port)
	}
	instanceName, err := dnsmessage.NewName(instance + "." + serviceName)
	if err != nil {
		return nil, fmt.Errorf("build instance name: %w", err)
	}
	hostName, err := dnsmessage.NewName(instance + ".local.")
	if err != nil {
		return nil, fmt.Errorf("build host name: %w", err)
	}

	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	builder.EnableCompression()
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	header := dnsmessage.ResourceHeader{Name: instanceName, Class: dnsmessage.ClassINET, TTL: 120}
	if err := builder.SRVResource(header, dnsmessage.SRVResource{
		Priority: 0, Weight: 0, Port: uint16(port), Target: hostName, // #nosec G115 -- bounded above
	}); err != nil {
		return nil, err
	}
	txt := []string{nodeIDKey + nodeID}
	// Only when offering. A node that is not in pairing mode announces exactly
	// what it announced before this existed: its id, so peers it has already
	// paired with can find its address.
	if offer.Fingerprint != "" {
		// Refused rather than dropped. Dropping it would announce the name and
		// platform of a node that reads as not offering: this node would
		// believe it was discoverable for pairing, nobody would see it as a
		// candidate, and it would have leaked two labels for nothing.
		fingerprint, err := identity.ParseFingerprint(offer.Fingerprint)
		if err != nil {
			return nil, fmt.Errorf("refusing to announce: %w", err)
		}
		txt = append(txt, fingerprintKey+fingerprint)
		for key, value := range map[string]string{
			displayNameKey: offer.DisplayName,
			platformKey:    offer.Platform,
		} {
			if clean := printableField(value); clean != "" {
				txt = append(txt, key+clean)
			}
		}
		sort.Strings(txt)
	}
	if err := builder.TXTResource(header, dnsmessage.TXTResource{TXT: txt}); err != nil {
		return nil, err
	}
	addressHeader := dnsmessage.ResourceHeader{Name: hostName, Class: dnsmessage.ClassINET, TTL: 120}
	for _, addr := range addresses {
		switch {
		case addr.Is4():
			if err := builder.AResource(addressHeader, dnsmessage.AResource{A: addr.As4()}); err != nil {
				return nil, err
			}
		case addr.Is6():
			if err := builder.AAAAResource(addressHeader, dnsmessage.AAAAResource{AAAA: addr.As16()}); err != nil {
				return nil, err
			}
		}
	}
	return builder.Finish()
}
