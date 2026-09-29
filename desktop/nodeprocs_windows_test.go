//go:build windows

package main

import (
	"slices"
	"strings"
	"testing"
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
