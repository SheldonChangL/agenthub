package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"agenthub.local/agenthub/internal/buildinfo"
	"agenthub.local/agenthub/internal/model"
)

// Client speaks JSON-RPC to a Codex App Server over one transport.
//
// A single background reader owns the transport, because a woken turn is not
// request/response. Starting a turn produces a stream the caller did not ask
// for — thread/started, item/*, turn/completed — and, crucially, requests from
// the server: Codex asks the client to approve a command, a file change, a
// permission. The earlier client read only while a call was outstanding and
// aborted on any server request, which is fine for thread/list and cannot
// drive a turn at all.
type Client struct {
	transport io.ReadWriter
	closer    io.Closer

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan rawResponse

	// onRequest answers what the server asks. Nil declines everything, which
	// is the safe default: a request arriving with no handler is one nobody is
	// present to answer.
	onRequest RequestHandler
	// onNotify observes the stream. Nil ignores it.
	onNotify func(method string, params json.RawMessage)

	done      chan struct{}
	closeOnce sync.Once
	readErr   atomic.Pointer[error]
}

// RequestHandler answers a request the server made of the client.
//
// Returning an error refuses it as a JSON-RPC error, which is how a request
// with no "no" in its result shape is declined: PermissionsRequestApproval has
// no denial variant at all, so an error is the only refusal that cannot be
// misread as a grant.
type RequestHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

type rawResponse struct {
	result json.RawMessage
	err    error
}

// Options adjust a Client at construction.
type Options struct {
	OnRequest RequestHandler
	OnNotify  func(method string, params json.RawMessage)
}

func NewClient(transport io.ReadWriter) *Client {
	return NewClientWithOptions(transport, Options{})
}

func NewClientWithOptions(transport io.ReadWriter, options Options) *Client {
	client := &Client{
		transport: transport,
		pending:   map[int64]chan rawResponse{},
		onRequest: options.OnRequest,
		onNotify:  options.OnNotify,
		done:      make(chan struct{}),
	}
	if closer, ok := transport.(io.Closer); ok {
		client.closer = closer
	}
	go client.read()
	return client
}

// Close stops the reader and closes the transport if it owns one.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.done)
		if c.closer != nil {
			err = c.closer.Close()
		}
	})
	return err
}

// Done is closed when the reader stops, whether from Close or from the
// transport ending. A supervisor watches it to know when to reconnect.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err reports why the reader stopped, if it stopped on its own.
func (c *Client) Err() error {
	if stored := c.readErr.Load(); stored != nil {
		return *stored
	}
	return nil
}

// read owns the transport for the client's whole life.
func (c *Client) read() {
	defer c.stop()
	scanner := bufio.NewScanner(c.transport)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var frame struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			// One unreadable line is not a reason to drop a working
			// connection: it is far more likely to be something new in the
			// protocol than a broken stream.
			log.Printf("codexapp: skipping an unreadable frame: %v", err)
			continue
		}
		switch {
		case frame.ID != nil && frame.Method != "":
			// A request from the server. Answered on its own goroutine: a
			// handler that blocks must not stop the reader, or the answer it
			// is waiting for could never arrive.
			go c.answer(*frame.ID, frame.Method, frame.Params)
		case frame.ID != nil:
			c.deliver(*frame.ID, frame)
		case frame.Method != "":
			if c.onNotify != nil {
				c.onNotify(frame.Method, frame.Params)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		wrapped := fmt.Errorf("read Codex App Server: %w", err)
		c.readErr.CompareAndSwap(nil, &wrapped)
		return
	}
	closed := errors.New("Codex App Server closed the connection")
	c.readErr.CompareAndSwap(nil, &closed)
}

// stop releases everyone still waiting, so a call outlives its transport by
// the time it takes to notice rather than for ever.
func (c *Client) stop() {
	c.closeOnce.Do(func() { close(c.done) })
	err := c.Err()
	if err == nil {
		err = errors.New("Codex App Server connection closed")
	}
	c.pendingMu.Lock()
	waiters := c.pending
	c.pending = map[int64]chan rawResponse{}
	c.pendingMu.Unlock()
	for _, waiter := range waiters {
		waiter <- rawResponse{err: err}
	}
}

func (c *Client) deliver(id int64, frame struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
},
) {
	c.pendingMu.Lock()
	waiter, ok := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if !ok {
		// A response to a call that has already given up. Nothing to do, and
		// not worth a log line: a cancelled call produces exactly this.
		return
	}
	switch {
	case frame.Error != nil:
		waiter <- rawResponse{err: fmt.Errorf("Codex App Server error %d: %s",
			frame.Error.Code, frame.Error.Message)}
	case len(frame.Result) == 0:
		waiter <- rawResponse{err: errors.New("Codex App Server response has no result")}
	default:
		waiter <- rawResponse{result: frame.Result}
	}
}

// answer replies to a request the server made.
func (c *Client) answer(id int64, method string, params json.RawMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if c.onRequest == nil {
		c.writeError(id, fmt.Sprintf("agenthub does not answer %q", method))
		return
	}
	result, err := c.onRequest(ctx, method, params)
	if err != nil {
		c.writeError(id, err.Error())
		return
	}
	if err := c.write(struct {
		ID     int64 `json:"id"`
		Result any   `json:"result"`
	}{ID: id, Result: result}); err != nil {
		log.Printf("codexapp: cannot answer %s: %v", method, err)
	}
}

