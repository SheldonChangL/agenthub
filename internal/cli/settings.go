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
//	ah settings set --peer-listen ADDR --allow-lan=true|false ...
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
	"       ah settings set [--peer-listen ADDR] [--allow-lan=true|false] [--discover=true|false]\n" +
	"                       [--auto-wake=true|false] [--treat-as-private CIDR]... [--clear-private-ranges]"

// settingsView is the answer both endpoints give.
type settingsView struct {
	Settings        nodeconfig.Settings `json:"settings"`
	Sources         map[string]string   `json:"sources"`
	Saved           nodeconfig.Settings `json:"saved"`
	RestartRequired bool                `json:"restartRequired"`
	Message         string              `json:"message"`
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
	peerListen := flags.String("peer-listen", "", "TLS listen address for peer traffic")
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
	if given["peer-listen"] {
		body[nodeconfig.SettingPeerListen] = *peerListen
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
		return err
	}
	return r.renderSettings(answer)
}

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
	saved := settingValues(view.Saved)
	writer := tabwriter.NewWriter(r.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SETTING\tIN EFFECT\tFROM\tNEXT START")
	for _, field := range nodeconfig.SettingNames {
		next := saved[field]
		if next == running[field] {
			next = ""
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", nodeconfig.FlagName(field), running[field], view.Sources[field], next)
	}
	if err := writer.Flush(); err != nil {
		return err
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
	return map[string]string{
		nodeconfig.SettingPeerListen:     settings.PeerListen,
		nodeconfig.SettingAllowLAN:       fmt.Sprintf("%t", settings.AllowLAN),
		nodeconfig.SettingDiscover:       fmt.Sprintf("%t", settings.Discover),
		nodeconfig.SettingTreatAsPrivate: ranges,
		nodeconfig.SettingAutoWake:       fmt.Sprintf("%t", settings.AutoWake),
	}
}
