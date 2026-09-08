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
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"golang.org/x/text/secure/precis"
	"golang.org/x/text/unicode/norm"

	"agenthub.local/agenthub/internal/identity"
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

// Announceable reports what an offer field would actually carry, which is the
// empty string when this node's own value cannot be announced.
//
// Exported so a node can find that out about itself at startup rather than
// leaving the owner to notice that their machine appears in someone else's
// candidate list with no name. A display name is the hostname, and a hostname
// can be longer than MaxCandidateFieldLength or hold something PRECIS refuses.
func Announceable(value string) string {
	return printableField(value)
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
	// goroutine per Listen call. The membership refresher is bounded the same
	// way, for the same reason.
	done := make(chan struct{})
	defer close(done)
	subscribe(ctx, done, connection, address)
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
		dispatch(ctx, from, buffer[:read], handlers)
	}
}

// dispatch parses one packet and gives it to every handler, with the address it
// arrived from.
//
// Separate from the read loop so the source can be tested without a socket.
//
// The source is what the candidate layer checks a claimed address against, so
// passing the wrong one — or the zero value — would leave that check comparing
// an announcement to nothing. It is worth checking because it is harder to
// choose than the packet's contents, not because it cannot be chosen: anyone
// who can open a raw socket on the segment can put whatever they like in it.
// What the check rules out is a sender using an ordinary UDP socket to claim
// somebody else's address. What this does not cover is the socket read itself.
func dispatch(ctx context.Context, from netip.AddrPort, packet []byte, handlers []PacketHandler) {
	// Passed as it arrived. Normalising a v4-mapped sender and stripping a zone
	// is the comparing side's job, and doing it in both places would leave
	// neither one load-bearing.
	source := from.Addr()
	// A datagram from loopback is refused here, before any handler sees it —
	// including Browser.apply, which does not look at the source at all.
	// Candidates refuses it a second time on its own, so the property does not
	// depend on every future reader of this socket remembering to.
	//
	// IP_MULTICAST_LOOP hands a copy of every outgoing multicast datagram back
	// to local sockets that have joined the group, and the copy is matched
	// against the membership of the interface it was *sent* on — the source
	// address is not consulted. So a local process sending to the group from
	// 127.0.0.1 is delivered to this socket through its ordinary membership on
	// the default interface. Measured: the row lands even when nothing has
	// joined loopback at all, so which interfaces are joined cannot prevent it.
	// The write even reports EADDRNOTAVAIL and the copy arrives regardless.
	// A unicast datagram straight at the port arrives too, since the socket is
	// bound to the wildcard — another reason this cannot be a question about
	// memberships.
	//
	// Without this, any unprivileged local process — a second user on a shared
	// machine, who cannot read the first user's files — could put a chosen
	// display name and fingerprint on the owner's candidate list, and could do
	// it under the default loopback-only policy, which is the configuration
	// where a forgery from the local network is refused. The source check the
	// candidate layer relies on is void here, because a forger on loopback
	// trivially sends from the address it claims.
	//
	// Nothing legitimate is dropped: reachableAt refuses to announce a loopback
	// address, and dialGroup binds the source to the address being advertised,
	// so this node's own announcements arrive from its real address and its
	// self-exclusion still sees them.
	if source.Unmap().IsLoopback() {
		return
	}
	announcements := ParseAnnouncements(packet)
	if len(announcements) == 0 {
		return
	}
	for _, handle := range handlers {
		handle(ctx, source, announcements)
	}
}

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

// subscribe joins the group everywhere a peer's announcement could arrive, and
// keeps doing so for as long as the listener runs.
//
// A function of its own, and a variable, so a test can see that Listen calls it
// and can watch what it is asked to do. The alternative here was a test that
// read this file for the call, which `if false { … }` around it defeats — and
// which fails a refactor that is correct.
//
// The nil interface ListenMulticastUDP was given joins one interface: the
// system's choice, which is the default route's. Measured — a datagram sent out
// of a second interface was not heard by that join at all. So a peer whose own
// listener is on a direct cable announces onto the cable, correctly, and this
// node would never hear it.
//
// Re-checked on a ticker rather than only once, because interfaces appear after
// a process starts: an owner plugs in a USB Ethernet adapter and runs a cable to
// the machine they want to pair with, which is the case this feature is for.
var subscribe = func(ctx context.Context, done <-chan struct{}, connection *net.UDPConn, group *net.UDPAddr) {
	subscribeGroup(ctx, done, newMembership(connection, group))
}

