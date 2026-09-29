//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// nodeProcessQuery lists agenthub-node.exe with its command line. Windows keeps
// a process's command line as the one string it was started with; WMI is the
// supported way to read another process's.
//
// Progress records off first: PowerShell reports Get-CimInstance's progress on
// stderr, serialised as `#< CLIXML`, and although only stdout is read as the
// answer (listNodeProcesses), a host that shows progress is also slower. The
// output encoding is set to UTF-8 because the console's code page would
// otherwise mangle a database path with anything outside it — and a mangled
// path is a different database.
const nodeProcessQuery = `$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$found = @(Get-CimInstance -ClassName Win32_Process | Where-Object { $_.Name -eq 'agenthub-node.exe' } |
  ForEach-Object { [pscustomobject]@{ pid = [int]$_.ProcessId; commandLine = $_.CommandLine } })
ConvertTo-Json -Compress -InputObject $found`

// listNodeProcesses finds every agenthub-node.exe, the arguments each was
// started with, and the environment it runs with: the same processes
// `taskkill /IM agenthub-node.exe` ends.
func listNodeProcesses(ctx context.Context) ([]nodeProcess, error) {
	// -EncodedCommand so the script reaches PowerShell as written: nothing in
	// it has to survive a second round of command-line quoting.
	encoded := base64.StdEncoding.EncodeToString(utf16LE(nodeProcessQuery))
	stdout, stderr, err := runCommandOutput(ctx, powerShellPath(os.Getenv),
		"-NoProfile", "-NonInteractive", "-EncodedCommand", encoded)
	if err != nil {
		return nil, fmt.Errorf("list agenthub-node.exe processes: %w: %s", err,
			strings.TrimSpace(strings.TrimSpace(stdout)+"\n"+strings.TrimSpace(stderr)))
	}
	found, err := parseNodeProcessList(stdout)
	if err != nil {
		if trimmed := strings.TrimSpace(stderr); trimmed != "" {
			err = fmt.Errorf("%w (PowerShell also said: %s)", err, trimmed)
		}
		return nil, err
	}
	for index := range found {
		found[index].Env, found[index].EnvErr = processEnvironment(found[index].PID)
		found[index].UserErr = runsAsThisUser(found[index].PID)
	}
	return found, nil
}

// powerShellPath is Windows PowerShell where Windows keeps it, rather than the
// first powershell.exe on a PATH anything may have put a directory in front
// of. Only without SystemRoot — which Windows always sets — does it fall back
// to the name.
func powerShellPath(getenv func(string) string) string {
	if root := getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	}
	return "powershell.exe"
}

func parseNodeProcessList(output string) ([]nodeProcess, error) {
	output = strings.TrimSpace(strings.TrimPrefix(output, "\ufeff"))
	if output == "" {
		return nil, nil
	}
	var rows []struct {
		PID         int     `json:"pid"`
		CommandLine *string `json:"commandLine"`
	}
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		return nil, fmt.Errorf("read the process list: %w: %s", err, output)
	}
	found := make([]nodeProcess, 0, len(rows))
	for _, row := range rows {
		// Empty for a process this user may not read, which is another user's
		// — one taskkill would also try to end.
		if row.CommandLine == nil || strings.TrimSpace(*row.CommandLine) == "" {
			return nil, fmt.Errorf("the command line of agenthub-node.exe (pid %d) is not readable by this user", row.PID)
		}
		found = append(found, nodeProcess{PID: row.PID, Argv: splitWindowsCommandLine(*row.CommandLine)})
	}
	return found, nil
}

func utf16LE(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		out = binary.LittleEndian.AppendUint16(out, unit)
	}
	return out
}

// processBasicInformation is windows.PROCESS_BASIC_INFORMATION with every
// address as a uintptr. The addresses are the other process's: held in a Go
// pointer field, one that happened to fall inside this process's heap would be
// taken for a pointer into it by the garbage collector.
type processBasicInformation struct {
	ExitStatus                   uint32
	PebBaseAddress               uintptr
	AffinityMask                 uintptr
	BasePriority                 int32
	UniqueProcessID              uintptr
	InheritedFromUniqueProcessID uintptr
}

// maxEnvironmentBytes bounds what is read of another process's environment.
// Windows allows a block far smaller than this in practice; a size past it
// means the block was misread, not that it is that large.
const maxEnvironmentBytes = 4 << 20

