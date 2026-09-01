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
		// The JSON carries a skipped count; printing it is how an unusable
		// provider record becomes visible to the owner.
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
	case "audience":
		return r.audience(ctx, args)
	case "nodes":
		return r.simple(ctx, http.MethodGet, "/v1/nodes", nil)
	case "pair":
		return r.pair(ctx, args)
	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: ah revoke <node-id>")
		}
		return r.simple(ctx, http.MethodDelete, "/v1/nodes/"+url.PathEscape(args[1]), nil)
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
	case "inbox-clear":
		// The inbox is bounded, so it needs emptying. Deletion is explicit
		// rather than inferred from reading: nothing tracks what has been read.
		if len(args) < 2 || len(args) > 3 {
			return errors.New("usage: ah inbox-clear <session-id> [message-id]")
		}
		path := "/v1/inbox/" + url.PathEscape(args[1])
		if len(args) == 3 {
			path += "/" + url.PathEscape(args[2])
		}
		return r.simple(ctx, http.MethodDelete, path, nil)
	case "outbound":
		// A message to another node is queued, not delivered, so there has to
		// be somewhere to find out what became of it.
		if len(args) != 2 {
			return errors.New("usage: ah outbound <message-id>")
		}
		return r.simple(ctx, http.MethodGet, "/v1/outbound/"+url.PathEscape(args[1]), nil)
	case "node":
		return r.simple(ctx, http.MethodGet, "/v1/node", nil)
	case "heartbeat":
		return r.simple(ctx, http.MethodGet, "/v1/heartbeat", nil)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// pair records a peer whose fingerprint the owner has already compared on both
// machines.
//
//	ah pair <node-id> <display-name> <platform> <public-key> <fingerprint...>
//
// The fingerprint is passed in deliberately: this command cannot verify
// anything by itself, and taking the value the person actually read means a
// substituted key is refused rather than trusted.
func (r runner) pair(ctx context.Context, args []string) error {
	if len(args) < 6 {
		return errors.New("usage: ah pair <node-id> <display-name> <platform> <public-key> <fingerprint>")
	}
	return r.simple(ctx, http.MethodPost, "/v1/nodes", map[string]string{
		"nodeId":               args[1],
		"displayName":          args[2],
		"platform":             args[3],
		"publicKey":            args[4],
		"confirmedFingerprint": strings.Join(args[5:], " "),
	})
}

// audience reads or replaces one session's export policy.
//
//	ah audience <session-id>
//	ah audience <session-id> none
//	ah audience <session-id> all-paired [--cwd] [--messages]
//	ah audience <session-id> selected <node-id>... [--cwd] [--messages]
func (r runner) audience(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: ah audience <session-id> [none|all-paired|selected <node-id>...] [--cwd] [--messages]")
	}
	path := "/v1/sessions/" + url.PathEscape(args[1]) + "/audience"
	if len(args) == 2 {
		return r.simple(ctx, http.MethodGet, path, nil)
	}

	modes := map[string]model.AudienceMode{
		"none":       model.AudienceNone,
		"all-paired": model.AudienceAllPaired,
		"selected":   model.AudienceSelected,
	}
	mode, ok := modes[args[2]]
	if !ok {
		return fmt.Errorf("unknown audience mode %q; want none, all-paired or selected", args[2])
	}

	nodes := make([]string, 0)
	exportCWD := false
	acceptMessages := false
	for _, argument := range args[3:] {
		switch argument {
		case "--cwd":
			exportCWD = true
		case "--messages":
			acceptMessages = true
		default:
			if strings.HasPrefix(argument, "-") {
				return fmt.Errorf("unknown flag %q; want --cwd or --messages", argument)
			}
			nodes = append(nodes, argument)
		}
	}
	if mode == model.AudienceSelected && len(nodes) == 0 {
		return errors.New("selected requires at least one node id; use none to publish to nobody")
	}
	if mode != model.AudienceSelected && len(nodes) > 0 {
		return fmt.Errorf("%s does not take node ids", args[2])
	}

	return r.simple(ctx, http.MethodPut, path, map[string]any{
		"mode":           mode,
		"nodes":          nodes,
		"exportCwd":      exportCWD,
		"acceptMessages": acceptMessages,
	})
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
	_, _ = fmt.Fprintln(w, "ID\tPROVIDER\tSTATUS\tMODE\tAUDIENCE\tCWD")
	for _, session := range allSessions {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			session.ID, session.Provider, session.Status, session.Management,
			describeAudience(session.Audience), session.CWD)
	}
	return w.Flush()
}

// describeAudience answers "published to whom" in one column. A count rather
// than a list keeps the table readable; ah audience <id> shows the nodes.
func describeAudience(audience model.Audience) string {
	switch audience.Mode {
	case model.AudienceAllPaired:
		return "all paired"
	case model.AudienceSelected:
		if len(audience.Nodes) == 1 {
			return "1 node"
		}
		return fmt.Sprintf("%d nodes", len(audience.Nodes))
	default:
		return "private"
	}
}

func (r runner) simple(ctx context.Context, method, path string, input any) error {
	body, err := r.request(ctx, method, path, input)
	if err != nil {
		return err
	}
	// A 2xx that carries no body is a success the API states by saying nothing:
	// DELETE /v1/nodes/{id} answers 204 No Content. Decoding that as JSON turns
	// a completed revocation into a reported failure, and telling an owner that
	// revoking a node failed when the trust is in fact gone is the wrong error
	// in the wrong direction — they would try again, or believe a peer still
	// has access that it does not.
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
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
	_, _ = fmt.Fprintln(output, "commands: discover, list, status, publish, unpublish, audience,")
	_, _ = fmt.Fprintln(output, "          nodes, pair, revoke, send, inbox, inbox-clear, outbound, node, heartbeat")
	_, _ = fmt.Fprintln(output, "  ah audience <session-id> [none|all-paired|selected <node-id>...] [--cwd] [--messages]")
	_, _ = fmt.Fprintln(output, "  ah pair <node-id> <display-name> <platform> <public-key> <fingerprint>")
	_, _ = fmt.Fprintln(output, "  ah send <node-id>/<provider>:<id> <message>   queues for a paired node")
	_, _ = fmt.Fprintln(output, "  ah outbound <message-id>                     what became of a queued message")
	_, _ = fmt.Fprintln(output, "  ah inbox-clear <session-id> [message-id]     empty an inbox, or drop one message")
}
