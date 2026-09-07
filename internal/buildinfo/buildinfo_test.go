package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

func stamped(revision string, modified bool) *debug.BuildInfo {
	settings := []debug.BuildSetting{{Key: "vcs.revision", Value: revision}}
	if modified {
		settings = append(settings, debug.BuildSetting{Key: "vcs.modified", Value: "true"})
	} else {
		settings = append(settings, debug.BuildSetting{Key: "vcs.modified", Value: "false"})
	}
	return &debug.BuildInfo{Settings: settings}
}

// The point of this package is that a report from someone else can be tied to
// code. Every case has to name something a maintainer can look up, or say
// plainly that it cannot.
func TestEveryBuildSaysWhichCodeItIs(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	for name, c := range map[string]struct {
		release  string
		info     *debug.BuildInfo
		ok       bool
		want     string
		contains []string
	}{
		"a release build says its tag": {
			release: "v0.2.0", info: stamped(revision, false), ok: true,
			want: "v0.2.0",
		},
		"a build from source says its revision": {
			release: "", info: stamped(revision, false), ok: true,
			contains: []string{"unreleased", "0123456789ab"},
		},
		"a dirty tree says so": {
			release: "", info: stamped(revision, true), ok: true,
			contains: []string{"modified source", "0123456789ab"},
		},
		"a tagged build of a dirty tree is not that tag alone": {
			release: "v0.2.0", info: stamped(revision, true), ok: true,
			contains: []string{"v0.2.0", "modified source", "0123456789ab"},
		},
		// -X takes whatever it is given, and a blank version reads as a bug in
		// the program rather than in the build that produced it.
		"a release of nothing but spaces is not a release": {
			release: "   ", info: stamped(revision, false), ok: true,
			contains: []string{"unreleased", "0123456789ab"},
		},
		"an unstamped build admits it": {
			release: "", info: nil, ok: false,
			want: "unreleased (no revision recorded)",
		},
		"an unstamped build with info but no vcs settings admits it": {
			release: "", info: &debug.BuildInfo{}, ok: true,
			want: "unreleased (no revision recorded)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := describe(c.release, c.info, c.ok)
			if c.want != "" && got != c.want {
				t.Errorf("describe() = %q, want %q", got, c.want)
			}
			for _, want := range c.contains {
				if !strings.Contains(got, want) {
					t.Errorf("describe() = %q, missing %q", got, want)
				}
			}
			if got == "" {
				t.Error("describe() said nothing at all")
			}
		})
	}
}

// A tagged build of a modified tree must not report the tag alone: someone
// reading it would fetch that tag and find code that was never built.
func TestATagOnADirtyTreeIsNotJustTheTag(t *testing.T) {
	got := describe("v1.0.0", stamped("abcdef0123456789", true), true)
	if got == "v1.0.0" {
		t.Fatal("a modified tree reported its tag as though it were that tag")
	}
	if !strings.Contains(got, "v1.0.0") {
		t.Errorf("the tag is still worth knowing: %q", got)
	}
}

// The revision has to be long enough to be unambiguous in a real repository.
func TestTheRevisionIsLongEnoughToLookUp(t *testing.T) {
	got := describe("", stamped("0123456789abcdef0123456789abcdef01234567", false), true)
	if !strings.Contains(got, "0123456789ab") {
		t.Errorf("describe() = %q, want at least 12 characters of the revision", got)
	}
	if strings.Contains(got, "0123456789abcdef0123456789abcdef01234567") {
		t.Errorf("describe() = %q; the full revision is noise in a log line", got)
	}
}

// Line is what goes in a log or an answer to --version, so it must carry the
// platform: a report that says darwin/arm64 is one fewer question.
func TestTheLineNamesTheProgramAndThePlatform(t *testing.T) {
	line := Line("agenthub-node")
	for _, want := range []string{"agenthub-node", "go1."} {
		if !strings.Contains(line, want) {
			t.Errorf("Line() = %q, missing %q", line, want)
		}
	}
	if strings.Contains(line, "  ") || strings.HasSuffix(line, " )") {
		t.Errorf("Line() = %q; it has empty space where a field should be", line)
	}
}
