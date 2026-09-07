package buildinfo

import "runtime"

// platform names what this binary runs on. A report that says darwin/arm64 is
// one fewer question to ask.
func platform() string {
	return runtime.GOOS + "/" + runtime.GOARCH + " " + runtime.Version()
}
