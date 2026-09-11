// Package service installs agenthub-node as a per-user background service, so
// the node outlives whichever terminal, agent or desktop window started it.
//
// The words a person meets are install, uninstall and status. launchd and
// systemd are what those words mean on each platform, and they stay in here:
// an owner who has to type `launchctl bootout` has been handed the
// implementation instead of the product.
//
// Identity and data are never touched. Uninstall removes the service
// registration and nothing else; the node's key and database stay where they
// were, so reinstalling later brings the same node back and pairings hold.
package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Label names the service on both platforms. launchd sees it as the job
// label; systemd sees it as the unit name (with .service appended).
const Label = "local.agenthub.node"

// UnitName is the systemd unit, which follows its own naming convention.
const UnitName = "agenthub-node"

// ErrUnsupported means this operating system has no service manager this
// package knows how to drive.
var ErrUnsupported = errors.New("background service is supported on macOS (launchd) and Linux (systemd --user) only")

// Config is what the service will run.
type Config struct {
	// NodeBinary is the absolute path to agenthub-node. Absolute, because a
	// service has no PATH worth relying on and no working directory of the
	// owner's choosing.
	NodeBinary string
	// Args are the node's own flags, verbatim, in the order they will be
	// passed. The node validates them; this package only carries them.
	Args []string
	// LogPath receives stdout and stderr on launchd. Empty means the
	// platform's own facility (the journal on systemd).
	LogPath string
}

// Runner executes the platform's service manager. It is an interface so the
// sequence of commands can be asserted without a real launchd or systemd.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// Manager installs, removes and inspects the service for one user.
type Manager struct {
	// GOOS selects the platform; runtime.GOOS in production, anything in tests.
	GOOS string
	// Home is the user's home directory, where the unit and (on macOS) the
	// log live.
	Home string
	// UID is the numeric user id launchd addresses a per-user domain by.
	UID string
	// Runner drives launchctl or systemctl.
	Runner Runner
	// Sleep replaces real waiting between retries; nil means time.Sleep.
	Sleep func(time.Duration)
}

// Report says what an install or uninstall did, in the order it did it.
type Report struct {
	UnitPath string
	Steps    []string
	// Notes are facts the owner should know that are not failures: where the
	// log is, what uninstall left behind, what systemd needs for boot start.
	Notes []string
}

// Status is what the service manager currently says.
type Status struct {
	Supported bool
	Installed bool
	Running   bool
	PID       int
	UnitPath  string
	LogHint   string
	// Raw is the manager's own words, for when the summary is not enough.
	Raw string
}

// UnitPath is where the unit file lives for this user.
func (m Manager) UnitPath() (string, error) {
	switch m.GOOS {
	case "darwin":
		return filepath.Join(m.Home, "Library", "LaunchAgents", Label+".plist"), nil
	case "linux":
		return filepath.Join(m.Home, ".config", "systemd", "user", UnitName+".service"), nil
	}
	return "", ErrUnsupported
}

// DefaultLogPath is where launchd output goes. systemd has the journal.
func (m Manager) DefaultLogPath() string {
	if m.GOOS == "darwin" {
		return filepath.Join(m.Home, "Library", "Logs", "agenthub", "node.log")
	}
	return ""
}

