// Command agenthub-mcp exposes one AgentHub session to the agent that launches
// it, over stdio.
//
// It is started by the agent as a child process, so it must be told which
// session it acts for. See -as.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"agenthub.local/agenthub/internal/mcpserver"
)

func main() {
	if err := run(); err != nil {
		// stdout is the MCP transport; diagnostics must not go near it.
		fmt.Fprintf(os.Stderr, "agenthub-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	defaultURL := os.Getenv("AGENTHUB_URL")
	if defaultURL == "" {
		defaultURL = "http://127.0.0.1:7462"
	}
	nodeURL := flag.String("url", defaultURL, "AgentHub node URL")
	as := flag.String("as", "",
		"the session this server acts for, as <provider>:<id> (required)")
	flag.Parse()

	// Anything written to stdout would be read as a protocol frame.
	log.SetOutput(os.Stderr)

	client, err := mcpserver.NewClient(*nodeURL)
	if err != nil {
		return err
	}

	ctx := context.Background()
	binding, err := mcpserver.Bind(ctx, client, *as)
	if err != nil {
		if errors.Is(err, mcpserver.ErrNoBinding) {
			// Said at length because the obvious alternative — letting the
			// caller name a session per call — is exactly what must not happen:
			// every agent on this machine can reach this same node.
			return errors.New(
				"-as is required: this server acts for exactly one session, and MCP does not\n" +
					"say which session called it, so it cannot be inferred. Start it as\n" +
					"  agenthub-mcp -as codex:<id>\n" +
					"and run one server per session you want to expose")
		}
		return err
	}

	nodeID, err := client.NodeID(ctx)
	if err != nil {
		return err
	}
	return mcpserver.New(client, binding, nodeID).Run(ctx)
}
