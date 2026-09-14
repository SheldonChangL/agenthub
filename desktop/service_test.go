package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeNode stands in for the agenthub-node lookup so a test about the form
// is not also a test about packaging. The path is unmistakable on sight.
func withFakeNode(t *testing.T) string {
	t.Helper()
	previous := findNode
	t.Cleanup(func() { findNode = previous })
	findNode = func() (string, error) { return "/fake/bundle/agenthub-node", nil }
	return "/fake/bundle/agenthub-node"
}

func TestInstallArgsCarryOnlyWhatTheOwnerSet(t *testing.T) {
	node := withFakeNode(t)
	args, err := installArgs(ServiceForm{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "service install --node-binary "+node {
		t.Errorf("empty form should pass no node flags beyond the pinned binary, got %v", args)
	}

	args, err = installArgs(ServiceForm{
		DBPath: " /Users/me/data/agenthub.db ", PeerListen: "122.122.122.1:7463", AllowLAN: true, Discover: true,
		TreatAsPrivate: []string{"122.122.0.0/16", " ", "10.9.0.0/16"}, AutoWake: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "service install --node-binary " + node + " --db /Users/me/data/agenthub.db --peer-listen 122.122.122.1:7463 --allow-lan --discover --treat-as-private 122.122.0.0/16 --treat-as-private 10.9.0.0/16 --auto-wake"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("args:\n%s\nwant:\n%s", got, want)
	}
}

func TestInstallArgsRefuseWhatAhCouldOnlyRefuseLater(t *testing.T) {
	withFakeNode(t)
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
	withFakeNode(t)
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
	// 0o700, not 0o600: the search now refuses a file it could not execute, so
	// a fixture without the bit would be testing the refusal, not the find.
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
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
	planted := filepath.Join(onPath, "ah")
	executable(t, planted)
	// Runnable, not merely present: an ah without the bit would be passed over
	// by a PATH search anyway, and this test would pass without proving that no
	// PATH search happens.
	if err := os.Chmod(planted, 0o700); err != nil {
		t.Fatal(err)
	}
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

// The flag that decides what a launchd job or a systemd unit runs at every
// boot. Without it `ah service install` resolves agenthub-node itself and ends
// at exec.LookPath — so taking PATH out of this file would only have moved that
// decision one hop down, into a file written once and executed forever.
func TestInstallArgsPinTheNodeBinaryInsideTheBundle(t *testing.T) {
	t.Setenv("AGENTHUB_NODE", "")
	macOS := filepath.Join(t.TempDir(), "AgentHub.app", "Contents", "MacOS")
	dir := executable(t, filepath.Join(macOS, "desktop"))
	bundled := filepath.Join(macOS, "agenthub-node")
	executable(t, bundled)

	previous := findNode
	t.Cleanup(func() { findNode = previous })
	findNode = func() (string, error) { return locateBinaryNear(dir, "agenthub-node", "AGENTHUB_NODE") }

	args, err := installArgs(ServiceForm{})
	if err != nil {
		t.Fatal(err)
	}
	at := -1
	for i, arg := range args {
		if arg == "--node-binary" {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("the install does not name a node binary at all: %v", args)
	}
	if at+1 >= len(args) || args[at+1] != bundled {
		t.Errorf("--node-binary names %v, want the bundled %q", args[at+1:], bundled)
	}

	// And when there is no agenthub-node to name, the install says so rather
	// than quietly leaving the flag off and letting ah fall back to PATH.
	findNode = func() (string, error) { return "", errors.New("agenthub-node was not found (looked: a, b)") }
	if _, err := installArgs(ServiceForm{}); err == nil {
		t.Error("a missing agenthub-node produced an install that would resolve it from PATH")
	}
}

// The hole a go.mod check alone leaves open. Three levels above
// <dir>/Anything.app/Contents/MacOS is <dir> itself, so an app installed in a
// world-writable directory — /Users/Shared is drwxrwxrwt — lets any other local
// account plant the two files that make that directory look like this project's
// checkout, and the GUI would run the planted ah to install a launchd job.
// Depth is not shape: it has to be desktop/build/bin.
func TestLocateBinaryRefusesACheckoutPlantedBesideAnInstalledApp(t *testing.T) {
	t.Setenv("AGENTHUB_AH", "")
	shared := t.TempDir()
	macOS := filepath.Join(shared, "AgentHub.app", "Contents", "MacOS")
	dir := executable(t, filepath.Join(macOS, "desktop"))

	// What the other account writes; nothing here needs the owner's cooperation
	// beyond their having put the app in a shared directory.
	checkout(t, shared)
	planted := filepath.Join(shared, "bin", "ah")
	executable(t, planted)

	found, err := locateBinaryNear(dir, "ah", "AGENTHUB_AH")
	if err == nil {
		t.Fatalf("the app would run %q, which anyone with write access to %q could have put there", found, shared)
	}
	if strings.Contains(err.Error(), planted) {
		t.Errorf("the planted binary was still one of the places searched: %v", err)
	}
}

// Contents/Resources is the other place the packaging step could leave a
// binary, and it was the one candidate nothing looked at: deleting that line
// left the suite green.
func TestLocateBinaryFindsWhatWasBundledInResources(t *testing.T) {
	t.Setenv("AGENTHUB_AH", "")
	app := filepath.Join(t.TempDir(), "AgentHub.app")
	dir := executable(t, filepath.Join(app, "Contents", "MacOS", "desktop"))
	bundled := filepath.Join(app, "Contents", "Resources", "ah")
	executable(t, bundled)

	found, err := locateBinaryNear(dir, "ah", "AGENTHUB_AH")
	if err != nil {
		t.Fatal(err)
	}
	if found != bundled {
		t.Errorf("found %q, want the one in Resources, %q", found, bundled)
	}
}

// The override names a binary to run. A directory of that name, or a file
// without the bit, is not one — and handing either back means failing later
// with a message about exec instead of here with one about the path.
func TestLocateBinaryRefusesAnOverrideThatIsNotAnExecutableFile(t *testing.T) {
	macOS := filepath.Join(t.TempDir(), "AgentHub.app", "Contents", "MacOS")
	dir := executable(t, filepath.Join(macOS, "desktop"))
	bundled := filepath.Join(macOS, "ah")
	executable(t, bundled)

	directory := filepath.Join(t.TempDir(), "ah")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	inert := filepath.Join(t.TempDir(), "ah")
	if err := os.WriteFile(inert, []byte("not a program\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, given := range map[string]string{
		"a directory":                directory,
		"a file with no execute bit": inert,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("AGENTHUB_AH", given)
			found, err := locateBinaryNear(dir, "ah", "AGENTHUB_AH")
			if err != nil {
				t.Fatal(err)
			}
			if found != bundled {
				t.Errorf("$AGENTHUB_AH pointing at %s was taken: found %q, want the bundled %q", name, found, bundled)
			}
		})
	}
}