// Install writes the unit, registers it and starts it. An existing
// registration is replaced, so install is also how a changed flag is applied.
func (m Manager) Install(ctx context.Context, config Config) (Report, error) {
	unitPath, err := m.UnitPath()
	if err != nil {
		return Report{}, err
	}
	if !filepath.IsAbs(config.NodeBinary) {
		return Report{}, fmt.Errorf("node binary %q must be an absolute path: a service has no working directory of yours", config.NodeBinary)
	}
	if _, err := os.Stat(config.NodeBinary); err != nil {
		return Report{}, fmt.Errorf("node binary: %w", err)
	}
	report := Report{UnitPath: unitPath}
	switch m.GOOS {
	case "darwin":
		if config.LogPath == "" {
			config.LogPath = m.DefaultLogPath()
		}
		// 0700: the log carries the node id, fingerprint and session counts,
		// which are the owner's to share, not the machine's.
		if err := os.MkdirAll(filepath.Dir(config.LogPath), 0o700); err != nil {
			return report, fmt.Errorf("create log directory: %w", err)
		}
		unit, err := LaunchdPlist(config)
		if err != nil {
			return report, err
		}
		if err := writeUnit(unitPath, unit); err != nil {
			return report, err
		}
		report.Steps = append(report.Steps, "wrote "+unitPath)
		domain := "gui/" + m.UID
		// A job that is not loaded makes bootout fail, and that is the common
		// case on a first install; any other failure is kept, because if the
		// bootstrap below then fails, the bootout's words are the cause.
		bootoutOut, bootoutErr := m.Runner.Run(ctx, "launchctl", "bootout", domain+"/"+Label)
		if bootoutErr != nil && notLoaded(bootoutOut, bootoutErr) {
			bootoutErr = nil
		}
		// bootout returns before the job is gone. A bootstrap that lands while
		// the old registration is still being torn down fails with EIO
		// ("Input/output error"), which a reinstall from the desktop app hit.
		// So wait until launchctl itself says the job is not loaded, then
		// retry the bootstrap a few times rather than once.
		m.waitUntilUnloaded(ctx, domain)
		var out string
		var bootstrapErr error
		for attempt := 0; attempt < 5; attempt++ {
			out, bootstrapErr = m.Runner.Run(ctx, "launchctl", "bootstrap", domain, unitPath)
			if bootstrapErr == nil {
				break
			}
			if !strings.Contains(out, "Input/output error") && !strings.Contains(out, "already loaded") {
				break
			}
			m.pause(ctx, 500*time.Millisecond)
		}
		if bootstrapErr != nil {
			message := fmt.Sprintf("launchctl bootstrap: %v: %s", bootstrapErr, strings.TrimSpace(out))
			if bootoutErr != nil {
				message += fmt.Sprintf(" (the bootout before it also failed: %v: %s)", bootoutErr, strings.TrimSpace(bootoutOut))
			}
			return report, errors.New(message)
		}
		report.Steps = append(report.Steps, "registered with launchd as "+Label+" (starts at login, restarted if it exits)")
		report.Notes = append(report.Notes, "log: "+config.LogPath)
	case "linux":
		unit := SystemdUnit(config)
		if err := writeUnit(unitPath, unit); err != nil {
			return report, err
		}
		report.Steps = append(report.Steps, "wrote "+unitPath)
		if out, err := m.Runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return report, fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(out))
		}
		if out, err := m.Runner.Run(ctx, "systemctl", "--user", "enable", "--now", UnitName); err != nil {
			return report, fmt.Errorf("systemctl enable --now: %w: %s", err, strings.TrimSpace(out))
		}
		report.Steps = append(report.Steps, "enabled and started "+UnitName+" (systemd --user; restarted if it exits)")
		report.Notes = append(report.Notes,
			"log: journalctl --user -u "+UnitName,
			"a user service starts when you log in; to have it start at boot with nobody logged in, run: loginctl enable-linger "+userName())
	default:
		return report, ErrUnsupported
	}
	return report, nil
}

// Uninstall stops the service and removes its registration. The node's key
// and database are not touched, and the report says so.
func (m Manager) Uninstall(ctx context.Context) (Report, error) {
	unitPath, err := m.UnitPath()
	if err != nil {
		return Report{}, err
	}
	report := Report{UnitPath: unitPath}
	switch m.GOOS {
	case "darwin":
		if out, err := m.Runner.Run(ctx, "launchctl", "bootout", "gui/"+m.UID+"/"+Label); err != nil && !notLoaded(out, err) {
			return report, fmt.Errorf("launchctl bootout: %w: %s", err, strings.TrimSpace(out))
		}
		report.Steps = append(report.Steps, "stopped and unregistered "+Label)
	case "linux":
		if out, err := m.Runner.Run(ctx, "systemctl", "--user", "disable", "--now", UnitName); err != nil && !notLoaded(out, err) {
			return report, fmt.Errorf("systemctl disable --now: %w: %s", err, strings.TrimSpace(out))
		}
		report.Steps = append(report.Steps, "stopped and disabled "+UnitName)
	default:
		return report, ErrUnsupported
	}
	switch err := os.Remove(unitPath); {
	case err == nil:
		report.Steps = append(report.Steps, "removed "+unitPath)
	case errors.Is(err, os.ErrNotExist):
		report.Steps = append(report.Steps, "no unit file at "+unitPath+" (already removed)")
	default:
		return report, fmt.Errorf("remove unit: %w", err)
	}
	if m.GOOS == "linux" {
		_, _ = m.Runner.Run(ctx, "systemctl", "--user", "daemon-reload")
	}
	report.Notes = append(report.Notes,
		"the node's identity (node.key) and database were not touched; reinstalling brings the same node back and pairings hold")
	return report, nil
}

