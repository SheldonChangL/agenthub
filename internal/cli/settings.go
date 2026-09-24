package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"

	"agenthub.local/agenthub/internal/nodeconfig"
)

// settings reads and writes the node's remembered start-up configuration.
//
//	ah settings
//	ah settings set --peer-listen ADDR [--peer-listen ADDR]... --allow-lan=true|false ...
//
// Reading is the default because the question asked most often is "is this
// machine serving the network, and did I ask for that or did it remember".
func (r runner) settings(ctx context.Context, args []string) error {
	switch {
	case len(args) == 1:
		return r.showSettings(ctx)
	case args[1] == "set":
		return r.writeSettings(ctx, args[2:])
	default:
		return fmt.Errorf("ah settings does not take %q\n%s", args[1], settingsUsage)
	}
}

const settingsUsage = "usage: ah settings\n" +
	"       ah settings set [--peer-listen ADDR]... [--allow-lan=true|false] [--discover=true|false]\n" +
	"                       [--auto-wake=true|false] [--treat-as-private CIDR]... [--clear-private-ranges]"

// settingsView is the answer both endpoints give.
type settingsView struct {
	Settings        nodeconfig.Settings `json:"settings"`
	Sources         map[string]string   `json:"sources"`
	Saved           nodeconfig.Settings `json:"saved"`
	RestartRequired bool                `json:"restartRequired"`
	// PeerListenWithdrawn says the node started with allowLan off beside a peer
	// listener it could not serve, and moved that listener back to the default.
	// The FROM column then reads "default", which is true of the value and
	// false about the database, where the default is now stored.
	PeerListenWithdrawn bool `json:"peerListenWithdrawn"`
	// PeerListenProblem says this process could not bind the peer listener it
	// was configured to serve and is running on loopback instead. The FROM
	// column then reads "default" for the same reason, and for a different
	// event: nothing was written, and the address in NEXT START is still the
	// owner's.
	PeerListenProblem *peerListenProblem `json:"peerListenProblem"`
	// PeerListeners is every configured peer address and whether it is bound.
	// Absent from a node that knows only one address.
	PeerListeners []peerListenerState `json:"peerListeners"`
	Message       string              `json:"message"`
}

// peerListenerState mirrors one entry of the API's peerListeners.
type peerListenerState struct {
	Address string `json:"address"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail"`
	Message string `json:"message"`
}

