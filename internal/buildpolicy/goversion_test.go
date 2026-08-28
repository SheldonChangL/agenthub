// Package buildpolicy holds tests that pin repository-wide build guarantees.
// It carries no production code: the guarantee lives in the modules' `go`
// directives, and this test fails if one is lowered.
package buildpolicy

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// minimumGoVersion is the oldest Go release these modules may be built with.
// The floor is a security control, not a language-feature choice: Go 1.25.0
// ships a standard library with reachable vulnerabilities in crypto/tls,
// crypto/x509 and net/http, and every binary here links that standard library.
// Raising the `go` directive makes the go command refuse an unpatched
// toolchain instead of building quietly against it.
var minimumGoVersion = goVersion{1, 27, 0}

func TestModulesRequireAPatchedGoToolchain(t *testing.T) {
	for _, path := range []string{"../../go.mod", "../../desktop/go.mod"} {
		declared, err := goDirective(path)
		if err != nil {
			t.Fatalf("goDirective(%q) error = %v", path, err)
		}
		parsed, err := parseGoVersion(declared)
		if err != nil {
			t.Fatalf("parseGoVersion(%q) from %q error = %v", declared, path, err)
		}
		if less(parsed, minimumGoVersion) {
			t.Errorf("%s declares go %s; want at least go %s", path, declared, minimumGoVersion)
		}
	}
}

type goVersion [3]int

func (v goVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

func less(a, b goVersion) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func goDirective(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "go" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("%s has no go directive", path)
}

func parseGoVersion(version string) (goVersion, error) {
	var parsed goVersion
	fields := strings.Split(version, ".")
	if len(fields) < 2 || len(fields) > 3 {
		return parsed, fmt.Errorf("malformed go version %q", version)
	}
	for i, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			return parsed, fmt.Errorf("malformed go version %q: %w", version, err)
		}
		parsed[i] = value
	}
	return parsed, nil
}
