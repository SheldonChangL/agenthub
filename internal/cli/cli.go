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
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"agenthub.local/agenthub/internal/buildinfo"
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
	showVersion := flags.Bool("version", false, "print the build version and exit")
	flags.Usage = func() { printUsage(stderr) }
	if err := flags.Parse(args); err != nil {
		return 2
	}
	// Answered before anything that needs a node, and before the URL is
	// validated: the first thing asked of a binary that is misbehaving is which
	// build it is, and that answer must not depend on the thing that is broken.
	if *showVersion {
		fmt.Fprintln(stdout, buildinfo.Line("ah"))
		return 0
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
	case "pairing":
		// `ah pairing` reads, `ah pairing on [seconds]` opens, `ah pairing off`
		// closes. Reading is the default because the question asked most often
		// is "am I advertising right now".
		switch {
		case len(args) == 1:
			return r.simple(ctx, http.MethodGet, "/v1/pairing", nil)
		case args[1] == "off":
			return r.simple(ctx, http.MethodDelete, "/v1/pairing", nil)
		case args[1] == "on":
			if len(args) > 3 {
				return fmt.Errorf("ah pairing on takes one duration at most, got %d: %s\nusage: ah pairing on [seconds]",
					len(args)-2, strings.Join(args[2:], " "))
			}
			body := map[string]int{}
			if len(args) == 3 {
				seconds, err := strconv.Atoi(args[2])
				// Echoed rather than answered with a bare usage line: the
				// argument came from a shell, and what went wrong is usually
				// visible in it — a stray quote, a duration like "5m", a flag
				// that landed in the wrong place.
				if err != nil {
					return fmt.Errorf("%q is not a number of seconds\nusage: ah pairing on [seconds]", args[2])
				}
				// Zero is refused rather than sent. The API reads zero as "no
				// preference" and opens its default window, so `ah pairing on 0`
				// would open five minutes for someone who asked for none.
				if seconds <= 0 {
					return fmt.Errorf("a pairing window of %d seconds would advertise nothing; "+
						"use `ah pairing off` to stop advertising\nusage: ah pairing on [seconds]", seconds)
				}
				body["seconds"] = seconds
			}
			return r.simple(ctx, http.MethodPost, "/v1/pairing", body)
		default:
			return fmt.Errorf("ah pairing does not take %q\nusage: ah pairing [on [seconds] | off]", args[1])
		}
	case "candidates":
		if len(args) != 1 {
			return fmt.Errorf("ah candidates takes no arguments, got %s\nusage: ah candidates",
				strings.Join(args[1:], " "))
		}
		return r.simple(ctx, http.MethodGet, "/v1/pairing/candidates", nil)
	case "nodes":
		// `ah nodes` lists, `ah nodes address <node-id> <host:port>` records
		// where one of them answers. The address is a sub-command of nodes
		// rather than a verb of its own because it is not a trust decision:
		// pairing says who a node is, this says where it currently is, and a
		// laptop that moved between networks needs the second changed without
		// touching the first.
		//
		// Until now there was no subcommand at all and the README told owners
		// to `curl -X PUT`. A peer with no recorded address is skipped
		// silently — `ah send` still answers `queued` — so the one repair for
		// the quietest failure in the system was the one thing the CLI could
		// not do.
		switch {
		case len(args) == 1:
			return r.simple(ctx, http.MethodGet, "/v1/nodes", nil)
		case args[1] == "address":
			if len(args) != 4 {
				return errors.New("usage: ah nodes address <node-id> <host:port>")
			}
			// host:port is validated by the node, which refuses an address
			// this build would not deliver to and says why. Checking it here
			// as well would mean two rules that can disagree, and the node's
			// is the one that decides whether anything is ever sent.
			return r.simple(ctx, http.MethodPut,
				"/v1/nodes/"+url.PathEscape(args[2])+"/address",
				map[string]string{"address": args[3]})
		default:
			return fmt.Errorf("ah nodes does not take %q\nusage: ah nodes [address <node-id> <host:port>]", args[1])
		}
	case "peers":
		if len(args) != 1 {
			return fmt.Errorf("ah peers takes no arguments, got %s\nusage: ah peers",
				strings.Join(args[1:], " "))
		}
		return r.peers(ctx)
	case "pair":
		return r.pair(ctx, args)
	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: ah revoke <node-id>")
		}
		return r.simple(ctx, http.MethodDelete, "/v1/nodes/"+url.PathEscape(args[1]), nil)
	case "send":
		// A message to another node must say which local session it is from,
		// because the node refuses to send on behalf of a session whose owner
		// has not opened outbound — and it cannot tell the owner's CLI from an
		// agent that was talked into calling it. The gate is per session, so
		// the message has to be attributed to one.
		//
		// --from is recognised anywhere, before or after the message, as
		// `--from <id>` or `--from=<id>` — unless a `--` sits before the
		// message, in which case everything after it is text. The message is
		// unquoted words, so one that mentions "--from" needs that `--`, right
		// after the destination (or before it). A `--` inside the message is
		// prose and stays; a --from after such a `--` is refused as ambiguous
		// rather than taken out of a sentence with the sender silently
		// changed. The first remaining word is the destination, the rest the
		// message.
		fromSession := ""
		fromSeen := false
		dashInProse := false
		rest := make([]string, 0, len(args)-1)
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--" && len(rest) <= 1:
				rest = append(rest, args[i+1:]...)
				i = len(args)
			case args[i] == "--":
				dashInProse = true
				rest = append(rest, args[i])
			case args[i] == "--from" || strings.HasPrefix(args[i], "--from="):
				if dashInProse {
					return errors.New("--from after a -- in the message is ambiguous: to send the words, " +
						"put -- right after the destination; to set the sender, put --from before the message")
				}
				if fromSeen {
					return errors.New("--from given twice; a message leaves from one session")
				}
				fromSeen = true
				if args[i] == "--from" {
					if i+1 >= len(args) {
						return errors.New("--from needs a value: the local session the message is from")
					}
					i++
					fromSession = args[i]
				} else {
					fromSession = strings.TrimPrefix(args[i], "--from=")
				}
				if fromSession == "" || strings.HasPrefix(fromSession, "-") {
					return errors.New("--from needs a value: the local session the message is from")
				}
			default:
				rest = append(rest, args[i])
			}
		}
		if len(rest) < 2 {
			return errors.New("usage: ah send [--from <local-session-id>] <session-id> [--] <message>\n" +
				"  --from is required when <session-id> names another node; put -- before a message that mentions --from")
		}
		body := map[string]string{"to": rest[0], "body": strings.Join(rest[1:], " ")}
		if fromSession != "" {
			body["from"] = fromSession
		}
		return r.simple(ctx, http.MethodPost, "/v1/messages", body)
	case "inbox":
		// `ah inbox <session-id>` reads; `ah inbox delete <session-id>
		// <message-id>` drops one message. The second is `inbox-clear`'s
		// single-message mode under the name a reader looks for: the skill that
		// deletes a message every tick told people to run `curl -X DELETE`,
		// because nobody finds that mode under `inbox-clear`.
		//
		// `delete` cannot collide with a session id: a session id always begins
		// with a provider name and a colon, which is what makes the two
		// readings separable at all.
		if len(args) >= 2 && args[1] == "delete" {
			if len(args) != 4 {
				return errors.New("usage: ah inbox delete <session-id> <message-id>")
			}
			if !looksLikeSessionID(args[2]) {
				return errors.New("usage: ah inbox delete <session-id> <message-id>\n" +
					"  a session id begins with a provider name, as in `claude:` or `codex:`")
			}
			return r.simple(ctx, http.MethodDelete,
				"/v1/inbox/"+url.PathEscape(args[2])+"/"+url.PathEscape(args[3]), nil)
		}
		if len(args) != 2 {
			return errors.New("usage: ah inbox <session-id>\n" +
				"       ah inbox delete <session-id> <message-id>")
		}
		return r.inbox(ctx, args[1])
	case "wakes":
		// What moved an agent while nobody was looking. The refusals are in
		// here too: an owner seeing nothing needs to know whether the node was
		// quiet or a limit was doing its job.
		if len(args) > 2 {
			return errors.New("usage: ah wakes [session-id]")
		}
		path := "/v1/wakes"
		if len(args) == 2 {
			path += "?session=" + url.QueryEscape(args[1])
		}
		return r.wakes(ctx, path)
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
		//
		// Without an id it lists, newest first. An owner who has lost the id —
		// the terminal that printed it is closed, or the send came from an
		// agent rather than from them — otherwise could not ask at all.
		//
		// --session narrows the listing to one local sender. It only means
		// anything while listing: with a message id the answer is that one
		// message, and silently ignoring the flag would let a caller believe
		// a filter was applied.
		rest, session, err := takeSessionFlag(args[1:])
		if err != nil {
			return err
		}
		if len(rest) > 1 || (len(rest) == 1 && session != "") {
			return errors.New("usage: ah outbound [message-id] | ah outbound [--session <session-id>]")
		}
		if len(rest) == 0 {
			path := "/v1/outbound"
			if session != "" {
				path += "?session=" + url.QueryEscape(session)
			}
			return r.simple(ctx, http.MethodGet, path, nil)
		}
		return r.simple(ctx, http.MethodGet, "/v1/outbound/"+url.PathEscape(rest[0]), nil)
	case "node":
		return r.simple(ctx, http.MethodGet, "/v1/node", nil)
	case "heartbeat":
		return r.simple(ctx, http.MethodGet, "/v1/heartbeat", nil)
	case "settings":
		return r.settings(ctx, args)
	case "service":
		return r.service(ctx, args)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// looksLikeSessionID reports whether a string reads as a session id rather than
