//go:build windows

package main

import (
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// What PowerShell's ConvertTo-Json gives back, including the forms it takes
// for nothing found, and a command line Windows quoted because of its spaces.
func TestParseNodeProcessList(t *testing.T) {
	for _, empty := range []string{"", "[]", "\ufeff[]\r\n"} {
		found, err := parseNodeProcessList(empty)
		if err != nil || len(found) != 0 {
			t.Errorf("%q: found %v, err %v; want nothing", empty, found, err)
		}
	}
	found, err := parseNodeProcessList(`[{"pid":12,"commandLine":"\"C:\\Program Files\\agenthub\\agenthub-node.exe\" --db \"C:\\Users\\me\\My Data\\agenthub.db\""}]`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`C:\Program Files\agenthub\agenthub-node.exe`, "--db", `C:\Users\me\My Data\agenthub.db`}
	if len(found) != 1 || found[0].PID != 12 || !slices.Equal(found[0].Argv, want) {
		t.Errorf("found %+v, want pid 12 with argv %q", found, want)
	}
	if _, err := parseNodeProcessList(`[{"pid":13,"commandLine":null}]`); err == nil || !strings.Contains(err.Error(), "13") {
		t.Errorf("an unreadable command line was not a failure naming its pid: %v", err)
	}
}

// Whatever a Go program starts a node with, the node's os.Args is what Go's
// own escaping and splitting make of it — syscall.EscapeArg on the way in (what
// os/exec does), splitWindowsCommandLine standing for os.commandLineToArgv on
// the way out — and this app has to read the same thing back.
func TestWindowsCommandLineRoundTrips(t *testing.T) {
	for _, argv := range [][]string{
		{`C:\Program Files\agenthub\agenthub-node.exe`, "--db", `C:\Users\me\My Data\agenthub.db`},
		{`"C:\quoted"\agenthub-node.exe`, "--db", `C:\a b\x.db`},
		{"agenthub-node.exe", "", "", "--display-name", ""},
		{"agenthub-node.exe", `say "hi"`, `a\"b`, `trailing\`, `trailing space\ `, `\\server\share\x.db`},
		{"agenthub-node.exe", "tab\there", `""`, `"`, `\"`, `a"b"c`, `C:\dir\\`},
	} {
		words := make([]string, 0, len(argv))
		for _, word := range argv {
			words = append(words, syscall.EscapeArg(word))
		}
		line := strings.Join(words, " ")
		if got := splitWindowsCommandLine(line); !slices.Equal(got, argv) {
			t.Errorf("%q\n  escaped %s\n  read back %q", argv, line, got)
		}
	}
}

func TestPowerShellIsTheOneWindowsKeeps(t *testing.T) {
	got := powerShellPath(func(name string) string {
		if name == "SystemRoot" {
			return `C:\Windows`
		}
		return ""
	})
	if got != `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` {
		t.Errorf("with SystemRoot: %s", got)
	}
	if got := powerShellPath(func(string) string { return "" }); got != "powershell.exe" {
		t.Errorf("without SystemRoot: %s", got)
	}
}

// processBasicInformation stands in for windows.PROCESS_BASIC_INFORMATION so
// that no other process's address sits in a Go pointer; it has to be laid out
// the same, or NtQueryInformationProcess fills the wrong fields.
func TestProcessBasicInformationIsLaidOutLikeWindows(t *testing.T) {
	var ours processBasicInformation
	var theirs windows.PROCESS_BASIC_INFORMATION
	if unsafe.Sizeof(ours) != unsafe.Sizeof(theirs) ||
		unsafe.Offsetof(ours.PebBaseAddress) != unsafe.Offsetof(theirs.PebBaseAddress) {
		t.Errorf("size %d/%d, PebBaseAddress at %d/%d", unsafe.Sizeof(ours), unsafe.Sizeof(theirs),
			unsafe.Offsetof(ours.PebBaseAddress), unsafe.Offsetof(theirs.PebBaseAddress))
	}
}

func TestParseEnvironmentBlock(t *testing.T) {
	// utf16.Encode, not windows.StringToUTF16: that one panics on the NULs a
	// block is made of, and appends one of its own.
	block := func(text string) []uint16 { return utf16.Encode([]rune(text)) }
	env, err := parseEnvironmentBlock(block("=C:=C:\\x\x00APPDATA=C:\\Users\\me \u00e9\\AppData\\Roaming\x00\x00"))
	if err != nil || !slices.Equal(env, []string{`=C:=C:\x`, "APPDATA=C:\\Users\\me \u00e9\\AppData\\Roaming"}) {
		t.Errorf("env %q, err %v", env, err)
	}
	for _, bad := range []string{"APPDATA=C:\\trunc", "NOEQUALS\x00\x00"} {
		if env, err := parseEnvironmentBlock(block(bad)); err == nil {
			t.Errorf("%q read as %q, want an error", bad, env)
		}
	}
}

// The reader of another process's environment, pointed at this one: what it
// reads has to be this process's own environment, and this process has to be
// this app's user.
func TestProcessEnvironmentReadsAProcess(t *testing.T) {
	env, err := processEnvironment(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range os.Environ() {
		if !slices.Contains(env, entry) {
			t.Errorf("%q is in this process's environment and not in what was read", entry)
		}
	}
	if err := runsAsThisUser(os.Getpid()); err != nil {
		t.Errorf("this process is not this process's user: %v", err)
	}
}
