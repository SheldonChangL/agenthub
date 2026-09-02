package mcpserver_test

import (
	"go/build"
	"os/exec"
	"strings"
	"testing"
)

// The database must not be reachable from this process at all.
//
// Opening it here would make a second writer of a store whose invariants assume
// one, and would put the audience filtering done on the way out of the node in
// two places, free to drift. It would also hand this process — which exists to
// be reachable by an agent acting on a message written on another machine —
// direct reach into rows the node's API would never return.
//
// Checking direct imports is not enough, and an earlier version of this test
// made exactly that mistake: it passed while binding.go imported
// internal/protocol, which imports internal/registry, so the linked binary
// carried the SQLite driver and the registry's query code. Nothing called them,
// but "nobody calls it today" is not a boundary. This checks the whole
// dependency closure of the built command.
func TestTheDatabaseIsNotLinkedIntoTheServer(t *testing.T) {
	// The full import path, not a relative one: the test's working directory is
	// this package, not the module root.
	command := exec.Command("go", "list", "-deps", "agenthub.local/agenthub/cmd/agenthub-mcp")
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v: %s", err, stderr.String())
	}
	deps := strings.Fields(string(output))
	if len(deps) < 10 {
		t.Fatalf("go list returned %d packages; this test would pass vacuously", len(deps))
	}

	forbidden := map[string]string{
		"agenthub.local/agenthub/internal/registry": "the registry is the node's to own",
		"database/sql":       "this process holds no database handle",
		"modernc.org/sqlite": "this process opens no database",
	}
	for _, dep := range deps {
		if reason, bad := forbidden[dep]; bad {
			t.Errorf("agenthub-mcp links %s: %s", dep, reason)
		}
		if strings.Contains(dep, "sqlite") {
			t.Errorf("agenthub-mcp links %s; it must reach the node over HTTP", dep)
		}
	}
}

// os/exec is in the closure because the MCP SDK's client half can launch a
// server as a subprocess. That is the SDK's, not ours, and it cannot be removed
// without dropping the SDK. What is enforceable is that our own packages never
// import it: this server runs no commands.
func TestTheServerRunsNoCommands(t *testing.T) {
	for _, name := range []string{
		"agenthub.local/agenthub/internal/mcpserver",
		"agenthub.local/agenthub/cmd/agenthub-mcp",
		"agenthub.local/agenthub/internal/address",
	} {
		pkg, err := build.Import(name, "", 0)
		if err != nil {
			t.Fatalf("import %s: %v", name, err)
		}
		if len(pkg.Imports) == 0 {
			t.Fatalf("%s reported no imports; this test would pass vacuously", name)
		}
		for _, imported := range pkg.Imports {
			if imported == "os/exec" {
				t.Errorf("%s imports os/exec; this server runs no commands", name)
			}
		}
	}
}
