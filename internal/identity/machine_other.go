//go:build !darwin

package identity

// localMachineName has no answer beyond the hostname on these platforms.
//
// On Linux and Windows gethostname() returns a name the machine holds itself
// rather than one a DHCP lease supplied, so the macOS problem this exists for
// does not arise. If one turns up, this is where it is answered.
func localMachineName() string { return "" }
