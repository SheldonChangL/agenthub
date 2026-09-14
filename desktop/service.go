package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The desktop app does not install the service itself; it runs `ah service`,
// so the CLI and the button are one implementation with two faces and cannot
// drift apart. What this file owns is finding ah, translating the form into
// the node's flags, and carrying ah's words back to the screen.

// ServiceForm is what the owner fills in before installing.
type ServiceForm struct {
	// DBPath is the node's database. Empty means the node's own default.
	DBPath string `json:"dbPath"`
	// PeerListen is the address other machines connect to. Empty means the
	// node's default, loopback — reachable by nothing but this machine.
	PeerListen string `json:"peerListen"`
	// AllowLAN is the one switch that lets anything leave this machine.
	AllowLAN bool `json:"allowLan"`
	Discover bool `json:"discover"`
	// TreatAsPrivate declares ranges the node should trust despite their
	// addresses, one CIDR per entry; a direct cable often needs it.
	TreatAsPrivate []string `json:"treatAsPrivate"`
	AutoWake       bool     `json:"autoWake"`
}

// ServiceStatus is `ah --json service status`, plus where ah was found — or
// why it was not, which is the one failure this file can explain itself.
type ServiceStatus struct {
	Tool          string `json:"tool"`
	ToolError     string `json:"toolError,omitempty"`
	Supported     bool   `json:"supported"`
	Installed     bool   `json:"installed"`
	Running       bool   `json:"running"`
	PID           int    `json:"pid"`
	UnitPath      string `json:"unitPath"`
	LogHint       string `json:"logHint"`
	NodeAnswering bool   `json:"nodeAnswering"`
	Node          string `json:"node"`
}

// ServiceResult is what an install or uninstall said.
type ServiceResult struct {
	Command string `json:"command"`
	Output  string `json:"output"`
}

// runTool executes ah. A variable so tests can stand in for the binary.
var runTool = func(ctx context.Context, tool string, args ...string) (string, error) {
	var output bytes.Buffer
	// #nosec G204 -- running a variable program is the point of this file, and
	// which program is the question locateBinary answers: a path the owner named
	// in the environment, or one at a fixed offset from this executable. PATH is
	// deliberately not among them, so "whatever came first on PATH" — the case
	// this rule is about — cannot be what lands here.
	command := exec.CommandContext(ctx, tool, args...)
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.String(), err
}

// toolPath remembers where ah was found. The places do not move while the
// app runs, and every panel refresh would otherwise stat them all again.
var toolPath string

// findTool locates ah: named by the owner, beside this executable, inside the
// bundle, then in the source tree this app was built from. Reported in that
// order when none of them has it. A variable so tests can stand in for the
// lookup.
//
// Shelling out at all is forced by the module split: desktop/ is its own Go
// module and cannot import internal/service. Running the same ah the owner
// runs keeps the two faces of the feature identical.
var findTool = func() (string, error) {
	if toolPath != "" {
		return toolPath, nil
	}
	path, err := locateTool()
	if err == nil {
		toolPath = path
	}
	return path, err
}

func locateTool() (string, error) {
	return locateBinary("ah", "AGENTHUB_AH")
}

// findNode locates the agenthub-node the installed service should run, the same
// way and in the same places as ah. A variable so tests can stand in for it.
//
// The install has to name it. Left to itself `ah service install` falls back to
// exec.LookPath("agenthub-node") and writes whatever that finds into the
// ExecStart of a launchd job or a systemd unit — started at boot, every boot.
// Taking PATH out of this file only moved that decision one hop down; the GUI
// already knows which agenthub-node it means, because the packaging step put it
// beside this executable, so it says so.
var findNode = func() (string, error) {
	return locateBinary("agenthub-node", "AGENTHUB_NODE")
}

// locateBinary is that search, with the binary's name and the environment
// variable that overrides it as parameters: ah, agenthub-node and agenthub-mcp
// ship side by side and are found the same way, and a second copy of this list
// would be a second thing to keep in step with how the app is packaged.
//
// PATH is deliberately not one of the places. What this finds is run to install
// a launchd job or a systemd unit; anything named ah that happened to come
// first on the owner's PATH would be run to do that, and the window would
// report its output as the app's own. Every place left is either named by the
// owner or sits at a fixed offset from this executable — which is what the
// packaging step fills in (desktop/build/bundle-binaries.sh).
//
// The answer is always absolute. A relative path works for exec, which resolves
// it against this process's working directory, but the caller may be writing it
// into a config file that another program will read from somewhere else.
func locateBinary(name, envName string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find %s: locating this executable: %w", name, err)
	}
	return locateBinaryNear(filepath.Dir(self), name, envName)
}

