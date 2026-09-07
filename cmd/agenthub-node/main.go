package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agenthub.local/agenthub/internal/api"
	"agenthub.local/agenthub/internal/buildinfo"
	"agenthub.local/agenthub/internal/discovery"
	"agenthub.local/agenthub/internal/hub"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/nodeconfig"
	"agenthub.local/agenthub/internal/pairing"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
	"agenthub.local/agenthub/internal/transport"
)

func main() {
	if err := run(); err != nil {
		log.Printf("agenthub-node: %v", err)
		os.Exit(1)
	}
}

func run() error {
	// First line, before anything can fail: a log someone sends back has to say
	// which build produced it.
	log.Print(buildinfo.Line("agenthub-node"))
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	dbPath := flag.String("db", defaults.database, "SQLite database path")
	listenAddress := flag.String("listen", "127.0.0.1:7462", "local HTTP listen address")
	claudeRoot := flag.String("claude-root", defaults.claude, "Claude data root")
	codexRoot := flag.String("codex-root", defaults.codex, "Codex data root")
	scanInterval := flag.Duration("scan-interval", 30*time.Second, "provider discovery interval")
	publishInterval := flag.Duration("publish-interval", 15*time.Second, "heartbeat publishing interval")
	peerListenAddress := flag.String("peer-listen", "127.0.0.1:7463", "TLS listen address for peer traffic")
	discover := flag.Bool("discover", false, "learn paired peers' addresses from mDNS on the local network")
	allowLAN := flag.Bool("allow-lan", false,
		"serve paired peers on a private network address instead of loopback only")
	outboundRetention := flag.Duration("outbound-retention", 7*24*time.Hour,
		"how long a delivered or refused outbound message stays queryable")
	var declaredPrivate stringList
	flag.Var(&declaredPrivate, "treat-as-private",
		"CIDR block to treat as a private network, repeatable "+
			"(for a network that is private despite its addresses, such as a direct cable)")
	flag.Parse()
	if *scanInterval <= 0 {
		return errors.New("scan interval must be positive")
	}
	// The peer listener is the only surface that may leave this machine, and
	// only when the owner says so. Without -allow-lan it stays on loopback,
	// which is what every earlier build did.
	//
	// The owner's API is never widened: it changes who may see a session and
	// revokes peers, and whoever can reach it can already restart the process.
	// What the owner says is private, recorded before it is used so the claim
	// is in the log next to whatever it later allows.
	declaredRanges, err := nodeconfig.ParsePrivateRanges(declaredPrivate)
	if err != nil {
		return err
	}
	if len(declaredRanges) > 0 {
		log.Printf("treating these as private networks on the owner's word: %s", declaredRanges)
		if !*allowLAN {
			// Said plainly, because the line above otherwise reads as though
			// something had been enabled.
			log.Printf("note: -treat-as-private has no effect without -allow-lan; the peer listener stays on loopback")
		}
	}
	if err := nodeconfig.ValidatePeerListen(*peerListenAddress, *allowLAN, declaredRanges); err != nil {
		return fmt.Errorf("peer listener: %w", err)
	}
	if err := nodeconfig.ValidateLoopback(*listenAddress); err != nil {
		return err
	}

	ctx := context.Background()
	store, err := registry.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	node, err := identity.LoadOrCreate(ctx, store)
	if err != nil {
		return fmt.Errorf("load node identity: %w", err)
	}
	// The signing key lives beside the database rather than in it: a database
	// gets copied and inspected far more casually than a file named node.key,
	// and a copy that carried the key would clone this node's identity.
	keypair, err := identity.LoadOrCreateKeypair(filepath.Dir(*dbPath))
	if err != nil {
		return fmt.Errorf("load node key: %w", err)
	}
	node.PublicKey = identity.EncodePublicKey(keypair.Public)
	node.Fingerprint = keypair.Fingerprint()
	service := hub.New(store, hub.Config{ClaudeRoot: *claudeRoot, CodexRoot: *codexRoot})
	result, err := service.Discover(ctx)
	if err != nil {
		return err
	}
	log.Printf("node %s discovered %d sessions (%d Claude, %d Codex)", node.ID, result.Total, result.Claude, result.Codex)
	log.Printf("node fingerprint %s", node.Fingerprint)

	// One policy decides three things that must agree: where this node will
	// deliver, which addresses discovery may record, and which addresses the
	// owner's API will accept. If they disagreed, an owner could save an address
	// that is silently never used, with the reason only in a log line.
	deliveryPolicy := transport.LoopbackOnly
	if *allowLAN {
		deliveryPolicy = transport.PrivateNetworks(declaredRanges)
	}

	heartbeats := protocol.NewHeartbeatBuilder(store, node, keypair)
	// Pairing mode and the candidate list exist only when this node is
	// listening on the local network. Without -discover the endpoints say that
	// rather than answering with an empty list: "nobody is advertising" and
	// "this node is not looking" are different facts.
	options := []api.Option{api.WithDeliveryPolicy(deliveryPolicy)}
	var candidates *discovery.Candidates
	var announcer *pairing.Announcer
	if *discover {
		pairingMode := pairing.NewMode()
		candidates = discovery.NewCandidates(node.ID, store.IsPaired, deliveryPolicy)
		// Built before the API rather than beside the listen loop, because the
		// API answers with what this announcer is actually managing to do: an
		// open window on a node with no announceable address is the one failure
		// an owner cannot see from the other machine.
		peerPort, err := listenPort(*peerListenAddress)
		if err != nil {
			return fmt.Errorf("read the peer listener's port for announcements: %w", err)
		}
		announcer = pairing.NewAnnouncer(pairingMode, discovery.MulticastGroupV4(),
			node.ID, node.ID, peerPort,
			pairing.LocalAddresses(deliveryPolicy, peerPort),
			discovery.Offer{
				DisplayName: node.DisplayName,
				Platform:    node.Platform,
				Fingerprint: node.Fingerprint,
			})
		if !announcer.Announceable() {
			// Said at startup, not only when someone tries to pair: this is a
			// configuration that cannot pair over the network, and the owner
			// should learn that before opening a window that announces nothing.
			log.Print("pairing mode will have no address to announce: " +
				"-peer-listen is on loopback or -allow-lan is off, so no peer could reach this node")
		}
		options = append(options, api.WithPairing(pairingMode, candidates, announcer))
	}
	apiServer := api.NewServer(store, service, heartbeats, node, options...)
	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// The peer surface is a second listener, over TLS, presenting this node's
	// identity key. A peer verifies that key against what it recorded when
	// pairing, so the connection itself proves who each side is — which a
	// forwardable challenge cannot do.
	//
	// It is separate from the owner's API on purpose: opening a port for
	// heartbeats must not also open the endpoints that change who may see a
	// session.
	// Built per handshake rather than once at startup: a node that runs longer
	// than the certificate's lifetime would otherwise serve an expired one. The
	// key never changes, so a renewal is invisible to a peer, which pins the key.
	rotating := identity.NewRotatingCertificate(keypair, node.ID)
	if _, err := rotating.GetCertificate(nil); err != nil {
		// Fail at startup rather than on the first peer connection.
		return fmt.Errorf("build node certificate: %w", err)
	}
	peerServer := &http.Server{
		Addr:              *peerListenAddress,
		Handler:           apiServer.PeerHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		TLSConfig: &tls.Config{
			GetCertificate: rotating.GetCertificate,
			MinVersion:     tls.VersionTLS13,
		},
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, nodeconfig.ShutdownSignals()...)
	defer signal.Stop(stop)
	serveError := make(chan error, 1)
	go func() { serveError <- server.ListenAndServe() }()
	go func() {
		// Listen explicitly so the connection cap wraps the listener. The
		// request rate limiter cannot bound this: it runs after the TLS
		// handshake, so the handshake's CPU and the connection's descriptor are
		// already spent by the time anything is counted.
		peerListener, err := net.Listen("tcp", peerServer.Addr)
		if err != nil {
			serveError <- fmt.Errorf("peer listener: %w", err)
			return
		}
		capped := nodeconfig.LimitConnections(peerListener, nodeconfig.MaxPeerConnections)
		// The certificate and key are already in TLSConfig.
		if err := peerServer.ServeTLS(capped, "", ""); !errors.Is(err, http.ErrServerClosed) {
			serveError <- fmt.Errorf("peer listener: %w", err)
		}
	}()
	go discoveryLoop(service, *scanInterval)
	go pruneLoop(store, *outboundRetention)

	// Publishing starts only after the listener is up: a peer that answers this
	// node's heartbeat by sending its own must find somewhere to send it.
	//
	// The delivery policy above is the boundary. Without -allow-lan it is
	// loopback only, so two nodes on one machine exchange real, signed, per-peer
	// heartbeats and nothing reaches the network.
	publisher := transport.NewPublisher(store, heartbeats, node.ID, deliveryPolicy, *publishInterval)
	publishCtx, stopPublishing := context.WithCancel(context.Background())
	defer stopPublishing()
	go publisher.Run(publishCtx)

	// Discovery only fills in addresses for nodes already paired, and only
	// addresses this build would deliver to. It cannot create trust, and a
	// forged announcement cannot leak anything: delivery pins TLS to the key
	// recorded when pairing, and whoever forged the packet does not hold it.
	if *discover {
		browser := discovery.NewBrowser(store, deliveryPolicy)
		// One packet, two readers: an address for a peer already paired, and an
		// offer from one that is not. Parsed once by Listen and given to both.
		go func() {
			if err := discovery.Listen(publishCtx, discovery.MulticastGroupV4(),
				browser.Handler(),
				candidateHandler(candidates),
			); err != nil {
				log.Printf("discovery stopped: %v", err)
			}
		}()

		// The announcer runs for the process's life and says nothing until the
		// owner opens the window. A loop started on demand is a loop that can
		// be started twice; what must be certain is that a closed window
		// announces nothing, and that is one condition in one place.
		go announcer.Run(publishCtx)
	}
	log.Printf("listening on http://%s", *listenAddress)
	log.Printf("peer listener on https://%s", *peerListenAddress)

	select {
	case err := <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stop:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		if err := peerServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown peer listener: %w", err)
		}
	}
	return nil
}

