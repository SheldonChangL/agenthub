package codexapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"
)

// Supervisor keeps one Codex App Server connection available.
//
// A wake arrives when it arrives. Connecting on demand and holding the
// connection open is what makes the difference between a message that starts a
// turn in a second and one that spends a process launch doing it — and, more
// importantly, it is what lets a turn's approval requests be answered, since
// those arrive on the same connection long after the call that started them.
type Supervisor struct {
	// command builds the process to talk to. A field so a test can supply
	// something that is not codex.
	command func(ctx context.Context) *exec.Cmd
	// handler answers what the server asks of the client, on every connection.
	handler RequestHandler
	// backoff bounds how fast a failing app-server is retried.
	backoff time.Duration

	mu      sync.Mutex
	client  *Client
	process *exec.Cmd
	// nextAttempt is when a reconnection may next be tried. A server that
	// exits immediately must not be respawned in a tight loop.
	nextAttempt time.Time
	now         func() time.Time
}

// SupervisorOptions adjust a Supervisor.
type SupervisorOptions struct {
	// Command overrides how the app-server process is started.
	Command func(ctx context.Context) *exec.Cmd
	// Handler answers the server's requests. Defaults to refusing everything,
	// which is right for the only caller: a woken turn has nobody present.
	Handler RequestHandler
	// Backoff is the shortest gap between connection attempts.
	Backoff time.Duration
	Now     func() time.Time
}

// NewSupervisor returns a supervisor over `codex app-server`.
func NewSupervisor(options SupervisorOptions) *Supervisor {
	supervisor := &Supervisor{
		command: options.Command,
		handler: options.Handler,
		backoff: options.Backoff,
		now:     options.Now,
	}
	if supervisor.command == nil {
		supervisor.command = func(ctx context.Context) *exec.Cmd {
			// #nosec G204 -- a fixed argv with no interpolation; the binary is
			// resolved from PATH exactly as the owner's own `codex` would be.
			return exec.CommandContext(ctx, "codex", "app-server")
		}
	}
	if supervisor.handler == nil {
		supervisor.handler = RefuseUnattendedApprovals("")
	}
	if supervisor.backoff <= 0 {
		supervisor.backoff = 5 * time.Second
	}
	if supervisor.now == nil {
		supervisor.now = time.Now
	}
	return supervisor
}

// Client returns a connected client, connecting if there is not one.
//
// The context bounds this attempt, not the connection: the process outlives
// the wake that started it, because the next wake should not pay for another
// launch and because a turn's approval requests arrive on it later.
func (s *Supervisor) Client(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		select {
		case <-s.client.Done():
			// The reader stopped. Clear it and fall through to reconnect, so
			// one dead app-server does not make every later wake fail.
			s.discard()
		default:
			return s.client, nil
		}
	}
	if now := s.now(); now.Before(s.nextAttempt) {
		return nil, fmt.Errorf("Codex App Server is not connected; the next attempt is in %s",
			s.nextAttempt.Sub(now).Round(time.Second))
	}
	s.nextAttempt = s.now().Add(s.backoff)

	client, process, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	s.client, s.process = client, process
	return client, nil
}

func (s *Supervisor) connect(ctx context.Context) (*Client, *exec.Cmd, error) {
	// Deliberately not the caller's context: cancelling one wake must not kill
	// a process the next wake will use. The process is stopped by Close.
	process := s.command(context.Background())
	stdin, err := process.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("codex app-server stdin: %w", err)
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("codex app-server stdout: %w", err)
	}
	if err := process.Start(); err != nil {
		return nil, nil, fmt.Errorf("start codex app-server: %w", err)
	}

	client := NewClientWithOptions(processTransport{Reader: stdout, Writer: stdin}, Options{
		OnRequest: s.handler,
	})
	// The handshake is part of connecting: a server that will not initialize
	// is not one to hand to a caller as if it were ready.
	if _, err := client.Initialize(ctx); err != nil {
		_ = client.Close()
		_ = process.Process.Kill()
		_ = process.Wait()
		return nil, nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	go func() {
		<-client.Done()
		// Reaped here so a server that exits does not stay a zombie until the
		// node does. The error is the process's, not this node's.
		if err := process.Wait(); err != nil {
			log.Printf("codexapp: app-server exited: %v", err)
		}
	}()
	return client, process, nil
}

// discard forgets the current connection. The caller holds the lock.
func (s *Supervisor) discard() {
	if s.client != nil {
		_ = s.client.Close()
	}
	if s.process != nil && s.process.Process != nil {
		_ = s.process.Process.Kill()
	}
	s.client, s.process = nil, nil
}

// Close stops the app-server this supervisor started.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discard()
	return nil
}

// processTransport is a child process's stdio as one stream.
type processTransport struct {
	Reader io.Reader
	Writer io.WriteCloser
}

func (p processTransport) Read(data []byte) (int, error)  { return p.Reader.Read(data) }
func (p processTransport) Write(data []byte) (int, error) { return p.Writer.Write(data) }

// Close closes only the write half. Closing stdin is how an app-server is told
// to stop; the read half ends on its own when the process does.
func (p processTransport) Close() error {
	if p.Writer == nil {
		return errors.New("no writer to close")
	}
	return p.Writer.Close()
}
