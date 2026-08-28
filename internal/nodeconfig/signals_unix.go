//go:build !windows

package nodeconfig

import (
	"os"
	"syscall"
)

func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
