package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeRestart replaces the things a restart does to the machine and reads
// from it. What the tests below assert is the sequence, which is where the
// damage lives: a node started before the old one is gone fails to bind the
// port and exits, leaving nothing running and a window saying it restarted;
// and a node started without the --db the old one had is a different node,
// answering as if nothing had happened (#205).
type fakeRestart struct {
	steps    []string
	answers  []bool // what the probe says, one per call, last value repeating
	probes   int
	startErr error

	// running is what listNodes finds until the stop; after it, nothing,
	// unless survivesStop. A process with neither Env nor EnvErr is given
	// nodeEnv, an environment whose default database exists.
	running      []nodeProcess
	listErr      error
	survivesStop bool
	stopped      bool
	// identities is what readNodeIdentity answers, one per call, the last
	// repeating; a zero NodeIdentity is a failed read.
	identities []NodeIdentity
	reads      int
	started    [][]string
	// appDB is the database appPaths names — this app's environment's
	// default — made to exist unless a test sets it.
	appDB string

	nodeEnv   []string
	nodePaths nodePaths
}

var sameNode = NodeIdentity{ID: "node_same", Fingerprint: "AAAA BBBB CCCC DDDD EEEE FFFF"}

func (f *fakeRestart) install(t *testing.T) {
	t.Helper()
	stopWas, startWas, probeWas := stopNode, startNode, probeNode
	listWas, identityWas, pathsWas := listNodes, readNodeIdentity, appPaths
	t.Cleanup(func() {
		stopNode, startNode, probeNode = stopWas, startWas, probeWas
		listNodes, readNodeIdentity, appPaths = listWas, identityWas, pathsWas
	})
	if f.identities == nil {
		f.identities = []NodeIdentity{sameNode}
	}
	if f.appDB == "" {
		f.appDB = existingFile(t, "app default", "agenthub.db")
	}
	f.nodeEnv, f.nodePaths = nodeEnvironment(t)
	for index := range f.running {
		if f.running[index].Env == nil && f.running[index].EnvErr == nil {
			f.running[index].Env = f.nodeEnv
		}
	}
	appHome := filepath.Dir(f.appDB)
	appPaths = func() (nodePaths, error) {
		return nodePaths{database: f.appDB, claude: filepath.Join(appHome, ".claude"), codex: filepath.Join(appHome, ".codex")}, nil
	}
	stopNode = func(context.Context) (string, error) {
		f.steps = append(f.steps, "stop")
		f.stopped = true
		return "SUCCESS: the process agenthub-node.exe has been terminated.", nil
	}
	startNode = func(binary, logPath string, args []string) error {
		f.steps = append(f.steps, "start "+binary)
		f.started = append(f.started, args)
		return f.startErr
	}
	probeNode = func(context.Context, string) bool {
		answer := false
		if len(f.answers) > 0 {
			if f.probes < len(f.answers) {
				answer = f.answers[f.probes]
			} else {
				answer = f.answers[len(f.answers)-1]
			}
		}
		f.probes++
		f.steps = append(f.steps, boolStep(answer))
		return answer
	}
	listNodes = func(context.Context) ([]nodeProcess, error) {
		f.steps = append(f.steps, "list")
		if f.stopped && !f.survivesStop {
			return nil, nil
		}
		return f.running, f.listErr
	}
	readNodeIdentity = func(context.Context, *client) (NodeIdentity, error) {
		identity := f.identities[len(f.identities)-1]
		if f.reads < len(f.identities) {
			identity = f.identities[f.reads]
		}
		f.reads++
		f.steps = append(f.steps, "identity")
		if identity.ID == "" {
			return NodeIdentity{}, errors.New("connection refused")
		}
		return identity, nil
	}
}

