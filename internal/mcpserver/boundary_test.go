package mcpserver_test

import (
	"go/build"
	"strings"
	"testing"
)

// This server reaches the registry only through the node's loopback HTTP API.
//
// Opening the database here would make a second writer of a store whose
// invariants assume one, and it would put the audience filtering that happens on
// the way out of the node in two places, free to drift. It would also hand this
// process — which exists to be reachable by an agent acting on a message written
// on another machine — direct reach into rows the API would never return.
//
// Import paths are the enforceable part of that: a package cannot open SQLite
// without importing a driver, and cannot read a session row without importing
// the registry.
func TestTheServerCannotReachTheDatabaseDirectly(t *testing.T) {
	forbidden := map[string]string{
		"agenthub.local/agenthub/internal/registry": "the registry is the node's to own",
		"database/sql":       "this server holds no database handle",
		"modernc.org/sqlite": "this server opens no database",
		"os/exec":            "this server runs no commands",
	}

	for _, name := range []string{
		"agenthub.local/agenthub/internal/mcpserver",
		"agenthub.local/agenthub/cmd/agenthub-mcp",
	} {
		pkg, err := build.Import(name, "", 0)
		if err != nil {
			t.Fatalf("import %s: %v", name, err)
		}
		if len(pkg.Imports) == 0 {
			t.Fatalf("%s reported no imports; this test would pass vacuously", name)
		}
		for _, imported := range pkg.Imports {
			if reason, bad := forbidden[imported]; bad {
				t.Errorf("%s imports %s: %s", name, imported, reason)
			}
			if strings.Contains(imported, "sqlite") {
				t.Errorf("%s imports %s; it must go through the node's HTTP API", name, imported)
			}
		}
	}
}
