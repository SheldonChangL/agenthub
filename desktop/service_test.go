package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallArgsCarryOnlyWhatTheOwnerSet(t *testing.T) {
	args, err := installArgs(ServiceForm{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "service install" {
		t.Errorf("empty form should pass no node flags, got %v", args)
	}

	args, err = installArgs(ServiceForm{
		DBPath: " /Users/me/data/agenthub.db ", PeerListen: "122.122.122.1:7463", AllowLAN: true, Discover: true,
		TreatAsPrivate: []string{"122.122.0.0/16", " ", "10.9.0.0/16"}, AutoWake: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "service install --db /Users/me/data/agenthub.db --peer-listen 122.122.122.1:7463 --allow-lan --discover --treat-as-private 122.122.0.0/16 --treat-as-private 10.9.0.0/16 --auto-wake"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("args:\n%s\nwant:\n%s", got, want)
	}
}

func TestInstallArgsRefuseWhatAhCouldOnlyRefuseLater(t *testing.T) {
	if _, err := installArgs(ServiceForm{PeerListen: "122.122.122.1"}); err == nil || !strings.Contains(err.Error(), "host:port") {
		t.Errorf("address without port: err = %v", err)
	}
	if _, err := installArgs(ServiceForm{TreatAsPrivate: []string{"122.122.0.0"}}); err == nil || !strings.Contains(err.Error(), "CIDR") {
		t.Errorf("range without prefix length: err = %v", err)
	}
	if _, err := installArgs(ServiceForm{TreatAsPrivate: []string{"999.1.1.1/40"}}); err == nil {
		t.Error("an out-of-range CIDR was accepted")
	}
}

func TestFindToolRemembersWhereAhIs(t *testing.T) {
	previous := findTool
	toolPath = ""
	t.Cleanup(func() { findTool = previous; toolPath = "" })
	t.Setenv("AGENTHUB_AH", "/definitely/not/here")
	t.Setenv("PATH", t.TempDir())
	if _, err := findTool(); err == nil {
		t.Fatal("found an ah that does not exist")
	}
	if toolPath != "" {
		t.Errorf("a failed lookup was cached as %q", toolPath)
	}
}

func withFakeTool(t *testing.T, output string, runErr error) *[]string {
	t.Helper()
	calls := &[]string{}
	previousRun, previousFind := runTool, findTool
	runTool = func(_ context.Context, tool string, args ...string) (string, error) {
		*calls = append(*calls, tool+" "+strings.Join(args, " "))
		return output, runErr
	}
	t.Setenv("AGENTHUB_AH", "/fake/ah")
	toolPath = ""
	t.Cleanup(func() { runTool = previousRun; findTool = previousFind; toolPath = "" })
	return calls
}

// The status the UI shows is ah's answer, decoded, with the node URL this
// app is pointed at — not a second opinion formed here.
func TestServiceStatusDecodesAhAndAddressesTheAppsNode(t *testing.T) {
	calls := withFakeTool(t, `{"service":{"Supported":true,"Installed":true,"Running":true,"PID":4242,"UnitPath":"/u/x.plist","LogHint":"/l/node.log"},"nodeAnswering":true,"node":"node answering as node_x"}`, nil)
	findTool = func() (string, error) { return "/fake/ah", nil }
	app := NewApp()
	app.ctx = context.Background()

	status := app.ServiceStatus()
	if status.ToolError != "" {
		t.Fatalf("tool error: %s", status.ToolError)
	}
	if !status.Installed || !status.Running || status.PID != 4242 || !status.NodeAnswering || status.UnitPath != "/u/x.plist" {
		t.Errorf("status = %+v", status)
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "/fake/ah --url http://127.0.0.1:7462 --json service status") {
		t.Errorf("calls = %v", *calls)
	}
}

func TestServiceStatusWithoutAhExplainsWhereItLooked(t *testing.T) {
	previous := findTool
	findTool = func() (string, error) { return "", errors.New("ah was not found (looked: a, b); set AGENTHUB_AH") }
	t.Cleanup(func() { findTool = previous })
	app := NewApp()
	app.ctx = context.Background()
	status := app.ServiceStatus()
	if !strings.Contains(status.ToolError, "AGENTHUB_AH") || status.Supported {
		t.Errorf("status = %+v", status)
	}
}

func TestInstallServiceCarriesAhsWordsBackOnFailure(t *testing.T) {
	withFakeTool(t, "listen address is not on a private network", errors.New("exit status 1"))
	findTool = func() (string, error) { return "/fake/ah", nil }
	app := NewApp()
	app.ctx = context.Background()
	result, err := app.InstallService(ServiceForm{PeerListen: "122.122.122.1:7463", AllowLAN: true})
	if err == nil || !strings.Contains(err.Error(), "private network") {
		t.Fatalf("err = %v, want ah's message", err)
	}
	if !strings.Contains(result.Command, "--peer-listen 122.122.122.1:7463 --allow-lan") {
		t.Errorf("command = %q", result.Command)
	}
}

// executable puts a file where an executable would be and returns the directory
// it landed in, which is what locateBinaryNear is given.
func executable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(path)
}

// checkout writes the one file that tells the search a directory is this
// project's tree rather than a stranger's.
func checkout(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The whole point of taking PATH out. What this finds installs a launchd job or
// a systemd unit, so an ah that is merely first on PATH must not be it: not
// found, and not run, even though it is sitting right there.
func TestLocateBinaryNeverRunsAnAhFromPATH(t *testing.T) {
	onPath := t.TempDir()
	executable(t, filepath.Join(onPath, "ah"))
	t.Setenv("PATH", onPath)
	t.Setenv("AGENTHUB_AH", "")

	// An app installed somewhere with nothing beside it — the case a colleague
	// hits, and the one where PATH used to answer.
	installed := filepath.Join(t.TempDir(), "Applications", "AgentHub.app", "Contents", "MacOS")
	dir := executable(t, filepath.Join(installed, "desktop"))

	found, err := locateBinaryNear(dir, "ah", "AGENTHUB_AH")
	if err == nil {
		t.Fatalf("ah was found at %q; the only ah anywhere is the one on PATH", found)
	}
	// The list of places tried is shown to the owner, so it must not advertise
	// a search that no longer happens. ", PATH)" is how the old list ended.
	if strings.Contains(err.Error(), onPath) || strings.Contains(err.Error(), ", PATH)") {
		t.Errorf("PATH is still one of the places searched: %v", err)
	}
}

// What the packaging step produces: ah beside the app executable, inside the
// bundle. This is the path a released .app has to take.
func TestLocateBinaryFindsWhatWasBundledBesideTheApp(t *testing.T) {
	t.Setenv("AGENTHUB_AH", "")
	macOS := filepath.Join(t.TempDir(), "AgentHub.app", "Contents", "MacOS")
	dir := executable(t, filepath.Join(macOS, "desktop"))
	bundled := filepath.Join(macOS, "ah")
	executable(t, bundled)

	found, err := locateBinaryNear(dir, "ah", "AGENTHUB_AH")
	if err != nil {
		t.Fatal(err)
	}
	if found != bundled {
		t.Errorf("found %q, want the bundled %q", found, bundled)
	}
}

// The source-tree fallback still serves a developer running the app out of
// desktop/build/bin — and only there. The same offsets under a directory that
// is not this project's checkout are a stranger's bin/ah, and are refused.
func TestLocateBinaryTakesTheSourceTreeOnlyFromThisProjectsCheckout(t *testing.T) {
	t.Setenv("AGENTHUB_AH", "")
	for _, layout := range []struct {
		name   string
		within string
	}{
		{"macOS", filepath.Join("desktop", "build", "bin", "agenthub-desktop.app", "Contents", "MacOS")},
		{"linux", filepath.Join("desktop", "build", "bin")},
	} {
		t.Run(layout.name, func(t *testing.T) {
			root := t.TempDir()
			dir := executable(t, filepath.Join(root, layout.within, "desktop"))
			built := filepath.Join(root, "bin", "ah")
			executable(t, built)

			if found, err := locateBinaryNear(dir, "ah", "AGENTHUB_AH"); err == nil {
				t.Fatalf("found %q under a directory with no go.mod of ours", found)
			}

			checkout(t, root)
			found, err := locateBinaryNear(dir, "ah", "AGENTHUB_AH")
			if err != nil {
				t.Fatal(err)
			}
			if found != built {
				t.Errorf("found %q, want the checkout's %q", found, built)
			}
		})
	}
}

// A go.mod is not enough on its own: it has to be ours.
func TestSomeoneElsesCheckoutIsNotOurs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/not/us\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isProjectCheckout(root) {
		t.Error("another project's go.mod was accepted as this project's checkout")
	}
	if isProjectCheckout(filepath.Join(root, "nowhere")) {
		t.Error("a directory with no go.mod at all was accepted")
	}
}