// processEnvironment reads another process's environment block: its PEB's
// process parameters name the block and its size, and ReadProcessMemory
// copies it out. WMI has no field for it. This is what psutil and Process
// Explorer do; it needs PROCESS_VM_READ, which a process of the same user
// grants. Only a process of this app's own width is read — the offsets are
// this build's — which is every node this app ships with.
func processEnvironment(pid int) ([]string, error) {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return nil, fmt.Errorf("pid %d is not a process id", pid)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false,
		uint32(pid)) // #nosec G115 -- checked to fit above
	if err != nil {
		return nil, fmt.Errorf("open pid %d to read its environment: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var wow64 bool
	if err := windows.IsWow64Process(handle, &wow64); err != nil {
		return nil, fmt.Errorf("ask whether pid %d is a 32-bit process: %w", pid, err)
	}
	var mine bool
	if err := windows.IsWow64Process(windows.CurrentProcess(), &mine); err != nil {
		return nil, fmt.Errorf("ask whether this app is a 32-bit process: %w", err)
	}
	if wow64 != mine {
		return nil, fmt.Errorf("pid %d is not the same width as this app, so its process parameters are laid out differently", pid)
	}

	var basic processBasicInformation
	// #nosec G103 -- the documented out-parameter of NtQueryInformationProcess, sized to the structure passed.
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessBasicInformation,
		unsafe.Pointer(&basic), uint32(unsafe.Sizeof(basic)), nil); err != nil {
		return nil, fmt.Errorf("query pid %d: %w", pid, err)
	}
	if basic.PebBaseAddress == 0 {
		return nil, fmt.Errorf("pid %d has no process environment block", pid)
	}
	var peb windows.PEB
	var params windows.RTL_USER_PROCESS_PARAMETERS
	parameters, err := readRemoteAddress(handle, basic.PebBaseAddress+unsafe.Offsetof(peb.ProcessParameters))
	if err != nil {
		return nil, fmt.Errorf("read pid %d's process parameters: %w", pid, err)
	}
	block, err := readRemoteAddress(handle, parameters+unsafe.Offsetof(params.Environment))
	if err != nil {
		return nil, fmt.Errorf("read where pid %d keeps its environment: %w", pid, err)
	}
	size, err := readRemoteAddress(handle, parameters+unsafe.Offsetof(params.EnvironmentSize))
	if err != nil {
		return nil, fmt.Errorf("read the size of pid %d's environment: %w", pid, err)
	}
	if block == 0 || size < 4 || size > maxEnvironmentBytes || size%2 != 0 {
		return nil, fmt.Errorf("pid %d's environment is at %#x and %d bytes, which is not a block this app can read", pid, block, size)
	}
	raw := make([]uint16, size/2)
	// #nosec G103 -- ReadProcessMemory's documented out-buffer, sized to the slice.
	if err := windows.ReadProcessMemory(handle, block, (*byte)(unsafe.Pointer(&raw[0])), size, nil); err != nil {
		return nil, fmt.Errorf("read pid %d's environment: %w", pid, err)
	}
	return parseEnvironmentBlock(raw)
}

// readRemoteAddress reads one pointer-sized value from another process.
func readRemoteAddress(handle windows.Handle, at uintptr) (uintptr, error) {
	var value uintptr
	// #nosec G103 -- ReadProcessMemory's documented out-buffer, the size of the value it fills.
	err := windows.ReadProcessMemory(handle, at, (*byte)(unsafe.Pointer(&value)), unsafe.Sizeof(value), nil)
	return value, err
}

// parseEnvironmentBlock splits a Windows environment block — NUL-terminated
// UTF-16 entries, the last followed by one more NUL — into its entries. A
// block that does not end that way was misread, and is an error rather than a
// guess: a truncated APPDATA is a different directory.
func parseEnvironmentBlock(raw []uint16) ([]string, error) {
	env := []string{}
	for start := 0; start < len(raw); {
		end := start
		for end < len(raw) && raw[end] != 0 {
			end++
		}
		if end == len(raw) {
			return nil, errors.New("the environment block has no end")
		}
		if end == start {
			return env, nil
		}
		entry := string(utf16.Decode(raw[start:end]))
		if !strings.Contains(entry, "=") {
			return nil, fmt.Errorf("the environment block holds %q, which is not NAME=value", entry)
		}
		env = append(env, entry)
		start = end + 1
	}
	return nil, errors.New("the environment block has no end")
}

// runsAsThisUser says whether a process runs as the same Windows account as
// this app, by the user SID in each one's token. nil means it does.
func runsAsThisUser(pid int) error {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return fmt.Errorf("pid %d is not a process id", pid)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false,
		uint32(pid)) // #nosec G115 -- checked to fit above
	if err != nil {
		return fmt.Errorf("open pid %d to read its user: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("read pid %d's user: %w", pid, err)
	}
	defer func() { _ = token.Close() }()
	theirs, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("read pid %d's user: %w", pid, err)
	}
	mine, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read this app's user: %w", err)
	}
	if !windows.EqualSid(theirs.User.Sid, mine.User.Sid) {
		return fmt.Errorf("pid %d runs as %s, and this app as %s", pid, theirs.User.Sid, mine.User.Sid)
	}
	return nil
}