// nodeEnvironment is the environment of a node started from somewhere this
// app was not: a home and a config directory of its own, which on Linux is
// XDG_CONFIG_HOME and on macOS HOME. Its default database is made to exist,
// and is not this app's.
func nodeEnvironment(t *testing.T) ([]string, nodePaths) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "node home")
	config := filepath.Join(home, "set by the shell")
	env := []string{"PATH=/usr/bin", "HOME=" + home, "USERPROFILE=" + home, "XDG_CONFIG_HOME=" + config, "APPDATA=" + config}
	paths, err := nodeDefaultPaths(runtime.GOOS, environmentLookup(runtime.GOOS, env))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.database), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.database, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	return env, paths
}

// existingFile makes a file under a fresh directory whose name has a space in
// it, the way macOS's own Application Support does.
func existingFile(t *testing.T, dir, name string) string {
	t.Helper()
	full := filepath.Join(t.TempDir(), dir+" dir")
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(full, name)
	if err := os.WriteFile(path, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// stoppedOrStarted reports whether the fake was asked to stop or start
// anything, which a refusal must not have been.
func (f *fakeRestart) stoppedOrStarted() bool {
	for _, step := range f.steps {
		if step == "stop" || strings.HasPrefix(step, "start ") {
			return true
		}
	}
	return false
}

func boolStep(answering bool) string {
	if answering {
		return "probe:answering"
	}
	return "probe:gone"
}

func restartApp(t *testing.T) *App {
	t.Helper()
	// The waits are the behaviour under test only in that they end; how long
	// they would take on a real machine is not something to spend here.
	startWas, stopWas, pollWas := nodeStartTimeout, nodeStopTimeout, nodePollInterval
	t.Cleanup(func() {
		nodeStartTimeout, nodeStopTimeout, nodePollInterval = startWas, stopWas, pollWas
	})
	nodeStartTimeout, nodeStopTimeout, nodePollInterval = 40*time.Millisecond, 40*time.Millisecond, time.Millisecond
	dirWas := logDir
	t.Cleanup(func() { logDir = dirWas })
	temporary := t.TempDir()
	logDir = func() (string, error) { return temporary, nil }
	app := NewApp()
	app.ctx = context.Background()
	// NewApp reads AGENTHUB_URL, and a developer's environment may point it
	// somewhere else; this is a test about the restart, not about that.
	app.url = defaultNodeURL
	app.client = newClient(defaultNodeURL)
	return app
}

func TestRestartNodeStopsBeforeItStarts(t *testing.T) {
	node := withFakeNode(t)
	fake := &fakeRestart{answers: []bool{false, true}}
	fake.install(t)

	result, err := restartAppProcess(t)
	if err != nil {
		t.Fatalf("restart: %v\n%s", err, result.Output)
	}
	want := []string{"list", "identity", "stop", "probe:gone", "start " + node, "probe:answering", "identity"}
	if strings.Join(fake.steps, ",") != strings.Join(want, ",") {
		t.Errorf("sequence = %v, want %v", fake.steps, want)
	}
	for _, phrase := range []string{"stopped the node", "started ", "is answering on"} {
		if !strings.Contains(result.Output, phrase) {
			t.Errorf("output does not account for %q:\n%s", phrase, result.Output)
		}
	}
}

// The node that would not die. Starting a second one on the same database is
// how a restart turns into an outage, so this has to be a failure and not a
// step that is merely skipped.
func TestRestartNodeRefusesToStartASecondNode(t *testing.T) {
	withFakeNode(t)
	fake := &fakeRestart{answers: []bool{true}}
	fake.install(t)

	_, err := restartAppProcess(t)
	if err == nil {
		t.Fatal("a node that never stopped answering was restarted anyway")
	}
	if !strings.Contains(err.Error(), "still answering") {
		t.Errorf("error does not say what went wrong: %v", err)
	}
	for _, step := range fake.steps {
		if strings.HasPrefix(step, "start ") {
			t.Fatalf("a second node was started over a live one: %v", fake.steps)
		}
	}
}

// A node that was stopped and did not come back is the worst outcome here, and
// the one the owner most needs told: the settings just saved are why.
func TestRestartNodeSaysWhenTheNodeDoesNotComeBack(t *testing.T) {
	withFakeNode(t)
	fake := &fakeRestart{answers: []bool{false}}
	fake.install(t)

	result, err := restartAppProcess(t)
	if err == nil {
		t.Fatal("a node that never answered was reported as restarted")
	}
	if !strings.Contains(err.Error(), "not answering") || !strings.Contains(err.Error(), "node.log") {
		t.Errorf("error should say it is not answering and where to look: %v", err)
	}
	// The steps that did happen are still the owner's account of what was done
	// to their machine, so a failure keeps them.
	if !strings.Contains(result.Output, "stopped the node") {
		t.Errorf("a failed restart threw away what it had already done:\n%s", result.Output)
	}
}

func TestRestartNodeReportsAFailedStart(t *testing.T) {
	withFakeNode(t)
	fake := &fakeRestart{answers: []bool{false}, startErr: errors.New("access is denied")}
	fake.install(t)

	if _, err := restartAppProcess(t); err == nil || !strings.Contains(err.Error(), "access is denied") {
		t.Errorf("err = %v, want the reason the start failed", err)
	}
}

// Pointed at someone else's node, this restart would stop a process it did not
// start and start a different one in its place.
func TestRestartNodeWillNotRestartANodeItDidNotStart(t *testing.T) {
	withFakeNode(t)
	fake := &fakeRestart{answers: []bool{false, true}}
	fake.install(t)

	app := restartApp(t)
	app.url = "http://127.0.0.1:9999"
	_, err := app.restartNodeProcess()
	if err == nil || !strings.Contains(err.Error(), "9999") {
		t.Fatalf("err = %v, want a refusal naming the node this window is pointed at", err)
	}
	if len(fake.steps) != 0 {
		t.Errorf("it touched the machine anyway: %v", fake.steps)
	}
}

func restartAppProcess(t *testing.T) (ServiceResult, error) {
	t.Helper()
	return restartApp(t).restartNodeProcess()
}

// #205: a node started by hand with --db is started again with that --db. The
// settings the node remembers are left off, because they are what the restart
// is applying: passed back, the old --peer-listen would win over the save that
// asked for this restart. Everything else goes back as it was, and the one path
// it did not name — its Codex root — is named, as its own environment put it.
func TestRestartNodeStartsItOnTheDatabaseItWasStartedWith(t *testing.T) {
	node := withFakeNode(t)
	database := existingFile(t, "hand", "mine.db")
	fake := &fakeRestart{
		answers: []bool{false, true},
		running: []nodeProcess{{PID: 4242, Argv: []string{
			"/usr/local/bin/agenthub-node", "--db", database, "-peer-listen", "192.168.1.5:7463", "--allow-lan",
			"-claude-root=/Users/me/.claude", "-display-name", "-looks-like-a-flag", "-auto-wake=false",
		}}},
	}
	fake.install(t)

	result, err := restartAppProcess(t)
	if err != nil {
		t.Fatalf("restart: %v\n%s", err, result.Output)
	}
	want := []string{"--db", database, "-claude-root=/Users/me/.claude", "-display-name", "-looks-like-a-flag",
		"--codex-root", fake.nodePaths.codex}
	if len(fake.started) != 1 || !slices.Equal(fake.started[0], want) {
		t.Fatalf("started with %q, want %q", fake.started, want)
	}
	want = []string{"list", "identity", "stop", "probe:gone", "list", "start " + node, "probe:answering", "identity"}
	if !slices.Equal(fake.steps, want) {
		t.Errorf("sequence = %v, want %v", fake.steps, want)
	}
	for _, phrase := range []string{database, "pid 4242", "the node that was stopped"} {
		if !strings.Contains(result.Output, phrase) {
			t.Errorf("output does not say %q:\n%s", phrase, result.Output)
		}
	}
}

// The node the app starts itself — no arguments, the default database — is
// started again on that same database, named: the path its own environment
// gave it, not one this app works out from its own.
func TestRestartNodeOnTheDefaultDatabaseNamesIt(t *testing.T) {
	withFakeNode(t)
	fake := &fakeRestart{
		answers: []bool{false, true},
		running: []nodeProcess{{PID: 7, Argv: []string{"/Applications/agenthub-desktop.app/Contents/MacOS/agenthub-node"}}},
	}
	fake.install(t)

	result, err := restartAppProcess(t)
	if err != nil {
		t.Fatalf("restart: %v\n%s", err, result.Output)
	}
	want := []string{"--db", fake.nodePaths.database, "--claude-root", fake.nodePaths.claude, "--codex-root", fake.nodePaths.codex}
	if len(fake.started) != 1 || !slices.Equal(fake.started[0], want) {
		t.Errorf("started with %q, want %q", fake.started, want)
	}
	if !strings.Contains(result.Output, fake.nodePaths.database) || !strings.Contains(result.Output, "its own environment") {
		t.Errorf("output does not say which database, or where that came from:\n%s", result.Output)
	}
}

// The case the review found: a node started from a shell whose environment
// put its default database somewhere else (XDG_CONFIG_HOME on Linux, HOME on
// macOS), restarted from a window whose environment has a default database of
// its own that also exists. The restart has to follow the node's, or it brings
// up the other identity and nothing on screen says so.
func TestRestartNodeFollowsTheNodesEnvironmentNotTheApps(t *testing.T) {
	withFakeNode(t)
	fake := &fakeRestart{
		answers: []bool{false, true},
		running: []nodeProcess{{PID: 8, Argv: []string{"/usr/bin/agenthub-node"}}},
	}
	fake.install(t)
	if fake.appDB == fake.nodePaths.database {
		t.Fatal("the fixture gives the app and the node the same database; the test would prove nothing")
	}

	if result, err := restartAppProcess(t); err != nil {
		t.Fatalf("restart: %v\n%s", err, result.Output)
	}
	if len(fake.started) != 1 || !slices.Contains(fake.started[0], fake.nodePaths.database) {
		t.Fatalf("started with %q, want --db %s, the node's own default", fake.started, fake.nodePaths.database)
	}
	if slices.Contains(fake.started[0], fake.appDB) {
		t.Errorf("started on this app's default database %s: %q", fake.appDB, fake.started[0])
	}
}

// Answering is not being the same node. One that came back as someone else is
// a failure, named with both ids and the command that brings the old one back
// — which names its database, so it works typed into any shell.
func TestRestartNodeReportsANodeThatCameBackAsSomeoneElse(t *testing.T) {
	for name, after := range map[string]NodeIdentity{
		"another id":  {ID: "node_new", Fingerprint: sameNode.Fingerprint},
		"another key": {ID: sameNode.ID, Fingerprint: "1111 2222 3333 4444 5555 6666"},
	} {
		t.Run(name, func(t *testing.T) {
			withFakeNode(t)
			fake := &fakeRestart{
				answers:    []bool{false, true},
				running:    []nodeProcess{{PID: 9, Argv: []string{"/opt/agenthub-node", "-peer-listen", "10.0.0.1:7463"}}},
				identities: []NodeIdentity{sameNode, after},
			}
			fake.install(t)

			result, err := restartAppProcess(t)
			if err == nil {
				t.Fatalf("a node that came back as %+v was reported as restarted:\n%s", after, result.Output)
			}
			recovery := commandLine("/opt/agenthub-node", []string{"-peer-listen", "10.0.0.1:7463",
				"--db", fake.nodePaths.database, "--claude-root", fake.nodePaths.claude, "--codex-root", fake.nodePaths.codex})
			for _, phrase := range []string{after.ID, sameNode.ID, "different database", recovery} {
				if !strings.Contains(err.Error(), phrase) {
					t.Errorf("error does not say %q: %v", phrase, err)
				}
			}
		})
	}
}

// On Windows, when this app's environment stood in for the node's, the node
// that came back as someone else came back on the paths this app inferred —
// so those are the one thing the way back must not name. It is the old
// command line as it was, to be run where the old node was first started.
func TestRestartNodeDoesNotHandBackAnInferredPathAsTheWayBack(t *testing.T) {
	withFakeNode(t)
	withEnvironmentFallback(t, true)
	fake := &fakeRestart{
		answers: []bool{false, true},
		running: []nodeProcess{{PID: 22, Argv: []string{`C:\agenthub\agenthub-node.exe`, "-discover"},
			EnvErr: errors.New("access is denied")}},
		identities: []NodeIdentity{sameNode, {ID: "node_new", Fingerprint: sameNode.Fingerprint}},
	}
	fake.install(t)

	result, err := restartAppProcess(t)
	if err == nil {
		t.Fatalf("a node that came back as someone else was reported as restarted:\n%s", result.Output)
	}
	if len(fake.started) != 1 || !slices.Contains(fake.started[0], fake.appDB) {
		t.Fatalf("started with %q; the fixture should have used this app's database %s", fake.started, fake.appDB)
	}
	if strings.Contains(err.Error(), fake.appDB) || strings.Contains(err.Error(), filepath.Dir(fake.appDB)) {
		t.Errorf("the way back names the inferred path %s, which is what brought up node_new: %v", fake.appDB, err)
	}
	for _, phrase := range []string{
		"node_new", sameNode.ID, commandLine(`C:\agenthub\agenthub-node.exe`, []string{"-discover"}),
		"environment it was first started in", "--db, --claude-root, --codex-root", "inferred",
	} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("error does not say %q: %v", phrase, err)
		}
	}
}

