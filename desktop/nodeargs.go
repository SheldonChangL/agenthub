package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Starting the node again the way it was started (#205).
//
// The node's identity is its database: the node id is a row in it and node.key
// sits beside it (cmd/agenthub-node/main.go, identity.LoadOrCreate and
// LoadOrCreateKeypair(filepath.Dir(dbPath))). A restart that drops a --db the
// node was started with brings up a different node — a new id, a new key, and
// every paired machine no longer recognising this one — and nothing about that
// announces itself: the node answers, the panel goes green.
//
// The node does not report which database it opened: GET /v1/node and GET
// /v1/node/settings carry neither the path nor the directory. What does carry
// it is the process's own command line, which is what this file reads — the
// arguments the running node was started with, parsed against the node's flag
// table, so the restart can hand the same ones to the node it starts.

// nodeFlagTakesValue is every flag agenthub-node defines, and whether it
// consumes a value (true) or is a boolean (false). A copy of
// cmd/agenthub-node/main.go, because this module shares no code with the node;
// TestNodeFlagTableMatchesTheNode reads the node's source so the two cannot
// drift. A flag this table does not know is a command line this app cannot
// read, and the restart refuses rather than guessing where its value ends.
var nodeFlagTakesValue = map[string]bool{
	"db":                 true,
	"listen":             true,
	"claude-root":        true,
	"codex-root":         true,
	"scan-interval":      true,
	"publish-interval":   true,
	"peer-listen":        true,
	"display-name":       true,
	"auto-wake":          false,
	"discover":           false,
	"allow-lan":          false,
	"outbound-retention": true,
	"treat-as-private":   true,
}

// nodePathFlags are the flags whose value is a path the node resolves against
// its own working directory. The restarted node runs in the directory of its
// binary, so a relative one would name a different place.
var nodePathFlags = []string{"db", "claude-root", "codex-root"}

// nodeArgument is one flag as it appeared on a command line: the words that
// spelled it (one, or two when the value was the next word) and what it said.
type nodeArgument struct {
	name  string
	value string
	words []string
}

// parseNodeArgs reads a command line the way the node's own flag.Parse does:
// one or two dashes, `-name value` or `-name=value`, a boolean taking only the
// `=` form, and parsing ending at `--` or the first word that is not a flag.
// What follows the end is returned as it stands.
func parseNodeArgs(args []string) ([]nodeArgument, []string, error) {
	var parsed []nodeArgument
	for index := 0; index < len(args); index++ {
		word := args[index]
		if len(word) < 2 || word[0] != '-' {
			return parsed, args[index:], nil
		}
		if word == "--" {
			return parsed, args[index:], nil
		}
		name := strings.TrimPrefix(strings.TrimPrefix(word, "-"), "-")
		if name == "" || name[0] == '-' || name[0] == '=' {
			return nil, nil, fmt.Errorf("%q is not a flag the node accepts", word)
		}
		value, hasValue := "", false
		if at := strings.IndexByte(name, '='); at >= 0 {
			name, value, hasValue = name[:at], name[at+1:], true
		}
		takesValue, known := nodeFlagTakesValue[name]
		if !known {
			return nil, nil, fmt.Errorf("-%s is not a flag this app knows the node to have", name)
		}
		words := []string{word}
		switch {
		case takesValue && !hasValue:
			if index+1 >= len(args) {
				return nil, nil, fmt.Errorf("-%s has no value", name)
			}
			index++
			value = args[index]
			words = append(words, value)
		case !takesValue && hasValue:
			if _, err := strconv.ParseBool(value); err != nil {
				return nil, nil, fmt.Errorf("-%s=%s is not a boolean", name, value)
			}
		case !takesValue:
			value = "true"
		}
		parsed = append(parsed, nodeArgument{name: name, value: value, words: words})
	}
	return parsed, nil, nil
}

// nodeRestartPlan is what a restart will start the node with, decided before
// anything is stopped.
type nodeRestartPlan struct {
	// args are the running node's own arguments less its remembered settings.
	args []string
	// db is the database the node was started on, or "" for its default.
	db string
}

// planNodeRestart turns the running node's arguments into the ones its
// replacement is started with, or says why it cannot.
//
// Everything is kept except the node's five remembered settings. Those are the
// thing a restart from this window exists to apply: the settings page wrote
// them into the database, and a flag given on the command line wins over the
// database and is recorded again — so passing the old --peer-listen back would
// silently undo the save that asked for the restart. Left off, the node takes
// what was saved, which is what the start without arguments this replaces did
// too. --db and everything else that is not a setting goes back as it was.
func planNodeRestart(args []string, defaultDB func() (string, error)) (nodeRestartPlan, error) {
	parsed, rest, err := parseNodeArgs(args)
	if err != nil {
		return nodeRestartPlan{}, fmt.Errorf("its command line could not be read: %w", err)
	}
	remembered := map[string]bool{}
	for _, flag := range nodeSettingFlags {
		remembered[flag] = true
	}
	var plan nodeRestartPlan
	for _, argument := range parsed {
		for _, pathFlag := range nodePathFlags {
			if argument.name == pathFlag && !filepath.IsAbs(argument.value) {
				return nodeRestartPlan{}, fmt.Errorf(
					"it was started with -%s %s, a relative path, and this app cannot see the directory that path was relative to",
					argument.name, argument.value)
			}
		}
		if argument.name == "db" {
			// Last one wins, as it does for the node.
			plan.db = argument.value
		}
		if remembered[argument.name] {
			continue
		}
		plan.args = append(plan.args, argument.words...)
	}
	plan.args = append(plan.args, rest...)

	// The database has to be there. A running node holds it open, so a path
	// that names nothing means the command line was misread — or, for the
	// default, that the node resolved its default somewhere this app does not
	// (another HOME or XDG_CONFIG_HOME) — and either way the node started from
	// here would open a different one.
	database := plan.db
	if database == "" {
		database, err = defaultDB()
		if err != nil {
			return nodeRestartPlan{}, fmt.Errorf("it runs on the default database, and this app could not work out where that is: %w", err)
		}
	}
	info, err := os.Stat(database)
	if err != nil || !info.Mode().IsRegular() {
		which := "its database " + database
		if plan.db == "" {
			which = "the default database this app would start it on, " + database + ","
		}
		return nodeRestartPlan{}, fmt.Errorf("%s is not there, so this app cannot tell it would come back on the same one", which)
	}
	return plan, nil
}

// defaultNodeDB is where the node puts its database when it is given no --db:
// cmd/agenthub-node/main.go defaultPaths, from this process's environment —
// which the node this app starts inherits.
func defaultNodeDB() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "agenthub", "agenthub.db"), nil
}

// commandLine renders a program and its arguments for a person to read, with
// anything that would not survive being pasted into a shell quoted.
func commandLine(program string, args []string) string {
	words := make([]string, 0, len(args)+1)
	for _, word := range append([]string{program}, args...) {
		if word == "" || strings.ContainsAny(word, " \t\n\"'\\$`") {
			word = strconv.Quote(word)
		}
		words = append(words, word)
	}
	return strings.Join(words, " ")
}
