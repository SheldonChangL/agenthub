package main

import (
	"context"
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
	"agenthub.local/agenthub/internal/hub"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/nodeconfig"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
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
	flag.Parse()
	if *scanInterval <= 0 {
		return errors.New("scan interval must be positive")
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
	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           api.NewServer(store, service, heartbeats, node).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, nodeconfig.ShutdownSignals()...)
	defer signal.Stop(stop)
	serveError := make(chan error, 1)
	go func() { serveError <- server.ListenAndServe() }()
	go discoveryLoop(service, *scanInterval)
	log.Printf("listening on http://%s", *listenAddress)

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