func discoveryLoop(service *hub.Hub, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := service.Discover(context.Background()); err != nil {
			log.Printf("discovery failed: %v", err)
		}
	}
}

type paths struct {
	database string
	claude   string
	codex    string
}

func defaultPaths() (paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return paths{}, fmt.Errorf("find home directory: %w", err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return paths{}, fmt.Errorf("find config directory: %w", err)
	}
	return paths{
		database: filepath.Join(config, "agenthub", "agenthub.db"),
		claude:   filepath.Join(home, ".claude"),
		codex:    filepath.Join(home, ".codex"),
	}, nil
}

// pruneLoop removes settled outbound rows once nothing reads them any more.
//
// They are kept for a while rather than deleted on settlement: `ah outbound
// <id>` is the only way an owner finds out what happened to a message, and
// deleting the answer at the moment it becomes true would make the command
// useless. They are not kept forever, because after the owner has looked, they
// are just rows.
func pruneLoop(store *registry.Registry, retention time.Duration) {
	if retention <= 0 {
		return
	}
	prune := func() {
		// Bounded per call. Without it one hung database call stops pruning for
		// the life of the process, silently.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		removed, err := store.PruneSettledOutbound(ctx, retention)
		if err != nil {
			log.Printf("pruning settled messages failed: %v", err)
			return
		}
		if removed > 0 {
			log.Printf("pruned %d settled outbound message(s) older than %s", removed, retention)
		}
	}

	// Once at startup, then hourly. A node restarted more often than the tick
	// would otherwise never prune at all.
	prune()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		prune()
	}
}

