package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/nodeconfig"
	"agenthub.local/agenthub/internal/service"
)

// newServiceManager builds the manager for this machine. A variable so tests
// can substitute one that drives a fake instead of launchd or systemd.
var newServiceManager = func() (service.Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return service.Manager{}, fmt.Errorf("find home directory: %w", err)
	}
	current, err := user.Current()
	if err != nil {
		return service.Manager{}, fmt.Errorf("find current user: %w", err)
	}
	return service.Manager{GOOS: runtime.GOOS, Home: home, UID: current.Uid, Runner: service.ExecRunner{}}, nil
}

// service installs, removes or inspects the node as a background service.
//
//	ah service install [--db PATH] [--listen ADDR] [--node-binary PATH]
//	ah service restart
//	ah service uninstall
//	ah service status
//
// Install still takes the node's own network flags by the node's own names,
// and still writes them into the unit, because installations out there were
// made that way. It is no longer the recommended route: the node remembers
// those settings in its database, so `ah settings set ...` records them once
// and the unit needs only --db. The peer listener is validated here with the
// node's rule before anything is written: a bad address would otherwise become
// a service that crashes on every restart.
func (r runner) service(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: ah service install [--db PATH] [--listen ADDR] [--node-binary PATH] | restart | uninstall | status\n" +
			"the node's network settings are remembered in its database: `ah settings set ...` records them, " +
			"`ah service restart` applies them")
	}
	manager, err := newServiceManager()
	if err != nil {
		return err
	}
	switch args[1] {
	case "install":
		return r.serviceInstall(ctx, manager, args[2:])
	case "uninstall":
		report, err := manager.Uninstall(ctx)
		if err != nil {
			return err
		}
		return r.printReport("uninstalled", report)
	case "restart":
		if len(args) > 2 {
			return fmt.Errorf("ah service restart takes no arguments, got %s", strings.Join(args[2:], " "))
		}
		report, err := manager.Restart(ctx)
		if err != nil {
			return err
		}
		return r.printReport("restarted", report)
	case "status":
		return r.serviceStatus(ctx, manager)
	default:
		return fmt.Errorf("unknown service command %q; want install, restart, uninstall or status", args[1])
	}
}