// executableName is what the file is actually called on this operating system.
// The packaging step writes ah.exe on Windows (desktop/build/bundle-binaries.sh),
// so a search for "ah" there would look straight past what was bundled.
func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// runnable reports whether a path is something this app could actually execute.
// os.Stat succeeding says only that the name exists: a directory called ah, or
// a text file, would otherwise be handed back as the tool and fail later with a
// message about exec rather than about the path being wrong.
func runnable(path string) bool {
	// #nosec G703 -- the taint gosec follows is AGENTHUB_AH / AGENTHUB_NODE /
	// AGENTHUB_MCP, set by the person this app runs as to name a binary they
	// want run instead of the bundled one; every other path here is built from
	// this executable's own location. Nothing the window or the network
	// supplies reaches it, and "traversal" into one's own account is not a
	// boundary crossed.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	// Windows decides by extension, not by a mode bit; there is none to check.
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// locateBinaryNear is that search with the executable's directory given rather
// than asked for, so a test can lay out a bundle or a checkout in a temporary
// directory and watch which of them the search will and will not accept.
func locateBinaryNear(dir, name, envName string) (string, error) {
	file := executableName(name)
	looked := make([]string, 0, 4)
	if given := os.Getenv(envName); given != "" {
		if runnable(given) {
			return absolute(given)
		}
		looked = append(looked, "$"+envName+"="+given)
	}
	// Where the packaging step puts them: beside this executable, which is
	// Contents/MacOS in a bundle and the unpacked directory on Linux, and
	// Contents/Resources for a bundle laid out the other way.
	for _, candidate := range []string{
		filepath.Join(dir, file),
		filepath.Join(dir, "..", "Resources", file),
	} {
		if runnable(candidate) {
			return absolute(filepath.Clean(candidate))
		}
		looked = append(looked, candidate)
	}
	// The source tree, for a developer running the app out of desktop/build/bin.
	if root, ok := checkoutAbove(dir); ok {
		candidate := filepath.Clean(filepath.Join(root, "bin", file))
		if runnable(candidate) {
			return absolute(candidate)
		}
		looked = append(looked, candidate)
	}
	return "", fmt.Errorf("%s was not found (looked: %s); set %s to its path",
		name, strings.Join(looked, ", "), envName)
}

// modulePath is this project's Go module. A directory whose go.mod declares it
// is the checkout this app was built from, rather than a stranger's directory
// that happens to sit at the same offset from where the app was unpacked.
const modulePath = "agenthub.local/agenthub"

// buildOutput is where `wails build` leaves the app, relative to the checkout.
var buildOutput = []string{"desktop", "build", "bin"}

// checkoutAbove reports the checkout a developer is running the app out of, if
// that is what dir is: <repo>/desktop/build/bin on Linux and Windows, and
// <repo>/desktop/build/bin/<app>.app/Contents/MacOS on macOS. Nothing else.
//
// Counting levels up and asking only whether a go.mod of ours is there is not
// enough, and was a hole. Three levels up from <dir>/Anything.app/Contents/MacOS
// is <dir> itself, so an app dropped in a world-writable directory — /Users/Shared
// is drwxrwxrwt — let any other local account plant a go.mod and a bin/ah there
// and have this GUI run it to install a launchd job. The path has to have the
// shape the build actually produces, not merely the right depth.
func checkoutAbove(dir string) (string, bool) {
	segments := strings.Split(filepath.ToSlash(filepath.Clean(dir)), "/")
	// A macOS bundle sits three further segments down: <x>.app/Contents/MacOS.
	if n := len(segments); n >= 3 &&
		segments[n-1] == "MacOS" && segments[n-2] == "Contents" &&
		strings.HasSuffix(segments[n-3], ".app") {
		segments = segments[:n-3]
	}
	n := len(segments)
	if n < len(buildOutput) {
		return "", false
	}
	for i, want := range buildOutput {
		if segments[n-len(buildOutput)+i] != want {
			return "", false
		}
	}
	above := strings.Join(segments[:n-len(buildOutput)], "/")
	// "" here is either a relative desktop/build/bin with nothing above it or
	// the filesystem root; neither is a checkout.
	if above == "" {
		return "", false
	}
	root := filepath.Clean(filepath.FromSlash(above))
	if !isProjectCheckout(root) {
		return "", false
	}
	return root, true
}

func isProjectCheckout(root string) bool {
	content, err := os.ReadFile(filepath.Clean(filepath.Join(root, "go.mod")))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "module "+modulePath {
			return true
		}
	}
	return false
}

func absolute(path string) (string, error) {
	full, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s to an absolute path: %w", path, err)
	}
	return full, nil
}

