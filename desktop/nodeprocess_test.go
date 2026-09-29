package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

	running []nodeProcess // what listNodes finds
	listErr error
	// identities is what readNodeIdentity answers, one per call, the last
	// repeating; a zero NodeIdentity is a failed read.
	identities []NodeIdentity
	reads      int
	started    [][]string
	defaultDB  string
}

var sameNode = NodeIdentity{ID: "node_same", Fingerprint: "AAAA BBBB CCCC DDDD EEEE FFFF"}

func (f *fakeRestart) install(t *testing.T) {
	t.Helper()
	stopWas, startWas, probeWas := stopNode, startNode, probeNode
	listWas, identityWas, defaultWas := listNodes, readNodeIdentity, defaultDB
	t.Cleanup(func() {
		stopNode, startNode, probeNode = stopWas, startWas, probeWas
		listNodes, readNodeIdentity, defaultDB = listWas, identityWas, defaultWas
	})
	if f.identities == nil {
		f.identities = []NodeIdentity{sameNode}
	}
	if f.defaultDB == "" {
		f.defaultDB = existingFile(t, "default", "agenthub.db")
	}
	stopNode = func(context.Context) (string, error) {
		f.steps = append(f.steps, "stop")
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
	defaultDB = func() (string, error) { return f.defaultDB, nil }
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
// asked for this restart. Everything else goes back as it was.
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
	want := []string{"--db", database, "-claude-root=/Users/me/.claude", "-display-name", "-looks-like-a-flag"}
	if len(fake.started) != 1 || !slices.Equal(fake.started[0], want) {
		t.Fatalf("started with %q, want %q", fake.started, want)
	}
	want = []string{"list", "identity", "stop", "probe:gone", "start " + node, "probe:answering", "identity"}
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
// restarted as it always was, with no arguments and no fuss.
func TestRestartNodeOnTheDefaultDatabaseStartsItWithNoArguments(t *testing.T) {
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
	if len(fake.started) != 1 || len(fake.started[0]) != 0 {
		t.Errorf("started with %q, want no arguments", fake.started)
	}
	if !strings.Contains(result.Output, "its default database") {
		t.Errorf("output does not say which database:\n%s", result.Output)
	}
}

// Answering is not being the same node. One that came back as someone else is
// a failure, named with both ids and the command that brings the old one back.
func TestRestartNodeReportsANodeThatCameBackAsSomeoneElse(t *testing.T) {
	for name, after := range map[string]NodeIdentity{
		"another id":  {ID: "node_new", Fingerprint: sameNode.Fingerprint},
		"another key": {ID: sameNode.ID, Fingerprint: "1111 2222 3333 4444 5555 6666"},
	} {
		t.Run(name, func(t *testing.T) {
			withFakeNode(t)
			database := existingFile(t, "hand", "mine.db")
			fake := &fakeRestart{
				answers:    []bool{false, true},
				running:    []nodeProcess{{PID: 9, Argv: []string{"/opt/agenthub-node", "-db", database}}},
				identities: []NodeIdentity{sameNode, after},
			}
			fake.install(t)

			result, err := restartAppProcess(t)
			if err == nil {
				t.Fatalf("a node that came back as %+v was reported as restarted:\n%s", after, result.Output)
			}
			for _, phrase := range []string{after.ID, sameNode.ID, "different database", database} {
				if !strings.Contains(err.Error(), phrase) {
					t.Errorf("error does not say %q: %v", phrase, err)
				}
			}
		})
	}
}

// Whatever cannot be known about the running node is found out before it is
// stopped. A refusal after the stop is the outage this exists to prevent.
func TestRestartNodeRefusesBeforeStoppingWhatItCannotStartAgain(t *testing.T) {
	cases := []struct {
		name    string
		running func(t *testing.T) []nodeProcess
		listErr error
		noDB    bool
		want    string
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
		{name: "default database not there", noDB: true, want: "default database", running: func(*testing.T) []nodeProcess {
			return []nodeProcess{{PID: 5, Argv: []string{"agenthub-node"}}}
		}},
		{name: "unknown flag", want: "-from-the-future", running: func(t *testing.T) []nodeProcess {
			return []nodeProcess{{PID: 6, Argv: []string{"agenthub-node", "-from-the-future", "x"}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFakeNode(t)
			fake := &fakeRestart{answers: []bool{false, true}, listErr: tc.listErr}
			if tc.running != nil {
				fake.running = tc.running(t)
			}
			if tc.noDB {
				fake.defaultDB = filepath.Join(t.TempDir(), "agenthub", "agenthub.db")
			}
			fake.install(t)

			_, err := restartAppProcess(t)
			if err == nil {
				t.Fatal("restarted a node it could not start again as it was")
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

// A node that answers but will not say who it is leaves nothing to check the
// restarted one against, so it is not restarted. One that is not answering at
// all — hung — is restarted on its own arguments, which is the point of the
// button on a dead node.
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

	hung := &fakeRestart{answers: []bool{false, false, true}, identities: []NodeIdentity{{}, sameNode}}
	hung.install(t)
	if result, err := restartAppProcess(t); err != nil {
		t.Errorf("a hung node was not restarted: %v\n%s", err, result.Output)
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