func (c *Client) writeError(id int64, message string) {
	type rpcError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := c.write(struct {
		ID    int64    `json:"id"`
		Error rpcError `json:"error"`
	}{ID: id, Error: rpcError{Code: -32000, Message: message}}); err != nil {
		log.Printf("codexapp: cannot refuse request %d: %v", id, err)
	}
}

func (c *Client) write(value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return json.NewEncoder(c.transport).Encode(value)
}

type InitializeResult struct {
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	UserAgent      string `json:"userAgent"`
}

func (c *Client) Initialize(ctx context.Context) (InitializeResult, error) {
	var result InitializeResult
	params := map[string]any{
		"clientInfo": map[string]string{"name": "agenthub", "version": buildinfo.Version()},
	}
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return InitializeResult{}, err
	}
	if result.CodexHome == "" || result.PlatformOS == "" {
		return InitializeResult{}, errors.New("Codex App Server returned incomplete initialize result")
	}
	if err := c.notify(ctx, "initialized"); err != nil {
		return InitializeResult{}, err
	}
	return result, nil
}

type ThreadStatus struct {
	Type string `json:"type"`
}

type Thread struct {
	ID        string       `json:"id"`
	CWD       string       `json:"cwd"`
	Status    ThreadStatus `json:"status"`
	UpdatedAt int64        `json:"updatedAt"`
}

type ThreadListResult struct {
	Data       []Thread `json:"data"`
	NextCursor *string  `json:"nextCursor"`
}

func (c *Client) ListThreads(ctx context.Context, cursor string) (ThreadListResult, error) {
	params := map[string]any{
		"limit":          200,
		"sortKey":        "updated_at",
		"sortDirection":  "desc",
		"useStateDbOnly": true,
	}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var result ThreadListResult
	if err := c.call(ctx, "thread/list", params, &result); err != nil {
		return ThreadListResult{}, err
	}
	if result.Data == nil {
		result.Data = make([]Thread, 0)
	}
	return result, nil
}

func (c *Client) call(ctx context.Context, method string, params any, destination any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.nextID.Add(1)
	waiter := make(chan rawResponse, 1)
	c.pendingMu.Lock()
	select {
	case <-c.done:
		c.pendingMu.Unlock()
		if err := c.Err(); err != nil {
			return err
		}
		return errors.New("Codex App Server connection is closed")
	default:
	}
	c.pending[id] = waiter
	c.pendingMu.Unlock()

	if err := c.write(struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: id, Method: method, Params: params}); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return fmt.Errorf("write Codex App Server %s request: %w", method, err)
	}

	select {
	case <-ctx.Done():
		// The waiter stays registered only long enough to be forgotten here,
		// so a late response finds nothing and is dropped rather than
		// delivered to the next caller to reuse the channel.
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return ctx.Err()
	case response := <-waiter:
		if response.err != nil {
			return response.err
		}
		if destination == nil {
			return nil
		}
		if err := json.Unmarshal(response.result, destination); err != nil {
			return fmt.Errorf("decode Codex App Server %s result: %w", method, err)
		}
		return nil
	}
}

func (c *Client) notify(ctx context.Context, method string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.write(map[string]string{"method": method}); err != nil {
		return fmt.Errorf("write Codex App Server %s notification: %w", method, err)
	}
	return nil
}

func NormalizeThreads(threads []Thread, observedAt time.Time) []model.Session {
	sessions := make([]model.Session, 0, len(threads))
	for _, thread := range threads {
		id := strings.TrimSpace(thread.ID)
		if !validID(id) {
			continue
		}
		status := model.StatusUnknown
		switch thread.Status.Type {
		case "active":
			status = model.StatusActive
		case "idle":
			status = model.StatusIdle
		case "notLoaded":
			status = model.StatusInactive
		case "systemError":
			status = model.StatusUnknown
		}
		lastSeen := observedAt.UTC()
		if thread.UpdatedAt > 0 {
			lastSeen = time.Unix(thread.UpdatedAt, 0).UTC()
		}
		sessions = append(sessions, model.Session{
			ID:                model.SessionID(model.ProviderCodex, id),
			Provider:          model.ProviderCodex,
			ProviderSessionID: id,
			Management:        model.Unmanaged,
			Visibility:        model.VisibilityPrivate,
			Status:            status,
			StatusSource:      "codex_app_server",
			CWD:               thread.CWD,
			Source:            "codex-app-server",
			LastSeenAt:        lastSeen,
			UpdatedAt:         observedAt.UTC(),
		})
	}
	return sessions
}

// validID matches what the registry will accept, so this client rejects a
// thread the store would reject later rather than surfacing it as usable.
func validID(id string) bool {
	if id == "" || len(id) > 256 || strings.ContainsFunc(id, unicode.IsControl) {
		return false
	}
	return model.ValidateProviderSessionID(id) == nil
}