// peerListenProblem mirrors the API's field of the same name.
type peerListenProblem struct {
	Address   string `json:"address"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
	RunningOn string `json:"runningOn"`
	Message   string `json:"message"`
}

func (r runner) showSettings(ctx context.Context) error {
	body, err := r.request(ctx, http.MethodGet, "/v1/node/settings", nil)
	if err != nil {
		return err
	}
	return r.renderSettings(body)
}

func (r runner) writeSettings(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("ah settings set", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// The node's flags, by the node's names. Booleans are written with `=` so
	// that turning one off is something a person can type: `--allow-lan false`
	// is two arguments to Go's flag package and the second one is not read.
	var peerListens nodeconfig.StringList
	flags.Var(&peerListens, "peer-listen",
		"TLS listen address for peer traffic, repeatable; given at all it replaces the whole stored list")
	allowLAN := flags.Bool("allow-lan", false, "serve paired peers on a private network address (use --allow-lan=false to stop)")
	discover := flags.Bool("discover", false, "learn paired peers' addresses from the local network")
	autoWake := flags.Bool("auto-wake", false, "let an arriving message start a turn")
	clearRanges := flags.Bool("clear-private-ranges", false, "forget every declared private range")
	var declaredPrivate nodeconfig.StringList
	flags.Var(&declaredPrivate, "treat-as-private",
		"CIDR block to treat as private, repeatable; given at all it replaces the whole stored set")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("ah settings set: %w\n%s", err, settingsUsage)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("ah settings set: unexpected argument %q\n%s", flags.Arg(0), settingsUsage)
	}

	given := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if len(given) == 0 {
		return errors.New("ah settings set changes nothing without a flag\n" + settingsUsage)
	}
	if given["treat-as-private"] && given["clear-private-ranges"] {
		return errors.New("--treat-as-private and --clear-private-ranges say opposite things about the same set; " +
			"pass one of them")
	}

	// Only what was actually typed is sent. A body carrying every field would
	// pin the four settings the owner did not mention to whatever this command
	// line defaulted them to, which is how a `--discover` turned -allow-lan off.
	body := map[string]any{}
	// One address is sent as peerListen, which every node understands and
	// which replaces the whole list on a node that has one. Several are sent
	// as peerListens, which an older node refuses as an unknown field — and
	// that refusal is translated below rather than shown as a JSON error.
	if given["peer-listen"] {
		if len(peerListens) == 1 {
			body[nodeconfig.SettingPeerListen] = peerListens[0]
		} else {
			body[nodeconfig.FieldPeerListens] = []string(peerListens)
		}
	}
	if given["allow-lan"] {
		body[nodeconfig.SettingAllowLAN] = *allowLAN
	}
	if given["discover"] {
		body[nodeconfig.SettingDiscover] = *discover
	}
	if given["auto-wake"] {
		body[nodeconfig.SettingAutoWake] = *autoWake
	}
	if given["treat-as-private"] {
		body[nodeconfig.SettingTreatAsPrivate] = []string(declaredPrivate)
	}
	if given["clear-private-ranges"] && *clearRanges {
		body[nodeconfig.SettingTreatAsPrivate] = []string{}
	}
	if len(body) == 0 {
		return errors.New("ah settings set changes nothing without a flag\n" + settingsUsage)
	}

	answer, err := r.request(ctx, http.MethodPut, "/v1/node/settings", body)
	if err != nil {
		if _, several := body[nodeconfig.FieldPeerListens]; several && strings.Contains(err.Error(), olderNodeRefusal) {
			return fmt.Errorf("this node is too old for more than one address: it does not know peerListens. "+
				"Update agenthub-node, or pass one --peer-listen (%w)", err)
		}
		return err
	}
	return r.renderSettings(answer)
}

// olderNodeRefusal is how a node that predates peerListens answers a body
// carrying it: the owner's API refuses unknown fields
// (internal/api/server.go, decodeJSON).
const olderNodeRefusal = "not valid JSON for this endpoint"

// renderSettings prints what is in effect, where it came from, and what a
// restart would change.
func (r runner) renderSettings(body []byte) error {
	if r.json {
		return writePrettyJSON(r.stdout, body)
	}
	var view settingsView
	if err := json.Unmarshal(body, &view); err != nil {
		return fmt.Errorf("decode response JSON: %w", err)
	}
	running := settingValues(view.Settings)
	if bound := boundPeerAddresses(view.PeerListeners); len(bound) > 0 {
		// What is being served, which on a node with several addresses is not
		// always all of them; the ones that are not are listed below.
		running[nodeconfig.SettingPeerListen] = strings.Join(bound, ", ")
	} else if view.PeerListenProblem != nil || len(view.Settings.PeerListens) == 0 {
		// Degraded, or a node that knows one address: the scalar is the
		// address served.
		running[nodeconfig.SettingPeerListen] = view.Settings.PeerListen
	}
	saved := settingValues(view.Saved)
	writer := tabwriter.NewWriter(r.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SETTING\tIN EFFECT\tFROM\tNEXT START")
	for _, field := range nodeconfig.SettingNames {
		next := saved[field]
		if next == running[field] {
			next = ""
		}
		from := view.Sources[field]
		if field == nodeconfig.SettingPeerListen && view.PeerListenWithdrawn {
			// Marked rather than given a fourth source name: "withdrawn" is not
			// a place a value comes from, and the three names are what the API
			// promises its other readers.
			from += " (withdrawn)"
		}
		if field == nodeconfig.SettingPeerListen && view.PeerListenProblem != nil {
			// Marked for the same reason, and distinctly: a reader who sees
			// "not bound" beside a default learns that the address in NEXT
			// START is not waiting for a restart, it is waiting for a machine.
			from += " (not bound)"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", nodeconfig.FlagName(field), running[field], from, next)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	for _, listener := range view.PeerListeners {
		if listener.State == "bound" {
			continue
		}
		if view.PeerListenProblem != nil && listener.Address == view.PeerListenProblem.Address {
			continue // said in full just below
		}
		sentence := listener.Message
		if sentence == "" {
			sentence = "not bound yet"
		}
		fmt.Fprintf(r.stdout, "peer listener %s is %s: %s\n", listener.Address, listener.State, sentence)
		if listener.Detail != "" {
			fmt.Fprintf(r.stdout, "  %s\n", listener.Detail)
		}
	}
	if view.PeerListenProblem != nil {
		// Before the restart line, because it changes what that line means: a
		// restart into the same unbindable address is the loop this whole path
		// exists to break.
		fmt.Fprintln(r.stdout, view.PeerListenProblem.Message)
		if view.PeerListenProblem.Detail != "" {
			fmt.Fprintf(r.stdout, "  %s\n", view.PeerListenProblem.Detail)
		}
		fmt.Fprintln(r.stdout,
			"choose an address this machine holds with `ah settings set --peer-listen ADDR`, "+
				"or keep this one and restart once the network is back")
	}
	if view.Message != "" {
		fmt.Fprintln(r.stdout, view.Message)
	}
	if view.RestartRequired {
		// Said whenever the two differ, not only after a write: a node left
		// running with a saved change is a machine whose settings page and
		// behaviour disagree, and nothing else on screen would say so.
		fmt.Fprintln(r.stdout,
			"the column above marks what is saved but not yet running; `ah service restart` applies it")
	}
	return nil
}

// settingValues renders one configuration field by field, for display.
func settingValues(settings nodeconfig.Settings) map[string]string {
	ranges := strings.Join(settings.TreatAsPrivate, ", ")
	if ranges == "" {
		ranges = "none"
	}
	peerListen := settings.PeerListen
	if len(settings.PeerListens) > 0 {
		peerListen = strings.Join(settings.PeerListens, ", ")
	}
	return map[string]string{
		nodeconfig.SettingPeerListen:     peerListen,
		nodeconfig.SettingAllowLAN:       fmt.Sprintf("%t", settings.AllowLAN),
		nodeconfig.SettingDiscover:       fmt.Sprintf("%t", settings.Discover),
		nodeconfig.SettingTreatAsPrivate: ranges,
		nodeconfig.SettingAutoWake:       fmt.Sprintf("%t", settings.AutoWake),
	}
}

// boundPeerAddresses is the configured addresses a node reports as bound.
func boundPeerAddresses(listeners []peerListenerState) []string {
	var bound []string
	for _, listener := range listeners {
		if listener.State == "bound" {
			bound = append(bound, listener.Address)
		}
	}
	return bound
}