func (r runner) serviceInstall(ctx context.Context, manager service.Manager, args []string) error {
	flags := flag.NewFlagSet("ah service install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	nodeBinary := flags.String("node-binary", "", "path to agenthub-node (default: beside ah, then PATH)")
	// The node's flags, by the node's names. Only those the owner set are
	// carried, so the node's own defaults apply to the rest.
	dbPath := flags.String("db", "", "SQLite database path (relative paths are made absolute)")
	listen := flags.String("listen", "", "local HTTP listen address")
	peerListen := flags.String("peer-listen", "", "TLS listen address for peer traffic")
	allowLAN := flags.Bool("allow-lan", false, "permit a non-loopback peer listener")
	discover := flags.Bool("discover", false, "learn paired peers' addresses from the local network")
	autoWake := flags.Bool("auto-wake", false, "let an arriving message start a turn")
	claudeRoot := flags.String("claude-root", "", "Claude data root")
	codexRoot := flags.String("codex-root", "", "Codex data root")
	var declaredPrivate nodeconfig.StringList
	flags.Var(&declaredPrivate, "treat-as-private", "CIDR block to treat as a private network, repeatable")
	// --display-name is deliberately not here. The node persists a chosen
	// name, and a flag on every start would pin the installed name over any
	// later rename; set it once with `agenthub-node --display-name`.
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("ah service install: %w", err)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("ah service install: unexpected argument %q", flags.Arg(0))
	}

	// Validated with the node's own rules, so a unit is not written for a
	// combination that fails at startup — a service that restarts on failure
	// would otherwise crash forever.
	//
	// This checks the flags on this command line against the defaults, not
	// against what the node has remembered: this command does not open the
	// database. A value saved earlier can still refuse the start, and the
	// node's own check at startup is the one that sees everything.
	listenAddress := *listen
	if listenAddress == "" {
		listenAddress = "127.0.0.1:7462"
	}
	if err := nodeconfig.ValidateLoopback(listenAddress); err != nil {
		return err
	}
	ranges, err := nodeconfig.ParsePrivateRanges(declaredPrivate)
	if err != nil {
		return err
	}
	peerAddress := *peerListen
	if peerAddress == "" {
		peerAddress = "127.0.0.1:7463"
	}
	if err := nodeconfig.ValidatePeerListen(peerAddress, *allowLAN, ranges); err != nil {
		return err
	}

	nodeArgs := make([]string, 0, 16)
	if *dbPath != "" {
		absolute, err := filepath.Abs(*dbPath)
		if err != nil {
			return fmt.Errorf("resolve --db: %w", err)
		}
		nodeArgs = append(nodeArgs, "--db", absolute)
	}
	// In a fixed order, so the unit is the same file for the same flags and a
	// diff of two installs shows only what changed.
	for _, pair := range []struct {
		name  string
		value *string
	}{{"listen", listen}, {"peer-listen", peerListen}, {"claude-root", claudeRoot}, {"codex-root", codexRoot}} {
		if *pair.value != "" {
			nodeArgs = append(nodeArgs, "--"+pair.name, *pair.value)
		}
	}
	// Written in the --flag=value form whenever the flag was given, false
	// included. A bare --allow-lan can only turn something on, so an owner who
	// installs with --allow-lan=false used to get a unit carrying nothing at
	// all: the setting they thought they had closed stayed remembered and
	// applied on every start. Go's flag package reads --allow-lan=false back,
	// which a second argument ("--allow-lan false") would not be.
	for _, pair := range []struct {
		name  string
		value *bool
	}{{"allow-lan", allowLAN}, {"discover", discover}} {
		if wasGiven(flags, pair.name) {
			nodeArgs = append(nodeArgs, fmt.Sprintf("--%s=%t", pair.name, *pair.value))
		}
	}
	for _, cidr := range declaredPrivate {
		nodeArgs = append(nodeArgs, "--treat-as-private", cidr)
	}
	if wasGiven(flags, "auto-wake") {
		nodeArgs = append(nodeArgs, fmt.Sprintf("--auto-wake=%t", *autoWake))
	}

	binary, err := resolveNodeBinary(*nodeBinary)
	if err != nil {
		return err
	}
	report, err := manager.Install(ctx, service.Config{NodeBinary: binary, Args: nodeArgs})
	if err != nil {
		return err
	}
	// Said here rather than refused, because units installed by earlier builds
	// carry these flags and still work. A flag in the unit wins over what the
	// node remembered — it is given on every start — so an owner who changes a
	// setting from the desktop and sees nothing happen needs to know why.
	if baked := bakedInSettings(nodeArgs); len(baked) > 0 {
		report.Notes = append(report.Notes,
			"this unit passes "+strings.Join(baked, ", ")+" on every start, which overrides anything saved later. "+
				"`ah settings set ...` remembers them instead, and then the service needs only --db")
	}
	// Installed is not the same as answering. Ask the node, briefly, so the
	// owner leaves knowing which of the two they have.
	if answer := r.waitForNode(ctx, 10*time.Second); answer != "" {
		report.Steps = append(report.Steps, answer)
	} else {
		report.Notes = append(report.Notes, "the node has not answered on "+r.baseURL+" yet; ah service status, and the log, say why")
	}
	return r.printReport("installed", report)
}

