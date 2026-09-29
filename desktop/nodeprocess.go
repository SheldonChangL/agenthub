package main

import (
	"bytes"
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
// one beside this app, detached, with its output going to a file, on the
// arguments the stopped one was started with.
func (a *App) restartNodeProcess() (ServiceResult, error) {
	nodeClient, nodeURL := a.current()
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

	// Everything that can refuse, refuses here, before anything is stopped: a
	// refusal after the stop leaves the owner with no node at all (#205).
	listCtx, cancelList := context.WithTimeout(a.ctx, nodeListTimeout)
	plan, running, err := planFromRunningNodes(listCtx)
	cancelList()
	if err != nil {
		return ServiceResult{}, err
	}

	ctx, cancel := context.WithTimeout(a.ctx, nodeStopTimeout+nodeStartTimeout+10*time.Second)
	defer cancel()

	// Who is answering now, so that what answers afterwards can be checked
	// against it. A node that answers and will not say who it is gives nothing
	// to check against, so that is a refusal too. A node that is not answering
	// at all (hung, or not running) has no identity on offer: restarting it on
	// its own arguments is what the owner asked for, but only when those name
	// its database for certain — planNodeRestart read it from the node's own
	// command line or environment — because afterwards there is nothing to
	// compare the node that answers with.
	before, err := readNodeIdentity(ctx, nodeClient)
	if err != nil {
		if probeNode(ctx, nodeURL) {
			return ServiceResult{}, fmt.Errorf(
				"could not read which node is answering on %s (%v), so there would be no way to tell afterwards "+
					"whether the node that came back is the same one; it was not restarted", nodeURL, err)
		}
		if running != nil && !plan.confirmed {
			return ServiceResult{}, fmt.Errorf(
				"the running node (pid %d) is not answering, so who it is cannot be checked after a restart, and "+
					"its database %s is only this app's inference (from %s), not read from the node; it was not "+
					"restarted: stop it and start it again with --db naming its database", running.PID, plan.db, plan.dbFrom)
		}
		before = NodeIdentity{}
	}

	result := ServiceResult{Command: commandLine(binary, plan.args) + " (stop, then start)"}
	var steps []string
	say := func(format string, args ...any) {
		steps = append(steps, fmt.Sprintf(format, args...))
		result.Output = strings.Join(steps, "\n")
	}
	if running != nil {
		say("the running node (pid %d) was started as %s, on the database %s (from %s)",
			running.PID, commandLine(running.program(), running.args()), plan.db, plan.dbFrom)
	}

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
	// The port closing is not the process ending: a node on its way out has
	// shut its listener and may still hold the database, and one that was hung
	// was never answering in the first place. The replacement opens the same
	// database, so it waits for the process itself.
	if running != nil {
		if err := waitForProcessGone(ctx, running.PID); err != nil {
			return result, fmt.Errorf("agenthub-node (pid %d) was asked to stop and has not exited (%v), so it was "+
				"not restarted: a second node on the same database would fail to start and leave nothing running",
				running.PID, err)
		}
	}
	say("stopped the node answering on %s", nodeURL)

	if err := startNode(binary, logPath, plan.args); err != nil {
		return result, fmt.Errorf("start %s: %w", commandLine(binary, plan.args), err)
	}
	say("started %s (log: %s)", commandLine(binary, plan.args), logPath)
	if !waitForNodeAnswering(ctx, nodeURL) {
		return result, fmt.Errorf(
			"the node was started but is not answering on %s after %s; the settings it has just been "+
				"given are the first thing to suspect, and the log says which: %s",
			nodeURL, nodeStartTimeout, logPath)
	}
	say("the node is answering on %s", nodeURL)

	// Answering is not the same as being the node that was stopped. A node
	// that came back on another database answers just as well, as someone
	// else: a new id, a new key, and no paired machine that knows it.
	after, err := readNodeIdentity(ctx, nodeClient)
	if err != nil {
		return result, fmt.Errorf(
			"the node is answering on %s but would not say which node it is (%v), so this app cannot "+
				"confirm it came back as the node that was stopped", nodeURL, err)
	}
	if before.ID != "" && (after.ID != before.ID || (before.Fingerprint != "" && after.Fingerprint != before.Fingerprint)) {
		recovery := "start it again the way it was started"
		if running != nil {
			recovery = "stop this one and start the old one again, which names every path it used: " +
				commandLine(running.program(), plan.recovery)
		}
		return result, fmt.Errorf(
			"the node that came back is %s, not %s, which was running before: it opened a different database, "+
				"which is a different identity that no paired machine recognises. Nothing was deleted — the old "+
				"node's database is where it was; %s", after.ID, before.ID, recovery)
	}
	switch {
	case before.ID != "":
		say("it is %s, the node that was stopped", after.ID)
	case running != nil:
		// Not silent: nothing could be compared, and the owner is told what
		// the restart rests on instead.
		say("it was not answering before the restart, so there was no id to compare; it came back as %s, "+
			"on the database %s (from %s)", after.ID, plan.db, plan.dbFrom)
	}
	return result, nil
}

// nodeProcess is one agenthub-node running on this machine and the command
// line it was started with, argv[0] included.
type nodeProcess struct {
	PID  int
	Argv []string
	// Env is the environment the process runs with, and EnvErr why it could
	// not be read; Env means nothing when EnvErr is set. It is where the node
	// found every path its command line does not name (nodeenv.go).
	Env    []string
	EnvErr error
	// UserErr is nil when the process is known to run as this app's user, and
	// otherwise says why that is not known. The macOS and Linux listings only
	// return this user's processes; on Windows it is read from the process.
	UserErr error
}

func (p nodeProcess) program() string {
	if len(p.Argv) == 0 {
		return nodeExecutable
	}
	return p.Argv[0]
}

func (p nodeProcess) args() []string {
	if len(p.Argv) < 2 {
		return nil
	}
	return p.Argv[1:]
}

// planFromRunningNodes decides what the restarted node is started with, from
// the one that is running, and refuses when that cannot be known.
//
// Nothing running is the first start: the node's defaults are what this app
// has always started, and there is no identity to lose. More than one is a
// machine this restart would stop entirely and bring back as one, so it is
// left alone. Exactly one is started again with its own arguments
// (planNodeRestart), and when those cannot be read the restart does not guess
// — a guess is the default database, which for a node started with --db is a
// new identity and every pairing gone.
func planFromRunningNodes(ctx context.Context) (nodeRestartPlan, *nodeProcess, error) {
	refuse := func(reason string) error {
		return fmt.Errorf("%s, so this app will not restart it: stop and start the node the way it was started", reason)
	}
	processes, err := listNodes(ctx)
	if err != nil {
		return nodeRestartPlan{}, nil, refuse(fmt.Sprintf(
			"could not read how the running node was started (%v), and a node started without its --db "+
				"would be a different node", err))
	}
	switch len(processes) {
	case 0:
		return nodeRestartPlan{}, nil, nil
	case 1:
	default:
		pids := make([]string, 0, len(processes))
		for _, process := range processes {
			pids = append(pids, fmt.Sprint(process.PID))
		}
		return nodeRestartPlan{}, nil, refuse(fmt.Sprintf(
			"%d agenthub-node processes are running (pids %s), and stopping by name stops all of them "+
				"to bring back one", len(processes), strings.Join(pids, ", ")))
	}
	running := processes[0]
	plan, err := planNodeRestart(running, appPaths)
	if err != nil {
		return nodeRestartPlan{}, nil, refuse(fmt.Sprintf("the running node (pid %d) cannot be started again as it is: %v",
			running.PID, err))
	}
	return plan, &running, nil
}

// nodeListTimeout bounds reading the running node's command line. Windows
// answers it through PowerShell, which can take seconds to start cold.
var nodeListTimeout = 30 * time.Second

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

// waitForProcessGone waits until no agenthub-node with this pid is listed,
// for as long as a stop is given, and says why not when it is still there.
// A listing that fails is asked again: a process on its way out can be caught
// half-gone, which some platforms answer with an error.
func waitForProcessGone(ctx context.Context, pid int) error {
	deadline := time.Now().Add(nodeStopTimeout)
	for {
		listCtx, cancel := context.WithTimeout(ctx, nodeListTimeout)
		processes, err := listNodes(listCtx)
		cancel()
		if err == nil {
			still := false
			for _, process := range processes {
				still = still || process.PID == pid
			}
			if !still {
				return nil
			}
			err = fmt.Errorf("it is still running after %s", nodeStopTimeout)
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nodePollInterval):
		}
	}
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

// The things this restart does to the machine, behind variables: the
// tests assert the sequence — stop, wait for gone, start, wait for answering —
// without killing anything on the machine running them, binding the node's
// port (which on a developer's machine belongs to their own node), or creating
// directories in their home.
//
// listNodes, readNodeIdentity and appPaths are the three things it reads from
// the machine to decide what to start: which nodes are running and how they
// were started, who is answering, and where a node with this app's environment
// would put its paths.
var (
	stopNode         = stopNodeProcesses
	startNode        = startNodeDetached
	probeNode        = nodeAnswers
	logDir           = nodeLogDir
	listNodes        = listNodeProcesses
	readNodeIdentity = func(ctx context.Context, c *client) (NodeIdentity, error) { return c.node(ctx) }
	appPaths         = appDefaultPaths
)

// runCommand executes one command and returns its combined output. A variable
// so tests can assert what would have been run without running it.
var runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
	// #nosec G204 -- name and args are this file's own constants; nothing the
	// window or the network supplies reaches here.
	output, err := quietly(exec.CommandContext(ctx, name, args...)).CombinedOutput()
	return string(output), err
}

// runCommandOutput executes one command and returns what it wrote to stdout
// and to stderr apart, for a command whose stdout is data: a stderr line mixed
// into it would be read as part of the answer.
var runCommandOutput = func(ctx context.Context, name string, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	// #nosec G204 -- name and args are the callers' own constants and an
	// encoded script of their own; nothing the window or the network supplies
	// reaches here.
	command := quietly(exec.CommandContext(ctx, name, args...))
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}
