package main

import (
	"slices"
	"testing"
)

// What a Go program's os.Args is for each command line, by Go's rules (the
// standard library's own test vectors for commandLineToArgv among them). The
// ones marked are where shell32's CommandLineToArgvW — which the previous
// reader used — answers differently, so a node's --db would have been read as
// something the node itself never saw.
func TestSplitWindowsCommandLineReadsTheWayGoDoes(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{`"C:\Program Files\agenthub\agenthub-node.exe" --db "C:\Users\me\My Data\agenthub.db"`,
			[]string{`C:\Program Files\agenthub\agenthub-node.exe`, "--db", `C:\Users\me\My Data\agenthub.db`}},
		{`agenthub-node.exe --db C:\a\b.db`, []string{"agenthub-node.exe", "--db", `C:\a\b.db`}},
		{"a\tb  c", []string{"a", "b", "c"}},
		{`x "a b c"  d  e`, []string{"x", "a b c", "d", "e"}},
		{`x "ab\"c"  "\\"  d`, []string{"x", `ab"c`, `\`, "d"}},
		{`x a\\\b d"e f"g h`, []string{"x", `a\\\b`, "de fg", "h"}},
		{`x a\\\"b c d`, []string{"x", `a\"b`, "c", "d"}},
		{`x a\\\\"b c" d e`, []string{"x", `a\\b c`, "d", "e"}},
		{`x "" y`, []string{"x", "", "y"}},
		// Go keeps the "prior to 2008" rule for a doubled quote inside quotes;
		// CommandLineToArgvW does not.
		{`x "a b c""`, []string{"x", `a b c"`}},
		{`x """CallMeIshmael"""  b  c`, []string{"x", `"CallMeIshmael"`, "b", "c"}},
		{`x """"Call Me Ishmael"" b c`, []string{"x", `"Call Me Ishmael"`, "b", "c"}},
		// argv[0] is read by the same rules as the rest; CommandLineToArgvW
		// reads it with no backslash escapes.
		{`"C:\dir\\"agenthub-node.exe --db x`, []string{`C:\dir\agenthub-node.exe`, "--db", "x"}},
		{`C:\a\"b c`, []string{`C:\a"b`, "c"}},
		{`"" --db x`, []string{"", "--db", "x"}},
	}
	for _, tc := range cases {
		if got := splitWindowsCommandLine(tc.line); !slices.Equal(got, tc.want) {
			t.Errorf("%s\n  = %q\n  want %q", tc.line, got, tc.want)
		}
	}
}
