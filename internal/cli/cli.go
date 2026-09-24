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
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"agenthub.local/agenthub/internal/buildinfo"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/pairing"
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
			return r.pairingWindow(ctx, http.MethodGet, nil)
		case args[1] == "off":
			return r.pairingWindow(ctx, http.MethodDelete, nil)
		case args[1] == "candidates":
			// An alias, so the window and the list it feeds live under one
			// verb. `ah candidates` stays: it is what exists in scripts and in
			// this node's own output.
			if len(args) != 2 {
				return fmt.Errorf("ah pairing candidates takes no arguments, got %s\n"+
					"usage: ah pairing candidates", strings.Join(args[2:], " "))
			}
			return r.simple(ctx, http.MethodGet, "/v1/pairing/candidates", nil)
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
			return r.pairingWindow(ctx, http.MethodPost, body)
		default:
			return fmt.Errorf("ah pairing does not take %q\n"+
				"usage: ah pairing [on [seconds] | off | candidates]", args[1])
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
		// `ah inbox counts` is the batch read: one line per local session that
		// is still holding something. Separable from an id for the same reason
		// `delete` is, and it answers the question `ah inbox <session>` cannot
		// be asked a thousand times to answer.
		if len(args) >= 2 && args[1] == "counts" {
			if len(args) != 2 {
				return errors.New("usage: ah inbox counts")
			}
			return r.inboxCounts(ctx)
		}
		if len(args) != 2 {
			return errors.New("usage: ah inbox <session-id>\n" +
				"       ah inbox counts\n" +
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
			// Blank is caught here rather than sent: the server refuses a
			// present-but-empty `session` by telling the caller to omit the
			// parameter, and a CLI user has no parameter to omit.
			session := strings.TrimSpace(args[1])
			if session == "" {
				return errors.New("usage: ah wakes [session-id]")
			}
			path += "?session=" + url.QueryEscape(session)
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

// pair is both routes to a trusted peer: the exchange, and the manual form.
//
//	ah pair request <host:port>   ask that machine to pair
//	ah pair pending [--all]       what is waiting, with the fingerprints to compare
//	ah pair approve <request-id>  yes, on the machine that was asked
//	ah pair confirm <request-id>  yes, on the machine that asked
//	ah pair reject <request-id>   no, on either
//	ah pair <node-id> <display-name> <platform> <public-key> <fingerprint...>
//
// The subcommands cannot collide with the manual form: its first argument is a
// node id, which is at least sixteen characters and in practice begins `node_`.
//
// The manual form stays. It is the route that needs nothing but a shell and a
// way to read five values aloud, and it is what still works when the two
// machines cannot open a connection to each other at all.
func (r runner) pair(ctx context.Context, args []string) error {
	if len(args) > 1 {
		switch args[1] {
		case "request":
			return r.pairRequest(ctx, args)
		case "pending":
			switch {
			case len(args) == 2:
				return r.pairPending(ctx, false)
			case len(args) == 3 && args[2] == "--all":
				return r.pairPending(ctx, true)
			}
			return errors.New("usage: ah pair pending [--all]")
		case "approve", "confirm", "reject":
			if len(args) != 3 {
				return fmt.Errorf("usage: ah pair %s <request-id>", args[1])
			}
			return r.pairDecide(ctx, args[1], args[2])
		}
	}
	if len(args) < 6 {
		return errors.New("usage: ah pair request <host:port>\n" +
			"       ah pair pending [--all] | approve <request-id> | confirm <request-id> | reject <request-id>\n" +
			"       ah pair <node-id> <display-name> <platform> <public-key> <fingerprint>")
	}
	return r.simple(ctx, http.MethodPost, "/v1/nodes", map[string]string{
		"nodeId":               args[1],
		"displayName":          args[2],
		"platform":             args[3],
		"publicKey":            args[4],
		"confirmedFingerprint": strings.Join(args[5:], " "),
	})
}

// pairingWindow opens, closes or reads the window, and says in words what the
// answer means.
//
// The JSON this used to print was the whole answer to a question asked while
// standing between two machines: an owner who ran `ah pairing on` was shown a
// state object and left to work out, from `announceableAddresses`, whether the
// other machine could find this one — and was never told the address to type
// there, which is the one thing that always works.
func (r runner) pairingWindow(ctx context.Context, method string, input any) error {
	body, err := r.request(ctx, method, "/v1/pairing", input)
	if err != nil {
		return err
	}
	if r.json {
		return writePrettyJSON(r.stdout, body)
	}
	var state pairingStateRow
	if err := json.Unmarshal(body, &state); err != nil {
		return fmt.Errorf("decode pairing state: %w", err)
	}
	r.printPairingState(state)
	return nil
}

// pairingStateRow is the window as the node reports it.
type pairingStateRow struct {
	Open             bool      `json:"open"`
	ExpiresAt        time.Time `json:"expiresAt"`
	RemainingSeconds int       `json:"remainingSeconds"`
	DisplayName      string    `json:"displayName"`
	// PeerAddress is where the other machine would send its request. Absent on
	// a node with no peer listener, and on a node too old to report it.
	PeerAddress string `json:"peerAddress"`
	// PeerAddressProblem is the node's own words for why that address is not
	// one the other machine could be told to type. Empty on a node that has a
	// usable address, and on a node too old to report the field — which is why
	// the address is checked here as well: printing a loopback address as the
	// one to type on the other machine is the failure this guards.
	PeerAddressProblem string `json:"peerAddressProblem"`
	// PeerAddressReachable is the node's own verdict on that address. A
	// pointer because its absence is the fact this side needs: a node that
	// reports it has answered the question, and a node that does not is one
	// from before the field existed, whose silence must not be read as "there
	// is no address".
	PeerAddressReachable *bool  `json:"peerAddressReachable"`
	Notice               string `json:"notice"`
	Announcing           struct {
		Addresses   int       `json:"announceableAddresses"`
		LastSuccess time.Time `json:"lastAnnouncedAt"`
		LastError   string    `json:"lastError"`
	} `json:"announcing"`
}

// announcing reports whether anything is actually going out over mDNS, and why
// not when nothing is.
func (state pairingStateRow) announcing() (bool, string) {
	switch {
	case state.Announcing.LastError != "":
		return false, state.Announcing.LastError
	case state.Announcing.Addresses == 0:
		return false, "this node has no address it can announce"
	default:
		return true, ""
	}
}

func (r runner) printPairingState(state pairingStateRow) {
	if !state.Open {
		_, _ = fmt.Fprintln(r.stdout, "Pairing window closed. This machine is not advertising and "+
			"will refuse pairing requests. Open one with `ah pairing on`.")
		return
	}
	_, _ = fmt.Fprintf(r.stdout, "Pairing window open until %s (%s left)",
		state.ExpiresAt.Local().Format("15:04:05"), remainingWords(state.RemainingSeconds))
	if state.DisplayName != "" {
		_, _ = fmt.Fprintf(r.stdout, ", as %q", state.DisplayName)
	}
	_, _ = fmt.Fprintln(r.stdout, ".")
	if announcing, why := state.announcing(); announcing {
		_, _ = fmt.Fprintln(r.stdout, "  announcing over mDNS  yes")
	} else {
		_, _ = fmt.Fprintf(r.stdout, "  announcing over mDNS  no (%s)\n", why)
	}
	// Printed whether or not this node announces: mDNS does not cross every
	// network the two machines might be on, and a reachable address always
	// works. What is never printed as the address to type is one that names
	// this machine: on the default node the peer listener is on loopback, and
	// "run ah pair request 127.0.0.1:7463 over there" is an instruction that
	// cannot work and reads as though it should.
	_, _ = fmt.Fprintf(r.stdout, "  next                  %s\n", r.nextStep(state))
	if state.Notice != "" {
		_, _ = fmt.Fprintf(r.stdout, "\n%s\n", state.Notice)
	}
}

// nextStep is the one thing to do next, in one sentence.
//
// One, deliberately. This block used to print the remedy on this line and then
// a notice underneath saying the other machine could still type this node's
// peer address — on the default node, where there is no address to type. A
// person holding two terminals cannot act on two instructions that contradict
// each other, and picks the wrong one.
//
// The node's own fields decide, in their own order of authority: a stated
// problem, then a stated verdict, and only then this side's reading of the
// address. The last case exists for a node from before those fields, and it is
// a reading of what that node actually sent — never a guess at what it did not.
func (r runner) nextStep(state pairingStateRow) string {
	if state.PeerAddressProblem != "" {
		return state.PeerAddressProblem
	}
	if state.PeerAddressReachable != nil {
		if *state.PeerAddressReachable && state.PeerAddress != "" {
			return "On the other machine, run: ah pair request " + state.PeerAddress
		}
		// Reachable with no address to show is not a thing a node says; if one
		// does, the honest answer is the one for an address nobody knows.
		return pairing.PeerAddressUnknown
	}
	if state.PeerAddress == "" {
		// A node too old to report any of this. Announcing nothing is not what
		// that means, and "this node only listens on this machine" — which is
		// what reading an empty address as loopback said — is a claim about a
		// configuration this side has not been told.
		return "this node predates address reporting, so it did not say where the other machine " +
			"should send its request; read its peer listener with `ah settings` here and type " +
			"that address on the other machine"
	}
	if problem := pairing.PeerAddressProblem(state.PeerAddress); problem != "" {
		return problem
	}
	return "On the other machine, run: ah pair request " + state.PeerAddress
}

// remainingWords is a countdown a person reads, from the seconds the node
// counted. Its own clock is not consulted: the node's number is the one the
// expiry beside it was computed from.
func remainingWords(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
}

// pairRequest asks the machine at an address to pair.
//
//	ah pair request <host:port>
//
// The address is the only thing that has to be carried between the two
// machines, and it is not a secret: nothing here is trusted because of it. What
// decides the pairing is the fingerprint this prints, compared against the one
// the other machine prints.
//
// There is no local name for the peer. A node is called what it calls itself,
// on both screens — a local rename put a different name on each machine while
// the exchange was asking the owner to check that the two screens agree.
func (r runner) pairRequest(ctx context.Context, args []string) error {
	address := ""
	for i := 2; i < len(args); i++ {
		switch {
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("unknown flag %q\nusage: ah pair request <host:port>", args[i])
		case address == "":
			address = args[i]
		default:
			return fmt.Errorf("ah pair request takes one address, got %q as well\n"+
				"usage: ah pair request <host:port>", args[i])
		}
	}
	if address == "" {
		return errors.New("usage: ah pair request <host:port>")
	}
	body, err := r.request(ctx, http.MethodPost, "/v1/pair/requests",
		map[string]string{"address": address})
	if err != nil {
		return err
	}
	if r.json {
		return writePrettyJSON(r.stdout, body)
	}
	request, err := decodePairRequest(body)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stdout, "Asked %s to pair.\n\n", address)
	r.printPairRequest(request)
	r.printPairNotice(request)
	return nil
}

// pairDecide is approve, confirm or reject, and says what just happened rather
// than printing the node's JSON at somebody standing between two laptops.
func (r runner) pairDecide(ctx context.Context, verb, requestID string) error {
	body, err := r.request(ctx, http.MethodPost,
		"/v1/pair/requests/"+url.PathEscape(requestID)+"/"+verb, map[string]string{})
	if err != nil {
		return err
	}
	if r.json {
		return writePrettyJSON(r.stdout, body)
	}
	request, err := decodePairRequest(body)
	if err != nil {
		return err
	}
	name := request.otherName()
	switch verb {
	case "approve":
		_, _ = fmt.Fprintf(r.stdout, "Approved. %s (%s) is now trusted on this machine.\n",
			name, request.NodeID)
		_, _ = fmt.Fprintf(r.stdout, "  its fingerprint  %s\n\n", request.Fingerprint)
	case "confirm":
		_, _ = fmt.Fprintf(r.stdout, "Confirmed. %s (%s) is now trusted on this machine.\n",
			name, request.NodeID)
		_, _ = fmt.Fprintf(r.stdout, "  its fingerprint  %s\n\n", request.Fingerprint)
	}
	// The node's own sentence and nothing else on a refusal: saying "not
	// trusted here" and then "not trusted on either machine" is the same fact
	// twice, and the second one is the one that is true of both machines.
	_, _ = fmt.Fprintln(r.stdout, request.NextStep)
	return nil
}

// pairPending shows what each side is waiting for, with the fingerprints to
// compare.
//
// Only what still needs somebody to do something, unless asked for everything:
// a list where the row waiting for a decision sat under four decided ones is
// how an owner misses their own pairing.
func (r runner) pairPending(ctx context.Context, all bool) error {
	path := "/v1/pair/requests"
	if all {
		path += "?all=true"
	}
	body, err := r.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if r.json {
		return writePrettyJSON(r.stdout, body)
	}
	var decoded struct {
		Requests []pairRequestRow `json:"requests"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decode pairing requests: %w", err)
	}
	if len(decoded.Requests) == 0 {
		if all {
			_, _ = fmt.Fprintln(r.stdout, "No pairing requests.")
			return nil
		}
		_, _ = fmt.Fprintln(r.stdout, "Nothing is waiting for a decision. "+
			"`ah pair pending --all` also shows requests finished in the last ten minutes.")
		return nil
	}
	for i, request := range decoded.Requests {
		if i > 0 {
			_, _ = fmt.Fprintln(r.stdout)
		}
		r.printPairRequest(request)
	}
	// Once, under the rows. It is five sentences about how to compare two
	// fingerprints, and repeating it per row buried the rows themselves.
	r.printPairNotice(decoded.Requests...)
	return nil
}

// printPairNotice prints the long explanation once, from the first row that
// carries one. Decided rows carry none: there is nothing left to compare.
func (r runner) printPairNotice(rows ...pairRequestRow) {
	for _, row := range rows {
		if row.Notice != "" {
			_, _ = fmt.Fprintf(r.stdout, "\n%s\n", row.Notice)
			return
		}
	}
}

// pairRequestRow is one exchange as the node reports it.
type pairRequestRow struct {
	ID          string `json:"id"`
	Direction   string `json:"direction"`
	NodeID      string `json:"nodeId"`
	DisplayName string `json:"displayName"`
	Platform    string `json:"platform"`
	// Fingerprint and LocalFingerprint are the other machine's and this one's.
	// Fingerprints is the same two values in the order both machines print
	// them, which is what a person actually reads; these two stay for a node
	// that does not send it.
	Fingerprint      string               `json:"fingerprint"`
	LocalFingerprint string               `json:"localFingerprint"`
	Fingerprints     []pairFingerprintRow `json:"fingerprints"`
	Address          string               `json:"address"`
	State            string               `json:"state"`
	Reason           string               `json:"reason"`
	ExpiresAt        string               `json:"expiresAt"`
	NextStep         string               `json:"nextStep"`
	Notice           string               `json:"notice"`
}

// pairFingerprintRow is one machine's fingerprint, as the node labelled it.
type pairFingerprintRow struct {
	Role        string `json:"role"`
	Machine     string `json:"machine"`
	Whose       string `json:"whose"`
	Fingerprint string `json:"fingerprint"`
}

func (row pairRequestRow) otherName() string {
	if strings.TrimSpace(row.DisplayName) != "" {
		return row.DisplayName
	}
	return row.NodeID
}

func decodePairRequest(body []byte) (pairRequestRow, error) {
	var request pairRequestRow
	if err := json.Unmarshal(body, &request); err != nil {
		return pairRequestRow{}, fmt.Errorf("decode pairing request: %w", err)
	}
	return request, nil
}

// printPairRequest puts the two fingerprints where a person will read them.
//
// Both, in the order the node gave them — which is the same order on both
// machines, the one that asked first — and labelled with the name each machine
// calls itself and which of the two is this one. One fingerprint on each screen
// is the version people get wrong: they see two different values, assume that
// is how it works, and confirm.
func (r runner) printPairRequest(request pairRequestRow) {
	_, _ = fmt.Fprintf(r.stdout, "%s  %s  (%s)\n", request.ID, request.State, request.Direction)
	_, _ = fmt.Fprintf(r.stdout, "  other machine   %s", request.otherName())
	if request.Platform != "" {
		_, _ = fmt.Fprintf(r.stdout, " on %s", request.Platform)
	}
	if request.Address != "" {
		_, _ = fmt.Fprintf(r.stdout, " at %s", request.Address)
	}
	_, _ = fmt.Fprintf(r.stdout, "\n                  %s\n", request.NodeID)
	rows := request.Fingerprints
	if len(rows) == 0 {
		// A node that does not send the ordered pair. Printed requester-first
		// all the same when the direction says which is which.
		mine := pairFingerprintRow{Role: "receiver", Whose: "this machine", Fingerprint: request.LocalFingerprint}
		theirs := pairFingerprintRow{Role: "requester", Whose: "the other machine", Fingerprint: request.Fingerprint}
		if request.Direction == "outgoing" {
			mine.Role, theirs.Role = "requester", "receiver"
			rows = []pairFingerprintRow{mine, theirs}
		} else {
			rows = []pairFingerprintRow{theirs, mine}
		}
	}
	// One line above the two values saying what to do with them. The long
	// notice printed under the block was an explanation arriving after the
	// thing it explains, which is where people stop reading.
	if request.Notice != "" {
		_, _ = fmt.Fprintln(r.stdout,
			"  compare these two lines with the other machine's screen, group by group:")
	}
	for _, row := range rows {
		label := row.Machine
		if label == "" {
			label = row.Whose
		} else {
			label += " (" + row.Whose + ")"
		}
		_, _ = fmt.Fprintf(r.stdout, "  %-9s %-34s %s\n", row.Role, label, row.Fingerprint)
	}
	if request.Reason != "" {
		_, _ = fmt.Fprintf(r.stdout, "  reason          %s\n", request.Reason)
	}
	if request.NextStep != "" {
		_, _ = fmt.Fprintf(r.stdout, "  next            %s\n", request.NextStep)
	}
}

// audience reads or replaces one session's export policy.
//
//	ah audience <session-id>
//	ah audience <session-id> none
//	ah audience <session-id> all-paired [--cwd] [--messages] [--outbound] [--auto-wake]
//	ah audience <session-id> selected <node-id>... [--cwd] [--messages] [--outbound] [--auto-wake]
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

// inboxCounts prints how much every local inbox is still holding.
//
// "Held", not "unread": nothing on the node marks a message read, and reading
// one here or in the desktop window does not either. A message leaves this
// count when an agent takes it or somebody deletes it.
//
// Sorted by depth, fullest first, because the answer this command is asked for
// is which session needs attention — and a full one is refusing mail right now,
// so it carries a marker rather than only a number.
func (r runner) inboxCounts(ctx context.Context) error {
	body, err := r.request(ctx, http.MethodGet, "/v1/inbox/counts", nil)
	if err != nil {
		return err
	}
	if r.json {
		return writePrettyJSON(r.stdout, body)
	}
	var decoded struct {
		Counts map[string]struct {
			Held     int  `json:"held"`
			Capacity int  `json:"capacity"`
			Full     bool `json:"full"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decode inbox counts: %w", err)
	}
	if len(decoded.Counts) == 0 {
		// Said as the fact it is. The node answers with only the sessions that
		// are holding something, so an empty map means every inbox is empty —
		// not that there are no sessions.
		_, _ = fmt.Fprintln(r.stdout, "Every local inbox is empty.")
		return nil
	}
	ids := make([]string, 0, len(decoded.Counts))
	for id := range decoded.Counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if decoded.Counts[ids[i]].Held != decoded.Counts[ids[j]].Held {
			return decoded.Counts[ids[i]].Held > decoded.Counts[ids[j]].Held
		}
		return ids[i] < ids[j]
	})
	w := tabwriter.NewWriter(r.stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "SESSION\tHELD\tSTATE")
	for _, id := range ids {
		count := decoded.Counts[id]
		state := ""
		if count.Full {
			state = "FULL — new messages are being refused"
		}
		_, _ = fmt.Fprintf(w, "%s\t%d/%d\t%s\n", id, count.Held, count.Capacity, state)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// The one sentence that keeps the column from being read as "unread".
	_, _ = fmt.Fprintln(r.stdout,
		"\nHeld is what the inbox still has, not what is unread: reading never marks anything.")
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
	_, _ = fmt.Fprintln(output, "  ah pairing [on [seconds] | off]              open the pairing window, for a while")
	_, _ = fmt.Fprintln(output, "  ah candidates | ah pairing candidates        machines advertising right now")
	_, _ = fmt.Fprintln(output, "  ah peers                                     what paired nodes have published to this one,")
	_, _ = fmt.Fprintln(output, "                                               with the address to send to")
	_, _ = fmt.Fprintln(output, "  ah nodes                                     the nodes this one trusts")
	_, _ = fmt.Fprintln(output, "  ah nodes address <node-id> <host:port>       record where a paired node answers; without one,")
	_, _ = fmt.Fprintln(output, "                                               delivery skips it and `ah send` still says queued")
	_, _ = fmt.Fprintln(output, "  ah audience <session-id> [none|all-paired|selected <node-id>...] [--cwd] [--messages] [--outbound] [--auto-wake]")
	_, _ = fmt.Fprintln(output, "  ah pair request <host:port>                  ask that machine to pair; no key is copied by hand")
	_, _ = fmt.Fprintln(output, "  ah pair pending [--all]                      requests waiting, with the two fingerprints to compare;")
	_, _ = fmt.Fprintln(output, "                                               --all also shows what finished in the last ten minutes")
	_, _ = fmt.Fprintln(output, "  ah pair approve <request-id>                 on the machine that was asked, once they match")
	_, _ = fmt.Fprintln(output, "  ah pair confirm <request-id>                 on the machine that asked, once they match")
	_, _ = fmt.Fprintln(output, "  ah pair reject <request-id>                  refuse one, on either machine")
	_, _ = fmt.Fprintln(output, "  ah pair <node-id> <display-name> <platform> <public-key> <fingerprint>")
	_, _ = fmt.Fprintln(output, "                                               the manual form, still here")
	_, _ = fmt.Fprintln(output, "  ah send [--from <local-session-id>] <session-id> [--] <message>")
	_, _ = fmt.Fprintln(output, "                                               --from is required when <session-id> names another node;")
	_, _ = fmt.Fprintln(output, "                                               put -- before a message that mentions --from")
	_, _ = fmt.Fprintln(output, "  ah outbound [message-id]                     what became of a queued message; without one, the last 50")
	_, _ = fmt.Fprintln(output, "  ah outbound [--session <session-id>]         the listing, narrowed to what one local session sent")
	_, _ = fmt.Fprintln(output, "  ah inbox <session-id>                        read a session's inbox")
	_, _ = fmt.Fprintln(output, "  ah inbox counts                              how much every local inbox is still holding, in one read")
	_, _ = fmt.Fprintln(output, "  ah inbox delete <session-id> <message-id>    drop one message, once it has been handled")
	_, _ = fmt.Fprintln(output, "  ah inbox-clear <session-id> [message-id]     empty an inbox, or drop one message")
	_, _ = fmt.Fprintln(output, "  ah wakes [session-id]                        what started a turn with nobody watching")
	_, _ = fmt.Fprintln(output, "  ah settings                                  what this node started with, and where each value came from")
	_, _ = fmt.Fprintln(output, "  ah settings set [--peer-listen ADDR]... [--allow-lan=true|false] [--discover=true|false]")
	_, _ = fmt.Fprintln(output, "                  [--auto-wake=true|false] [--treat-as-private CIDR]... [--clear-private-ranges]")
	_, _ = fmt.Fprintln(output, "                                               remember these; they apply when the node next starts")
	_, _ = fmt.Fprintln(output, "  ah service install [--db PATH] [--listen ADDR] [--peer-listen ADDR]... [--allow-lan] [--discover]")
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
//
// Both spellings are accepted, as `--from` accepts both. `--session=<id>` is
// the form a person who writes flags reaches for, and reading it as a
// positional made `ah outbound --session=codex:mine` ask for a message whose
// id is the flag — answered 404 UNKNOWN_MESSAGE, which says nothing about the
// real mistake.
func takeSessionFlag(args []string) (rest []string, session string, err error) {
	seen := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		joined := strings.HasPrefix(argument, "--session=")
		if argument != "--session" && !joined {
			rest = append(rest, argument)
			continue
		}
		if seen {
			return nil, "", errors.New("--session was given twice")
		}
		seen = true
		switch {
		case joined:
			session = strings.TrimPrefix(argument, "--session=")
		case index+1 < len(args):
			session = args[index+1]
			index++
		default:
			return nil, "", errors.New("--session needs a session id")
		}
		// Trimmed here rather than left to the node: a value of spaces is
		// otherwise trimmed away at the far end and answered with the whole
		// node's listing, which reads as one session's and is not.
		session = strings.TrimSpace(session)
		if session == "" {
			return nil, "", errors.New("--session needs a session id")
		}
	}
	return rest, session, nil
}
