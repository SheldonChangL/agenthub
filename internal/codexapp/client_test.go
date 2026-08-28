package codexapp

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func TestClientInitializesAndMapsLiveThreadStatus(t *testing.T) {
	responses := strings.Join([]string{
		`{"id":1,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex"}}`,
		`{"method":"thread/status/changed","params":{}}`,
		`{"id":2,"result":{"data":[{"id":"01a045ef-7f39-76a1-a638-e72b3153571d","cwd":"/work/demo","status":{"type":"active"},"updatedAt":1787882400}],"nextCursor":null}}`,
	}, "\n") + "\n"
	transport := &memoryTransport{Reader: bytes.NewBufferString(responses)}
	client := NewClient(transport)

	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	threads, err := client.ListThreads(context.Background(), "")
	if err != nil {
		t.Fatalf("ListThreads() error = %v", err)
	}
	if len(threads.Data) != 1 || threads.Data[0].Status.Type != "active" {
		t.Fatalf("threads = %#v", threads)
	}

	now := time.Date(2026, 8, 28, 10, 1, 0, 0, time.UTC)
	sessions := NormalizeThreads(threads.Data, now)
	if len(sessions) != 1 || sessions[0].Status != model.StatusActive || sessions[0].Visibility != model.VisibilityPrivate {
		t.Fatalf("sessions = %#v", sessions)
	}
	requests := transport.Writer.String()
	for _, expected := range []string{`"method":"initialize"`, `"method":"initialized"`, `"method":"thread/list"`} {
		if !strings.Contains(requests, expected) {
			t.Fatalf("requests = %s; missing %s", requests, expected)
		}
	}
}

func TestNormalizeThreadsMapsNonRunnableStatesConservatively(t *testing.T) {
	now := time.Now().UTC()
	threads := []Thread{
		{ID: "not-loaded", Status: ThreadStatus{Type: "notLoaded"}, UpdatedAt: now.Unix()},
		{ID: "error", Status: ThreadStatus{Type: "systemError"}, UpdatedAt: now.Unix()},
	}

	sessions := NormalizeThreads(threads, now)
	if sessions[0].Status != model.StatusInactive || sessions[1].Status != model.StatusUnknown {
		t.Fatalf("statuses = %q, %q", sessions[0].Status, sessions[1].Status)
	}
}

type memoryTransport struct {
	Reader *bytes.Buffer
	Writer bytes.Buffer
}

func (m *memoryTransport) Read(data []byte) (int, error) {
	return m.Reader.Read(data)
}

func (m *memoryTransport) Write(data []byte) (int, error) {
	return m.Writer.Write(data)
}
