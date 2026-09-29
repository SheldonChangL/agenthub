package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Where a node puts what it was not told where to put (#205).
//
// A node started without --db opens the database cmd/agenthub-node/main.go
// defaultPaths names: os.UserConfigDir()/agenthub/agenthub.db, with the Claude
// and Codex roots under os.UserHomeDir(). Both read the node's own environment
// — HOME on macOS; XDG_CONFIG_HOME, then HOME, on Linux; APPDATA and
// USERPROFILE on Windows — and the node's environment is not this app's. A node
// started from a shell that set XDG_CONFIG_HOME, restarted from a window the
// dock opened without it, comes back on a different database: a different
// identity, and no paired machine that knows it. So the restart works the
// default out from the running node's environment, and hands the result to the
// node it starts as an explicit --db, rather than letting that node work it out
// again from this app's.

// nodePaths are the three paths the node resolves from its environment when
// its command line does not name them.
type nodePaths struct {
	database string
	claude   string
	codex    string
}

// pathFor is the path the node would use for one of nodePathFlags.
func (p nodePaths) pathFor(flag string) string {
	switch flag {
	case "db":
		return p.database
	case "claude-root":
		return p.claude
	case "codex-root":
		return p.codex
	}
	return ""
}

// nodeDefaultPaths is cmd/agenthub-node/main.go defaultPaths, evaluated
// against an environment that need not be this process's: os.UserHomeDir and
// os.UserConfigDir as the Go standard library spells them for goos, with
// getenv in place of os.Getenv.
func nodeDefaultPaths(goos string, getenv func(string) string) (nodePaths, error) {
	home, err := userHomeDir(goos, getenv)
	if err != nil {
		return nodePaths{}, err
	}
	config, err := userConfigDir(goos, getenv)
	if err != nil {
		return nodePaths{}, err
	}
	return nodePaths{
		database: filepath.Join(config, "agenthub", "agenthub.db"),
		claude:   filepath.Join(home, ".claude"),
		codex:    filepath.Join(home, ".codex"),
	}, nil
}

// userHomeDir is os.UserHomeDir for the platforms this app ships on.
func userHomeDir(goos string, getenv func(string) string) (string, error) {
	name := "HOME"
	if goos == "windows" {
		name = "USERPROFILE"
	}
	if value := getenv(name); value != "" {
		return value, nil
	}
	return "", errors.New(name + " is not set")
}

// userConfigDir is os.UserConfigDir for the platforms this app ships on,
// including its refusal of a relative XDG_CONFIG_HOME.
func userConfigDir(goos string, getenv func(string) string) (string, error) {
	switch goos {
	case "windows":
		if dir := getenv("AppData"); dir != "" {
			return dir, nil
		}
		return "", errors.New("APPDATA is not set")
	case "darwin":
		if home := getenv("HOME"); home != "" {
			return home + "/Library/Application Support", nil
		}
		return "", errors.New("HOME is not set")
	default:
		dir := getenv("XDG_CONFIG_HOME")
		if dir == "" {
			home := getenv("HOME")
			if home == "" {
				return "", errors.New("neither XDG_CONFIG_HOME nor HOME is set")
			}
			return home + "/.config", nil
		}
		if !filepath.IsAbs(dir) {
			return "", errors.New("XDG_CONFIG_HOME is a relative path")
		}
		return dir, nil
	}
}

// environmentLookup reads one variable out of a process's environment the way
// that process's own os.Getenv would: on Unix the first entry for a name wins
// (syscall.copyenv) and names are exact; on Windows names are compared without
// regard to case, and the entries Windows keeps for per-drive directories
// ("=C:=C:\...") name nothing.
func environmentLookup(goos string, environment []string) func(string) string {
	return func(name string) string {
		for _, entry := range environment {
			at := strings.IndexByte(entry, '=')
			if at <= 0 {
				continue
			}
			key := entry[:at]
			if key == name || (goos == "windows" && strings.EqualFold(key, name)) {
				return entry[at+1:]
			}
		}
		return ""
	}
}

// appDefaultPaths is where a node would put its paths if it had this app's
// environment — which the node this app starts does.
func appDefaultPaths() (nodePaths, error) {
	return nodeDefaultPaths(runtime.GOOS, os.Getenv)
}

// nodeEnvironmentFallback is whether, when the running node's environment
// cannot be read, this app's own may stand in for it. Only on Windows, and
// only with the checks planNodeRestart makes: there the reading can fail on a
// machine that is otherwise ordinary, and the node is normally started by the
// installer's Startup shortcut, as the same user, from the same Explorer
// environment this app was. On macOS and Linux a process of the same user
// always has a readable environment, so failing to read it is a reason to stop
// rather than to guess. A variable so the tests can take either branch.
var nodeEnvironmentFallback = runtime.GOOS == "windows"
