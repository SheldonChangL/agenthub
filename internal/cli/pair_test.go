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

// `ah pair` now has two shapes, and the subcommands must not have taken the
// manual form's arguments away from it: the manual route is what still works
// when the two machines cannot open a connection to each other.
func TestPairRoutesEachShapeToItsEndpoint(t *testing.T) {
	cases := map[string]struct {
		args       []string
		wantMethod string
		wantPath   string
		wantBody   map[string]any
	}{
		"request": {
			args:       []string{"pair", "request", "192.168.1.42:7463"},
			wantMethod: http.MethodPost, wantPath: "/v1/pair/requests",
			wantBody: map[string]any{"address": "192.168.1.42:7463"},
		},
		"approve": {
			args:       []string{"pair", "approve", "pair_1"},
			wantMethod: http.MethodPost, wantPath: "/v1/pair/requests/pair_1/approve",
		},
		"confirm": {
			args:       []string{"pair", "confirm", "pair_1"},
			wantMethod: http.MethodPost, wantPath: "/v1/pair/requests/pair_1/confirm",
		},
		"reject": {
			args:       []string{"pair", "reject", "pair_1"},
			wantMethod: http.MethodPost, wantPath: "/v1/pair/requests/pair_1/reject",
		},
		"the manual form still posts a trusted node": {
			args: []string{"pair", "node_0123456789abcdef0123", "laptop", "darwin/arm64", "AAAA",
				"2DCF", "9604", "DBA9", "778A", "6DDD", "035B"},
			wantMethod: http.MethodPost, wantPath: "/v1/nodes",
			wantBody: map[string]any{
				"nodeId": "node_0123456789abcdef0123", "displayName": "laptop",
				"platform": "darwin/arm64", "publicKey": "AAAA",
				"confirmedFingerprint": "2DCF 9604 DBA9 778A 6DDD 035B",
			},
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"pair_1","state":"pending","direction":"outgoing",` +
					`"nodeId":"node_abcdef01234567890abc","fingerprint":"AAAA BBBB CCCC DDDD EEEE FFFF",` +
					`"localFingerprint":"1111 2222 3333 4444 5555 6666"}`))
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			args := append([]string{"--url", server.URL}, test.args...)
			if code := Run(context.Background(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
			}
			if gotMethod != test.wantMethod || gotPath != test.wantPath {
				t.Fatalf("request = %s %s, want %s %s", gotMethod, gotPath, test.wantMethod, test.wantPath)
			}
			for key, want := range test.wantBody {
				if got := gotBody[key]; got != want {
					t.Errorf("body[%q] = %v, want %v", key, got, want)
				}
			}
		})
	}
}

// The whole exchange rests on a person comparing two values, so both have to be
// on the screen, labelled, on both machines.
func TestPairPendingPrintsBothFingerprints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/pair/requests" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requests":[{"id":"pair_1","direction":"incoming","state":"pending",` +
			`"nodeId":"node_abcdef01234567890abc","displayName":"other laptop","platform":"linux/amd64",` +
			`"fingerprint":"AAAA BBBB CCCC DDDD EEEE FFFF",` +
			`"localFingerprint":"1111 2222 3333 4444 5555 6666",` +
			`"notice":"Compare the fingerprint below"}]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "pair", "pending"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{
		"pair_1", "other laptop",
		"AAAA BBBB CCCC DDDD EEEE FFFF",
		"1111 2222 3333 4444 5555 6666",
		"Compare the fingerprint",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, stdout.String())
		}
	}
}

// An empty list is an answer, not an error, and has to read as one.
func TestPairPendingSaysWhenThereIsNothing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requests":[]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "pair", "pending"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Nothing is waiting") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// A node too old to know this route answers 404, and the message the node turns
// that into has to reach the person who typed the command.
func TestPairRequestReportsAnOlderNodeReadably(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"PEER_TOO_OLD",` +
			`"message":"it is running a build from before the pairing exchange existed"}}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(),
		[]string{"--url", server.URL, "pair", "request", "192.168.1.42:7463"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("a refused pairing request exited 0")
	}
	if !strings.Contains(stderr.String(), "before the pairing exchange existed") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// Input that cannot mean one thing is refused before anything is sent.
func TestPairSubcommandsRefuseIncoherentInput(t *testing.T) {
	cases := map[string][]string{
		"request without an address":   {"pair", "request"},
		"request with two addresses":   {"pair", "request", "a:1", "b:2"},
		"request with an unknown flag": {"pair", "request", "a:1", "--force"},
		"a local name for the peer":    {"pair", "request", "a:1", "--name", "theirs"},
		"approve without an id":        {"pair", "approve"},
		"pending with an argument":     {"pair", "pending", "extra"},
		"too few arguments":            {"pair", "node_0123456789abcdef0123", "laptop"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("the node was contacted for input that cannot mean one thing")
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(),
				append([]string{"--url", server.URL}, args...), &stdout, &stderr); code == 0 {
				t.Fatalf("exit = 0 for %v; stdout = %q", args, stdout.String())
			}
		})
	}
}

// The fingerprints print in the order the node gave them — requester first on
// both machines — labelled with the name each machine calls itself and with
// which of the two this one is.
func TestPairPendingPrintsTheOrderedFingerprints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "" {
			t.Fatalf("plain `ah pair pending` asked for everything: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requests":[{"id":"pair_1","direction":"incoming","state":"pending",` +
			`"nodeId":"node_abcdef01234567890abc","displayName":"other laptop","platform":"linux/amd64",` +
			`"fingerprints":[` +
			`{"role":"requester","machine":"other laptop","whose":"the other machine",` +
			`"fingerprint":"AAAA BBBB CCCC DDDD EEEE FFFF"},` +
			`{"role":"receiver","machine":"this laptop","whose":"this machine",` +
			`"fingerprint":"1111 2222 3333 4444 5555 6666"}],` +
			`"nextStep":"Compare the two fingerprints, then on this machine run: ah pair approve pair_1",` +
			`"notice":"Two fingerprints are shown"}]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "pair", "pending"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	printed := stdout.String()
	for _, want := range []string{
		"pair_1", "other laptop (the other machine)", "this laptop (this machine)",
		"requester", "receiver",
		"AAAA BBBB CCCC DDDD EEEE FFFF", "1111 2222 3333 4444 5555 6666",
		"on this machine run: ah pair approve pair_1", "Two fingerprints are shown",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("output does not contain %q:\n%s", want, printed)
		}
	}
	// The requester's line comes first, as it does on the other machine.
	if strings.Index(printed, "AAAA BBBB") > strings.Index(printed, "1111 2222") {
		t.Errorf("the receiver's fingerprint printed first:\n%s", printed)
	}
}

// --all is a different question and asks the node a different question.
func TestPairPendingAllAsksForFinishedRequestsToo(t *testing.T) {
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requests":[]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(),
		[]string{"--url", server.URL, "pair", "pending", "--all"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(asked, "all=true") {
		t.Errorf("--all asked for %q", asked)
	}
}

// approve, confirm and reject are read by somebody standing between two
// laptops. They say what happened, who is trusted now, and which machine runs
// the next command — not the node's JSON.
func TestPairDecisionsPrintSomethingAPersonCanRead(t *testing.T) {
	cases := map[string]struct {
		verb  string
		reply string
		want  []string
	}{
		"approve": {
			verb: "approve",
			reply: `{"id":"pair_1","direction":"incoming","state":"approved",` +
				`"nodeId":"node_abcdef01234567890abc","displayName":"other laptop",` +
				`"fingerprint":"AAAA BBBB CCCC DDDD EEEE FFFF",` +
				`"nextStep":"other laptop is trusted here. Nothing more to do on this machine; ` +
				`wait for them to confirm. If they never do, undo it with: ah revoke node_abcdef01234567890abc"}`,
			want: []string{
				"Approved", "other laptop", "node_abcdef01234567890abc",
				"AAAA BBBB CCCC DDDD EEEE FFFF", "Nothing more to do on this machine",
				"ah revoke node_abcdef01234567890abc",
			},
		},
		"confirm": {
			verb: "confirm",
			reply: `{"id":"pair_1","direction":"outgoing","state":"approved",` +
				`"nodeId":"node_abcdef01234567890abc","displayName":"other laptop",` +
				`"fingerprint":"AAAA BBBB CCCC DDDD EEEE FFFF",` +
				`"nextStep":"Done: other laptop is trusted here, and this machine is trusted there."}`,
			want: []string{"Confirmed", "other laptop", "trusted", "Done:"},
		},
		"reject": {
			verb: "reject",
			reply: `{"id":"pair_1","direction":"outgoing","state":"rejected",` +
				`"nodeId":"node_abcdef01234567890abc","displayName":"other laptop","reason":"declined",` +
				`"nextStep":"Refused. Nothing from this request is trusted on either machine."}`,
			want: []string{"Refused", "Nothing from this request is trusted"},
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.reply))
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(),
				[]string{"--url", server.URL, "pair", test.verb, "pair_1"}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
			}
			printed := stdout.String()
			for _, want := range test.want {
				if !strings.Contains(printed, want) {
					t.Errorf("output does not contain %q:\n%s", want, printed)
				}
			}
			if strings.Contains(printed, `"id":`) {
				t.Errorf("raw JSON was printed at a person:\n%s", printed)
			}
		})
	}
}

// --json is still the shape a script reads.
func TestPairDecisionsStillHaveAJSONForm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pair_1","state":"approved"}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(),
		[]string{"--url", server.URL, "--json", "pair", "approve", "pair_1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"state": "approved"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