// Status asks the service manager, and only the service manager. Whether the
// node answers on its port is a separate question the caller can ask it.
func (m Manager) Status(ctx context.Context) (Status, error) {
	unitPath, err := m.UnitPath()
	if err != nil {
		return Status{Supported: false}, nil
	}
	status := Status{Supported: true, UnitPath: unitPath}
	if _, err := os.Stat(unitPath); err == nil {
		status.Installed = true
	}
	switch m.GOOS {
	case "darwin":
		status.LogHint = m.DefaultLogPath()
		out, err := m.Runner.Run(ctx, "launchctl", "print", "gui/"+m.UID+"/"+Label)
		status.Raw = strings.TrimSpace(out)
		if err != nil {
			return status, nil
		}
		status.Running = strings.Contains(out, "state = running")
		if match := launchdPID.FindStringSubmatch(out); match != nil {
			status.PID, _ = strconv.Atoi(match[1])
		}
	case "linux":
		status.LogHint = "journalctl --user -u " + UnitName
		out, _ := m.Runner.Run(ctx, "systemctl", "--user", "show", UnitName, "-p", "ActiveState", "-p", "MainPID")
		status.Raw = strings.TrimSpace(out)
		status.Running = strings.Contains(out, "ActiveState=active")
		if match := systemdPID.FindStringSubmatch(out); match != nil {
			status.PID, _ = strconv.Atoi(match[1])
		}
	}
	return status, nil
}

var (
	launchdPID = regexp.MustCompile(`\bpid = (\d+)`)
	systemdPID = regexp.MustCompile(`MainPID=(\d+)`)
)

// waitUntilUnloaded polls launchctl until it says the job is not loaded, for
// a bounded time. Only that answer ends the wait early: a print that fails
// for some other reason says nothing about the job, so the wait runs out
// rather than pretending.
func (m Manager) waitUntilUnloaded(ctx context.Context, domain string) {
	for attempt := 0; attempt < 20; attempt++ {
		out, err := m.Runner.Run(ctx, "launchctl", "print", domain+"/"+Label)
		if err != nil && notLoaded(out, err) {
			return
		}
		m.pause(ctx, 250*time.Millisecond)
	}
}

// pause sleeps unless the context ends first. Sleep is a field so the tests
// do not wait; nil means real time.
func (m Manager) pause(ctx context.Context, duration time.Duration) {
	if m.Sleep != nil {
		m.Sleep(duration)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(duration):
	}
}

// notLoaded recognises the manager saying there was nothing to stop.
func notLoaded(out string, err error) bool {
	text := out + " " + err.Error()
	for _, marker := range []string{"Could not find service", "No such process", "not loaded", "does not exist", "Unit " + UnitName + ".service not loaded"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func writeUnit(path string, content []byte) error {
	// 0700 and 0600: launchd and systemd --user read the unit as the owning
	// user, so nobody else needs to.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// WriteFile applies the mode only when it creates the file. A reinstall
	// over a unit written by an earlier build keeps that build's mode, so it
	// is set explicitly — measured: two machines reinstalled and both units
	// stayed 0644.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

func userName() string {
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "$USER"
}

// LaunchdPlist renders the launchd job. KeepAlive restarts the node if it
// exits; RunAtLoad starts it at login and on bootstrap.
func LaunchdPlist(config Config) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, argument := range append([]string{config.NodeBinary}, config.Args...) {
		buffer.WriteString("\t\t<string>")
		if err := xml.EscapeText(&buffer, []byte(argument)); err != nil {
			return nil, fmt.Errorf("escape argument %q: %w", argument, err)
		}
		buffer.WriteString("</string>\n")
	}
	buffer.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>`)
	if err := xml.EscapeText(&buffer, []byte(config.LogPath)); err != nil {
		return nil, err
	}
	buffer.WriteString(`</string>
	<key>StandardErrorPath</key>
	<string>`)
	if err := xml.EscapeText(&buffer, []byte(config.LogPath)); err != nil {
		return nil, err
	}
	buffer.WriteString(`</string>
</dict>
</plist>
`)
	return buffer.Bytes(), nil
}

// SystemdUnit renders the user unit. Restart=on-failure with a short delay
// covers a node that dies; a node that exits cleanly (a signal from the
// owner) stays down, which is what a person stopping it expects.
func SystemdUnit(config Config) []byte {
	parts := make([]string, 0, 1+len(config.Args))
	for _, argument := range append([]string{config.NodeBinary}, config.Args...) {
		parts = append(parts, systemdQuote(argument))
	}
	return []byte(`[Unit]
Description=AgentHub node (agenthub-node)
After=network-online.target

[Service]
ExecStart=` + strings.Join(parts, " ") + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`)
}

// systemdQuote makes one ExecStart argument survive systemd's own parsing:
// double quotes around anything with a space or a quote, backslash escapes
// inside, and % doubled because a bare % is a specifier.
func systemdQuote(argument string) string {
	escaped := strings.ReplaceAll(argument, "%", "%%")
	if !strings.ContainsAny(escaped, " \t\"'\\$") {
		return escaped
	}
	escaped = strings.ReplaceAll(escaped, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	escaped = strings.ReplaceAll(escaped, `$`, `$$`)
	return `"` + escaped + `"`
}