// stringList collects a flag given more than once.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// candidateHandler feeds offers from unpaired nodes to the candidate list.
//
// Errors are logged rather than returned: one bad packet on a multicast group
// anyone can write to must not stop this node listening.
//
// The recurring conditions are logged at most once a minute. Whoever filled the
// list can keep sending, and a line per packet would turn a bounded list — the
// thing the candidate layer exists to keep bounded — into unbounded log
// output, which is the same flood by another route.
func candidateHandler(candidates *discovery.Candidates) discovery.PacketHandler {
	var mu sync.Mutex
	said := make(map[string]time.Time, 2)
	// Throttled per condition rather than globally, so a real error is not
	// swallowed by a full list that is being reported.
	atMostHourly := func(condition, message string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if last, seen := said[condition]; seen && time.Since(last) < time.Minute {
			return
		}
		said[condition] = time.Now()
		log.Printf(message, args...)
	}
	return func(ctx context.Context, source netip.Addr, announcements []discovery.Announcement) {
		changed, err := candidates.ObserveAll(ctx, source, announcements)
		switch {
		case errors.Is(err, discovery.ErrCandidatesFull):
			atMostHourly("full",
				"the pairing candidate list is full at %d; a machine opening pairing mode now will not appear",
				discovery.MaxCandidates)
		case err != nil:
			atMostHourly("error", "could not read pairing offers: %v", err)
		case changed > 0:
			log.Printf("%d new pairing candidate(s)", changed)
		}
	}
}

// listenPort reads the port a listen address names, which is what an
// announcement has to carry: a peer needs the port this node answers TLS on,
// not the one multicast arrived from.
func listenPort(address string) (int, error) {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return 0, err
	}
	// LookupPort rather than Atoi, because a listen address may name a service
	// ("localhost:https") and the listener itself accepts one. Refusing what the
	// listener accepts would stop the node over an address that works.
	parsed, err := net.LookupPort("tcp", port)
	if err != nil {
		return 0, fmt.Errorf("port %q in %q is not a port this node can announce: %w", port, address, err)
	}
	// Port zero asks the kernel to choose, so the number here is not the one the
	// listener ends up on — announcing it would invite peers to connect to
	// nothing. The peer listener does not support it either way.
	if parsed == 0 {
		return 0, fmt.Errorf("the peer listener must name a fixed port, not 0, so an announcement can carry it")
	}
	return parsed, nil
}
