package main

import (
	"context"
	"errors"
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
