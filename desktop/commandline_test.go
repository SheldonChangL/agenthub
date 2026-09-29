package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

// What a person is handed to paste: the words, quoted for the shell they have.
func TestShellCommandLineQuotesForEachShell(t *testing.T) {
	for _, tc := range []struct {
		goos    string
		program string
		args    []string
		want    string
	}{
		{"darwin", "/Applications/AgentHub.app/Contents/MacOS/agenthub-node",
			[]string{"--db", "/Users/me/Library/Application Support/agenthub/agenthub.db"},
			`/Applications/AgentHub.app/Contents/MacOS/agenthub-node --db '/Users/me/Library/Application Support/agenthub/agenthub.db'`},
		{"linux", "/usr/bin/agenthub-node",
			[]string{"--db", "/srv/$HOME/`x`/it's/a.db", "", "=x", `back\slash`, "-listen", "127.0.0.1:7462"},
			`/usr/bin/agenthub-node --db '/srv/$HOME/` + "`x`" + `/it'\''s/a.db' '' '=x' 'back\slash' -listen 127.0.0.1:7462`},
		{"windows", `C:\Program Files\AgentHub\agenthub-node.exe`,
			[]string{"--db", `C:\Users\me\My Data\agenthub.db`, "--display-name", "it's $me `now` \u2019q", "--", "-discover"},
			`& 'C:\Program Files\AgentHub\agenthub-node.exe' --db 'C:\Users\me\My Data\agenthub.db' --display-name 'it''s $me ` +
				"`now`" + ` ` + "\u2019\u2019" + `q' '--' -discover`},
		{"windows", `C:\agenthub\agenthub-node.exe`, []string{"--db", `C:\a\x.db`, "--db=C:\\x", "--%"},
			`& 'C:\agenthub\agenthub-node.exe' --db 'C:\a\x.db' '--db=C:\x' '--%'`},
	} {
		if got := shellCommandLine(tc.goos, tc.program, tc.args); got != tc.want {
			t.Errorf("%s %q:\n  got  %s\n  want %s", tc.goos, tc.args, got, tc.want)
		}
	}
}

// TestPrintArgsHelper is not a test: it is the program the command lines in
// TestCommandLineSurvivesTheShell run, and it prints the arguments it got.
func TestPrintArgsHelper(t *testing.T) {
	if os.Getenv("AGENTHUB_PRINT_ARGS_HELPER") != "1" {
		t.Skip("helper process")
	}
	line, _ := json.Marshal(os.Args[1:])
	fmt.Printf("ARGS %s\n", line)
}

// The quoting above, handed to the real shell: this test's own binary is the
// program, and what it prints is what the shell passed it. On Windows that is
// Windows PowerShell 5.1, the one every Windows has; elsewhere every POSIX
// shell this machine has at /bin.
func TestCommandLineSurvivesTheShell(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestPrintArgsHelper$", "--",
		"--db", filepath.Join("a dir", "Application Support", "agenthub.db"), "$HOME", "`whoami`", "it's",
		"\u2019curly\u2018", "%PATH%", "a;b", "(x)", "@y", "a&b", "a|b", "*", "~", "-listen", "127.0.0.1:7462"}
	var shells [][]string
	if runtime.GOOS == "windows" {
		// Windows PowerShell 5.1 drops an empty argument and passes a " inside
		// one unescaped (shellCommandLine); neither is a Windows path, and
		// neither is asked of it here.
		powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		shells = append(shells, []string{powershell, "-NoProfile", "-NonInteractive", "-EncodedCommand"})
	} else {
		args = append(args, "", `"quoted"`, `trailing\`, `$(echo hi)`, "=x", "!bang", "new\nline")
		for _, shell := range []string{"/bin/sh", "/bin/bash", "/bin/zsh", "/bin/dash"} {
			if _, err := os.Stat(shell); err == nil {
				shells = append(shells, []string{shell, "-c"})
			}
		}
	}
	if len(shells) == 0 {
		t.Fatal("no shell to paste into")
	}
	line := commandLine(self, args)
	for _, shell := range shells {
		script := line
		if runtime.GOOS == "windows" {
			units := utf16.Encode([]rune(line))
			encoded := make([]byte, 0, len(units)*2)
			for _, unit := range units {
				encoded = binary.LittleEndian.AppendUint16(encoded, unit)
			}
			script = base64.StdEncoding.EncodeToString(encoded)
		}
		// #nosec G204 -- a shell at a fixed path, running this test's own binary.
		command := exec.Command(shell[0], append(slices.Clone(shell[1:]), script)...)
		command.Env = append(os.Environ(), "AGENTHUB_PRINT_ARGS_HELPER=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s\n%s", shell[0], err, line, output)
		}
		var got []string
		for _, printed := range strings.Split(string(output), "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimRight(printed, "\r"), "ARGS "); ok {
				if err := json.Unmarshal([]byte(rest), &got); err != nil {
					t.Fatal(err)
				}
			}
		}
		if !slices.Equal(got, args) {
			t.Errorf("%s was handed\n  %s\nand passed on %q, want %q", shell[0], line, got, args)
		}
	}
}
