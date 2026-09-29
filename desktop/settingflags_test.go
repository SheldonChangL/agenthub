package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// nodeSettingFlags is a copy, and a copy has to be checked.
//
// This module shares no code with the node on purpose — it is what keeps Wails'
// CGo requirement away from a cross-compiled agenthub-node — so the five
// settings are spelled here a second time. The cost of that is a sixth setting
// added on one side only: the panel would stop recognising a unit that pins it,
// and go back to showing an owner a settings page whose saves are overridden
// with nothing on screen to say why.
//
// So the node's own list is read from its source, the way the frontend's static
// checks in this package read the frontend's. A test that imported the package
// would be the better tool and is the thing this module may not do.
func TestNodeSettingFlagsMatchTheNodesOwnList(t *testing.T) {
	source := filepath.Join("..", "internal", "nodeconfig", "settings.go")
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read the node's settings: %v", err)
	}
	names := settingNamesFrom(t, string(content))
	want := make([]string, 0, len(names))
	for _, name := range names {
		want = append(want, flagSpelling(name))
	}
	if !slices.Equal(nodeSettingFlags, want) {
		t.Errorf("nodeSettingFlags = %q, the node's own settings are %q;\n"+
			"a setting the node has and this list does not is one the panel cannot see pinned into a unit",
			nodeSettingFlags, want)
	}
}

// settingNamesFrom reads the constants SettingNames is built from, in the order
// SettingNames lists them.
func settingNamesFrom(t *testing.T, source string) []string {
	t.Helper()
	constants := map[string]string{}
	for _, match := range regexp.MustCompile(`(?m)^\s*(Setting\w+)\s*=\s*"([^"]+)"`).FindAllStringSubmatch(source, -1) {
		constants[match[1]] = match[2]
	}
	block := regexp.MustCompile(`(?s)var SettingNames = \[\]string\{(.*?)\}`).FindStringSubmatch(source)
	if block == nil {
		t.Fatal("SettingNames is no longer a slice literal in the node's settings.go; this check needs rewriting")
	}
	var names []string
	for _, reference := range strings.Split(block[1], ",") {
		reference = strings.TrimSpace(reference)
		if reference == "" {
			continue
		}
		value, ok := constants[reference]
		if !ok {
			t.Fatalf("SettingNames mentions %q, which is not a constant this check found", reference)
		}
		names = append(names, value)
	}
	if len(names) == 0 {
		t.Fatal("no setting names were read; this check would pass on an empty list")
	}
	return names
}

// flagSpelling is nodeconfig.FlagName: camelCase to kebab-case.
func flagSpelling(field string) string {
	var out strings.Builder
	for _, character := range field {
		if character >= 'A' && character <= 'Z' {
			out.WriteByte('-')
			out.WriteRune(character + ('a' - 'A'))
			continue
		}
		out.WriteRune(character)
	}
	return out.String()
}

// nodeFlagTakesValue is a copy too, and the restart reads a running node's
// command line with it (#205). A flag the node gained and this table lacks
// makes every node started with it one the restart refuses; one whose kind is
// wrong here makes the restart misread where a --db value ends.
func TestNodeFlagTableMatchesTheNode(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "cmd", "agenthub-node", "main.go"))
	if err != nil {
		t.Fatalf("read the node's main: %v", err)
	}
	definition := regexp.MustCompile(`flag\.(String|Bool|Duration|Int|Int64|Uint|Uint64|Float64|Var|Func|BoolFunc|TextVar)\(\s*(?:&\w+,\s*)?"([^"]+)"`)
	found := map[string]bool{}
	for _, match := range definition.FindAllStringSubmatch(string(content), -1) {
		found[match[2]] = match[1] != "Bool" && match[1] != "BoolFunc"
	}
	if len(found) == 0 {
		t.Fatal("no flag definitions were read from the node's main; this check would pass on nothing")
	}
	for name, takesValue := range found {
		mine, ok := nodeFlagTakesValue[name]
		if !ok {
			t.Errorf("the node defines -%s and nodeFlagTakesValue does not: a node started with it cannot be restarted", name)
		} else if mine != takesValue {
			t.Errorf("-%s: takes a value = %v here, %v in the node", name, mine, takesValue)
		}
	}
	for name := range nodeFlagTakesValue {
		if _, ok := found[name]; !ok {
			t.Errorf("nodeFlagTakesValue has -%s, which the node does not define", name)
		}
	}
	for _, flag := range nodeSettingFlags {
		if _, ok := nodeFlagTakesValue[flag]; !ok {
			t.Errorf("remembered setting -%s is not in nodeFlagTakesValue", flag)
		}
	}
}
