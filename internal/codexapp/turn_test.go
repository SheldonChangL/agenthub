package codexapp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// Resuming by thread id is what keeps a wake from forking a second
// conversation beside the one the owner is watching.
//
// codex-cli 0.153.4's ThreadResumeParams: "If thread_id identifies a running
// thread, app-server rejoins that thread." So the client's whole job here is
// to address the thread by id and pass nothing that would override that —
// notably not `path`, which for a running thread is a consistency check and
// for a non-running one takes precedence over the id.
func TestResumeAddressesTheThreadByIdAlone(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := client.ResumeThread(context.Background(), "01a045ef-7f39-76a1-a638-e72b3153571d")
		done <- err
	}()

	id, method := server.nextCall(t)
	if method != "thread/resume" {
		t.Fatalf("method = %q", method)
	}
	server.reply(t, id, map[string]any{
		"thread": map[string]any{"id": "01a045ef-7f39-76a1-a638-e72b3153571d"},
		"cwd":    "/work/demo", "model": "gpt-5", "modelProvider": "openai",
		"approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": nil,
	})
	if err := <-done; err != nil {
		t.Fatalf("ResumeThread() error = %v", err)
	}

	params := server.seen()[0]["params"].(map[string]any)
	if params["threadId"] != "01a045ef-7f39-76a1-a638-e72b3153571d" {
		t.Errorf("threadId = %v", params["threadId"])
	}
	if _, ok := params["path"]; ok {
		t.Error("resume sent a path; for a running thread it is a consistency check that can " +
			"refuse the rejoin, and for a stopped one it overrides the id")
	}
	if params["excludeTurns"] != true {
		t.Error("resume hydrated the whole history to append one message")
	}
}

// A turn started by a wake says so, and carries the peer's words as untrusted.
func TestATurnCarriesTheMessageAsUntrustedContext(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(context.Background(), StartTurnParams{
			ThreadID:    "thread-1",
			Input:       []TurnInput{{Type: "text", Text: "a peer sent this"}},
			TurnTrigger: "agenthub-wake",
			AdditionalContext: map[string]ContextEntry{
				"agenthub:message": {Kind: ContextUntrusted, Value: "written by node_peer"},
			},
		})
		done <- err
	}()

	id, method := server.nextCall(t)
	if method != "turn/start" {
		t.Fatalf("method = %q", method)
	}
	server.reply(t, id, map[string]any{"turn": map[string]any{"id": "turn-1"}})
	if err := <-done; err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	params := server.seen()[0]["params"].(map[string]any)
	if params["turnTrigger"] != "agenthub-wake" {
		t.Errorf("turnTrigger = %v; an owner reading their own Codex history cannot see "+
			"which turns this node started", params["turnTrigger"])
	}
	context, ok := params["additionalContext"].(map[string]any)
	if !ok {
		t.Fatalf("additionalContext = %v", params["additionalContext"])
	}
	entry, ok := context["agenthub:message"].(map[string]any)
	if !ok {
		t.Fatalf("the message fragment is missing: %v", context)
	}
	// The one defence here that does not depend on prose holding: Codex has a
	// first-class notion of untrusted context, and this uses it.
	if entry["kind"] != ContextUntrusted {
		t.Errorf("kind = %v, want %q", entry["kind"], ContextUntrusted)
	}
}

