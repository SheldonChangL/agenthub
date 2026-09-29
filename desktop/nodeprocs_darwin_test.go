//go:build darwin

package main

import (
	"encoding/binary"
	"slices"
	"strings"
	"testing"
)

// procargs2 builds what kern.procargs2 answers: argc, the executable path
// padded with NULs to a multiple of eight, the arguments, the environment and
// its terminating empty entry, then the kernel's own strings.
func procargs2(path string, argv, env []string) []byte {
	raw := binary.LittleEndian.AppendUint32(nil, uint32(len(argv))) // #nosec G115 -- a test's handful of arguments
	raw = append(raw, path...)
	raw = append(raw, 0)
	for (len(raw)-4)%8 != 0 {
		raw = append(raw, 0)
	}
	for _, word := range append(append(slices.Clone(argv), env...), "") {
		raw = append(raw, word...)
		raw = append(raw, 0)
	}
	return append(raw, "\x00\x00ptr_munge=\x00main_stack=\x00"...)
}

func TestParseProcargs2(t *testing.T) {
	env := []string{"HOME=/Users/me", "XDG_CONFIG_HOME=/elsewhere"}
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
			gotArgv, gotEnv, envErr, err := parseProcargs2(procargs2(path, argv, env))
			if err != nil || envErr != nil {
				t.Fatalf("path %q, argv %q: %v, %v", path, argv, err, envErr)
			}
			if !slices.Equal(gotArgv, argv) {
				t.Errorf("path %q: argv = %q, want %q", path, gotArgv, argv)
			}
			if !slices.Equal(gotEnv, env) {
				t.Errorf("path %q, argv %q: env = %q, want %q", path, argv, gotEnv, env)
			}
		}
	}
}

func TestParseProcargs2RefusesWhatItCannotRead(t *testing.T) {
	good := procargs2("/bin/agenthub-node", []string{"agenthub-node", "--db", "/p/a.db"}, []string{"HOME=/Users/me"})
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
		if argv, _, _, err := parseProcargs2(tc.raw); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: read %q, err %v; want an error saying %q", name, argv, err, tc.want)
		}
	}
	// A command line that reads with an environment that does not is still a
	// command line; the environment's failure is its own.
	truncated := good[:len(good)-len("\x00\x00ptr_munge=\x00main_stack=\x00")-len("HOME=/Users/me\x00")-1+4]
	argv, _, envErr, err := parseProcargs2(truncated)
	if err != nil || envErr == nil || len(argv) != 3 {
		t.Errorf("truncated environment: argv %q, envErr %v, err %v; want the argv and an environment error", argv, envErr, err)
	}
}
