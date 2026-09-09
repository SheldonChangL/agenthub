package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// nodeStub answers the wake stream however a test says.
type nodeStub struct {
	server  *httptest.Server
	calls   atomic.Int64
	waits   chan string
	answer  func(call int64) (int, string, string)
	hang    bool
	baseURL string
}

func newNodeStub(t *testing.T, answer func(call int64) (int, string, string)) *nodeStub {
	t.Helper()
	stub := &nodeStub{answer: answer, waits: make(chan string, 64)}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := stub.calls.Add(1)
		select {
		case stub.waits <- r.URL.Query().Get("wait"):
		default:
		}
		if stub.hang {
			<-r.Context().Done()
			return
		}
		status, header, body := stub.answer(call)
		if header != "" {
			w.Header().Set("Agenthub-Wake-Wait", header)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(stub.server.Close)
	stub.baseURL = stub.server.URL
	return stub
}

func stubClient(t *testing.T, stub *nodeStub) *Client {
	t.Helper()
	client, err := NewClient(stub.baseURL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// A quiet poll is not an error, and the client adopts the wait the node says
// it used.
//
// The client cannot see the node's write deadline — it belongs to the
// http.Server, not the request — so it asks and is corrected. Without this it
// picked a number, and the number it picked was one the node could never
// answer within.
func TestTheClientAdoptsTheWaitTheNodeReports(t *testing.T) {
	stub := newNodeStub(t, func(int64) (int, string, string) {
		return http.StatusNoContent, "7s", ""
	})
	client := stubClient(t, stub)

	push, err := client.WaitForWake(context.Background(), "claude:x")
	if err != nil || push != nil {
		t.Fatalf("a quiet poll gave %+v, %v", push, err)
	}
	if first := <-stub.waits; first != WakeWait.String() {
		t.Errorf("the first poll asked for %q, want the default %s", first, WakeWait)
	}
	if _, err := client.WaitForWake(context.Background(), "claude:x"); err != nil {
		t.Fatal(err)
	}
	if second := <-stub.waits; second != "7s" {
		t.Errorf("the second poll asked for %q; the node said it holds for 7s", second)
	}
}

// A wake with no message id is refused rather than pushed as an empty one.
func TestAWakeWithNoMessageIdIsRefused(t *testing.T) {
	stub := newNodeStub(t, func(int64) (int, string, string) {
		return http.StatusOK, "", `{"body":"hello","notice":"careful"}`
	})
	if _, err := stubClient(t, stub).WaitForWake(context.Background(), "claude:x"); err == nil {
		t.Error("a wake with no message id was accepted")
	}
}

// The node's two terminal answers are told apart, because they mean different
// things to the loop that reads them.
func TestTheClientDistinguishesUnavailableFromReplaced(t *testing.T) {
	unavailable := newNodeStub(t, func(int64) (int, string, string) {
		return http.StatusNotFound, "", `{"error":{"code":"WAKE_UNAVAILABLE"}}`
	})
	if _, err := stubClient(t, unavailable).WaitForWake(context.Background(), "claude:x"); !errors.Is(err, ErrWakeUnavailable) {
		t.Errorf("a 404 gave %v, want ErrWakeUnavailable", err)
	}
	replaced := newNodeStub(t, func(int64) (int, string, string) {
		return http.StatusConflict, "", `{"error":{"code":"WAKE_STREAM_REPLACED"}}`
	})
	if _, err := stubClient(t, replaced).WaitForWake(context.Background(), "claude:x"); !errors.Is(err, ErrWakeReplaced) {
		t.Errorf("a 409 gave %v, want ErrWakeReplaced", err)
	}
}

// pushWakes stops when another subscriber takes the session over.
//
// Polling on would displace the winner in turn, and the two would take the
// session from each other for as long as both ran.
func TestPushWakesStopsWhenDisplaced(t *testing.T) {
	stub := newNodeStub(t, func(int64) (int, string, string) {
		return http.StatusConflict, "", `{"error":{"code":"WAKE_STREAM_REPLACED"}}`
	})
	server := pushServer(t, stub)

	done := make(chan struct{})
	go func() { defer close(done); server.pushWakes(context.Background(), &injectingTransport{}) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a displaced server kept polling; the two would take the session " +
			"from each other for as long as both ran")
	}
	if calls := stub.calls.Load(); calls != 1 {
		t.Errorf("it polled %d times after being displaced", calls)
	}
}

// A failing node is not polled in a tight loop.
//
// Without a wait between attempts this is a busy loop against a machine
// already having a bad moment, and it is the owner's own machine.
func TestPushWakesDoesNotSpinAgainstAFailingNode(t *testing.T) {
	stub := newNodeStub(t, func(int64) (int, string, string) {
		return http.StatusInternalServerError, "", `{"error":{"code":"BOOM"}}`
	})
	server := pushServer(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	server.pushWakes(ctx, &injectingTransport{})

	if calls := stub.calls.Load(); calls > 3 {
		t.Errorf("it polled %d times in 1.5s against a failing node", calls)
	}
	if calls := stub.calls.Load(); calls == 0 {
		t.Error("it never polled at all")
	}
}

// A node that says it has no wake stream is retried, not abandoned.
//
// It used to stop for the life of the process, so a node restarted with
// -auto-wake left this server silent until the agent itself restarted.
func TestPushWakesKeepsTryingAfterAnUnavailableNode(t *testing.T) {
	stub := newNodeStub(t, func(int64) (int, string, string) {
		return http.StatusNotFound, "", `{"error":{"code":"WAKE_UNAVAILABLE"}}`
	})
	server := pushServer(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	server.pushWakes(ctx, &injectingTransport{})

	// It polled, and it was still waiting to try again rather than having
	// returned after the first answer.
	if calls := stub.calls.Load(); calls != 1 {
		t.Errorf("it polled %d times, want one attempt then a wait", calls)
	}
	if ctx.Err() == nil {
		t.Error("pushWakes returned before its context ended, so it gave up on the node")
	}
}

// A transport that has not connected refuses to write rather than dereferencing
// a nil connection.
func TestNotifyingBeforeTheClientConnectsIsAnError(t *testing.T) {
	transport := &injectingTransport{}
	err := transport.notify(context.Background(), ChannelMethod, &channelParams{Content: "hi"})
	if err == nil {
		t.Fatal("notifying an unconnected transport was accepted")
	}
	if !strings.Contains(err.Error(), ChannelMethod) {
		t.Errorf("the error does not say what could not be sent: %v", err)
	}
}

func pushServer(t *testing.T, stub *nodeStub) *server {
	t.Helper()
	built, err := New(stubClient(t, stub), Binding{sessionID: "claude:x"},
		"node_1234567890123456", WithChannel())
	if err != nil {
		t.Fatal(err)
	}
	return built
}

// The long-poll client has no fixed timeout of its own.
//
// How long a poll may take changes with what the node says it holds one for,
// so a client timeout chosen on the first call is wrong for every later one:
// putting a fifteen-second one back cut every twenty-five-second poll, and the
// suite stayed green. The deadline belongs on the request.
func TestTheLongPollClientHasNoFixedTimeout(t *testing.T) {
	stub := newNodeStub(t, func(int64) (int, string, string) {
		return http.StatusNoContent, "", ""
	})
	client := stubClient(t, stub)
	if _, err := client.WaitForWake(context.Background(), "claude:x"); err != nil {
		t.Fatal(err)
	}
	if client.longPoll == nil {
		t.Fatal("no long-poll client was built")
	}
	if client.longPoll.Timeout != 0 {
		t.Errorf("the long-poll client has a %s timeout; a poll the node holds for longer "+
			"is cut by it, and how long the node holds one is not known here",
			client.longPoll.Timeout)
	}
}

// A poll that outlasts what the node said it would hold gives up.
//
// The deadline is on the request instead, so it moves with the wait — a node
// that stops answering mid-poll is noticed rather than held forever.
func TestAPollThatOutlastsTheNodeGivesUp(t *testing.T) {
	stub := newNodeStub(t, func(int64) (int, string, string) {
		return http.StatusNoContent, "1s", ""
	})
	client := stubClient(t, stub)
	// Learn the short wait, then make the node stop answering.
	if _, err := client.WaitForWake(context.Background(), "claude:x"); err != nil {
		t.Fatal(err)
	}
	// Hangs until the client gives up, rather than sleeping blindly: a handler
	// that outlives the test blocks httptest.Server.Close.
	stub.hang = true

	started := time.Now()
	if _, err := client.WaitForWake(context.Background(), "claude:x"); err == nil {
		t.Error("a poll the node never answered came back without an error")
	}
	if elapsed := time.Since(started); elapsed > 40*time.Second {
		t.Errorf("it waited %s on a node holding a 1s poll open", elapsed)
	}
}
