package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/service"
)

type recordingRunner struct{ calls []string }

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, line)
	// launchd knows no such job, which is what install waits for after bootout.
	if strings.HasPrefix(line, "launchctl print ") {
		return "Could not find service", errors.New("exit status 113")
	}
	return "", nil
}

// useFakeManager points the service command at a manager writing into a temp
// home and driving a recording runner, and restores the real one afterwards.
func useFakeManager(t *testing.T, goos string) (home string, runner *recordingRunner) {
	t.Helper()
	home = t.TempDir()
	runner = &recordingRunner{}
	previous := newServiceManager
	newServiceManager = func() (service.Manager, error) {
		return service.Manager{GOOS: goos, Home: home, UID: "501", Runner: runner, Sleep: func(time.Duration) {}}, nil
	}
	t.Cleanup(func() { newServiceManager = previous })
	return home, runner
}

func fakeNodeBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agenthub-node")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func nodeThatAnswers(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"node_test","displayName":"test box"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestServiceInstallCarriesTheNodesFlagsByTheNodesNames(t *testing.T) {
	home, runner := useFakeManager(t, "darwin")
	node := fakeNodeBinary(t)
	server := nodeThatAnswers(t)

	var stdout, stderr bytes.Buffer
	args := []string{"--url", server.URL, "service", "install",
		"--node-binary", node,
		"--db", "data/agenthub.db", // relative on purpose
		"--peer-listen", "122.122.122.1:7463", "--allow-lan", "--discover",
		"--treat-as-private", "122.122.0.0/16", "--treat-as-private", "10.9.0.0/16",
		"--auto-wake", "--display-name", "the machine on my desk"}
	if code := Run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}

	plist, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", service.Label+".plist"))
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	wantArgs := []string{
		node,
		"--db", filepath.Join(wd, "data", "agenthub.db"),
		"--peer-listen", "122.122.122.1:7463",
		"--display-name", "the machine on my desk",
		"--allow-lan", "--discover",
		"--treat-as-private", "122.122.0.0/16", "--treat-as-private", "10.9.0.0/16",
		"--auto-wake",
	}
	text := string(plist)
	position := 0
	for _, want := range wantArgs {
		needle := "<string>" + want + "</string>"
		index := strings.Index(text[position:], needle)
		if index < 0 {
			t.Fatalf("plist lacks %q in order:\n%s", needle, text)
		}
		position += index + len(needle)
	}
	if !strings.Contains(stdout.String(), "node answering on "+server.URL+" as node_test") {
		t.Errorf("stdout should report the node answering:\n%s", stdout.String())
	}
	if len(runner.calls) != 3 || !strings.HasPrefix(runner.calls[2], "launchctl bootstrap gui/501 ") {
		t.Errorf("manager calls = %v", runner.calls)
	}
}

func TestServiceInstallLeavesUnsetFlagsToTheNode(t *testing.T) {
	home, _ := useFakeManager(t, "linux")
	node := fakeNodeBinary(t)
	server := nodeThatAnswers(t)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "service", "install", "--node-binary", node}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", service.UnitName+".service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "ExecStart="+node+"\n") {
		t.Errorf("an install with no node flags should pass none:\n%s", unit)
	}
}

// The same rule the node applies at startup, applied before a unit exists:
// a bad peer listener must not become a service that crashes on every restart.
func TestServiceInstallRefusesWhatTheNodeWouldRefuse(t *testing.T) {
	home, runner := useFakeManager(t, "darwin")
	node := fakeNodeBinary(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"lan address without --allow-lan", []string{"--peer-listen", "192.168.1.10:7463"}, "allow-lan"},
		{"public address without declaring it private", []string{"--peer-listen", "122.122.122.1:7463", "--allow-lan"}, "private"},
		{"unknown flag", []string{"--peer-listne", "x"}, "peer-listne"},
		{"stray argument", []string{"extra"}, "extra"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"service", "install", "--node-binary", node}, testCase.args...)
			if code := Run(context.Background(), args, &stdout, &stderr); code == 0 {
				t.Fatalf("accepted: stdout = %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), testCase.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr.String(), testCase.want)
			}
		})
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("a refused install wrote into home: %v", entries)
	}
	if len(runner.calls) != 0 {
		t.Errorf("a refused install drove the service manager: %v", runner.calls)
	}
}

func TestServiceInstallWithoutABinaryToRunSaysWhatToPass(t *testing.T) {
	useFakeManager(t, "darwin")
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"service", "install"}, &stdout, &stderr); code == 0 {
		t.Fatal("install without a node binary anywhere was accepted")
	}
	if !strings.Contains(stderr.String(), "--node-binary") {
		t.Errorf("stderr = %q, want the flag to pass", stderr.String())
	}
}

func TestServiceUninstallReportsWhatItLeftAlone(t *testing.T) {
	home, runner := useFakeManager(t, "linux")
	unit := filepath.Join(home, ".config", "systemd", "user", service.UnitName+".service")
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"service", "uninstall"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Error("unit file still present")
	}
	if !strings.Contains(stdout.String(), "not touched") {
		t.Errorf("stdout should say identity and data stay:\n%s", stdout.String())
	}
	if runner.calls[0] != "systemctl --user disable --now "+service.UnitName {
		t.Errorf("first call = %q", runner.calls[0])
	}
}

func TestServiceStatusSeparatesTheServiceFromTheNode(t *testing.T) {
	useFakeManager(t, "darwin")
	// Nothing installed, nothing answering.
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", "http://127.0.0.1:1", "service", "status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "not installed") || !strings.Contains(out, "node: not answering") {
		t.Errorf("status output:\n%s", out)
	}

	// Nothing installed, but a node answering — registered some other way.
	server := nodeThatAnswers(t)
	stdout.Reset()
	if code := Run(context.Background(), []string{"--url", server.URL, "--json", "service", "status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"nodeAnswering":true`) || !strings.Contains(stdout.String(), `"Installed":false`) {
		t.Errorf("json status:\n%s", stdout.String())
	}
}

func TestServiceUnknownSubcommandIsRefused(t *testing.T) {
	useFakeManager(t, "darwin")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"service", "restart"}, &stdout, &stderr); code == 0 {
		t.Fatal("accepted an unknown service command")
	}
	if !strings.Contains(stderr.String(), "install, uninstall or status") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
