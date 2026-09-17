package main

import runtimepkg "runtime"

// release is the version this build of the window reports.
//
// The release workflow stamps it with `-X main.release=<tag>`; nothing else
// sets it. An unstamped build says "unreleased" rather than inventing a number,
// because the point of showing a version at all is that a bug report maps to a
// build — and "1.0.0", which is what wails writes into Info.plist when nobody
// says otherwise, maps to every build ever made.
var release = "unreleased"

// VersionInfo is what the window shows in its title bar. The two platform
// fields come from this binary rather than from the node: they describe the app
// the person is looking at, and they have to be answerable while the node is
// unreachable.
type VersionInfo struct {
	Release string `json:"release"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
}

// Version reports this build's identity.
func (a *App) Version() VersionInfo {
	return VersionInfo{Release: release, GOOS: runtimepkg.GOOS, GOARCH: runtimepkg.GOARCH}
}
