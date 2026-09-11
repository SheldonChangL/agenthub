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
	"agenthub.local/agenthub/internal/codexapp"
	"agenthub.local/agenthub/internal/codexdriver"
	"agenthub.local/agenthub/internal/discovery"
	"agenthub.local/agenthub/internal/hub"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/label"
	"agenthub.local/agenthub/internal/nodeconfig"
	"agenthub.local/agenthub/internal/pairing"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
	"agenthub.local/agenthub/internal/transport"
	"agenthub.local/agenthub/internal/wake"
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
	peerListenAddress := flag.String("peer-listen", nodeconfig.DefaultPeerListen,
		"TLS listen address for peer traffic. Remembered: given once, it applies on every later start")
	displayName := flag.String("display-name", "",
		"what this node calls itself to other machines, and it is announced to everyone on "+
			"the segment while pairing mode is open. Pinned once given: without this flag the "+
			"name follows the machine's own name, which is not always the one the network calls "+
			"it. Pass it empty, or as nothing but spaces, to hand the name back to the machine")
	autoWake := flag.Bool("auto-wake", false,
		"let an arriving message start a turn in the agent it was addressed to, for sessions "+
			"whose owner opened that per-session switch. Off here means no session can be woken "+
			"whatever its own setting says. Remembered: given once, it applies on every later start")
	discover := flag.Bool("discover", false,
		"learn paired peers' addresses from mDNS on the local network. Remembered: given once, "+
			"it applies on every later start")
	allowLAN := flag.Bool("allow-lan", false,
		"serve paired peers on a private network address instead of loopback only. Remembered: "+
			"given once, it applies on every later start, and every start says so in the log")
	outboundRetention := flag.Duration("outbound-retention", 7*24*time.Hour,
		"how long a delivered or refused outbound message stays queryable")
	var declaredPrivate nodeconfig.StringList
	flag.Var(&declaredPrivate, "treat-as-private",
		"CIDR block to treat as a private network, repeatable "+
			"(for a network that is private despite its addresses, such as a direct cable). "+
			"Remembered, and given at all it replaces the whole remembered set")
	flag.Parse()
	if *scanInterval <= 0 {
		return errors.New("scan interval must be positive")
	}
	// -listen is checked here and nowhere else, because it is not one of the
	// remembered settings. The owner's API has no authentication and is safe
	// only because reaching it means being on this machine; a value that could
	// be stored is a value a mistaken write could move.
	if err := nodeconfig.ValidateLoopback(*listenAddress); err != nil {
		return err
	}

	ctx := context.Background()
	store, err := registry.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	// What this node will run with, and where each value came from. A flag
	// given now wins and is recorded; a flag left off means whatever was said
	// last time. This is why `ah service install` needs only --db: the unit
	// file stopped being a second copy of the configuration.
	given := flagSettings(flag.CommandLine, peerListenAddress, allowLAN, discover, autoWake, declaredPrivate)
	startup, err := applyStartupSettings(ctx, store, given, log.Printf)
	if err != nil {
		return err
	}
	settings, sources, declaredRanges := startup.settings, startup.sources, startup.ranges

	node, err := identity.LoadOrCreate(ctx, store, *displayName, wasSet(flag.CommandLine, "display-name"))
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
	// Printed because it is announced. An owner who never looks at this only
	// finds out what their machine calls itself by reading it off someone
	// else's screen, and it is not always the name they expect: with no
	// HostName set, macOS answers gethostname() from DHCP and DNS.
	log.Printf("node display name %q (%s) — announced to the local network while pairing mode "+
		"is open; -display-name changes it", node.DisplayName, nameProvenance(node.NameIsChosen))

	// One policy decides three things that must agree: where this node will
	// deliver, which addresses discovery may record, and which addresses the
	// owner's API will accept. If they disagreed, an owner could save an address
	// that is silently never used, with the reason only in a log line.
	deliveryPolicy := transport.LoopbackOnly
	if settings.AllowLAN {
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
	if settings.Discover {
		pairingMode := pairing.NewMode()
		candidates = discovery.NewCandidates(node.ID, store.IsPaired, deliveryPolicy)
		// Built before the API rather than beside the listen loop, because the
		// API answers with what this announcer is actually managing to do: an
		// open window on a node with no announceable address is the one failure
		// an owner cannot see from the other machine.
		endpoint, err := pairing.PeerEndpoint(deliveryPolicy, settings.PeerListen)
		if err != nil {
			// Stopping the node rather than starting one that cannot pair: the
			// owner asked for -discover, and a peer listener an announcement
			// cannot describe makes that request impossible to honour. The
			// error says what is wrong with the address, not that reading it
			// failed.
			return fmt.Errorf("-discover needs a peer listener an announcement can describe: %w", err)
		}
		announcer = pairing.NewAnnouncer(pairingMode, discovery.MulticastGroupV4(),
			node.ID, node.ID, endpoint,
			discovery.Offer{
				DisplayName: node.DisplayName,
				Platform:    node.Platform,
				Fingerprint: node.Fingerprint,
			})
		// A field the announcement cannot carry is dropped silently, so this
		// node would believe it announces a name while appearing nameless in
		// everyone else's list. Said here because the value is this machine's
		// own and the owner can change it.
		for field, value := range map[string]string{
			"display name": node.DisplayName,
			"platform":     node.Platform,
		} {
			if value != "" && label.Printable(value) == "" {
				log.Printf("pairing announcements will carry no %s: %q cannot be announced, "+
					"so this node will appear without one in other machines' candidate lists",
					field, value)
			}
		}
		if reason := announcer.Unannounceable(); reason != "" {
			// Said at startup, not only when someone tries to pair: this is a
			// configuration that cannot pair over the network, and the owner
			// should learn that before opening a window that announces nothing.
			// The reason comes from the endpoint so it names the actual cause —
			// loopback and IPv6 are different problems with different fixes.
			log.Printf("pairing mode will announce nothing with -peer-listen %s: %s",
				settings.PeerListen, reason)
		}
		options = append(options, api.WithPairing(pairingMode, candidates, announcer))
	}
	// Waking is off at the node as well as at the session, and both have to be
	// open. A per-session switch alone would mean an owner who set one months
	// ago, before this existed, finds turns starting after an upgrade; a node
	// switch alone would wake every session at once. Two switches, and the
	// narrow one is not enough on its own.
	// One number, shared with the API, because the wake stream holds a request
	// open and has to end before this cuts it. Go arms the write deadline when
	// the request header is read, so a poll as long as the deadline is a poll
	// that can never answer — measured: at 30s against a 30s deadline, every
	// quiet poll produced an empty reply instead of its 204.
	if settings.AutoWake {
		supervisor := codexapp.NewSupervisor(codexapp.SupervisorOptions{})
		defer func() { _ = supervisor.Close() }()
		// Two drivers, one per provider, because the two providers are reached
		// in opposite directions: this node dials Codex's app-server, and a
		// Claude Code agent's MCP server dials this node. The channel driver
		// is therefore also the subscription point the API serves.
		channels := wake.NewChannelDriver()
		options = append(options,
			api.WithWaker(wake.New(
				store, registry.DefaultWakeLimits(),
				codexdriver.New(supervisor), channels,
			)),
			api.WithChannelSubscriber(channels),
			api.WithWriteTimeout(ownerWriteTimeout),
		)
		log.Printf("auto-wake is on for this node; a session is woken only if its own " +
			"autoWake is also open (ah audience <id> ... --auto-wake)")
	}
	// Published on the owner surface whether it is on or off: a desktop that
	// offers a per-session auto-wake switch has to be able to say that the node
	// flag is closed and the switch will do nothing.
	options = append(options, api.WithAutoWake(settings.AutoWake),
		// Published so the desktop can show what this node is running with,
		// where each value came from, and what a restart would change.
		api.WithNodeSettings(settings, sources))
	apiServer := api.NewServer(store, service, heartbeats, node, options...)
	server := ownerServer(*listenAddress, apiServer.Handler())

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
		Addr:              settings.PeerListen,
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
	if settings.Discover {
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
	log.Printf("peer listener on https://%s", settings.PeerListen)

	select {
	case err := <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stop:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return shutDown(shutdownCtx, apiServer, server, peerServer)
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
	atMostPerMinute := func(condition, message string, args ...any) {
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
			atMostPerMinute("full",
				"the pairing candidate list is full at %d; a machine opening pairing mode now will not appear",
				discovery.MaxCandidates)
		case err != nil:
			atMostPerMinute("error", "could not read pairing offers: %v", err)
		case changed > 0:
			log.Printf("%d new pairing candidate(s)", changed)
		}
	}
}

// nameProvenance says where the announced name came from.
//
// An owner who sees the wrong name needs to know whether the fix is to rename
// the machine or to pass the flag, and those are different actions. Printed
// because this string leaves the machine: it is the one thing about this node
// that strangers on the segment read.
func nameProvenance(chosen bool) string {
	if chosen {
		return "chosen with -display-name"
	}
	return "read from this machine"
}

// settingsStore is the part of the registry the start-up settings need.
//
// An interface rather than *registry.Registry so the order below can be tested
// against a store that records what it was asked to do: the bug this shape
// exists to prevent is invisible in the return value and visible only in what
// reached the database.
type settingsStore interface {
	GetNodeSettings(ctx context.Context) (nodeconfig.Partial, error)
	SaveNodeSettings(ctx context.Context, settings nodeconfig.Partial) error
}

// startupSettings is what this start will run with.
type startupSettings struct {
	settings nodeconfig.Settings
	sources  map[string]string
	ranges   nodeconfig.PrivateRanges
}

// applyStartupSettings resolves the configuration, validates it, and only then
// records what this command line gave.
//
// The order is the whole point. Saving before validating turns one mistyped
// flag into a node that can never start again: the bad value is remembered, so
// the next start refuses too, with nothing on the command line to blame — and
// the `ah settings set ...` the refusal suggests goes through the owner's API,
// which a node that will not start is not serving. Nothing is remembered until
// it is known to be startable, which is the order the API's PUT already uses.
//
// logf is injected so a test can read the start-up log. Every setting is
// printed on every start, and -allow-lan is the only switch here that lets
// anything leave this machine: remembered, those lines are the one place it is
// visible.
func applyStartupSettings(ctx context.Context, store settingsStore, given nodeconfig.Partial,
	logf func(string, ...any)) (startupSettings, error) {
	remembered, err := store.GetNodeSettings(ctx)
	if err != nil {
		return startupSettings{}, err
	}
	settings, sources := nodeconfig.Resolve(given, remembered, nodeconfig.DefaultSettings())
	// The same rule the owner's PUT applies, applied here before anything is
	// validated: a remembered LAN listener plus -allow-lan=false is a refusal
	// that repeats on every restart, and the API that could undo it belongs to
	// the node that is not starting. The withdrawal is recorded below in the
	// same save as the flags, so the next start does not have to redo it.
	//
	// Only when this command line said nothing about the listener. An owner who
	// typed -peer-listen 192.168.1.10:7463 -allow-lan=false gave two halves that
	// contradict each other, and guessing which half they meant is not this
	// function's decision to make: that start is still refused.
	if address, withdrawn := nodeconfig.WithdrawPeerListen(
		settings.AllowLAN, given.PeerListen != nil, settings.PeerListen); withdrawn {
		logf("withdrawing peer-listen: allow-lan is off, so the remembered LAN listener %q was not bound; "+
			"using %s and remembering it", settings.PeerListen, address)
		settings.PeerListen = address
		given.PeerListen = &address
		// Not "remembered": the remembered address is the one just withdrawn.
		// Of the three provenances this map can carry, the value now in effect
		// is the default, and saying so keeps the owner's API from reporting a
		// value nobody stored.
		sources[nodeconfig.SettingPeerListen] = nodeconfig.SourceDefault
	}
	// Printed before the validation that may end this start, so the refusal
	// below is read next to the values it is about.
	for _, line := range nodeconfig.Describe(settings, sources) {
		logf("setting %s", line)
	}
	declaredRanges, err := settings.Validate()
	if err != nil {
		return startupSettings{}, rememberedRefusal(err, sources)
	}
	if err := store.SaveNodeSettings(ctx, given); err != nil {
		return startupSettings{}, fmt.Errorf("remember node settings: %w", err)
	}
	// The owner's word about which networks are private, recorded next to
	// whatever it later allows.
	if len(declaredRanges) > 0 {
		logf("treating these as private networks on the owner's word: %s", declaredRanges)
		if !settings.AllowLAN {
			// Said plainly, because the line above otherwise reads as though
			// something had been enabled.
			logf("note: -treat-as-private has no effect without -allow-lan; the peer listener stays on loopback")
		}
	}
	return startupSettings{settings: settings, sources: sources, ranges: declaredRanges}, nil
}

// flagSettings collects the remembered settings this command line actually
// gave, as opposed to the ones left at their defaults.
//
// Whether a flag was passed, not whether its value differs from the default:
// -allow-lan=false is an owner turning something off, and the zero value
// cannot tell that apart from the flag being absent. Without the distinction,
// a node would forget an answer every time it started without the flag —
// which is the whole failure these settings exist to end.
func flagSettings(flags *flag.FlagSet, peerListen *string, allowLAN, discover, autoWake *bool,
	declaredPrivate nodeconfig.StringList) nodeconfig.Partial {
	var given nodeconfig.Partial
	if wasSet(flags, "peer-listen") {
		given.PeerListen = peerListen
	}
	if wasSet(flags, "allow-lan") {
		given.AllowLAN = allowLAN
	}
	if wasSet(flags, "discover") {
		given.Discover = discover
	}
	if wasSet(flags, "auto-wake") {
		given.AutoWake = autoWake
	}
	// Given at all, it replaces the whole set. A declaration says which
	// networks the owner believes are private; adding to a set they cannot see
	// would make the belief something nobody ever stated.
	if wasSet(flags, "treat-as-private") {
		ranges := []string(declaredPrivate)
		given.TreatAsPrivate = &ranges
	}
	return given
}

// rememberedRefusal explains a start-up refusal caused by a value nobody typed.
//
// A remembered setting can stop being valid without anything changing on this
// machine's command line: a network is renumbered, a declared range no longer
// covers the address, and the node then refuses to start over a flag that is
// not there. The refusal has to say where the value came from and how to
// replace it, or the owner reads an error about an address they never gave.
func rememberedRefusal(err error, sources map[string]string) error {
	remembered := make([]string, 0, len(nodeconfig.SettingNames))
	for _, field := range nodeconfig.SettingNames {
		if sources[field] == nodeconfig.SourceRemembered {
			remembered = append(remembered, "-"+nodeconfig.FlagName(field))
		}
	}
	if len(remembered) == 0 {
		return err
	}
	return fmt.Errorf("%w\n"+
		"this node is remembering %s from an earlier start, so the refusal is about a value that "+
		"is not on this command line. Run agenthub-node again with the flag that replaces the "+
		"stored value — for example `-peer-listen %s` — and the new value is remembered in its "+
		"place. (`ah settings` reads this node's own API, which only answers once the node starts, "+
		"so it cannot undo a value that is stopping it.)",
		err, strings.Join(remembered, ", "), nodeconfig.DefaultPeerListen)
}

// wasSet reports whether a flag was passed, as opposed to left at its default.
//
// Whether it was passed, not whether it has a value: -display-name given empty
// releases a pinned name back to the machine, and the zero value cannot tell
// that apart from the flag being absent. Without the distinction, pinning is a
// door that locks behind you.
func wasSet(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// ownerWriteTimeout is the deadline for a response on the owner listener.
//
// One number, shared with the API through WithWriteTimeout, because the wake
// stream holds a request open and has to answer before this cuts it. Go arms
// the write deadline when the request header is read, so a poll as long as the
// deadline is a poll that can never answer: at 30s against a 30s deadline,
// every quiet poll produced an empty reply instead of its 204.
const ownerWriteTimeout = 60 * time.Second

// ownerServer is the loopback listener the owner's own tools talk to.
//
// A function so the deadline it sets and the one the API is told about can be
// checked against each other. They were two literals, and changing either
// alone passed every test while putting the wake stream back to holding polls
// past the deadline that cuts them.
func ownerServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      ownerWriteTimeout,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

// drainable is the part of the API server shutdown needs.
type drainable interface{ Drain() }

// shutDown ends held requests, then both listeners.
//
// Drain first: http.Server.Shutdown waits for handlers to return and does not
// cancel their contexts, so one held wake stream kept the node alive to its
// own deadline — the shutdown then reported a timeout and the peer listener
// below was never closed at all. Measured: 5.08s and exit 1 without it, 0.10s
// with it.
//
// Both listeners are attempted whatever the first one does. A peer listener
// left open is a socket still accepting deliveries from the network after this
// process has decided to stop.
func shutDown(ctx context.Context, apiServer drainable, owner, peers *http.Server) error {
	apiServer.Drain()
	ownerErr := owner.Shutdown(ctx)
	peerErr := peers.Shutdown(ctx)
	if ownerErr != nil {
		return fmt.Errorf("shutdown server: %w", ownerErr)
	}
	if peerErr != nil {
		return fmt.Errorf("shutdown peer listener: %w", peerErr)
	}
	return nil
}
