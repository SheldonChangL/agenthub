//go:build windows

package nodeconfig

import "os"

func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
