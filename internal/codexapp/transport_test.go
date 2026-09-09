package codexapp

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
	"testing"
)

// fakeServer is a Codex App Server that answers on demand.
//
// A pre-filled buffer cannot test this client any more, and the reason is the
// point of the rewrite: one background reader owns the transport for the
// client's whole life, so a transport that reaches EOF is a server that hung
// up, not a script that ran out. Everything a woken turn depends on —
// out-of-order responses, notifications between them, requests from the server
// — needs a peer that talks back.
type fakeServer struct {
	toClient   *io.PipeWriter
	fromClient *bufio.Scanner
	transport  io.ReadWriter

	mu       sync.Mutex
	requests []map[string]any
}

type pipeTransport struct {
	io.Reader
	io.Writer
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	server := &fakeServer{
		toClient:   serverWrites,
		fromClient: bufio.NewScanner(serverReads),
		transport:  pipeTransport{Reader: clientReads, Writer: clientWrites},
	}
	server.fromClient.Buffer(make([]byte, 64*1024), 8*1024*1024)
	t.Cleanup(func() {
		_ = clientWrites.Close()
		_ = serverWrites.Close()
	})
	return server
}

// next reads one frame the client sent and records it.
func (s *fakeServer) next(t *testing.T) map[string]any {
	t.Helper()
	if !s.fromClient.Scan() {
		t.Fatalf("the client sent nothing: %v", s.fromClient.Err())
	}
	var frame map[string]any
	if err := json.Unmarshal(s.fromClient.Bytes(), &frame); err != nil {
		t.Fatalf("decode client frame %q: %v", s.fromClient.Bytes(), err)
	}
	s.mu.Lock()
	s.requests = append(s.requests, frame)
	s.mu.Unlock()
	return frame
}

// nextCall reads frames until one has an id, skipping notifications.
func (s *fakeServer) nextCall(t *testing.T) (int64, string) {
	t.Helper()
	for range 10 {
		frame := s.next(t)
		if raw, ok := frame["id"]; ok {
			id, _ := raw.(float64)
			method, _ := frame["method"].(string)
			return int64(id), method
		}
	}
	t.Fatal("the client sent only notifications")
	return 0, ""
}

func (s *fakeServer) send(t *testing.T, frame any) {
	t.Helper()
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.toClient.Write(append(encoded, '\n')); err != nil {
		t.Fatalf("write to client: %v", err)
	}
}

func (s *fakeServer) reply(t *testing.T, id int64, result any) {
	t.Helper()
	s.send(t, map[string]any{"id": id, "result": result})
}

func (s *fakeServer) seen() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.requests...)
}

// hangUp ends the connection from the server's side.
func (s *fakeServer) hangUp() { _ = s.toClient.Close() }