// subscribeGroup joins now and keeps joining.
//
// Separate from the variable above so both halves are covered. Replacing
// subscribe with a recorder proves Listen calls it and hands it a live context,
// and nothing else — reducing this body to nothing passed the whole suite,
// which is how the multi-interface join came to have no coverage at all despite
// being the point of the change.
func subscribeGroup(ctx context.Context, done <-chan struct{}, joins *membership) {
	if added := joins.rejoin(); len(added) > 0 {
		// "Joined", not "listening on": what is true is that this socket now
		// has a membership on each of these.
		log.Printf("joined the announcement group on %s", strings.Join(added, ", "))
	}
	go joins.keepFresh(ctx, done)
}

// RejoinInterval is how often the group membership is re-checked for interfaces
// that have appeared since the process started.
//
// Bounded by the shortest window a peer can open, not by CandidateTTL: an
// unjoined interface has no row to expire, so the TTL says nothing about this.
// A peer may open a window for as little as thirty seconds and announces every
// twenty.
//
// What ten seconds buys is a bound on the delay, not a guarantee: an interface
// appearing late in a short window can still be joined after that peer's last
// announcement, so the shortest windows remain a matter of timing. Ten is the
// point where the delay is small next to the interval a peer announces at,
// while the cost stays one interface enumeration.
//
// The pairing package owns those two numbers and imports this one, so they
// cannot be named here; the test writes them down and pairing's own test pins
// them.
const RejoinInterval = 10 * time.Second

// membership keeps this socket subscribed to the group on every interface that
// could carry it, not only the one the default route uses.
//
// Best-effort by design. The join the socket already has from
// ListenMulticastUDP is what discovery ran on before any of this, so a machine
// where every extra join fails is no worse off, and one interface refusing must
// not stop the others.
//
// Joining widely grants nothing. Every packet still goes through the same
// checks: an offer must name the address it came from, its node id must be
// unpaired and well formed, and its address must be one the delivery policy
// accepts. What arrives on one more interface is one more set of claims.
type membership struct {
	packet *ipv4.PacketConn
	group  *net.UDPAddr
	// rejoin is the work a tick does, and every is how often. Fields so a test
	// can watch the loop doing it without waiting half a minute — the same seam
	// the announce loop uses for its own send.
	rejoin func() []string
	every  time.Duration
	// join is the syscall, injected for the same reason: a test needs to see
	// which interfaces were offered, and to make one of them fail.
	join func(*net.Interface, *net.UDPAddr) error

	mu sync.Mutex
	// reported is the set of failures already logged, so a condition that
	// recurs every tick is one line rather than one line a tick.
	reported map[string]struct{}
}

func newMembership(connection *net.UDPConn, group *net.UDPAddr) *membership {
	packet := ipv4.NewPacketConn(connection)
	m := &membership{
		packet: packet,
		group:  group,
		every:  RejoinInterval,
		join: func(iface *net.Interface, group *net.UDPAddr) error {
			return packet.JoinGroup(iface, group)
		},
	}
	m.rejoin = m.refresh
	return m
}

