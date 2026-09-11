package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
		"--auto-wake"}
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
		"--allow-lan=true", "--discover=true",
		"--treat-as-private", "122.122.0.0/16", "--treat-as-private", "10.9.0.0/16",
		"--auto-wake=true",
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

// An owner who installs with --allow-lan=false is closing a switch, and the
// unit has to carry that. A bare --allow-lan can only turn something on, so
// writing nothing left the remembered value in force while the install said
// the opposite — the one combination where the note and the file disagree, on
// the setting that decides whether this machine serves the network.
func TestServiceInstallWritesABooleanTurnedOff(t *testing.T) {
	home, _ := useFakeManager(t, "linux")
	node := fakeNodeBinary(t)
	server := nodeThatAnswers(t)
	var stdout, stderr bytes.Buffer
	args := []string{"--url", server.URL, "service", "install", "--node-binary", node,
		"--allow-lan=false", "--discover=false", "--auto-wake=false"}
	if code := Run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", service.UnitName+".service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--allow-lan=false", "--discover=false", "--auto-wake=false"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("the unit does not carry %q, so the remembered value stays in force:\n%s", want, unit)
		}
	}
	// And the note describes that same file rather than the command line.
	if !strings.Contains(stdout.String(), "--allow-lan") {
		t.Errorf("the install should say the unit passes --allow-lan:\n%s", stdout.String())
	}
}

// The note is a promise about the unit on disk. A flag given here that reaches
// no unit must not be named in it, or an owner whose setting will not change
// is sent to the wrong explanation.
func TestTheInstallNoteNamesOnlyWhatTheUnitCarries(t *testing.T) {
	if baked := bakedInSettings([]string{"--db", "/tmp/a.db", "--listen", "127.0.0.1:7462"}); len(baked) != 0 {
		t.Errorf("bakedInSettings named %v for a unit carrying no remembered flag", baked)
	}
	baked := bakedInSettings([]string{"--db", "/tmp/a.db", "--peer-listen", "192.168.1.10:7463",
		"--allow-lan=false", "--treat-as-private", "10.9.0.0/16", "--treat-as-private", "10.10.0.0/16"})
	want := []string{"--peer-listen", "--allow-lan", "--treat-as-private"}
	if len(baked) != len(want) {
		t.Fatalf("bakedInSettings = %v, want %v", baked, want)
	}
	for index, name := range want {
		if baked[index] != name {
			t.Fatalf("bakedInSettings = %v, want %v", baked, want)
		}
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
		{"owner API off loopback", []string{"--listen", "0.0.0.0:7462"}, "loopback"},
		{"display name belongs to the node, not the unit", []string{"--display-name", "x"}, "display-name"},
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
	var decoded struct {
		NodeAnswering bool `json:"nodeAnswering"`
		Service       struct {
			Installed bool
		} `json:"service"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("json status is not JSON: %v\n%s", err, stdout.String())
	}
	if !decoded.NodeAnswering || decoded.Service.Installed {
		t.Errorf("json status:\n%s", stdout.String())
	}
}

func TestServiceUnknownSubcommandIsRefused(t *testing.T) {
	useFakeManager(t, "darwin")
	var stdout, stderr bytes.Buffer
	// `restart` used to be the unknown one here and is now a command, so the
	// case needs a word that is still not one.
	if code := Run(context.Background(), []string{"service", "reload"}, &stdout, &stderr); code == 0 {
		t.Fatal("accepted an unknown service command")
	}
	if !strings.Contains(stderr.String(), "install, restart, uninstall or status") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// `ah service restart` is the step between saving a setting and it being in
// effect, so it has to exist as a word the owner types.
func TestServiceRestartRestartsWithoutTouchingTheRegistration(t *testing.T) {
	_, runner := useFakeManager(t, "linux")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"service", "restart"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if len(runner.calls) != 1 || runner.calls[0] != "systemctl --user restart "+service.UnitName {
		t.Fatalf("calls = %v", runner.calls)
	}
	if !strings.Contains(stdout.String(), "restarted") {
		t.Errorf("stdout = %q", stdout.String())
	}
	// An argument means the owner meant something this command does not do;
	// answering it by restarting anyway would be the wrong kind of helpful.
	var out, errOut bytes.Buffer
	if code := Run(context.Background(), []string{"service", "restart", "now"}, &out, &errOut); code == 0 {
		t.Fatal("ah service restart accepted an argument")
	}
}

// The flags still work — units out there were installed with them — but the
// install has to say that a flag in the unit overrides anything saved later,
// or a setting changed from the desktop appears to do nothing.
func TestServiceInstallSaysBakedInFlagsOverrideSavedSettings(t *testing.T) {
	useFakeManager(t, "linux")
	node := nodeThatAnswers(t)
	binary := fakeNodeBinary(t)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--url", node.URL, "service", "install",
		"--db", filepath.Join(t.TempDir(), "agenthub.db"), "--node-binary", binary, "--discover"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"--discover", "overrides anything saved later", "ah settings set"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}

	// And an install that bakes in nothing says nothing about it: a note on
	// every install is a note nobody reads.
	var plain, plainErr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", node.URL, "service", "install",
		"--db", filepath.Join(t.TempDir(), "agenthub.db"), "--node-binary", binary}, &plain, &plainErr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, plainErr.String())
	}
	if strings.Contains(plain.String(), "overrides anything saved later") {
		t.Errorf("a --db-only install still warned about baked-in flags:\n%s", plain.String())
	}
}