// Nobody is present, so nothing is approved.
//
// This is the boundary that makes waking safe to have. Codex asks before it
// runs a command, edits a file, or widens its own permissions; a woken turn
// has no one to ask, and every one of those is refused rather than left
// hanging — an unanswered request wedges the session on a prompt the owner
// never saw.
func TestAWokenTurnApprovesNothing(t *testing.T) {
	server := newFakeServer(t)
	client := NewClientWithOptions(server.transport, Options{
		OnRequest: RefuseUnattendedApprovals(""),
	})
	t.Cleanup(func() { _ = client.Close() })

	for _, request := range []struct {
		method       string
		wantDecision string
	}{
		{"item/commandExecution/requestApproval", "decline"},
		{"item/fileChange/requestApproval", "decline"},
	} {
		server.send(t, map[string]any{
			"id": 900, "method": request.method,
			"params": map[string]any{"command": []string{"cat", "/Users/someone/.ssh/id_ed25519"}},
		})
		frame := server.next(t)
		result, ok := frame["result"].(map[string]any)
		if !ok {
			t.Errorf("%s was answered with %v, want a typed refusal", request.method, frame)
			continue
		}
		if result["decision"] != request.wantDecision {
			t.Errorf("%s decision = %v, want %q", request.method, result["decision"], request.wantDecision)
		}
	}

	// The older pair take ReviewDecision, whose refusal is a `denied` object
	// rather than a string. `abort` would also be refused, but it halts the
	// turn until the user's next command — and there is no user.
	for _, method := range []string{"execCommandApproval", "applyPatchApproval"} {
		server.send(t, map[string]any{"id": 902, "method": method, "params": map[string]any{}})
		frame := server.next(t)
		result, ok := frame["result"].(map[string]any)
		if !ok {
			t.Errorf("%s was answered with %v", method, frame)
			continue
		}
		decision, ok := result["decision"].(map[string]any)
		if !ok {
			t.Errorf("%s decision = %v, want a denied object", method, result["decision"])
			continue
		}
		denied, ok := decision["denied"].(map[string]any)
		if !ok {
			t.Errorf("%s decision = %v, want denied", method, decision)
			continue
		}
		// rejection is required by DeniedReviewDecision. An empty object is
		// schema-invalid, which the app-server is free to treat as a
		// malformed answer — and an answer it discards is one nobody gave.
		if rejection, _ := denied["rejection"].(string); rejection == "" {
			t.Errorf("%s sent denied with no rejection: %v", method, denied)
		}
	}

	// Elicitation has a typed decline as well.
	server.send(t, map[string]any{
		"id": 903, "method": "mcpServer/elicitation/request", "params": map[string]any{},
	})
	if frame := server.next(t); frame["result"].(map[string]any)["action"] != "decline" {
		t.Errorf("elicitation was answered with %v", frame)
	}

	// The ones whose response shape has no refusal in it. Answering with a
	// result would have to be a grant, so these are refused as errors — and so
	// is anything added to the protocol after this was written.
	for _, method := range []string{
		"item/permissions/requestApproval",
		"item/tool/requestUserInput",
		"item/tool/call",
		"currentTime/read",
		"something/inventedLater",
	} {
		server.send(t, map[string]any{"id": 901, "method": method, "params": map[string]any{}})
		frame := server.next(t)
		if _, ok := frame["result"]; ok {
			t.Errorf("%s was answered with a result: %v", method, frame)
			continue
		}
		failure, ok := frame["error"].(map[string]any)
		if !ok {
			t.Errorf("%s was not refused: %v", method, frame)
			continue
		}
		if message, _ := failure["message"].(string); !strings.Contains(message, method) {
			t.Errorf("%s refusal does not name what was refused: %v", method, failure["message"])
		}
	}
}

// A client with no handler refuses everything, because a request arriving with
// nothing to answer it is one nobody is present for.
func TestAClientWithNoHandlerRefusesEveryRequest(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)
	t.Cleanup(func() { _ = client.Close() })

	server.send(t, map[string]any{
		"id": 5, "method": "item/commandExecution/requestApproval", "params": map[string]any{},
	})
	frame := server.next(t)
	if _, ok := frame["error"]; !ok {
		t.Errorf("a client with no handler answered %v", frame)
	}
}

// An approval that arrives while a call is outstanding is answered without
// stalling the call, and the call still gets its response.
//
// The old client could not do this at all: it read only while a call was in
// flight and treated any server request as fatal to that call. A real turn
// interleaves the two constantly.
func TestAnApprovalDuringACallStallsNeither(t *testing.T) {
	server := newFakeServer(t)
	answered := make(chan string, 1)
	client := NewClientWithOptions(server.transport, Options{
		OnRequest: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			answered <- method
			return map[string]any{"decision": "decline"}, nil
		},
	})
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(context.Background(), StartTurnParams{
			ThreadID: "thread-1", Input: []TurnInput{{Type: "text", Text: "go"}},
		})
		done <- err
	}()
	id, _ := server.nextCall(t)

	// Codex asks a question before answering the call.
	server.send(t, map[string]any{
		"id": 77, "method": "item/commandExecution/requestApproval", "params": map[string]any{},
	})
	select {
	case method := <-answered:
		if method != "item/commandExecution/requestApproval" {
			t.Fatalf("handler saw %q", method)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a request arriving during a call was never handled")
	}
	if frame := server.next(t); frame["id"].(float64) != 77 {
		t.Fatalf("the refusal was not addressed to the request: %v", frame)
	}

	server.reply(t, id, map[string]any{"turn": map[string]any{"id": "turn-1"}})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the call failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call never completed after the interleaved request")
	}
}

