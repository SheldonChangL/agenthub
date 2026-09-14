package discovery

import (
	"testing"
	"time"
)

// TestTheTrustCacheTTLStaysWithinItsClaimedBound pins the constant itself, not
// the behaviour expressed in terms of it.
//
// Unpairing does not invalidate the cached "paired" answers explicitly, so this
// TTL is the entire guarantee that a node the owner has just unpaired stops
// being excluded — and every other test of that property advances its clock by
// multiples of trustCacheTTL, which makes them pass for any value it is given.
// Raising the constant would quietly widen the window in which a revoked node
// is still treated as paired, with nothing turning red. This is what turns red.
//
// The figure is the one trustCacheTTL's own comment claims and the one the PR
// argues is imperceptible; changing the constant means changing that argument,
// so it means changing this line too.
func TestTheTrustCacheTTLStaysWithinItsClaimedBound(t *testing.T) {
	if trustCacheTTL > 5*time.Second {
		t.Fatalf("trustCacheTTL = %v; a revoked node stays excluded for that long, "+
			"and nothing but this bound ever ends the exclusion", trustCacheTTL)
	}
	if trustCacheTTL < time.Second {
		t.Fatalf("trustCacheTTL = %v; below a second the cache stops absorbing "+
			"the flood it exists to absorb", trustCacheTTL)
	}
}