// bakedInSettings names the remembered settings this install wrote into the
// unit file, which is the second source of truth these settings exist to end.
//
// Read off the arguments that were written, not off the flags that were given.
// Those two are not the same list — a flag can be given and still reach no
// unit — and the note is a promise about the file on disk: telling an owner
// that a unit passes --allow-lan when it passes nothing points them at the
// wrong explanation for a setting that will not change.
func bakedInSettings(nodeArgs []string) []string {
	remembered := map[string]bool{
		"peer-listen": true, "allow-lan": true, "discover": true,
		"treat-as-private": true, "auto-wake": true,
	}
	seen := make(map[string]bool, len(remembered))
	given := make([]string, 0, len(remembered))
	for _, argument := range nodeArgs {
		name, ok := strings.CutPrefix(argument, "--")
		if !ok {
			continue
		}
		name, _, _ = strings.Cut(name, "=")
		if remembered[name] && !seen[name] {
			seen[name] = true
			given = append(given, "--"+name)
		}
	}
	return given
}

// wasGiven reports whether a flag was passed, as opposed to left at its
// default. For a boolean the two are indistinguishable by value, and
// --allow-lan=false is an owner closing a switch.
func wasGiven(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// resolveNodeBinary finds agenthub-node: the path given, else beside this
// executable, else on PATH. Always returned absolute, because the service
// runs without the owner's PATH or working directory.
func resolveNodeBinary(given string) (string, error) {
	if given != "" {
		absolute, err := filepath.Abs(given)
		if err != nil {
			return "", fmt.Errorf("resolve --node-binary: %w", err)
		}
		return absolute, nil
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "agenthub-node")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err == nil {
			return filepath.Abs(candidate)
		}
	}
	if found, err := exec.LookPath("agenthub-node"); err == nil {
		return filepath.Abs(found)
	}
	return "", errors.New("agenthub-node not found beside ah or on PATH; pass --node-binary /absolute/path/to/agenthub-node")
}

// waitForNode polls the owner's API until it answers or the budget is spent.
func (r runner) waitForNode(ctx context.Context, budget time.Duration) string {
	deadline := time.Now().Add(budget)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/v1/node", nil)
		if err == nil {
			response, err := r.client.Do(request)
			if err == nil {
				var identity struct {
					ID          string `json:"id"`
					DisplayName string `json:"displayName"`
				}
				_ = json.NewDecoder(response.Body).Decode(&identity)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK && identity.ID != "" {
					return fmt.Sprintf("node answering on %s as %s (%q)", r.baseURL, identity.ID, identity.DisplayName)
				}
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (r runner) serviceStatus(ctx context.Context, manager service.Manager) error {
	status, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	answer := r.waitForNode(ctx, 0)
	if r.json {
		return r.printJSONValue(map[string]any{
			"service":       status,
			"nodeAnswering": answer != "",
			"node":          answer,
		})
	}
	if !status.Supported {
		fmt.Fprintln(r.stdout, "background service: not supported on this operating system")
	} else {
		state := "not installed"
		switch {
		case status.Installed && status.Running:
			state = fmt.Sprintf("installed and running (pid %d)", status.PID)
		case status.Installed:
			state = "installed but not running"
		case status.Running:
			state = fmt.Sprintf("running (pid %d) but no unit file at %s — registered some other way", status.PID, status.UnitPath)
		}
		fmt.Fprintln(r.stdout, "background service:", state)
		fmt.Fprintln(r.stdout, "unit:", status.UnitPath)
		if status.LogHint != "" {
			fmt.Fprintln(r.stdout, "log:", status.LogHint)
		}
	}
	if answer != "" {
		fmt.Fprintln(r.stdout, answer)
	} else {
		fmt.Fprintln(r.stdout, "node: not answering on", r.baseURL)
	}
	return nil
}

func (r runner) printReport(verb string, report service.Report) error {
	if r.json {
		return r.printJSONValue(map[string]any{"result": verb, "report": report})
	}
	for _, step := range report.Steps {
		fmt.Fprintln(r.stdout, step)
	}
	for _, note := range report.Notes {
		fmt.Fprintln(r.stdout, "note:", note)
	}
	return nil
}

// printJSONValue prints one value the way every other --json path does.
func (r runner) printJSONValue(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writePrettyJSON(r.stdout, data)
}
