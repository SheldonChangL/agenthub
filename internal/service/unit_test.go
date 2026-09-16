package service

import (
	"slices"
	"testing"
)

// Round trips, because the two halves have to agree and only one of them is
// ever looked at by a person. A renderer that changes shape and a reader that
// does not would hand the owner a blank database path, which is the failure
// this reader exists to prevent.
func TestInstalledCommandRoundTripsWhatWasWritten(t *testing.T) {
	// A path with a space in it, because that is the argument systemd quoting
	// exists for and the one a naive split gets wrong.
	database := "/home/a user/Library/Application Support/agenthub/agenthub.db"
	binary := "/opt/agent hub/bin/agenthub-node"
	config := Config{
		NodeBinary: binary,
		Args:       []string{"--db", database},
		LogPath:    "/tmp/node.log",
	}
	want := []string{"--db", database}

	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			home := t.TempDir()
			manager := Manager{GOOS: platform, Home: home, UID: "501"}
			path, err := manager.UnitPath()
			if err != nil {
				t.Fatal(err)
			}
			var content []byte
			switch platform {
			case "darwin":
				content, err = LaunchdPlist(config)
				if err != nil {
					t.Fatal(err)
				}
			case "linux":
				content = SystemdUnit(config)
			}
			writeUnitForTest(t, path, content)

			program, args := manager.InstalledCommand()
			if program != binary {
				t.Errorf("program = %q, want %q", program, binary)
			}
			if !slices.Equal(args, want) {
				t.Errorf("arguments = %q, want %q", args, want)
			}
			if got := ArgumentValue(args, "db"); got != database {
				t.Errorf("--db = %q, want %q", got, database)
			}
		})
	}
}

// The state that started all this: a unit registered with no --db at all. The
// reader has to be able to say "there is no database path here" rather than
// fail, because that is a different thing to tell the owner than "it is this
// one".
func TestInstalledCommandOnAUnitWithNoDatabase(t *testing.T) {
	home := t.TempDir()
	manager := Manager{GOOS: "darwin", Home: home, UID: "501"}
	path, err := manager.UnitPath()
	if err != nil {
		t.Fatal(err)
	}
	content, err := LaunchdPlist(Config{NodeBinary: "/bin/agenthub-node", LogPath: "/tmp/node.log"})
	if err != nil {
		t.Fatal(err)
	}
	writeUnitForTest(t, path, content)

	program, args := manager.InstalledCommand()
	if program != "/bin/agenthub-node" {
		t.Errorf("program = %q", program)
	}
	if len(args) != 0 {
		t.Errorf("arguments = %q, want none", args)
	}
	if got := ArgumentValue(args, "db"); got != "" {
		t.Errorf("--db = %q, want empty", got)
	}
}

// No unit, no answer, no error. The caller falls back to what it did before.
func TestInstalledCommandWithoutAUnit(t *testing.T) {
	manager := Manager{GOOS: "darwin", Home: t.TempDir(), UID: "501"}
	if program, args := manager.InstalledCommand(); program != "" || args != nil {
		t.Errorf("InstalledCommand() = %q, %q on a machine with no unit", program, args)
	}
}

// A legacy unit: the five settings burned in as flags, which is how a service
// installed before the node remembered them still overrides the settings page.
// The panel can only offer to re-register it cleanly if it can see them.
func TestInstalledCommandSeesFlagsAUnitStillCarries(t *testing.T) {
	home := t.TempDir()
	manager := Manager{GOOS: "linux", Home: home, UID: "501"}
	path, err := manager.UnitPath()
	if err != nil {
		t.Fatal(err)
	}
	writeUnitForTest(t, path, SystemdUnit(Config{
		NodeBinary: "/bin/agenthub-node",
		Args: []string{"--db", "/data/agenthub.db", "--peer-listen", "122.122.122.1:7463",
			"--allow-lan", "--discover", "--treat-as-private", "122.122.0.0/16"},
		LogPath: "/tmp/node.log",
	}))

	_, args := manager.InstalledCommand()
	if got := ArgumentValue(args, "peer-listen"); got != "122.122.122.1:7463" {
		t.Errorf("--peer-listen = %q, want the address burned into the unit", got)
	}
	if got := ArgumentValue(args, "db"); got != "/data/agenthub.db" {
		t.Errorf("--db = %q", got)
	}
}

// Both spellings, because a unit hand-edited by an owner is still a unit this
// has to read.
func TestArgumentValueAcceptsBothSpellings(t *testing.T) {
	for _, args := range [][]string{
		{"--db", "/data/x.db"},
		{"--db=/data/x.db"},
		{"-db", "/data/x.db"},
		{"-db=/data/x.db"},
	} {
		if got := ArgumentValue(args, "db"); got != "/data/x.db" {
			t.Errorf("ArgumentValue(%q) = %q", args, got)
		}
	}
	// A flag at the end with nothing after it is absent, not a panic.
	if got := ArgumentValue([]string{"--db"}, "db"); got != "" {
		t.Errorf("a trailing flag yielded %q", got)
	}
}

func writeUnitForTest(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := writeUnit(path, content); err != nil {
		t.Fatal(err)
	}
}
