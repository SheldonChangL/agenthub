//go:build darwin

package identity

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// localMachineName asks macOS what this machine is called, rather than what the
// network currently calls it.
//
// ComputerName is the name the owner sees in System Settings; LocalHostName is
// its Bonjour form. Either is this machine's own, unlike gethostname() with no
// HostName set, which answers from DHCP and DNS.
//
// scutil rather than a preferences file: the file is a plist whose layout is
// not a contract, and this runs once at startup with a fallback either way.
func localMachineName() string {
	for _, key := range []string{"ComputerName", "LocalHostName"} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", key).Output()
		cancel()
		if err != nil {
			continue
		}
		if name := strings.TrimSpace(string(out)); name != "" {
			return name
		}
	}
	return ""
}
