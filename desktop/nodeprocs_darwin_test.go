//go:build darwin

package main

import (
	"encoding/binary"
	"slices"
	"strings"
	"testing"
)

// appleStrings are what the kernel puts after a process's environment in
// kern.procargs2, as Darwin 25.6 gives them.
var appleStrings = []string{
	"pfz=0xfffe2c000", "stack_guard=0x3d7b995806ce0098", "ptr_munge=0x7abc68295f7c2aba",
	"main_stack=0x16fad0000,0x7fc000,0x16bad0000,0x4000000", "executable_cdhash=0a530a653cf11fb5b23f029303c82bf9135b6315",
	"executable_boothash=e3922da85a7b8d73c6df38885a2cc5856c9d40db", "th_port=0x103", "security_config=0x0",
}

// procargs2 builds what kern.procargs2 answers, laid out the way the kernel
// lays it out: argc; the executable path, NUL-terminated and padded with NULs
// to a multiple of eight; the arguments; the environment, padded with NULs to
// a multiple of eight — so one that ends on that boundary is followed by no
// NUL but its own; the apple strings; and, when trailing, NULs to a multiple
// of eight after them. Without it the buffer ends on the last string's NUL.
func procargs2(path string, argv, env []string, trailing bool) []byte {
	raw := binary.LittleEndian.AppendUint32(nil, uint32(len(argv))) // #nosec G115 -- a test's handful of arguments
	pad := func() {
		for (len(raw)-4)%8 != 0 {
			raw = append(raw, 0)
		}
	}
	raw = append(raw, path...)
	raw = append(raw, 0)
	pad()
	for _, word := range append(slices.Clone(argv), env...) {
		raw = append(raw, word...)
		raw = append(raw, 0)
	}
	pad()
	for _, word := range appleStrings {
		raw = append(raw, word...)
		raw = append(raw, 0)
	}
	if trailing {
		pad()
	}
	return raw
}

func TestParseProcargs2(t *testing.T) {
	// Every length of path modulo eight, so the padding is every length it can
	// be, from none to seven NULs after the path's own.
	for extra := range 8 {
		path := "/Applications/agenthub-node" + strings.Repeat("x", extra)
		for _, argv := range [][]string{
			{path, "--db", "/Users/me/Library/Application Support/agenthub/agenthub.db"},
			// The review's case: an empty argv[0] is one NUL, the same byte as
			// the padding, and must not be swallowed with it.
			{"", "--db", "/p/a.db"},
			{"", "", "--db", "/p/a.db"},
			{"agenthub-node"},
		} {
			// Every length of environment modulo eight too: the one that ends
			// on the boundary has no NUL between it and the apple strings, and
			// with no trailing padding the buffer ends without an empty entry
			// anywhere after the arguments.
			for fill := range 8 {
				home := "/Users/me" + strings.Repeat("e", fill)
				env := []string{"HOME=" + home, "XDG_CONFIG_HOME=/elsewhere"}
				for _, trailing := range []bool{false, true} {
					gotArgv, gotEnv, err := parseProcargs2(procargs2(path, argv, env, trailing))
					if err != nil {
						t.Fatalf("path %q, argv %q, env %q, trailing %v: %v", path, argv, env, trailing, err)
					}
					if !slices.Equal(gotArgv, argv) {
						t.Errorf("path %q: argv = %q, want %q", path, gotArgv, argv)
					}
					if want := append(slices.Clone(env), appleStrings...); !slices.Equal(gotEnv, want) {
						t.Errorf("path %q, argv %q, trailing %v: env = %q, want %q", path, argv, trailing, gotEnv, want)
					}
					if got := environmentLookup("darwin", gotEnv)("HOME"); got != home {
						t.Errorf("HOME = %q, want %q", got, home)
					}
				}
			}
		}
	}
}

// No environment at all, and an empty entry in one, are read as what they
// are: nothing, and nothing in between.
func TestParseProcargs2ReadsEmptyEnvironments(t *testing.T) {
	for _, env := range [][]string{nil, {"HOME=/Users/me", "", "PATH=/usr/bin"}} {
		want := append(slices.DeleteFunc(slices.Clone(env), func(entry string) bool { return entry == "" }), appleStrings...)
		for _, trailing := range []bool{false, true} {
			_, got, err := parseProcargs2(procargs2("/bin/agenthub-node", []string{"agenthub-node"}, env, trailing))
			if err != nil || !slices.Equal(got, want) {
				t.Errorf("env %q, trailing %v: read %q, %v; want %q", env, trailing, got, err, want)
			}
		}
	}
	// A platform binary's environment is withheld: the buffer ends at the
	// arguments.
	raw := procargs2("/bin/agenthub-node", []string{"agenthub-node"}, nil, false)
	raw = raw[:4+24+len("agenthub-node\x00")]
	if argv, env, err := parseProcargs2(raw); err != nil || len(argv) != 1 || len(env) != 0 {
		t.Errorf("no environment: argv %q, env %q, err %v", argv, env, err)
	}
}

func TestParseProcargs2RefusesWhatItCannotRead(t *testing.T) {
	good := procargs2("/bin/agenthub-node", []string{"agenthub-node", "--db", "/p/a.db"}, []string{"HOME=/Users/me"}, true)
	lies := func(argc uint32) []byte {
		raw := slices.Clone(good)
		binary.LittleEndian.PutUint32(raw, argc)
		return raw
	}
	for name, tc := range map[string]struct {
		raw  []byte
		want string
	}{
		"too short": {[]byte{1, 0}, "answered 2 bytes"},
		// More arguments than bytes is a count to refuse before it sizes
		// anything, not one to discover the end of.
		"argc past the end":  {lies(uint32(len(good))), "arguments in"},
		"argc huge":          {lies(0x7fffffff), "arguments in"},
		"argc negative":      {lies(0xffffffff), "arguments in"},
		"no end to the path": {append(binary.LittleEndian.AppendUint32(nil, 1), "/bin/agenthub-node"...), "no end"},
		"padding not zero":   {append(binary.LittleEndian.AppendUint32(nil, 1), "/bin/x\x00zz\x00"...), "no padding"},
		"fewer arguments":    {lies(40), "ended after"},
	} {
		if argv, _, err := parseProcargs2(tc.raw); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: read %q, err %v; want an error saying %q", name, argv, err, tc.want)
		}
	}
}