// ServiceStatus asks ah whether the node is installed as a service and
// whether it answers, against the node URL this app is pointed at.
func (a *App) ServiceStatus() ServiceStatus {
	tool, err := findTool()
	if err != nil {
		return ServiceStatus{ToolError: err.Error()}
	}
	_, nodeURL := a.current()
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	output, err := runTool(ctx, tool, "--url", nodeURL, "--json", "service", "status")
	if err != nil {
		return ServiceStatus{Tool: tool, ToolError: fmt.Sprintf("ah service status: %v: %s", err, strings.TrimSpace(output))}
	}
	var decoded struct {
		Service struct {
			Supported bool
			Installed bool
			Running   bool
			PID       int
			UnitPath  string
			LogHint   string
		} `json:"service"`
		NodeAnswering bool   `json:"nodeAnswering"`
		Node          string `json:"node"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		return ServiceStatus{Tool: tool, ToolError: fmt.Sprintf("decode ah service status: %v: %s", err, strings.TrimSpace(output))}
	}
	return ServiceStatus{
		Tool: tool, Supported: decoded.Service.Supported, Installed: decoded.Service.Installed,
		Running: decoded.Service.Running, PID: decoded.Service.PID, UnitPath: decoded.Service.UnitPath,
		LogHint: decoded.Service.LogHint, NodeAnswering: decoded.NodeAnswering, Node: decoded.Node,
	}
}

// InstallService turns the form into `ah service install` and runs it.
func (a *App) InstallService(form ServiceForm) (ServiceResult, error) {
	args, err := installArgs(form)
	if err != nil {
		return ServiceResult{}, err
	}
	return a.runService(args...)
}

// RestartService restarts the installed service so new node settings take
// effect. The node reads them at start-up and has no hot reload, so a write
// that is not followed by this changes nothing about the running process.
func (a *App) RestartService() (ServiceResult, error) {
	return a.runService("service", "restart")
}

// UninstallService stops the service and removes its registration; ah says
// what it left alone.
func (a *App) UninstallService() (ServiceResult, error) {
	return a.runService("service", "uninstall")
}

func (a *App) runService(args ...string) (ServiceResult, error) {
	tool, err := findTool()
	if err != nil {
		return ServiceResult{}, err
	}
	_, nodeURL := a.current()
	full := append([]string{"--url", nodeURL}, args...)
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	output, err := runTool(ctx, tool, full...)
	result := ServiceResult{Command: tool + " " + strings.Join(full, " "), Output: strings.TrimSpace(output)}
	if err != nil {
		return result, fmt.Errorf("%s: %w: %s", result.Command, err, result.Output)
	}
	return result, nil
}

// installArgs translates the form into the node's flags. Only what the owner
// set is passed, so the node's defaults apply to the rest; the peer listener
// itself is validated by ah with the node's rule, not guessed at here.
func installArgs(form ServiceForm) ([]string, error) {
	node, err := findNode()
	if err != nil {
		return nil, err
	}
	args := []string{"service", "install", "--node-binary", node}
	if path := strings.TrimSpace(form.DBPath); path != "" {
		args = append(args, "--db", path)
	}
	if address := strings.TrimSpace(form.PeerListen); address != "" {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return nil, fmt.Errorf("peer listen address must be host:port: %w", err)
		}
		args = append(args, "--peer-listen", address)
	}
	if form.AllowLAN {
		args = append(args, "--allow-lan")
	}
	if form.Discover {
		args = append(args, "--discover")
	}
	for _, cidr := range form.TreatAsPrivate {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		// Shape only; whether the range is acceptable is ah's call, with the
		// node's own rule, and its message comes back to the panel verbatim.
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return nil, fmt.Errorf("treat-as-private entries must be CIDR blocks such as 122.122.0.0/16: %w", err)
		}
		args = append(args, "--treat-as-private", cidr)
	}
	if form.AutoWake {
		args = append(args, "--auto-wake")
	}
	return args, nil
}

// LocalAddress is one address the peer listener could bind to.
type LocalAddress struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
	// Subnet is the interface's own network in CIDR form, so a range the owner
	// is asked to declare is the one the cable actually carries, not a guess.
	Subnet string `json:"subnet"`
	// Private approximates whether the node would accept the address without
	// --treat-as-private: RFC 1918, loopback and link-local. The node's own
	// rule lives in internal/nodeconfig, which this module cannot import.
	Private bool `json:"private"`
}

// LocalAddresses lists this machine's IPv4 addresses so the form can offer
// them instead of asking the owner to type one.
func (a *App) LocalAddresses() ([]LocalAddress, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	addresses := make([]LocalAddress, 0, 4)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		unicast, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range unicast {
			ipNet, ok := address.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			ip := ipNet.IP.To4()
			network := &net.IPNet{IP: ip.Mask(ipNet.Mask), Mask: ipNet.Mask}
			addresses = append(addresses, LocalAddress{
				Interface: iface.Name,
				Address:   ip.String(),
				Subnet:    network.String(),
				Private:   ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast(),
			})
		}
	}
	return addresses, nil
}