// refresh attempts every interface that could carry a peer's announcement and
// reports the ones that were not already joined.
//
// No record is kept of what has been joined, because the kernel keeps one: a
// duplicate join fails, so a repeat call reports nothing and the log stays
// quiet. A cache here would be an optimisation whose one distinctive behaviour
// is harmful — an interface destroyed and re-created at the same index would be
// skipped as already joined, which is precisely the case this refresh exists
// for.
//
// What relying on the kernel's record does not cover, and is not covered
// anywhere yet: on Linux a socket membership is matched on the group and the
// interface index, while the device-level group is torn down when an interface
// loses its last IPv4 address. If that is right — measured on macOS, reasoned
// on Linux, see #93 — then an interface whose DHCP lease lapses and returns
// stops delivering, and this refresh cannot repair it, because the duplicate
// join is refused and nothing here leaves the group first.
//
// Most join failures are expected and are not reported: the membership the
// system already took refuses to be duplicated, and an interface with no IPv4
// stack at all refuses with EAFNOSUPPORT. Having no IPv4 *address* is not what
// decides it — measured on this machine, four address-less interfaces joined
// while five others refused — so the returned names are filtered by address
// rather than by whether the join worked. Anything unexpected earns one line,
// because a machine at its multicast membership limit fails here (Linux allows
// twenty by default) and the interface just plugged in may be the one refused.
func (m *membership) refresh() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		m.reportOnce("interfaces", fmt.Sprintf(
			"could not list this machine's interfaces, so no announcement can be heard on one "+
				"that appears later: %v", err))
		return nil
	}
	var added []string
	for i := range interfaces {
		iface := &interfaces[i]
		if !canCarryAnnouncements(iface) {
			continue
		}
		err := m.join(iface, m.group)
		switch {
		case err == nil:
			// Named for the log only when the interface has an IPv4 address of
			// its own. On this machine four address-less interfaces join
			// successfully while en0 — the only one with an address, and the
			// only one an announcement can arrive on — is refused as a
			// duplicate of the socket's own membership and never named. A line
			// listing the four read as evidence the join was doing something.
			if hasIPv4(iface) {
				added = append(added, iface.Name)
			}
		case errors.Is(err, syscall.EADDRINUSE), errors.Is(err, syscall.EAFNOSUPPORT),
			errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.ENODEV):
			// Expected: already joined, or no IPv4 on this device.
		default:
			m.reportOnce(iface.Name,
				fmt.Sprintf("could not listen for announcements on %s: %v", iface.Name, err))
		}
	}
	return added
}

// canCarryAnnouncements reports whether an interface is one a peer's
// announcement could legitimately arrive on.
//
// Loopback and point-to-point are excluded because nothing legitimate announces
// on them: reachableAt refuses to announce a loopback address, and a tun-mode
// tunnel carries no multicast at all. A membership there could only ever
// receive something forged, so taking one serves nothing.
//
// It is **not** a security property, and an earlier version of this comment
// claimed it was. Two measurements say otherwise. A forged datagram sent to the
// group from loopback arrives through the membership of the interface it was
// sent on — the default one, which every configuration has — so refusing to
// join lo0 never blocked it; what blocks it is the source check in dispatch.
// And ListenMulticastUDP binds the wildcard, not the group, so a datagram
// unicast straight at the port is delivered without any membership being
// consulted: excluding a tunnel does not stop a peer on that tunnel sending
// one. Whether a packet is acted on is decided by the checks it passes, never
// by which interfaces this socket has joined.
func canCarryAnnouncements(iface *net.Interface) bool {
	return refuseFlags(iface) == nil
}

