package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Restarting the node without a service manager.
//
// The node reads its settings once, at start-up, so a setting saved from this
// window means nothing until the process is replaced. On macOS and Linux that
// is launchd's or systemd's job and `ah service restart` asks them. Windows has
// no service manager this app drives (internal/service answers Supported:false,
// #65), and the installer starts the node from a Startup shortcut — so on the
// platform where a setting is most likely to need a restart, the only thing
// left was Task Manager. That is not an instruction a window may hand out.
//
// So this file is the fallback: stop whatever agenthub-node is running on this
// machine, start the one that shipped beside this app, and wait until it
// answers. It is the app's own last resort, not a service: the node it starts
// is registered with nothing and will not come back at the next login. Windows
// login start stays the installer's Startup shortcut until #65 is done.

// nodeStartTimeout bounds the wait for a restarted node to answer. The node
// opens its listener after reading settings and its database, and a refusal
// (a peer listener it will not bind, say) is an exit, not a hang — so this is
// long enough to cover a slow disk and short enough that a window which will
// never get an answer says so while the owner is still watching.
var nodeStartTimeout = 20 * time.Second

// Variables rather than constants only so the tests can shorten them: a test
// about what a stalled restart reports should not take the stall's own length.
//
// nodeStopTimeout bounds the wait for the old process to be gone. Two nodes on
// one database is the thing worth avoiding here: the second one fails to bind
// the same port and exits, which would look exactly like a restart that broke
// the node.
var nodeStopTimeout = 10 * time.Second

// RestartNode applies settings the node has already been given, by whatever
// means this machine has. A registered service is restarted through ah, so
// that path stays the one `ah service restart` takes; anything else is the
// process restart below.
func (a *App) RestartNode() (ServiceResult, error) {
	status := a.ServiceStatus()
	if status.Supported && status.Installed {
		return a.RestartService()
	}
	return a.restartNodeProcess()
}

// restartNodeProcess stops every agenthub-node on this machine and starts the
// one beside this app, detached, with its output going to a file.
func (a *App) restartNodeProcess() (ServiceResult, error) {
	_, nodeURL := a.current()
	// Killing by executable name cannot tell one node from another, and what
	// starts afterwards takes the node's own defaults — which is the node the
	// installer runs and the node this app points at out of the box. An owner
	// who has pointed this window at some other node on this machine is running
	// something this app did not place and could not re-create, so it says so
	// rather than stopping it and starting the wrong thing in its place.
	if nodeURL != defaultNodeURL {
		return ServiceResult{}, fmt.Errorf(
			"this window is pointed at %s, not the node this app starts (%s), so it will not restart it: "+
				"stop and start that node the way it was started", nodeURL, defaultNodeURL)
	}
	binary, err := findNode()
	if err != nil {
		return ServiceResult{}, err
	}
	logPath, err := nodeLogPath()
	if err != nil {
		return ServiceResult{}, err
	}
	result := ServiceResult{Command: binary + " (stop, then start)"}
	var steps []string
	say := func(format string, args ...any) {
		steps = append(steps, fmt.Sprintf(format, args...))
		result.Output = strings.Join(steps, "\n")
	}

	ctx, cancel := context.WithTimeout(a.ctx, nodeStopTimeout+nodeStartTimeout+10*time.Second)
	defer cancel()

	// Not checked: on a machine where the node is not running there is nothing
	// to stop, and every platform's tool reports that as a failure. Whether the
	// stop worked is answered below by asking the port, which is the question
	// that actually matters.
	stopOutput, _ := stopNode(ctx)
	if trimmed := strings.TrimSpace(stopOutput); trimmed != "" {
		say("%s", trimmed)
	}
	if !waitForNodeGone(ctx, nodeURL) {
		return result, errors.New("the node is still answering on " + nodeURL +
			" after being asked to stop, so it was not restarted: a second node on the same database " +
			"would fail to start and leave nothing running")
	}
	say("stopped the node answering on %s", nodeURL)

	if err := startNode(binary, logPath); err != nil {
		return result, fmt.Errorf("start %s: %w", binary, err)
	}
	say("started %s (log: %s)", binary, logPath)
	if !waitForNodeAnswering(ctx, nodeURL) {
		return result, fmt.Errorf(
			"the node was started but is not answering on %s after %s; the settings it has just been "+
				"given are the first thing to suspect, and the log says which: %s",
			nodeURL, nodeStartTimeout, logPath)
	}
	say("the node is answering on %s", nodeURL)
	return result, nil
}

// nodeLogPath is where a node started from this window writes its output. A
// process started detached has nowhere else to put it, and its startup lines
// are the only account of a node that refuses to come back.
func nodeLogPath() (string, error) {
	dir, err := logDir()
	if err != nil {
		return "", err
	}
	// 0700: the log carries the node id, its fingerprint and session counts,
	// which are the owner's to share, not the machine's. Same reasoning as
	// internal/service's launchd log.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create log directory: %w", err)
	}
	return filepath.Join(dir, "node.log"), nil
}

// openNodeLog opens the log for appending. Appending rather than truncating:
// the interesting case is a node that has failed to start more than once, and
// each restart would otherwise erase the account of the last one.
func openNodeLog(path string) (*os.File, error) {
	// #nosec G304 -- the path is this file's own: a directory the platform
	// names for a user's logs (nodeLogDir) joined with a fixed file name.
	// Nothing the window, the node or the network supplies reaches it, so
	// there is no traversal to scope with os.Root.
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// waitForNodeGone reports whether the node stopped answering before the
// deadline.
func waitForNodeGone(ctx context.Context, nodeURL string) bool {
	return waitForNode(ctx, nodeURL, nodeStopTimeout, false)
}

// waitForNodeAnswering reports whether a node answered before the deadline.
func waitForNodeAnswering(ctx context.Context, nodeURL string) bool {
	return waitForNode(ctx, nodeURL, nodeStartTimeout, true)
}

// waitForNode polls /healthz until it says what `want` says, or time runs out.
// The endpoint is the node's own liveness answer and needs no database read,
// so a node still opening its listener answers nothing rather than answering
// wrongly.
func waitForNode(ctx context.Context, nodeURL string, limit time.Duration, want bool) bool {
	deadline := time.Now().Add(limit)
	for {
		if probeNode(ctx, nodeURL) == want {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(nodePollInterval):
		}
	}
}

// nodePollInterval is how often the node is asked while waiting.
var nodePollInterval = 250 * time.Millisecond

// nodeAnswers is one probe, with its own short timeout: a node that is being
// killed can accept a connection and never reply.
func nodeAnswers(ctx context.Context, nodeURL string) bool {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probe, http.MethodGet, nodeURL+"/healthz", nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

// The three things this restart does to the machine, behind variables: the
// tests assert the sequence — stop, wait for gone, start, wait for answering —
// without killing anything on the machine running them, binding the node's
// port (which on a developer's machine belongs to their own node), or creating
// directories in their home.
var (
	stopNode  = stopNodeProcesses
	startNode = startNodeDetached
	probeNode = nodeAnswers
	logDir    = nodeLogDir
)

// runCommand executes one command and returns its combined output. A variable
// so tests can assert what would have been run without running it.
var runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
	// #nosec G204 -- name and args are this file's own constants; nothing the
	// window or the network supplies reaches here.
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(output), err
}
