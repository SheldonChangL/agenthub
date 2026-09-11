package nodeconfig

import "strings"

// StringList collects a repeatable flag, such as -treat-as-private. It is a
// flag.Value shared by the node and by ah, so both spell the same flag the
// same way.
type StringList []string

// String renders the collected values, comma-separated.
func (s *StringList) String() string { return strings.Join(*s, ",") }

// Set appends one value.
func (s *StringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}