// A server that hangs up releases everyone waiting, rather than leaving a wake
// blocked until its context expires.
func TestAClosedConnectionFailsCallsInFlight(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := client.ResumeThread(context.Background(), "thread-1")
		done <- err
	}()
	server.nextCall(t)
	server.hangUp()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a call survived the connection closing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a call outlived its transport")
	}
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done() never closed, so a supervisor would never reconnect")
	}
	if client.Err() == nil {
		t.Error("Err() says nothing about why the reader stopped")
	}
	// And a call started afterwards fails immediately rather than hanging.
	if _, err := client.ResumeThread(context.Background(), "thread-1"); err == nil {
		t.Error("a call on a closed client did not fail")
	}
}

// The handshake declares experimentalApi.
//
// turn/start.additionalContext is behind it: without the capability the
// app-server refuses the turn with -32600, so the untrusted marking — the one
// barrier here that does not depend on prose — is unreachable and every wake
// fails. That is not hypothetical; it is how the first real wake failed, and
// deleting the line passed the whole suite.
func TestInitializeDeclaresTheCapabilityAdditionalContextNeeds(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := client.Initialize(context.Background())
		done <- err
	}()
	id, method := server.nextCall(t)
	if method != "initialize" {
		t.Fatalf("first call = %q", method)
	}
	server.reply(t, id, map[string]any{
		"codexHome": "/tmp/codex", "platformFamily": "unix",
		"platformOs": "macos", "userAgent": "codex",
	})
	// The `initialized` notification is drained before waiting: the pipe is
	// unbuffered, so the client's write blocks until somebody reads it, and
	// Initialize does not return until that write completes.
	if frame := server.next(t); frame["method"] != "initialized" {
		t.Fatalf("after the reply the client sent %v", frame)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	params, ok := server.seen()[0]["params"].(map[string]any)
	if !ok {
		t.Fatalf("initialize sent %v", server.seen()[0])
	}
	capabilities, ok := params["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("initialize declared no capabilities: %v", params)
	}
	if capabilities["experimentalApi"] != true {
		t.Errorf("experimentalApi = %v; additionalContext is refused without it",
			capabilities["experimentalApi"])
	}
}

// A request whose id is a string is answered, with that id.
//
// RequestId in the protocol schema is `string | int64`. Decoding it as a
// number dropped the frame as unreadable and nothing was written back — an
// approval left unanswered, which wedges the turn on a prompt nobody is
// present to see.
func TestARequestWithAStringIdIsStillAnswered(t *testing.T) {
	server := newFakeServer(t)
	client := NewClientWithOptions(server.transport, Options{
		OnRequest: RefuseUnattendedApprovals(""),
	})
	t.Cleanup(func() { _ = client.Close() })

	server.send(t, map[string]any{
		"id": "req-abc", "method": "item/commandExecution/requestApproval",
		"params": map[string]any{},
	})
	frame := server.next(t)
	if frame["id"] != "req-abc" {
		t.Errorf("the answer came back with id %v, want the string it was sent with", frame["id"])
	}
	result, ok := frame["result"].(map[string]any)
	if !ok || result["decision"] != "decline" {
		t.Errorf("a string-id approval was answered with %v", frame)
	}
}

// Close stops the reader and closes the transport, whichever ended first.
//
// The two shared one sync.Once: a reader that ended on its own closed `done`
// through it, and Close then did nothing — the transport stayed open and the
// child process behind it stayed running. Closing the transport is also the
// only thing that can unblock a reader still parked in Read.
func TestCloseReleasesTheTransportEvenAfterTheReaderStopped(t *testing.T) {
	server := newFakeServer(t)
	transport := &countingCloser{ReadWriter: server.transport}
	client := NewClient(transport)

	// The reader ends on its own.
	server.hangUp()
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the reader never noticed the connection ending")
	}
	if transport.closed() != 0 {
		t.Fatalf("the transport was closed %d times before Close", transport.closed())
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if transport.closed() != 1 {
		t.Errorf("Close() closed the transport %d times after the reader had stopped",
			transport.closed())
	}
	// And it stays idempotent.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.closed() != 1 {
		t.Errorf("a second Close() closed the transport again (%d)", transport.closed())
	}
}

type countingCloser struct {
	io.ReadWriter
	mu    sync.Mutex
	count int
}

func (c *countingCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	return nil
}

func (c *countingCloser) closed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}
