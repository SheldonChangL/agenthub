package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"agenthub.local/agenthub/internal/model"
)

type runner struct {
	baseURL string
	json    bool
	client  *http.Client
	stdout  io.Writer
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ah", flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaultURL := os.Getenv("AGENTHUB_URL")
	if defaultURL == "" {
		defaultURL = "http://127.0.0.1:7462"
	}
	baseURL := flags.String("url", defaultURL, "AgentHub node URL")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { printUsage(stderr) }
	if err := flags.Parse(args); err != nil {
		return 2
	}
	remaining := flags.Args()
	if len(remaining) == 0 {
		printUsage(stderr)
		return 2
	}
	parsedURL, err := url.Parse(*baseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		fmt.Fprintln(stderr, "invalid --url")
		return 2
	}

	r := runner{
		baseURL: strings.TrimRight(*baseURL, "/"),
		json:    *jsonOutput,
		client:  &http.Client{Timeout: 10 * time.Second},
		stdout:  stdout,
	}
	if err := r.command(ctx, remaining); err != nil {
		fmt.Fprintln(stderr, "ah:", err)
		return 1
	}
	return 0
}

func (r runner) command(ctx context.Context, args []string) error {
	switch args[0] {
	case "discover":
		return r.simple(ctx, http.MethodPost, "/v1/discover", nil)
	case "list", "ls", "ps":
		return r.list(ctx)
	case "status":
		if len(args) != 2 {
			return errors.New("usage: ah status <session-id>")
		}
		return r.simple(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(args[1]), nil)
	case "publish", "unpublish":
		if len(args) != 2 {
			return fmt.Errorf("usage: ah %s <session-id>", args[0])
		}
		visibility := model.VisibilityPublic
		if args[0] == "unpublish" {
			visibility = model.VisibilityPrivate
		}
		return r.simple(ctx, http.MethodPut, "/v1/sessions/"+url.PathEscape(args[1])+"/visibility", map[string]any{"visibility": visibility})
	case "send":
		if len(args) < 3 {
			return errors.New("usage: ah send <session-id> <message>")
		}
		return r.simple(ctx, http.MethodPost, "/v1/messages", map[string]string{"to": args[1], "body": strings.Join(args[2:], " ")})
	case "inbox":
		if len(args) != 2 {
			return errors.New("usage: ah inbox <session-id>")
		}
		return r.simple(ctx, http.MethodGet, "/v1/inbox/"+url.PathEscape(args[1]), nil)
	case "node":
		return r.simple(ctx, http.MethodGet, "/v1/node", nil)
	case "heartbeat":
		return r.simple(ctx, http.MethodGet, "/v1/heartbeat", nil)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (r runner) list(ctx context.Context) error {
	type listResponse struct {
		Sessions   []model.Session `json:"sessions"`
		Pagination struct {
			Page       int `json:"page"`
			TotalItems int `json:"totalItems"`
			TotalPages int `json:"totalPages"`
		} `json:"pagination"`
	}
	allSessions := make([]model.Session, 0)
	totalItems := 0
	for page := 1; ; page++ {
		body, err := r.request(ctx, http.MethodGet, fmt.Sprintf("/v1/sessions?page=%d&pageSize=200", page), nil)
		if err != nil {
			return err
		}
		var response listResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return fmt.Errorf("decode session list: %w", err)
		}
		allSessions = append(allSessions, response.Sessions...)
		totalItems = response.Pagination.TotalItems
		if response.Pagination.TotalPages == 0 || page >= response.Pagination.TotalPages {
			break
		}
	}
	if r.json {
		data, err := json.Marshal(map[string]any{
			"sessions":   allSessions,
			"pagination": map[string]int{"page": 1, "pageSize": len(allSessions), "totalItems": totalItems, "totalPages": 1},
		})
		if err != nil {
			return fmt.Errorf("encode session list: %w", err)
		}
		return writePrettyJSON(r.stdout, data)
	}
	w := tabwriter.NewWriter(r.stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tPROVIDER\tSTATUS\tMODE\tVISIBILITY\tCWD")
	for _, session := range allSessions {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", session.ID, session.Provider, session.Status, session.Management, session.Visibility, session.CWD)
	}
	return w.Flush()
}

func (r runner) simple(ctx context.Context, method, path string, input any) error {
	body, err := r.request(ctx, method, path, input)
	if err != nil {
		return err
	}
	return writePrettyJSON(r.stdout, body)
}

func (r runner) request(ctx context.Context, method, path string, input any) ([]byte, error) {
	var body io.Reader
	if input != nil {
		var encoded bytes.Buffer
		if err := json.NewEncoder(&encoded).Encode(input); err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = &encoded
	}
	request, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("contact node: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &apiError) == nil && apiError.Error.Message != "" {
			return nil, fmt.Errorf("%s: %s", apiError.Error.Code, apiError.Error.Message)
		}
		return nil, fmt.Errorf("node returned HTTP %d", response.StatusCode)
	}
	return data, nil
}

func writePrettyJSON(output io.Writer, data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode response JSON: %w", err)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "usage: ah [--url URL] [--json] <command>")
	_, _ = fmt.Fprintln(output, "commands: discover, list, status, publish, unpublish, send, inbox, node, heartbeat")
}
