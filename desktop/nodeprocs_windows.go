//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// nodeProcessQuery lists agenthub-node.exe with its command line. Windows keeps
// a process's command line as the one string it was started with; WMI is the
// supported way to read another process's. The output encoding is set to
// UTF-8 first, because the console's code page would otherwise mangle a
// database path with anything outside it — and a mangled path is a different
// database.
const nodeProcessQuery = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$found = @(Get-CimInstance -ClassName Win32_Process | Where-Object { $_.Name -eq 'agenthub-node.exe' } |
  ForEach-Object { [pscustomobject]@{ pid = [int]$_.ProcessId; commandLine = $_.CommandLine } })
ConvertTo-Json -Compress -InputObject $found`

// listNodeProcesses finds every agenthub-node.exe and the arguments each was
// started with: the same processes `taskkill /IM agenthub-node.exe` ends.
func listNodeProcesses(ctx context.Context) ([]nodeProcess, error) {
	// -EncodedCommand so the script reaches PowerShell as written: nothing in
	// it has to survive a second round of command-line quoting.
	encoded := base64.StdEncoding.EncodeToString(utf16LE(nodeProcessQuery))
	output, err := runCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded)
	if err != nil {
		return nil, fmt.Errorf("list agenthub-node.exe processes: %w: %s", err, strings.TrimSpace(output))
	}
	return parseNodeProcessList(output)
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
		argv, err := windows.DecomposeCommandLine(*row.CommandLine)
		if err != nil {
			return nil, fmt.Errorf("read the command line of agenthub-node.exe (pid %d): %w", row.PID, err)
		}
		found = append(found, nodeProcess{PID: row.PID, Argv: argv})
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
