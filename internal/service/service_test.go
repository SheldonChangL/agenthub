package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner records every command and answers from a script keyed on the
// joined command line; anything unscripted succeeds with empty output.
type fakeRunner struct {
	calls   []string
	answers map[string]struct {
		out string
		err error
	}
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if answer, ok := f.answers[line]; ok {
		return answer.out, answer.err
	}
	// Unscripted, launchd knows no such job: print fails, which is what the
	// install path waits for after a bootout.
	if strings.HasPrefix(line, "launchctl print ") {
		return "Could not find service", errors.New("exit status 113")
	}
	return "", nil
}

// noSleep keeps the retry loops from taking real time in tests.
func noSleep(time.Duration) {}

func writeFakeNode(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agenthub-node")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLaunchdPlistCarriesEveryArgumentInOrderAndEscaped(t *testing.T) {
	plist, err := LaunchdPlist(Config{
		NodeBinary: "/opt/agenthub/agenthub-node",
		Args:       []string{"--db", "/Users/me/data/agenthub.db", "--display-name", "desk & <lab>"},
		LogPath:    "/Users/me/Library/Logs/agenthub/node.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(plist)
	wantInOrder := []string{
		"<string>" + Label + "</string>",
		"<string>/opt/agenthub/agenthub-node</string>",
		"<string>--db</string>",
		"<string>/Users/me/data/agenthub.db</string>",
		"<string>--display-name</string>",
		"<string>desk &amp; &lt;lab&gt;</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<string>/Users/me/Library/Logs/agenthub/node.log</string>",
	}
	position := 0
	for _, want := range wantInOrder {
		index := strings.Index(text[position:], want)
		if index < 0 {
			t.Fatalf("plist lacks %q after position %d:\n%s", want, position, text)
		}
		position += index + len(want)
	}
}

func TestSystemdUnitQuotesWhatSystemdWouldMisread(t *testing.T) {
	unit := string(SystemdUnit(Config{
		NodeBinary: "/home/me/agenthub/bin/agenthub-node",
		Args:       []string{"--display-name", "the machine on my desk", "--treat-as-private", "122.122.0.0/16", "--db", "/home/me/100%/x.db"},
	}))
	want := `ExecStart=/home/me/agenthub/bin/agenthub-node --display-name "the machine on my desk" --treat-as-private 122.122.0.0/16 --db /home/me/100%%/x.db`
	if !strings.Contains(unit, want) {
		t.Fatalf("unit ExecStart wrong:\n%s\nwant line:\n%s", unit, want)
	}
	for _, line := range []string{"Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(unit, line) {
			t.Errorf("unit lacks %q", line)
		}
	}
}

func TestSystemdQuote(t *testing.T) {
	cases := map[string]string{
		"plain":            "plain",
		"has space":        `"has space"`,
		`say "hi"`:         `"say \"hi\""`,
		`back\slash there`: `"back\\slash there"`,
		"50%":              "50%%",
		"$HOME x":          `"$$HOME x"`,
	}
	for in, want := range cases {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstallOnDarwinWritesPlistThenBootstraps(t *testing.T) {
	home := t.TempDir()
	node := writeFakeNode(t)
	runner := &fakeRunner{}
	manager := Manager{GOOS: "darwin", Home: home, UID: "501", Runner: runner, Sleep: noSleep}

	report, err := manager.Install(context.Background(), Config{NodeBinary: node, Args: []string{"--db", "/x/agenthub.db", "--allow-lan"}})
	if err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if report.UnitPath != unitPath {
		t.Errorf("unit path = %q, want %q", report.UnitPath, unitPath)
	}
	content, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	if !strings.Contains(string(content), "<string>--allow-lan</string>") {
		t.Errorf("plist lacks the node flag:\n%s", content)
	}
	logPath := filepath.Join(home, "Library", "Logs", "agenthub", "node.log")
	if !strings.Contains(string(content), logPath) {
		t.Errorf("plist lacks the default log path %s", logPath)
	}
	if _, err := os.Stat(filepath.Dir(logPath)); err != nil {
		t.Errorf("log directory not created: %v", err)
	}
	wantCalls := []string{
		"launchctl bootout gui/501/" + Label,
		"launchctl print gui/501/" + Label,
		"launchctl bootstrap gui/501 " + unitPath,
	}
	if strings.Join(runner.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(runner.calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	if len(report.Notes) == 0 || !strings.Contains(report.Notes[0], logPath) {
		t.Errorf("report does not say where the log is: %v", report.Notes)
	}
}

func TestInstallOnDarwinReportsABootstrapFailureWithTheManagersWords(t *testing.T) {
	home := t.TempDir()
	node := writeFakeNode(t)
	unitPath := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	runner := &fakeRunner{answers: map[string]struct {
		out string
		err error
	}{
		"launchctl bootstrap gui/501 " + unitPath: {out: "Bootstrap failed: 5: Input/output error", err: errors.New("exit status 5")},
	}}
	manager := Manager{GOOS: "darwin", Home: home, UID: "501", Runner: runner, Sleep: noSleep}
	_, err := manager.Install(context.Background(), Config{NodeBinary: node})
	if err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("err = %v, want the manager's own message", err)
	}
}

func TestInstallOnLinuxWritesUnitReloadsThenEnables(t *testing.T) {
	home := t.TempDir()
	node := writeFakeNode(t)
	runner := &fakeRunner{}
	manager := Manager{GOOS: "linux", Home: home, UID: "1000", Runner: runner, Sleep: noSleep}

	report, err := manager.Install(context.Background(), Config{NodeBinary: node, Args: []string{"--discover"}})
	if err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", UnitName+".service")
	content, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	if !strings.Contains(string(content), "ExecStart="+node+" --discover") {
		t.Errorf("unit ExecStart wrong:\n%s", content)
	}
	wantCalls := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now " + UnitName,
	}
	if strings.Join(runner.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(runner.calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	if report.UnitPath != unitPath {
		t.Errorf("unit path = %q", report.UnitPath)
	}
	joined := strings.Join(report.Notes, "\n")
	if !strings.Contains(joined, "journalctl") || !strings.Contains(joined, "enable-linger") {
		t.Errorf("notes should say where the log is and how to start at boot: %v", report.Notes)
	}
}

func TestInstallRefusesARelativeOrMissingBinary(t *testing.T) {
	manager := Manager{GOOS: "darwin", Home: t.TempDir(), UID: "501", Runner: &fakeRunner{}, Sleep: noSleep}
	if _, err := manager.Install(context.Background(), Config{NodeBinary: "bin/agenthub-node"}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative binary: err = %v", err)
	}
	if _, err := manager.Install(context.Background(), Config{NodeBinary: "/nowhere/agenthub-node"}); err == nil {
		t.Error("missing binary was accepted")
	}
	entries, _ := os.ReadDir(manager.Home)
	if len(entries) != 0 {
		t.Errorf("a refused install wrote something: %v", entries)
	}
}

func TestUninstallRemovesOnlyTheUnitAndSaysSo(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			node := writeFakeNode(t)
			runner := &fakeRunner{}
			manager := Manager{GOOS: goos, Home: home, UID: "501", Runner: runner, Sleep: noSleep}
			if _, err := manager.Install(context.Background(), Config{NodeBinary: node}); err != nil {
				t.Fatal(err)
			}
			// Something that is not the unit, standing in for the node's key.
			bystander := filepath.Join(home, "node.key")
			if err := os.WriteFile(bystander, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			runner.calls = nil

			report, err := manager.Uninstall(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(report.UnitPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("unit still present after uninstall: %v", err)
			}
			if _, err := os.Stat(bystander); err != nil {
				t.Errorf("uninstall touched a file that is not the unit: %v", err)
			}
			if !strings.Contains(strings.Join(report.Notes, "\n"), "not touched") {
				t.Errorf("report does not say identity and data were left alone: %v", report.Notes)
			}
			first := runner.calls[0]
			if goos == "darwin" && first != "launchctl bootout gui/501/"+Label {
				t.Errorf("darwin first call = %q", first)
			}
			if goos == "linux" && first != "systemctl --user disable --now "+UnitName {
				t.Errorf("linux first call = %q", first)
			}
		})
	}
}

func TestUninstallWhenNothingIsLoadedStillRemovesTheUnit(t *testing.T) {
	home := t.TempDir()
	unitPath := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{answers: map[string]struct {
		out string
		err error
	}{
		"launchctl bootout gui/501/" + Label: {out: "Boot-out failed: 3: No such process", err: errors.New("exit status 3")},
	}}
	manager := Manager{GOOS: "darwin", Home: home, UID: "501", Runner: runner, Sleep: noSleep}
	if _, err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall with nothing loaded: %v", err)
	}
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("stale unit left behind")
	}
}

func TestStatusReadsTheManagersAnswer(t *testing.T) {
	home := t.TempDir()
	unitPath := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{answers: map[string]struct {
		out string
		err error
	}{
		"launchctl print gui/501/" + Label: {out: "gui/501/local.agenthub.node = {\n\tactive count = 1\n\tpath = /x\n\tstate = running\n\n\tprogram = /x/agenthub-node\n\tpid = 4242\n}"},
	}}
	manager := Manager{GOOS: "darwin", Home: home, UID: "501", Runner: runner, Sleep: noSleep}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.Installed || !status.Running || status.PID != 4242 {
		t.Errorf("status = %+v", status)
	}

	linux := Manager{GOOS: "linux", Home: t.TempDir(), UID: "1000", Runner: &fakeRunner{answers: map[string]struct {
		out string
		err error
	}{
		"systemctl --user show " + UnitName + " -p ActiveState -p MainPID": {out: "ActiveState=active\nMainPID=777\n"},
	}}, Sleep: noSleep}
	status, err = linux.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Installed || !status.Running || status.PID != 777 {
		t.Errorf("linux status = %+v (unit file absent, service active)", status)
	}
}

func TestUnsupportedPlatformIsRefusedNotGuessed(t *testing.T) {
	manager := Manager{GOOS: "windows", Home: t.TempDir(), UID: "0", Runner: &fakeRunner{}, Sleep: noSleep}
	if _, err := manager.Install(context.Background(), Config{NodeBinary: writeFakeNode(t)}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("install: err = %v", err)
	}
	if _, err := manager.Uninstall(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("uninstall: err = %v", err)
	}
	status, err := manager.Status(context.Background())
	if err != nil || status.Supported {
		t.Errorf("status = %+v, %v; want unsupported without error", status, err)
	}
}

// The reinstall that the desktop app hit: bootout returns while launchd is
// still tearing the job down, and an immediate bootstrap fails with EIO.
func TestInstallOnDarwinWaitsForTheOldJobAndRetriesBootstrap(t *testing.T) {
	home := t.TempDir()
	node := writeFakeNode(t)
	unitPath := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	runner := &sequencedRunner{
		print:     []error{nil, nil, errors.New("exit status 113")}, // still loaded twice, then gone
		bootstrap: []error{errors.New("exit status 5"), nil},        // EIO once, then fine
	}
	manager := Manager{GOOS: "darwin", Home: home, UID: "501", Runner: runner, Sleep: noSleep}
	if _, err := manager.Install(context.Background(), Config{NodeBinary: node}); err != nil {
		t.Fatalf("install should have outlasted the teardown: %v", err)
	}
	want := []string{
		"launchctl bootout gui/501/" + Label,
		"launchctl print gui/501/" + Label,
		"launchctl print gui/501/" + Label,
		"launchctl print gui/501/" + Label,
		"launchctl bootstrap gui/501 " + unitPath,
		"launchctl bootstrap gui/501 " + unitPath,
	}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(runner.calls, "\n"), strings.Join(want, "\n"))
	}
}

