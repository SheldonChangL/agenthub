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
	"strconv"
	"strings"
	"time"

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

	// nodeIDKey is the TXT record key carrying the announcing node's id.
	nodeIDKey = "node="
)

// Announcement is one peer's claim about where it can be reached.
//
// It is a claim, not a fact. The name of this type is deliberate: nothing about
// an announcement has been verified when it is produced.
type Announcement struct {
	NodeID  string
	Address string
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

// Browser listens for peer announcements and records the ones that name a
// node this owner has already paired with.
type Browser struct {
	resolver Resolver
	policy   AddressPolicy
	// applied remembers the last address stored for each node, so a peer
	// re-announcing an unchanged address does not write on every packet.
	applied map[string]string
}

func NewBrowser(resolver Resolver, policy AddressPolicy) *Browser {
	return &Browser{resolver: resolver, policy: policy, applied: map[string]string{}}
}

// Apply records an announcement if, and only if, it names an already-paired
// node and carries an address this build will deliver to.
//
// The two checks are the entire trust boundary of this package. An unpaired
// node id is dropped without a trace of it reaching the registry, because
// SetNodeAddress would not create a row for it anyway and calling it would be
// asking the storage layer to enforce a rule this layer is responsible for.
func (b *Browser) Apply(ctx context.Context, announcement Announcement) (bool, error) {
	if announcement.NodeID == "" || announcement.Address == "" {
		return false, nil
	}
	trusted, err := b.resolver.TrustedNodeIDs(ctx)
	if err != nil {
		return false, fmt.Errorf("read trusted nodes: %w", err)
	}
	paired := false
	for _, nodeID := range trusted {
		if nodeID == announcement.NodeID {
			paired = true
			break
		}
	}
	if !paired {
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
	if err := b.resolver.SetNodeAddress(ctx, announcement.NodeID, announcement.Address); err != nil {
		return false, fmt.Errorf("record address for %q: %w", announcement.NodeID, err)
	}
	b.applied[announcement.NodeID] = announcement.Address
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
	addresses := map[string][]netip.Addr{}

	collect := func(header dnsmessage.ResourceHeader, body dnsmessage.ResourceBody) {
		name := strings.ToLower(header.Name.String())
		switch resource := body.(type) {
		case *dnsmessage.SRVResource:
			services[name] = service{target: strings.ToLower(resource.Target.String()), port: resource.Port}
		case *dnsmessage.TXTResource:
			for _, entry := range resource.TXT {
				if after, found := strings.CutPrefix(entry, nodeIDKey); found {
					nodeIDs[name] = after
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
				NodeID:  nodeID,
				Address: net.JoinHostPort(addr.Unmap().String(), strconv.Itoa(int(svc.port))),
			})
		}
	}
	return announcements
}

// MulticastGroupV4 is the standard IPv4 mDNS group.
func MulticastGroupV4() string { return multicastAddressV4 }

// MulticastGroupV6 is the standard IPv6 mDNS group.
func MulticastGroupV6() string { return multicastAddressV6 }

// Listen joins the mDNS group and applies announcements until ctx is done.
func (b *Browser) Listen(ctx context.Context, group string) error {
	address, err := net.ResolveUDPAddr("udp", group)
	if err != nil {
		return fmt.Errorf("resolve mDNS group %q: %w", group, err)
	}
	connection, err := net.ListenMulticastUDP("udp", nil, address)
	if err != nil {
		return fmt.Errorf("join mDNS group %q: %w", group, err)
	}
	defer func() { _ = connection.Close() }()
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()

	buffer := make([]byte, maxPacket)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		read, _, err := connection.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read mDNS packet: %w", err)
		}
		for _, announcement := range ParseAnnouncements(buffer[:read]) {
			if _, err := b.Apply(ctx, announcement); err != nil {
				log.Printf("discovery could not apply an announcement: %v", err)
			}
		}
	}
}

// Announce writes this node's own service record to the group.
func Announce(ctx context.Context, group, nodeID, instance string, port int, addresses []netip.Addr) error {
	packet, err := buildAnnouncement(nodeID, instance, port, addresses)
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

func buildAnnouncement(nodeID, instance string, port int, addresses []netip.Addr) ([]byte, error) {
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
	if err := builder.TXTResource(header, dnsmessage.TXTResource{TXT: []string{nodeIDKey + nodeID}}); err != nil {
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