// refuseFlags is the one rule about an interface's flags, and says which flag
// refused. The message names the condition and not the interface, so a caller
// can compose one sentence around it.
//
// One function because there were two, kept in step by hand: the membership
// asked a boolean and the announcing side asked for a reason, and nothing made
// them agree. Two mutations survived on that — dropping the up check and the
// multicast check from the membership's copy — because only the reason-shaped
// one was tested.
func refuseFlags(iface *net.Interface) error {
	switch {
	case iface.Flags&net.FlagUp == 0:
		return errors.New("is down")
	case iface.Flags&net.FlagPointToPoint != 0:
		// A tunnel carries MULTICAST on macOS and for OpenVPN on Linux, so the
		// other flags do not exclude it. What a tun-mode tunnel lacks is
		// multicast delivery: the two ends can reach each other over TCP, which
		// is why `ah pair` works across one, but a datagram sent to a group
		// gets nowhere.
		return errors.New("is a point-to-point interface — a tunnel — which carries no " +
			"multicast, so an announcement sent on it would reach nobody. Pairing by hand " +
			"with `ah pair` works over it")
	case iface.Flags&net.FlagLoopback != 0:
		// Nothing legitimate announces from loopback, so a membership there
		// could only receive a forgery. The forgery arrives through the default
		// interface's membership regardless — see dispatch, which is what
		// actually refuses it — but joining an interface no announcement can
		// legitimately arrive on serves nothing.
		return errors.New("is loopback and reaches no other machine")
	case iface.Flags&net.FlagMulticast == 0:
		return errors.New("cannot carry a multicast packet")
	}
	return nil
}

// hasIPv4 reports whether an interface holds an IPv4 address, which is what
// decides whether an announcement could arrive on it. Not the same question as
// whether a join succeeds: an address-less interface joins on this machine, and
// several address-less ones refuse.
func hasIPv4(iface *net.Interface) bool {
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		prefix, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if parsed, ok := netip.AddrFromSlice(prefix.IP); ok && parsed.Unmap().Is4() {
			return true
		}
	}
	return false
}

// reportOnce logs a message the first time it is seen, so a condition that
// recurs on every refresh does not become a line every RejoinInterval.
// Keyed on the interface rather than on the message, so the set is bounded by
// the interfaces this machine has had rather than growing with every distinct
// errno text. A host churning veths or ppp units would otherwise accumulate one
// entry per name per error for the process's life.
func (m *membership) reportOnce(key, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reported == nil {
		m.reported = map[string]struct{}{}
	}
	if _, seen := m.reported[key]; seen {
		return
	}
	m.reported[key] = struct{}{}
	log.Print(message)
}

// keepFresh re-checks the membership until the listener stops.
func (m *membership) keepFresh(ctx context.Context, done <-chan struct{}) {
	ticker := time.NewTicker(m.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if added := m.rejoin(); len(added) > 0 {
				// Logged only when it changes: an interface appearing is worth
				// a line, and the same list every RejoinInterval is not.
				log.Printf("joined the announcement group on %s", strings.Join(added, ", "))
			}
		}
	}
}

// dialGroup opens the socket an announcement leaves by.
//
// A receiver accepts an announcement only when the address it carries is the
// address the datagram came from — see the source check in Candidates.observe.
// So the packet has to leave by the interface that holds the address being
// advertised, not by whichever one the default route happens to pick.
//
// This is not hypothetical. With a peer listener on a direct cable
// (122.122.122.1 on en8) and wifi as the default route, net.DialUDP with a nil
// local address sent from 192.168.161.2, every receiver dropped the packet for
// naming an address it did not come from, and the announcing node reported a
// successful announcement with no error — an open window that nobody could ever
// see.
//
// Both halves are set, and setting only the source is worse than setting
// neither where it does not also select the interface: the packet then goes out
// on the wrong wire with a source matching its own claim, so a receiver on a
// segment that cannot reach the address accepts the row rather than dropping
// it. See SetMulticastInterface below for what was measured where.
//
// Anything this cannot send correctly is refused rather than sent the old way.
// A nil-local dial here would be the bug above, one branch away and reported as
// a success: nothing in this package needs it, and a caller that grew a second
// address should find out at the call rather than on someone else's screen.
func dialGroup(target *net.UDPAddr, addresses []netip.Addr) (*net.UDPConn, error) {
	if len(addresses) != 1 || !addresses[0].Is4() {
		return nil, fmt.Errorf(
			"an announcement carries exactly one IPv4 address, which is the one it is sent "+
				"from and the one a receiver checks it against; got %v", addresses)
	}
	source := addresses[0]
	iface, err := interfaceHolding(source)
	if err != nil {
		return nil, err
	}
	connection, err := net.DialUDP("udp4", &net.UDPAddr{IP: source.AsSlice()}, target)
	if err != nil {
		return nil, fmt.Errorf("dial mDNS group %q from %v: %w", target, source, err)
	}
	// The outgoing interface for a multicast datagram is a socket option of its
	// own, separate from the bound source. On macOS, binding the source was
	// measured to be enough — a datagram bound to an address on a second
	// interface arrived at a join on that interface only. On platforms where
	// egress follows the route to the group instead, it is not, and the packet
	// would leave by the default interface carrying a source that matches its
	// own claim: a receiver on a segment that cannot reach the address would
	// then accept the row rather than drop it. Set for that reason.
	if err := ipv4.NewPacketConn(connection).SetMulticastInterface(iface); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("send announcements for %v on %s: %w", source, iface.Name, err)
	}
	return connection, nil
}