// A bootstrap failure that is not EIO is not retried: it is a real answer.
func TestInstallOnDarwinDoesNotRetryAnUnrelatedBootstrapFailure(t *testing.T) {
	runner := &sequencedRunner{bootstrap: []error{errors.New("exit status 37")}, bootstrapOut: "Bootstrap failed: 37: Operation already in progress"}
	manager := Manager{GOOS: "darwin", Home: t.TempDir(), UID: "501", Runner: runner, Sleep: noSleep}
	if _, err := manager.Install(context.Background(), Config{NodeBinary: writeFakeNode(t)}); err == nil {
		t.Fatal("a non-EIO bootstrap failure was swallowed")
	}
	bootstraps := 0
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "launchctl bootstrap") {
			bootstraps++
		}
	}
	if bootstraps != 1 {
		t.Errorf("bootstrap attempted %d times, want 1", bootstraps)
	}
}

// sequencedRunner answers launchctl print and bootstrap from queues, one
// answer per call, and treats an exhausted queue as success.
type sequencedRunner struct {
	calls        []string
	print        []error
	bootstrap    []error
	bootstrapOut string
}

func (s *sequencedRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	s.calls = append(s.calls, line)
	switch {
	case strings.HasPrefix(line, "launchctl print "):
		if len(s.print) == 0 {
			return "", errors.New("exit status 113")
		}
		err := s.print[0]
		s.print = s.print[1:]
		return "", err
	case strings.HasPrefix(line, "launchctl bootstrap "):
		if len(s.bootstrap) == 0 {
			return "", nil
		}
		err := s.bootstrap[0]
		s.bootstrap = s.bootstrap[1:]
		out := s.bootstrapOut
		if err != nil && out == "" {
			out = "Bootstrap failed: 5: Input/output error"
		}
		return out, err
	}
	return "", nil
}
