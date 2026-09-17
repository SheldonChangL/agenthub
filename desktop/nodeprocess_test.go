package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRestart replaces the three things a restart does to the machine. What
// the tests below assert is the sequence, which is where the damage lives: a
// node started before the old one is gone fails to bind the port and exits,
// leaving nothing running and a window saying it restarted.
type fakeRestart struct {
	steps    []string
	answers  []bool // what the probe says, one per call, last value repeating
	probes   int
	startErr error
}

func (f *fakeRestart) install(t *testing.T) {
	t.Helper()
	stopWas, startWas, probeWas := stopNode, startNode, probeNode
	t.Cleanup(func() { stopNode, startNode, probeNode = stopWas, startWas, probeWas })
	stopNode = func(context.Context) (string, error) {
		f.steps = append(f.steps, "stop")
		return "SUCCESS: the process agenthub-node.exe has been terminated.", nil
	}
	startNode = func(binary, logPath string) error {
		f.steps = append(f.steps, "start "+binary)
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
	want := []string{"stop", "probe:gone", "start " + node, "probe:answering"}
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
