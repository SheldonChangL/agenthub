// Package buildinfo answers one question: which code is this binary?
//
// A report from someone else — a log line, a bug, a screenshot of the desktop
// app — is only actionable if it can be tied to a commit. Without that, the
// first exchange is always "which build?" and the answer is usually "the one I
// downloaded", which names nothing.
package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// release is the tag a release build was cut from, injected with
//
//	-ldflags "-X agenthub.local/agenthub/internal/buildinfo.release=v0.2.0"
//
// It is empty for every other build, and that is the common case: a colleague
// building from source, a CI job, a developer's laptop. Those are not
// versionless — the toolchain records the revision they came from, and that is
// what the rest of this package reads.
var release string

// Version describes this binary as precisely as the build allowed.
//
// A release build says its tag. Any other build says the revision the go
// command stamped, which is exact and is what a maintainer actually needs. A
// binary built from a dirty tree says so, because a revision that does not
// describe the source is worse than no revision: it points at code that was
// never built.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	return describe(release, info, ok)
}

func describe(release string, info *debug.BuildInfo, ok bool) string {
	revision, modified, stamped := vcs(info, ok)
	switch {
	case release != "" && stamped && modified:
		// A tagged build of a dirty tree is not that tag.
		return release + " (modified source, revision " + short(revision) + ")"
	case release != "":
		return release
	case !stamped:
		// `go run`, and any build the toolchain could not stamp.
		return "unreleased (no revision recorded)"
	case modified:
		return "unreleased (modified source, revision " + short(revision) + ")"
	default:
		return "unreleased (revision " + short(revision) + ")"
	}
}

func vcs(info *debug.BuildInfo, ok bool) (revision string, modified bool, stamped bool) {
	if !ok || info == nil {
		return "", false, false
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return revision, modified, revision != ""
}

// short is the prefix a person compares against a commit list. Not truncated so
// far that two commits could share it: twelve characters is what git itself
// grows to for a repository of any size.
func short(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

// Line is what a program prints when asked its version, or logs at startup.
func Line(program string) string {
	return fmt.Sprintf("%s %s (%s)", program, Version(), strings.TrimSpace(platform()))
}
