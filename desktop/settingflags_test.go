package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
//
// So the node's flags are read from its source as Go, not as text: every
// non-test file of cmd/agenthub-node is parsed, and every call that defines a
// flag on the command line — flag.String, flag.StringVar, flag.Var,
// flag.Func, flag.BoolFunc, flag.TextVar and the rest, called on the package
// or on flag.CommandLine — gives its name and whether it is a boolean. A call
// on either that this check does not know is a failure, not a skip, so a
// definer Go adds later cannot slip past it.
func TestNodeFlagTableMatchesTheNode(t *testing.T) {
	found := nodeFlagsFromSource(t, filepath.Join("..", "cmd", "agenthub-node"))
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
	// flag.Var and flag.TextVar take whatever value they are given, and a
	// flag.Value with IsBoolFlag() true parses as a boolean. Deciding that per
	// call would take type-checking the node; instead this holds the node to
	// having no such value at all, and says so the day it gains one.
	root := filepath.Join("..")
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, declaration := range file.Decls {
				if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv != nil && function.Name.Name == "IsBoolFlag" {
					t.Errorf("%s declares IsBoolFlag: a flag.Var given that value is a boolean, which this check cannot tell apart; "+
						"teach it to", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// flagDefiners are the flag package's functions (and flag.CommandLine's
// methods) that define a flag: where in their arguments the name is, how many
// arguments they take, and whether the flag is a boolean.
var flagDefiners = map[string]struct {
	nameAt, args int
	boolean      bool
}{
	"Bool": {0, 3, true}, "BoolVar": {1, 4, true}, "BoolFunc": {0, 3, true},
	"String": {0, 3, false}, "StringVar": {1, 4, false},
	"Int": {0, 3, false}, "IntVar": {1, 4, false}, "Int64": {0, 3, false}, "Int64Var": {1, 4, false},
	"Uint": {0, 3, false}, "UintVar": {1, 4, false}, "Uint64": {0, 3, false}, "Uint64Var": {1, 4, false},
	"Float64": {0, 3, false}, "Float64Var": {1, 4, false},
	"Duration": {0, 3, false}, "DurationVar": {1, 4, false},
	"Var": {1, 3, false}, "TextVar": {1, 4, false}, "Func": {0, 3, false},
}

// flagNonDefiners are the rest of the flag package a program may call
// without defining anything.
var flagNonDefiners = map[string]bool{
	"Parse": true, "Parsed": true, "Args": true, "Arg": true, "NArg": true, "NFlag": true,
	"Visit": true, "VisitAll": true, "Lookup": true, "Set": true, "PrintDefaults": true,
	"NewFlagSet": true, "UnquoteUsage": true, "Usage": true,
	"Output": true, "SetOutput": true, "Name": true, "ErrorHandling": true, "Init": true,
	"ContinueOnError": true, "ExitOnError": true, "PanicOnError": true, "ErrHelp": true,
	"Flag": true, "FlagSet": true, "Value": true, "Getter": true,
}

// nodeFlagsFromSource reads every flag the node's main package defines on the
// command line: name → whether it takes a value.
func nodeFlagsFromSource(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the node's source: %v", err)
	}
	found := map[string]bool{}
	files := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		fileSet := token.NewFileSet()
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		flagName := ""
		for _, spec := range file.Imports {
			if spec.Path.Value != `"flag"` {
				continue
			}
			flagName = "flag"
			if spec.Name != nil {
				flagName = spec.Name.Name
			}
		}
		if flagName == "" {
			continue
		}
		if flagName == "_" || flagName == "." {
			t.Fatalf("%s imports flag as %s; this check reads flag.X calls and needs rewriting", path, flagName)
		}
		// Every mention of flag.X or flag.CommandLine.X has to be something
		// this check knows, whether it defines a flag or not.
		onFlag := func(selector *ast.SelectorExpr) bool {
			if isIdent(selector.X, flagName) {
				return selector.Sel.Name != "CommandLine"
			}
			inner, ok := selector.X.(*ast.SelectorExpr)
			return ok && isIdent(inner.X, flagName) && inner.Sel.Name == "CommandLine"
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || !onFlag(selector) {
				return true
			}
			if _, defines := flagDefiners[selector.Sel.Name]; !defines && !flagNonDefiners[selector.Sel.Name] {
				t.Errorf("%s: %s.%s is not a flag function this check knows; say here whether it defines a flag",
					fileSet.Position(selector.Pos()), flagName, selector.Sel.Name)
			}
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			definer, ok := flagDefiners[selector.Sel.Name]
			if !ok || definer.nameAt >= len(call.Args) {
				return true
			}
			literal, isLiteral := call.Args[definer.nameAt].(*ast.BasicLit)
			isLiteral = isLiteral && literal.Kind == token.STRING
			if !onFlag(selector) {
				// A definer's shape on some other receiver — a FlagSet handed
				// in as a parameter, say — may or may not be the command line.
				if isLiteral && len(call.Args) == definer.args {
					t.Errorf("%s: .%s(%s, ...) looks like a flag defined on something other than %s or %s.CommandLine; "+
						"this check cannot tell whether that is the node's command line", fileSet.Position(call.Pos()),
						selector.Sel.Name, literal.Value, flagName, flagName)
				}
				return true
			}
			if !isLiteral {
				t.Errorf("%s: the flag's name is not a string literal; this check cannot read it",
					fileSet.Position(call.Args[definer.nameAt].Pos()))
				return true
			}
			flag, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("%s: %v", fileSet.Position(literal.Pos()), err)
			}
			if _, twice := found[flag]; twice {
				t.Errorf("%s: -%s is defined twice", fileSet.Position(call.Pos()), flag)
			}
			found[flag] = !definer.boolean
			return true
		})
	}
	if files == 0 || len(found) == 0 {
		t.Fatalf("no flag definitions were read from %s (%d files); this check would pass on nothing", dir, files)
	}
	return found
}

func isIdent(expression ast.Expr, name string) bool {
	ident, ok := expression.(*ast.Ident)
	return ok && ident.Name == name
}
