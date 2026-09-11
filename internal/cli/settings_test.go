package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const settingsAnswer = `{
  "settings": {"peerListen":"192.168.1.10:7463","allowLan":true,"discover":false,"treatAsPrivate":["122.122.0.0/16"],"autoWake":false},
  "sources": {"peerListen":"flag","allowLan":"remembered","discover":"default","treatAsPrivate":"remembered","autoWake":"default"},
  "saved": {"peerListen":"192.168.1.10:7463","allowLan":true,"discover":true,"treatAsPrivate":["122.122.0.0/16"],"autoWake":false},
  "restartRequired": true,
  "message": ""
}`

// `ah settings` has one job: say what this node is running with and whether
// anybody typed it. A remembered -allow-lan appears nowhere else.
func TestSettingsPrintsEveryValueWithItsSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/node/settings" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, settingsAnswer)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "settings"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"peer-listen", "192.168.1.10:7463", "flag",
		"allow-lan", "remembered",
		"122.122.0.0/16",
		"ah service restart",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// discover differs between what is running and what is saved, and the
	// difference is the reason to restart, so it has to be on screen.
	if !strings.Contains(out, "true") {
		t.Errorf("the saved-but-not-running value is missing:\n%s", out)
	}
}

// Only what was typed is sent. A body carrying every field would pin the four
// settings the owner did not mention to this command line's defaults, which is
// how a `--discover` would quietly turn -allow-lan off.
func TestSettingsSetSendsOnlyTheFlagsGiven(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/node/settings" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, settingsAnswer)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(),
		[]string{"--url", server.URL, "settings", "set", "--allow-lan=false", "--treat-as-private", "10.9.0.0/16"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if len(received) != 2 {
		t.Fatalf("body = %v; only the two flags given may be sent", received)
	}
	// The point of the `=false` spelling: a switch has to be closable.
	if allow, ok := received["allowLan"].(bool); !ok || allow {
		t.Fatalf("allowLan = %v; --allow-lan=false must send false", received["allowLan"])
	}
	ranges, ok := received["treatAsPrivate"].([]any)
	if !ok || len(ranges) != 1 || ranges[0] != "10.9.0.0/16" {
		t.Fatalf("treatAsPrivate = %v", received["treatAsPrivate"])
	}
}

// Nothing reaches the node when the command line does not say what to change,
// or says two opposite things about the same set.
func TestSettingsSetRefusesIncoherentInput(t *testing.T) {
	cases := map[string][]string{
		"no flags":           {"settings", "set"},
		"unknown flag":       {"settings", "set", "--allow-wan=true"},
		"unknown subcommand": {"settings", "show"},
		"stray argument":     {"settings", "set", "--discover=true", "please"},
		"clear and declare": {"settings", "set",
			"--treat-as-private", "10.9.0.0/16", "--clear-private-ranges"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			// An unreachable node on purpose: these must fail before a request.
			code := Run(context.Background(), append([]string{"--url", "http://127.0.0.1:1"}, args...), &stdout, &stderr)
			if code == 0 {
				t.Fatalf("exit = 0, stdout = %q", stdout.String())
			}
			if strings.Contains(stderr.String(), "contact node") {
				t.Fatalf("the node was contacted before the input was checked: %s", stderr.String())
			}
		})
	}
}

// --clear-private-ranges withdraws the whole declaration, which it can only do
// by sending an empty list: omitting the field would leave it stored.
func TestSettingsSetClearsPrivateRangesExplicitly(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, settingsAnswer)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(),
		[]string{"--url", server.URL, "settings", "set", "--clear-private-ranges"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	ranges, ok := received["treatAsPrivate"].([]any)
	if !ok || len(ranges) != 0 {
		t.Fatalf("treatAsPrivate = %v; clearing must send an empty list", received["treatAsPrivate"])
	}
}
