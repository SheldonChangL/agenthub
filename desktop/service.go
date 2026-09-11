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
// bundle, in the source tree, then on PATH. Reported in that order when none
// of them has it. A variable so tests can stand in for the lookup.
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

// locateBinary is that search, with the binary's name and the environment
// variable that overrides it as parameters: ah and agenthub-mcp ship side by
// side and are found the same way, and a second copy of this list would be a
// second thing to keep in step with how the app is packaged.
//
// The answer is always absolute. A relative path works for exec, which resolves
// it against this process's working directory, but the caller may be writing it
// into a config file that another program will read from somewhere else.
func locateBinary(name, envName string) (string, error) {
	looked := make([]string, 0, 6)
	if given := os.Getenv(envName); given != "" {
		if _, err := os.Stat(given); err == nil {
			return absolute(given)
		}
		looked = append(looked, "$"+envName+"="+given)
	}
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		for _, candidate := range []string{
			filepath.Join(dir, name),
			filepath.Join(dir, "..", "Resources", name),
			// The source tree: <repo>/desktop/build/bin/<app>.app/Contents/MacOS
			// on macOS (six levels up), <repo>/desktop/build/bin on Linux
			// (three), with the binaries at <repo>/bin/<name>.
			filepath.Join(dir, "..", "..", "..", "..", "..", "..", "bin", name),
			filepath.Join(dir, "..", "..", "..", "bin", name),
		} {
			if _, err := os.Stat(candidate); err == nil {
				return absolute(filepath.Clean(candidate))
			}
			looked = append(looked, candidate)
		}
	}
	if found, err := exec.LookPath(name); err == nil {
		return absolute(found)
	}
	looked = append(looked, "PATH")
	return "", fmt.Errorf("%s was not found (looked: %s); set %s to its path",
		name, strings.Join(looked, ", "), envName)
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
	args := []string{"service", "install"}
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