// as a subcommand or a message id.
//
// It is the whole reason `ah inbox delete` can exist: session ids are the only
// argument `ah inbox` ever took, and they all carry a provider prefix, so a
// bare word in that position is a mistake rather than an inbox nobody can read.
// Deliberately not a validity check — the node decides that, and answers with
// its own reason.
func looksLikeSessionID(arg string) bool {
	provider, _, found := strings.Cut(arg, ":")
	return found && model.KnownProvider(provider)
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
		return errors.New("usage: ah audience <session-id> [none|all-paired|selected <node-id>...] [--cwd] [--messages] [--outbound] [--auto-wake]")
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
	allowOutbound := false
	autoWake := false
	for _, argument := range args[3:] {
		switch argument {
		case "--cwd":
			exportCWD = true
		case "--messages":
			acceptMessages = true
		case "--outbound":
			allowOutbound = true
		case "--auto-wake":
			autoWake = true
		default:
			if strings.HasPrefix(argument, "-") {
				return fmt.Errorf("unknown flag %q; want --cwd, --messages, --outbound or --auto-wake", argument)
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
		"allowOutbound":  allowOutbound,
		"autoWake":       autoWake,
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

// peers answers "who can I send to, and what did they publish".
//
// This exists because the answer was previously unreachable from the CLI. A
// remote session does not appear in `ah list`, which is owner-local by design;
// it appears in the presence endpoint, addressed as <node-id>/<session-id>, and
// there was no command that showed it. The only way to find the id to send to
// was to read the endpoint with curl — and `ah send` to an unqualified id
// answers "session not found", which is true and unhelpful. Found by using the
// thing: it cost time in the two-host run recorded in docs/verification.md.
func (r runner) peers(ctx context.Context) error {
	body, err := r.request(ctx, http.MethodGet, "/v1/peers", nil)
	if err != nil {
		return err
	}
	if r.json {
		return writePrettyJSON(r.stdout, body)
	}
	var decoded struct {
		Peers []struct {
			NodeID      string    `json:"nodeId"`
			DisplayName string    `json:"displayName"`
			Online      bool      `json:"online"`
			ReceivedAt  time.Time `json:"receivedAt"`
			Sessions    []struct {
				ID       string `json:"id"`
				Provider string `json:"provider"`
				Status   string `json:"status"`
			} `json:"sessions"`
			SessionsWithheld bool `json:"sessionsWithheld"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decode peers: %w", err)
	}
	if len(decoded.Peers) == 0 {
		_, _ = fmt.Fprintln(r.stdout, "No paired nodes. Pair one with `ah pair`, or see who is "+
			"advertising with `ah candidates`.")
		return nil
	}

	w := tabwriter.NewWriter(r.stdout, 0, 4, 2, ' ', 0)
	// NODE is the id, not the name. Trust is keyed on the id, `ah revoke` and
	// `ah audience selected` take it, and two peers may carry the same display
	// name — the candidate list has a flag for exactly that collision. The name
	// is quoted in STATE as what it is: a label they chose.
	_, _ = fmt.Fprintln(w, "SEND TO\tPROVIDER\tSTATUS\tNODE\tSTATE")
	for _, peer := range decoded.Peers {
		// Quoted, so a control character in a name cannot forge a row. The name
		// comes from this owner's own trust store, which does not require it to
		// be printable, and this is the first place the CLI prints one as raw
		// text rather than through the JSON encoder.
		name := fmt.Sprintf("%q", peer.DisplayName)
		switch {
		case peer.ReceivedAt.IsZero():
			// Never heard from is not the same as gone quiet, and an owner
			// waiting for a machine to appear needs to know which.
			_, _ = fmt.Fprintf(w, "-\t-\t-\t%s\t%s, never heard from\n", peer.NodeID, name)
		case !peer.Online:
			// The node stops serving what an expired snapshot held, so the
			// empty list here is this node withholding stale state — not the
			// peer having published nothing. Saying the latter would be a claim
			// about the peer that nothing supports, and it is the state every
			// sleeping machine is in.
			_, _ = fmt.Fprintf(w, "-\t-\t-\t%s\t%s, offline since %s; what it last published is no longer shown\n",
				peer.NodeID, name, peer.ReceivedAt.Format(time.RFC3339))
		case peer.SessionsWithheld:
			// The node refused what this peer published, which looks identical
			// on the wire to a peer publishing nothing.
			_, _ = fmt.Fprintf(w, "-\t-\t-\t%s\t%s, online, published something this node refused\n",
				peer.NodeID, name)
		case len(peer.Sessions) == 0:
			// Online, so the empty list is the peer's own doing and this is the
			// one case where saying so is true.
			_, _ = fmt.Fprintf(w, "-\t-\t-\t%s\t%s, online, nothing published to this node\n",
				peer.NodeID, name)
		default:
			for _, session := range peer.Sessions {
				// The full address, which is what `ah send` needs. Printing the
				// bare session id is what sent the previous reader to curl.
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s, online\n",
					session.ID, session.Provider, session.Status, peer.NodeID, name)
			}
		}
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

// inbox reads a session's inbox in pages and prints the whole.
//
// One request for everything is a size a peer controls: bodies are bounded, but
// fifty of them serialised can pass the response cap, after which nothing
// decodes and the owner cannot see what is jamming their inbox — the situation
// this command exists for. Pages of ten stay well inside it.
func (r runner) inbox(ctx context.Context, sessionID string) error {
	type page struct {
		Messages []json.RawMessage `json:"messages"`
		Held     int               `json:"held"`
		Capacity int               `json:"capacity"`
		Full     bool              `json:"full"`
		Next     string            `json:"next,omitempty"`
	}
	whole := page{Messages: []json.RawMessage{}}
	after := ""
	// A ceiling on the pages, not only on what ends them. The node is trusted,
	// but a bug or a wrong URL that answers with a cursor that never advances
	// would otherwise spin here until the context died, with the terminal
	// filling. The inbox bound is 500 and a page is 10.
	const maxPages = 60
	stopped := false
	pagesRead := 0
	for pages := 0; ; pages++ {
		if pages == maxPages {
			stopped = true
			break
		}
		path := "/v1/inbox/" + url.PathEscape(sessionID) + "?limit=10&after=" + url.QueryEscape(after)
		body, err := r.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		var current page
		if err := json.Unmarshal(body, &current); err != nil {
			return fmt.Errorf("decode inbox page: %w", err)
		}
		whole.Held, whole.Capacity, whole.Full = current.Held, current.Capacity, current.Full
		if current.Next == after && after != "" {
			// The same cursor again. Its messages are the ones already held, so
			// appending them would print a duplicate on the way out.
			stopped = true
			break
		}
		whole.Messages = append(whole.Messages, current.Messages...)
		pagesRead++
		if current.Next == "" || len(current.Messages) == 0 {
			break
		}
		after = current.Next
	}
	// Encoded straight out rather than marshalled and decoded again: an inbox
	// at its bound is tens of megabytes, and the round trip through `any` would
	// hold three copies of it at once.
	//
	// Printed before the error below, so a truncated answer is still an answer:
	// the messages that were read are worth having even when the reason the
	// reading stopped is a misbehaving node.
	if stopped {
		whole.Next = after
	}
	encoder := json.NewEncoder(r.stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(whole); err != nil {
		return err
	}
	if stopped {
		// Not silent. A truncated answer that looks complete is the failure
		// this command was just fixed for, in a different costume.
		bound := ""
		if whole.Capacity > 0 {
			// Only when the node said what its bound is. A broken node may not
			// have, and "holds at most 0 messages" explains nothing.
			bound = fmt.Sprintf(" An inbox holds at most %d messages.", whole.Capacity)
		}
		return fmt.Errorf("stopped after %d page(s): the node is still issuing cursors, or issued "+
			"the same one twice.%s This is the node misbehaving. What was read is above "+
			"and `next` says where it stopped", pagesRead, bound)
	}
	return nil
}

// responseCap bounds what this CLI reads from the node in one answer.
const responseCap = 4 * 1024 * 1024

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
	data, err := io.ReadAll(io.LimitReader(response.Body, responseCap))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(data) >= responseCap {
		// Cut off, so it will not decode — and "unexpected end of JSON input"
		// sends the reader nowhere. Say what happened.
		return nil, fmt.Errorf("the node's answer reached the %d byte limit and was cut off; "+
			"something in it is too large to read here. For an inbox, `ah inbox-clear <session> [message-id]` removes what is there",
			responseCap)
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
	_, _ = fmt.Fprintln(output, "       ah --version")
	_, _ = fmt.Fprintln(output, "commands: discover, list, status, publish, unpublish, audience,")
	_, _ = fmt.Fprintln(output, "          nodes, peers, pair, revoke, send, inbox, inbox-clear, outbound,")
	_, _ = fmt.Fprintln(output, "          wakes, node, heartbeat, pairing, candidates, settings, service")
	_, _ = fmt.Fprintln(output, "  ah pairing [on [seconds] | off]              advertise on the local network, for a while")
	_, _ = fmt.Fprintln(output, "  ah candidates                                machines advertising right now")
	_, _ = fmt.Fprintln(output, "  ah peers                                     what paired nodes have published to this one,")
	_, _ = fmt.Fprintln(output, "                                               with the address to send to")
	_, _ = fmt.Fprintln(output, "  ah nodes                                     the nodes this one trusts")
	_, _ = fmt.Fprintln(output, "  ah nodes address <node-id> <host:port>       record where a paired node answers; without one,")
	_, _ = fmt.Fprintln(output, "                                               delivery skips it and `ah send` still says queued")
	_, _ = fmt.Fprintln(output, "  ah audience <session-id> [none|all-paired|selected <node-id>...] [--cwd] [--messages] [--outbound] [--auto-wake]")
	_, _ = fmt.Fprintln(output, "  ah pair <node-id> <display-name> <platform> <public-key> <fingerprint>")
	_, _ = fmt.Fprintln(output, "  ah send [--from <local-session-id>] <session-id> [--] <message>")
	_, _ = fmt.Fprintln(output, "                                               --from is required when <session-id> names another node;")
	_, _ = fmt.Fprintln(output, "                                               put -- before a message that mentions --from")
	_, _ = fmt.Fprintln(output, "  ah outbound [message-id]                     what became of a queued message; without one, the last 50")
	_, _ = fmt.Fprintln(output, "  ah outbound [--session <session-id>]         the listing, narrowed to what one local session sent")
	_, _ = fmt.Fprintln(output, "  ah inbox <session-id>                        read a session's inbox")
	_, _ = fmt.Fprintln(output, "  ah inbox delete <session-id> <message-id>    drop one message, once it has been handled")
	_, _ = fmt.Fprintln(output, "  ah inbox-clear <session-id> [message-id]     empty an inbox, or drop one message")
	_, _ = fmt.Fprintln(output, "  ah wakes [session-id]                        what started a turn with nobody watching")
	_, _ = fmt.Fprintln(output, "  ah settings                                  what this node started with, and where each value came from")
	_, _ = fmt.Fprintln(output, "  ah settings set [--peer-listen ADDR] [--allow-lan=true|false] [--discover=true|false]")
	_, _ = fmt.Fprintln(output, "                  [--auto-wake=true|false] [--treat-as-private CIDR]... [--clear-private-ranges]")
	_, _ = fmt.Fprintln(output, "                                               remember these; they apply when the node next starts")
	_, _ = fmt.Fprintln(output, "  ah service install [--db PATH] [--listen ADDR] [--peer-listen ADDR] [--allow-lan] [--discover]")
	_, _ = fmt.Fprintln(output, "                     [--treat-as-private CIDR]... [--auto-wake] [--node-binary PATH]")
	_, _ = fmt.Fprintln(output, "                                               run the node as a background service that starts at login")
	_, _ = fmt.Fprintln(output, "                                               and is restarted if it exits; node flags are the node's own")
	_, _ = fmt.Fprintln(output, "  ah service uninstall                         stop it and remove the registration; identity and data stay")
	_, _ = fmt.Fprintln(output, "  ah service restart                           restart it, which is how a saved setting takes effect")
	_, _ = fmt.Fprintln(output, "  ah service status                            is it installed, running, and is the node answering")
}

// wakes renders the trail of what has started a turn with nobody watching.
func (r runner) wakes(ctx context.Context, path string) error {
	body, err := r.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if r.json {
		return writePrettyJSON(r.stdout, body)
	}
	var decoded struct {
		Wakes []struct {
			MessageID          string    `json:"messageId"`
			SourceNodeID       string    `json:"sourceNodeId"`
			SourceSession      string    `json:"sourceSession"`
			DestinationSession string    `json:"destinationSession"`
			Hops               int       `json:"hops"`
			Outcome            string    `json:"outcome"`
			Detail             string    `json:"detail"`
			At                 time.Time `json:"at"`
		} `json:"wakes"`
		Limits struct {
			Hops          int    `json:"hops"`
			Pair          int    `json:"pair"`
			PairWindow    string `json:"pairWindow"`
			Session       int    `json:"session"`
			SessionWindow string `json:"sessionWindow"`
			Node          int    `json:"node"`
			NodeWindow    string `json:"nodeWindow"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decode wakes: %w", err)
	}
	if len(decoded.Wakes) == 0 {
		// Two different quiets, and the difference matters: a node where
		// nothing has been turned on cannot wake, and saying "nothing woke
		// anything" would read as reassurance about a setting that is off.
		_, _ = fmt.Fprintln(r.stdout, "No agent has been woken on this node. Waking is per session "+
			"and closed by default; `ah audience <session-id> ... --auto-wake` opens it.")
		return nil
	}

	w := tabwriter.NewWriter(r.stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "WHEN\tWOKE\tFROM\tHOPS\tOUTCOME")
	for _, event := range decoded.Wakes {
		// Every one of these came off the wire from another machine. Quoted so
		// a control character in a session label cannot forge a row, the same
		// treatment `ah peers` gives a display name.
		source := fmt.Sprintf("%q", event.SourceSession)
		if event.SourceSession == "" {
			source = "this machine"
		}
		outcome := event.Outcome
		if event.Detail != "" {
			outcome += " (" + event.Detail + ")"
		}
		_, _ = fmt.Fprintf(w, "%s\t%q\t%s\t%d\t%s\n",
			event.At.Format(time.RFC3339), event.DestinationSession, source, event.Hops, outcome)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// The rules, beside the trail. A refusal is only legible next to the limit
	// that produced it, and an owner reading "refused_pair_rate" with no idea
	// what the pair limit is cannot tell whether it is tuned wrongly.
	_, _ = fmt.Fprintf(r.stdout, "\nLimits: %d hops; %d per pair / %s; %d per session / %s; %d per node / %s.\n",
		decoded.Limits.Hops, decoded.Limits.Pair, decoded.Limits.PairWindow,
		decoded.Limits.Session, decoded.Limits.SessionWindow,
		decoded.Limits.Node, decoded.Limits.NodeWindow)
	_, _ = fmt.Fprintln(r.stdout, "A message held back by a limit is still in the inbox; `ah inbox <session-id>` reads it.")
	return nil
}

// takeSessionFlag pulls an optional `--session <id>` out of a command's
// arguments and returns what is left.
//
// Written as a flag rather than a positional, because `ah outbound` already
// has a positional and the two mean opposite things: one message, or many
// narrowed to a sender. A caller that passes both is told so rather than
// having one of them quietly win.
func takeSessionFlag(args []string) (rest []string, session string, err error) {
	for index := 0; index < len(args); index++ {
		if args[index] != "--session" {
			rest = append(rest, args[index])
			continue
		}
		if index+1 >= len(args) || args[index+1] == "" {
			return nil, "", errors.New("--session needs a session id")
		}
		if session != "" {
			return nil, "", errors.New("--session was given twice")
		}
		session = args[index+1]
		index++
	}
	return rest, session, nil
}
