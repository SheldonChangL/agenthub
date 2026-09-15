package service

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"strings"
	"unicode/utf16"
)

// The Windows side of this package: a per-user scheduled task.
//
// Windows has no launchd and no `systemctl --user`, and the thing it does have
// — a service — is machine-wide, starts before anyone logs in, and would run
// the node as a different principal. That is the wrong shape for this node
// twice over: node.key is sealed with DPAPI against the user who created it,
// and the sessions the node reports are found by reading that user's own
// ~/.claude and ~/.codex. So the registration here is a Task Scheduler task in
// the owner's own account, triggered at their logon, running with their own
// interactive token.
//
// Two honest differences from the other two platforms, both stated in the
// install report rather than papered over:
//
//  1. The task starts the node; it does not watch it. Task Scheduler can
//     restart a task whose process exits, but the process it would watch is
//     the launcher below, not the node — see TaskXML's action. A node that
//     exits therefore stays stopped until the next logon or a restart from the
//     app, where launchd's KeepAlive and systemd's Restart= would bring it
//     back.
//  2. It runs only while the owner is logged on. "Run whether user is logged
//     on or not" would store their password and run the node in session 0,
//     which breaks both DPAPI and the provider scan.

// TaskName is what the task is called in the owner's Task Scheduler library.
// A person reading a list of tasks is the audience: it is not the label the
// other platforms use, because nothing on Windows keys off that.
const TaskName = "AgentHub Node"

// TaskXML renders the task definition.
//
// Registered from an XML file rather than from `schtasks /Create /SC ONLOGON`
// flags, because two of the settings below have no flag and both of them are
// bugs if left at their defaults: ExecutionTimeLimit defaults to 72 hours, on
// expiry of which Task Scheduler would terminate a node that had been running
// since Monday, and MultipleInstancesPolicy decides what a second logon does.
//
// The action is the launcher, not the node. `ah service run-node` starts
// agenthub-node detached, with its output on a file, and exits — so the node
// gets no console window of its own, while agenthub-node.exe keeps its console
// subsystem and still prints when an owner runs it by hand to find out why it
// will not start. That diagnosis is the only one available to them, and a
// GUI-subsystem node would have traded it for a log file nobody can find.
func TaskXML(config Config, userName string) ([]byte, error) {
	if strings.TrimSpace(config.Launcher) == "" {
		return nil, fmt.Errorf("the scheduled task needs the path to ah, which starts the node")
	}
	if strings.TrimSpace(userName) == "" {
		return nil, fmt.Errorf("the scheduled task needs the name of the user to run as")
	}
	arguments, err := taskArguments(config)
	if err != nil {
		return nil, err
	}
	var document bytes.Buffer
	document.WriteString(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Runs the AgentHub node for this user, starting at logon.</Description>
    <URI>\` + TaskName + `</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + escape(userName) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + escape(userName) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + escape(config.Launcher) + `</Command>
      <Arguments>` + escape(arguments) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`)
	// UTF-16LE with a BOM, which is what schtasks /XML accepts. A UTF-8 file
	// is refused with "The task XML contains a value which is incorrectly
	// formatted or out of range", an error that says nothing about encoding
	// and sends the reader looking at their paths.
	return utf16LE(document.String()), nil
}

// taskArguments is the launcher's command line: where the node is, where its
// output goes, and then the node's own flags after a `--` that keeps them from
// being read as the launcher's.
func taskArguments(config Config) (string, error) {
	if strings.TrimSpace(config.NodeBinary) == "" {
		return "", fmt.Errorf("the scheduled task needs the path to agenthub-node")
	}
	parts := []string{"service", "run-node", "--node-binary", quoteArgument(config.NodeBinary)}
	if config.LogPath != "" {
		parts = append(parts, "--log", quoteArgument(config.LogPath))
	}
	if len(config.Args) > 0 {
		parts = append(parts, "--")
		for _, argument := range config.Args {
			parts = append(parts, quoteArgument(argument))
		}
	}
	return strings.Join(parts, " "), nil
}

// quoteArgument quotes what Windows would otherwise split. Paths under
// "C:\Program Files" and any user name with a space in it are the ordinary
// case here, not the exotic one.
func quoteArgument(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\"") {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func escape(value string) string {
	var buffer bytes.Buffer
	// EscapeText only fails on a failing writer, and a bytes.Buffer does not
	// fail; the value is still escaped rather than interpolated raw, because a
	// user name or an install path is not this package's to trust as markup.
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

// utf16LE encodes the document the way schtasks wants to read it.
func utf16LE(text string) []byte {
	encoded := utf16.Encode([]rune(text))
	out := make([]byte, 0, 2+len(encoded)*2)
	out = binary.LittleEndian.AppendUint16(out, 0xFEFF) // byte order mark
	for _, unit := range encoded {
		out = binary.LittleEndian.AppendUint16(out, unit)
	}
	return out
}