// CanAnnounceFrom reports why an announcement could not be sent from an
// address, or nil when it could.
//
// Asked live rather than answered from startup, because the answer changes:
// an interface can lose the address, lose multicast, or go away, and a node
// that opens a pairing window an hour later should be told the truth then
// rather than what was true at boot.
func CanAnnounceFrom(address netip.Addr) error {
	_, err := interfaceHolding(address)
	return err
}

// interfaceHolding finds the interface that carries an address.
//
// An error rather than a fallback: the fallback is the behaviour that made a
// node announce an address no receiver would accept, and reporting success for
// it is what made that invisible.
func interfaceHolding(address netip.Addr) (*net.Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces to announce from %v: %w", address, err)
	}
	// Every holder, not one. An address can sit on two interfaces — a virtual
	// address on a physical interface and a loopback alias, which is how
	// keepalived and anycast setups are built — and taking whichever came last
	// in the enumeration meant a machine that holds the address on a perfectly
	// good interface could be refused because the same address is also on a
	// tunnel.
	var holders []*net.Interface
	for i := range interfaces {
		addrs, err := interfaces[i].Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			prefix, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			held, ok := netip.AddrFromSlice(prefix.IP)
			if ok && held.Unmap() == address {
				holders = append(holders, &interfaces[i])
				break
			}
		}
	}
	return chooseHolder(holders, address)
}

// chooseHolder picks the interface to announce from, out of every one holding
// the address.
//
// Separate from the walk so the multiple-holder case can be tested: no address
// on this machine sits on two interfaces, so taking whichever came last in the
// enumeration went unnoticed — the same way the tunnel branch did.
func chooseHolder(holders []*net.Interface, address netip.Addr) (*net.Interface, error) {
	if len(holders) == 0 {
		return nil, announceableFrom(nil, address)
	}
	// One usable holder is enough, and it is the one to announce from. Taking
	// the last instead meant a machine holding the address on a good interface
	// could be refused because the same address is also on a loopback alias or
	// a tunnel — how keepalived and anycast setups are built.
	var refused error
	for _, holder := range holders {
		err := announceableFrom(holder, address)
		if err == nil {
			return holder, nil
		}
		if refused == nil {
			refused = err
		}
	}
	return nil, refused
}

// announceableFrom judges an interface that holds the address.
//
// Separate from the search so every answer can be tested on flags a test
// chooses. Whether this machine happens to have a tunnel carrying an IPv4
// address decides nothing about whether the code handles one — and it does not
// have one, which is how the tunnel branch went untested while three places of
// prose claimed it was the case being caught.
func announceableFrom(holder *net.Interface, address netip.Addr) error {
	if holder == nil {
		return fmt.Errorf("no interface on this machine holds %v, so an announcement "+
			"naming it could not come from it", address)
	}
	if err := refuseFlags(holder); err != nil {
		// Composed, not concatenated. refuseFlags returns the condition alone
		// so this reads as one sentence: an owner meets these in the 409 body,
		// in the announce status and in the startup log.
		return fmt.Errorf("%s holds %v but %w", holder.Name, address, err)
	}
	return nil
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
	connection, err := dialGroup(target, addresses)
	if err != nil {
		return err
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