// Whatever cannot be known about the running node is found out before it is
// stopped. A refusal after the stop is the outage this exists to prevent.
func TestRestartNodeRefusesBeforeStoppingWhatItCannotStartAgain(t *testing.T) {
	cases := []struct {
		name     string
		running  func(t *testing.T) []nodeProcess
		listErr  error
		fallback bool
		want     string
	}{
		{name: "command line unreadable", listErr: errors.New("operation not permitted"), want: "operation not permitted"},
		{name: "two nodes", want: "2 agenthub-node processes", running: func(*testing.T) []nodeProcess {
			return []nodeProcess{{PID: 1, Argv: []string{"agenthub-node"}}, {PID: 2, Argv: []string{"agenthub-node"}}}
		}},
		{name: "relative database", want: "relative path", running: func(*testing.T) []nodeProcess {
			return []nodeProcess{{PID: 3, Argv: []string{"agenthub-node", "--db", "data/agenthub.db"}}}
		}},
		{name: "database not there", want: "is not there", running: func(t *testing.T) []nodeProcess {
			return []nodeProcess{{PID: 4, Argv: []string{"agenthub-node", "--db", filepath.Join(t.TempDir(), "gone.db")}}}
		}},
		{name: "default database not there", want: "is not there", running: func(t *testing.T) []nodeProcess {
			home := t.TempDir()
			return []nodeProcess{{PID: 5, Argv: []string{"agenthub-node"},
				Env: []string{"HOME=" + home, "USERPROFILE=" + home, "APPDATA=" + home}}}
		}},
		{name: "unknown flag", want: "-from-the-future", running: func(t *testing.T) []nodeProcess {
			return []nodeProcess{{PID: 6, Argv: []string{"agenthub-node", "-from-the-future", "x"}}}
		}},
		{name: "environment unreadable", want: "environment could not be read", running: func(t *testing.T) []nodeProcess {
			return []nodeProcess{{PID: 10, Argv: []string{"agenthub-node"}, EnvErr: errors.New("permission denied")}}
		}},
		{name: "environment unreadable, only the roots missing", want: "-claude-root, -codex-root", running: func(t *testing.T) []nodeProcess {
			database := existingFile(t, "hand", "mine.db")
			return []nodeProcess{{PID: 11, Argv: []string{"agenthub-node", "--db", database}, EnvErr: errors.New("permission denied")}}
		}},
		{name: "environment without a home", want: "HOME", running: func(t *testing.T) []nodeProcess {
			return []nodeProcess{{PID: 12, Argv: []string{"agenthub-node"}, Env: []string{"PATH=/usr/bin"}}}
		}},
		{name: "a relative home", want: "relative path", running: func(t *testing.T) []nodeProcess {
			return []nodeProcess{{PID: 13, Argv: []string{"agenthub-node"},
				Env: []string{"HOME=home", "USERPROFILE=home", "APPDATA=home"}}}
		}},
		{name: "listening elsewhere", want: "-listen 127.0.0.1:7999", running: func(t *testing.T) []nodeProcess {
			database := existingFile(t, "hand", "mine.db")
			return []nodeProcess{{PID: 14, Argv: []string{"agenthub-node", "--db", database, "-listen", "127.0.0.1:7999"}}}
		}},
		{name: "fallback, another user", fallback: true, want: "runs as S-1-5-21-2", running: func(t *testing.T) []nodeProcess {
			return []nodeProcess{{PID: 15, Argv: []string{"agenthub-node"}, EnvErr: errors.New("access denied"),
				UserErr: errors.New("pid 15 runs as S-1-5-21-2, and this app as S-1-5-21-1")}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFakeNode(t)
			withEnvironmentFallback(t, tc.fallback)
			fake := &fakeRestart{answers: []bool{false, true}, listErr: tc.listErr}
			if tc.running != nil {
				fake.running = tc.running(t)
			}
			fake.install(t)

			_, err := restartAppProcess(t)
			if err == nil {
				t.Fatalf("restarted a node it could not start again as it was: started %q", fake.started)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "will not restart it") {
				t.Errorf("error = %v, want it to say %q and that it will not restart it", err, tc.want)
			}
			if fake.stoppedOrStarted() {
				t.Errorf("refused only after touching the node: %v", fake.steps)
			}
		})
	}
}

func withEnvironmentFallback(t *testing.T, on bool) {
	t.Helper()
	was := nodeEnvironmentFallback
	t.Cleanup(func() { nodeEnvironmentFallback = was })
	nodeEnvironmentFallback = on
}

// Windows, where the node's environment may not be readable: this app's own
// default stands in, for a node of this app's user, and only while the node
// can be asked afterwards who it is.
func TestRestartNodeLetsTheAppsEnvironmentStandInOnlyWhereItMayAndIsChecked(t *testing.T) {
	unreadable := func() []nodeProcess {
		return []nodeProcess{{PID: 21, Argv: []string{`C:\agenthub\agenthub-node.exe`}, EnvErr: errors.New("access is denied")}}
	}

	withFakeNode(t)
	withEnvironmentFallback(t, true)
	fake := &fakeRestart{answers: []bool{false, true}, running: unreadable()}
	fake.install(t)
	result, err := restartAppProcess(t)
	if err != nil {
		t.Fatalf("restart: %v\n%s", err, result.Output)
	}
	if len(fake.started) != 1 || !slices.Contains(fake.started[0], fake.appDB) {
		t.Fatalf("started with %q, want --db %s", fake.started, fake.appDB)
	}
	if !strings.Contains(result.Output, "this app's environment") || !strings.Contains(result.Output, "the node that was stopped") {
		t.Errorf("output does not say the database was this app's inference, checked by id:\n%s", result.Output)
	}

	// The same node, not answering: an inferred database with nothing to
	// check it against afterwards is not restarted.
	hung := &fakeRestart{answers: []bool{false}, running: unreadable(), identities: []NodeIdentity{{}, sameNode}}
	hung.install(t)
	if _, err := restartAppProcess(t); err == nil || !strings.Contains(err.Error(), "not answering") {
		t.Errorf("err = %v, want a refusal: a hung node on an inferred database", err)
	}
	if hung.stoppedOrStarted() {
		t.Errorf("stopped a hung node on an inferred database: %v", hung.steps)
	}

	// Off — macOS and Linux — the same unreadable environment is a refusal.
	withEnvironmentFallback(t, false)
	off := &fakeRestart{answers: []bool{false, true}, running: unreadable()}
	off.install(t)
	if _, err := restartAppProcess(t); err == nil || off.stoppedOrStarted() {
		t.Errorf("err = %v, steps %v; want a refusal before anything is touched", err, off.steps)
	}
}

// A node that answers but will not say who it is leaves nothing to check the
// restarted one against, so it is not restarted. One that is not answering at
// all — hung — is restarted on its own arguments, which is the point of the
// button on a dead node; with nothing to compare, the output says so and what
// it rested on instead.
func TestRestartNodeNeedsToKnowWhoIsAnswering(t *testing.T) {
	withFakeNode(t)
	fake := &fakeRestart{answers: []bool{true}, identities: []NodeIdentity{{}}}
	fake.install(t)
	if _, err := restartAppProcess(t); err == nil || !strings.Contains(err.Error(), "could not read which node") {
		t.Errorf("err = %v, want a refusal that says the answering node would not say who it is", err)
	}
	if fake.stoppedOrStarted() {
		t.Errorf("stopped a node it could not identify: %v", fake.steps)
	}

	hung := &fakeRestart{
		answers:    []bool{false, false, true},
		identities: []NodeIdentity{{}, sameNode},
		running:    []nodeProcess{{PID: 31, Argv: []string{"/usr/bin/agenthub-node"}}},
	}
	hung.install(t)
	result, err := restartAppProcess(t)
	if err != nil {
		t.Fatalf("a hung node was not restarted: %v\n%s", err, result.Output)
	}
	if !strings.Contains(result.Output, "no id to compare") || !strings.Contains(result.Output, hung.nodePaths.database) {
		t.Errorf("a restart with nothing to compare did not say so, and on what database:\n%s", result.Output)
	}
}

// The port closing is not the process exiting. A node that stops answering
// and does not go — hung, or slow to let go of its database — is not joined
// by a second one.
func TestRestartNodeWaitsForTheProcessToExit(t *testing.T) {
	for name, identities := range map[string][]NodeIdentity{
		"answering, then closes the port": {sameNode},
		"hung from the start":             {{}},
	} {
		t.Run(name, func(t *testing.T) {
			withFakeNode(t)
			fake := &fakeRestart{
				answers:      []bool{false},
				identities:   identities,
				running:      []nodeProcess{{PID: 41, Argv: []string{"/usr/bin/agenthub-node"}}},
				survivesStop: true,
			}
			fake.install(t)
			_, err := restartAppProcess(t)
			if err == nil || !strings.Contains(err.Error(), "pid 41") || !strings.Contains(err.Error(), "has not exited") {
				t.Fatalf("err = %v, want it to say pid 41 has not exited", err)
			}
			if len(fake.started) != 0 {
				t.Errorf("started a second node beside one that had not exited: %v", fake.steps)
			}
		})
	}
}

// A listing that catches an agenthub-node on its way out fails — macOS
// answers EINVAL for a process that is exiting and not yet a zombie. That is
// asked again rather than refused at once, and the process is not taken for
// gone: a failure that lasts is a refusal, and a failure of another kind is
// not asked again.
func TestRestartNodeAsksAgainAListingThatCaughtANodeExiting(t *testing.T) {
	attemptsWas, intervalWas := nodeListAttempts, nodeListRetryInterval
	t.Cleanup(func() { nodeListAttempts, nodeListRetryInterval = attemptsWas, intervalWas })
	nodeListRetryInterval = time.Millisecond
	exiting := fmt.Errorf("read the command line of agenthub-node (pid 52): %w", syscall.EINVAL)
	running := func() []nodeProcess { return []nodeProcess{{PID: 51, Argv: []string{"/usr/bin/agenthub-node"}}} }

	withFakeNode(t)
	fake := &fakeRestart{answers: []bool{false, true}, running: running()}
	fake.install(t)
	failures, listed := 2, listNodes
	listNodes = func(ctx context.Context) ([]nodeProcess, error) {
		if failures > 0 {
			failures--
			return nil, exiting
		}
		return listed(ctx)
	}
	result, err := restartAppProcess(t)
	if err != nil {
		t.Fatalf("a listing that failed twice on an exiting process was not asked again: %v\n%s", err, result.Output)
	}
	if len(fake.started) != 1 || !slices.Contains(fake.started[0], fake.nodePaths.database) {
		t.Errorf("started with %q, want --db %s", fake.started, fake.nodePaths.database)
	}

	for name, tc := range map[string]struct {
		err   error
		calls int
	}{
		"exiting, and it lasts": {exiting, nodeListAttempts},
		"another failure":       {errors.New("operation not permitted"), 1},
	} {
		stuck := &fakeRestart{answers: []bool{false, true}, running: running()}
		stuck.install(t)
		calls := 0
		listNodes = func(context.Context) ([]nodeProcess, error) {
			calls++
			return nil, tc.err
		}
		if _, err := restartAppProcess(t); err == nil || !strings.Contains(err.Error(), "will not restart it") {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
		if stuck.stoppedOrStarted() {
			t.Errorf("%s: a listing that did not succeed was taken for no node running: %v", name, stuck.steps)
		}
		if calls != tc.calls {
			t.Errorf("%s: listed %d times, want %d", name, calls, tc.calls)
		}
	}
}

// After the start, a node that answers and will not say who it is has not
// been shown to be the one that was stopped.
func TestRestartNodeDoesNotClaimANodeItCannotIdentify(t *testing.T) {
	withFakeNode(t)
	fake := &fakeRestart{answers: []bool{false, true}, identities: []NodeIdentity{sameNode, {}}}
	fake.install(t)
	if _, err := restartAppProcess(t); err == nil || !strings.Contains(err.Error(), "cannot confirm") {
		t.Errorf("err = %v, want it to say the restarted node could not be confirmed", err)
	}
}

func TestParseNodeArgsReadsTheWayTheNodeDoes(t *testing.T) {
	parsed, rest, err := parseNodeArgs([]string{
		"--db=/a b/x.db", "-listen", "127.0.0.1:7462", "-discover", "--allow-lan=false", "-display-name", "--db", "stop", "later",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, argument := range parsed {
		got = append(got, argument.name+"="+argument.value)
	}
	want := []string{"db=/a b/x.db", "listen=127.0.0.1:7462", "discover=true", "allow-lan=false", "display-name=--db"}
	if !slices.Equal(got, want) {
		t.Errorf("parsed %q, want %q", got, want)
	}
	if !slices.Equal(rest, []string{"stop", "later"}) {
		t.Errorf("rest = %q, want parsing to end at the first word that is not a flag", rest)
	}
	for _, bad := range [][]string{{"-db"}, {"-allow-lan=maybe"}, {"---db", "x"}, {"-nope"}} {
		if _, _, err := parseNodeArgs(bad); err == nil {
			t.Errorf("%q was read, want an error: the node would have refused it", bad)
		}
	}
}

// TestCommandOutputHelper is not a test: it is the command
// TestRunCommandOutputKeepsStderrApart runs, writing to both streams the way
// PowerShell writes its answer to stdout and its progress records to stderr.
func TestCommandOutputHelper(t *testing.T) {
	if os.Getenv("AGENTHUB_COMMAND_OUTPUT_HELPER") != "1" {
		t.Skip("helper process")
	}
	fmt.Fprint(os.Stderr, "#< CLIXML\n<Objs><Obj S=\"progress\"/></Objs>\n")
	fmt.Fprint(os.Stdout, `[{"pid":1,"commandLine":"agenthub-node"}]`)
	os.Exit(0)
}

// The Windows process list is JSON on stdout. Anything PowerShell writes to
// stderr — a CLIXML progress record, a warning — is not part of it, and read
// as part of it would make every restart refuse.
func TestRunCommandOutputKeepsStderrApart(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTHUB_COMMAND_OUTPUT_HELPER", "1")
	stdout, stderr, err := runCommandOutput(context.Background(), self, "-test.run=^TestCommandOutputHelper$")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, stdout, stderr)
	}
	if stdout != `[{"pid":1,"commandLine":"agenthub-node"}]` {
		t.Errorf("stdout = %q, want the JSON alone", stdout)
	}
	if !strings.Contains(stderr, "CLIXML") {
		t.Errorf("stderr = %q, want the progress record kept apart", stderr)
	}
}
