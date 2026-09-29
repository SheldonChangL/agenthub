package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
	// args are the running node's own arguments less its remembered settings,
	// with every path it resolved from its environment given explicitly.
	args []string
	// db is the database the node runs on: an absolute path to a file that is
	// there.
	db string
	// dbFrom says where db was read from, for a person: its --db, its own
	// environment, or this app's standing in for it.
	dbFrom string
	// confirmed is whether db was read from the running node itself — its
	// command line or its environment — rather than inferred from this app's
	// environment. An inferred one is checked afterwards by the node id, so a
	// node that cannot be asked who it is is not restarted on one.
	confirmed bool
	// recovery is what starts the running node again. When every path it
	// used was read from the node itself (inferred is empty), it is the node's
	// whole command line with those paths made explicit, which starts it again
	// whatever the environment it is typed into. When any was inferred from
	// this app's environment, it is the node's command line as it was, and
	// nothing more: an inferred path is exactly what would have brought up the
	// wrong node, so it is not handed back as the way to the right one.
	recovery []string
	// inferred are the path flags whose values this app worked out from its
	// own environment, standing in for the node's (nodeEnvironmentFallback).
	inferred []string
}

// planNodeRestart turns the running node's command line and environment into
// the arguments its replacement is started with, or says why it cannot.
//
// Everything is kept except the node's five remembered settings. Those are the
// thing a restart from this window exists to apply: the settings page wrote
// them into the database, and a flag given on the command line wins over the
// database and is recorded again — so passing the old --peer-listen back would
// silently undo the save that asked for the restart. Left off, the node takes
// what was saved, which is what the start without arguments this replaces did
// too. --db and everything else that is not a setting goes back as it was.
//
// A path the command line did not name — --db above all — is the one the node
// worked out from its own environment (nodeenv.go), and it is passed to the
// replacement explicitly, because the replacement inherits this app's
// environment instead. When the node's environment could not be read, this
// app's stands in for it only where nodeEnvironmentFallback allows and only
// for a node running as this app's user; otherwise the restart refuses.
func planNodeRestart(running nodeProcess, appPaths func() (nodePaths, error)) (nodeRestartPlan, error) {
	parsed, rest, err := parseNodeArgs(running.args())
	if err != nil {
		return nodeRestartPlan{}, fmt.Errorf("its command line could not be read: %w", err)
	}
	remembered := map[string]bool{}
	for _, flag := range nodeSettingFlags {
		remembered[flag] = true
	}
	given := map[string]string{}
	listen := ""
	var kept, all []string
	for _, argument := range parsed {
		all = append(all, argument.words...)
		for _, pathFlag := range nodePathFlags {
			if argument.name != pathFlag {
				continue
			}
			if !filepath.IsAbs(argument.value) {
				return nodeRestartPlan{}, fmt.Errorf(
					"it was started with -%s %s, a relative path, and this app cannot see the directory that path was relative to",
					argument.name, argument.value)
			}
			// Last one wins, as it does for the node.
			given[pathFlag] = argument.value
		}
		if argument.name == "listen" {
			listen = argument.value
		}
		if remembered[argument.name] {
			continue
		}
		kept = append(kept, argument.words...)
	}

	// The restart stops every agenthub-node by name and waits on this app's
	// address; a node listening somewhere else is not the one this window
	// talks to, and the one started in its place would not be either.
	if want := defaultNodeListen(); listen != "" && listen != want {
		return nodeRestartPlan{}, fmt.Errorf(
			"it was started with -listen %s, not %s, so it is not the node this app starts or talks to", listen, want)
	}

	plan := nodeRestartPlan{confirmed: true, dbFrom: "its --db"}
	var missing []string
	for _, pathFlag := range nodePathFlags {
		if _, ok := given[pathFlag]; !ok {
			missing = append(missing, pathFlag)
		}
	}
	var defaults nodePaths
	if len(missing) > 0 {
		from := "its own environment"
		switch {
		case running.EnvErr == nil:
			defaults, err = nodeDefaultPaths(runtime.GOOS, environmentLookup(runtime.GOOS, running.Env))
			if err != nil {
				return nodeRestartPlan{}, fmt.Errorf(
					"it was started without -%s, and its own environment does not say where that is: %w",
					strings.Join(missing, ", -"), err)
			}
		case nodeEnvironmentFallback && running.UserErr == nil:
			defaults, err = appPaths()
			if err != nil {
				return nodeRestartPlan{}, fmt.Errorf(
					"it was started without -%s, its environment could not be read (%v), and this app could not "+
						"work out the default from its own either: %w", strings.Join(missing, ", -"), running.EnvErr, err)
			}
			from = "this app's environment (the node's own could not be read: " + running.EnvErr.Error() + ")"
			for _, pathFlag := range missing {
				plan.inferred = append(plan.inferred, "--"+pathFlag)
			}
			if _, ok := given["db"]; !ok {
				plan.confirmed = false
			}
		case nodeEnvironmentFallback:
			return nodeRestartPlan{}, fmt.Errorf(
				"it was started without -%s, so it uses the default its own environment names; that environment "+
					"could not be read (%v), and this app's own stands in for it only for a node running as this "+
					"app's user, which this one could not be confirmed to be (%v)",
				strings.Join(missing, ", -"), running.EnvErr, running.UserErr)
		default:
			return nodeRestartPlan{}, fmt.Errorf(
				"it was started without -%s, so it uses the default its own environment names, and that "+
					"environment could not be read (%v)", strings.Join(missing, ", -"), running.EnvErr)
		}
		if _, ok := given["db"]; !ok {
			plan.dbFrom = from
		}
	}

	var added []string
	for _, pathFlag := range missing {
		path := defaults.pathFor(pathFlag)
		if !filepath.IsAbs(path) {
			return nodeRestartPlan{}, fmt.Errorf(
				"its default -%s works out to %q, a relative path, which names a different place from another directory",
				pathFlag, path)
		}
		added = append(added, "--"+pathFlag, path)
	}
	plan.db = given["db"]
	if plan.db == "" {
		plan.db = defaults.database
	}
	plan.args = append(append(append(plan.args, kept...), added...), rest...)
	if len(plan.inferred) > 0 {
		plan.recovery = append(append([]string{}, all...), rest...)
	} else {
		plan.recovery = append(append(append([]string{}, all...), added...), rest...)
	}

	// The database has to be there. A running node holds it open, so a path
	// that names nothing means the command line or the environment was
	// misread, and either way the node started from here would open a
	// different one.
	info, err := os.Stat(plan.db)
	if err != nil || !info.Mode().IsRegular() {
		return nodeRestartPlan{}, fmt.Errorf(
			"its database %s (from %s) is not there, so this app cannot tell it would come back on the same one",
			plan.db, plan.dbFrom)
	}
	return plan, nil
}

// defaultNodeListen is the -listen address of the node this app talks to:
// defaultNodeURL's host and port, which is also the node's own default.
func defaultNodeListen() string {
	if parsed, err := url.Parse(defaultNodeURL); err == nil {
		return parsed.Host
	}
	return strings.TrimPrefix(defaultNodeURL, "http://")
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
