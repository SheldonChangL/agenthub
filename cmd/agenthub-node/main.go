package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"agenthub.local/agenthub/internal/api"
	"agenthub.local/agenthub/internal/discovery"
	"agenthub.local/agenthub/internal/hub"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/nodeconfig"
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
	flag.Parse()
	if *scanInterval <= 0 {
		return errors.New("scan interval must be positive")
	}
	// The peer listener is gated by the same rule as the owner's API. It is the
	// listener that a later step will widen, and it must be a deliberate change
	// to this line rather than something that happened because nobody checked.
	if err := nodeconfig.ValidateLoopback(*peerListenAddress); err != nil {
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

	heartbeats := protocol.NewHeartbeatBuilder(store, node, keypair)
	apiServer := api.NewServer(store, service, heartbeats, node)
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
		// The certificate and key are already in TLSConfig.
		if err := peerServer.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
			serveError <- fmt.Errorf("peer listener: %w", err)
		}
	}()
	go discoveryLoop(service, *scanInterval)

	// Publishing starts only after the listener is up: a peer that answers this
	// node's heartbeat by sending its own must find somewhere to send it.
	//
	// LoopbackOnly is the boundary from docs/multinode-plan.md. Two nodes on one
	// machine exchange real, signed, per-peer heartbeats; nothing reaches the
	// network until the step that widens this is done deliberately.
	publisher := transport.NewPublisher(store, heartbeats, node.ID, transport.LoopbackOnly, *publishInterval)
	publishCtx, stopPublishing := context.WithCancel(context.Background())
	defer stopPublishing()
	go publisher.Run(publishCtx)

	// Discovery only fills in addresses for nodes already paired, and only
	// addresses this build would deliver to. It cannot create trust, and a
	// forged announcement cannot leak anything: delivery pins TLS to the key
	// recorded when pairing, and whoever forged the packet does not hold it.
	if *discover {
		browser := discovery.NewBrowser(store, transport.LoopbackOnly)
		go func() {
			if err := browser.Listen(publishCtx, discovery.MulticastGroupV4()); err != nil {
				log.Printf("discovery stopped: %v", err)
			}
		}()
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
