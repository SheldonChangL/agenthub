package identity

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/label"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

// LoadOrCreate returns this node's identity, creating it on first run.
//
// A name nobody picked follows the machine. That is the whole point rather than
// a convenience: a node created before this code existed took its name from
// os.Hostname(), which on macOS answers from DHCP and DNS, and correcting only
// newly created nodes would leave every node that actually has the problem
// announcing a stranger's name forever. So a stored name that nobody chose is
// re-read from the machine on every start.
//
// A name someone did pick is left alone, including across a machine rename.
//
// Trust is keyed on the node id, so either change is safe: a peer keeps the
// name it recorded at pairing time until it pairs again.
//
// Passing an empty name explicitly — given, with chosen empty — hands the name
// back to the machine. It is the only way out of a pinned name, and without it
// the flag is a door that locks behind you.
//
// A chosen name is resolved to its announceable form by label.Announceable,
// the same function the store applies on the way in.
func LoadOrCreate(ctx context.Context, store *registry.Registry, chosen string, given bool) (model.NodeIdentity, error) {
	chosen = strings.TrimSpace(chosen)
	// An empty name that was actually passed means the opposite of one that was
	// not: hand the name back to the machine. Without the distinction, pinning
	// is a one-way door out of which the only exit is editing the database.
	release := given && chosen == ""
	if chosen != "" {
		// Resolved here as well as in the store, so the comparison below is
		// against the form that would actually be written. The same function
		// the store calls, not a second rule.
		announceable, err := label.Announceable(chosen)
		if err != nil {
			return model.NodeIdentity{}, err
		}
		chosen = announceable
	}

	identity, err := store.GetNodeIdentity(ctx)
	if err == nil {
		name, pick := chosen, true
		switch {
		case chosen != "":
		case identity.NameIsChosen && !release:
			// Somebody picked this. Not ours to revise.
			return identity, nil
		default:
			name, pick = MachineName(), false
		}
		if name == identity.DisplayName && pick == identity.NameIsChosen {
			return identity, nil
		}
		// SetNodeDisplayName rather than SaveNodeIdentity: the latter refuses
		// to overwrite, deliberately, because the id must not change.
		if err := store.SetNodeDisplayName(ctx, name, pick); err != nil {
			return model.NodeIdentity{}, err
		}
		return store.GetNodeIdentity(ctx)
	}
	if !errors.Is(err, registry.ErrNotFound) {
		return model.NodeIdentity{}, err
	}

	nodeID, err := id.New("node_")
	if err != nil {
		return model.NodeIdentity{}, err
	}
	name, pick := chosen, true
	if name == "" {
		name, pick = MachineName(), false
	}
	identity = model.NodeIdentity{
		ID:           nodeID,
		DisplayName:  name,
		Platform:     runtime.GOOS + "/" + runtime.GOARCH,
		CreatedAt:    time.Now().UTC(),
		NameIsChosen: pick,
	}
	if err := store.SaveNodeIdentity(ctx, identity); err != nil {
		return model.NodeIdentity{}, err
	}
	return store.GetNodeIdentity(ctx)
}

// machineNameLookup is what MachineName asks first. A variable so the
// preference between the machine's own name and the network's can be tested on
// any platform, including the CI runners, where the real lookup answers "".
var machineNameLookup = localMachineName

// MachineName is what this machine calls itself, for a node whose name nobody
// has picked.
//
// Not os.Hostname() alone. On macOS with no HostName set — the default —
// gethostname() answers with whatever DHCP and DNS say this address is called,
// so a machine can end up announcing a name that belongs to whoever held the
// address before it. Measured on one: ComputerName was "sheldon.chang mac"
// while os.Hostname() returned "J-FrankieChang.jet-opto.com.tw", a previous
// occupant of that DNS record. The node then broadcast that name to everyone on
// the segment, which is both wrong and somebody else's.
//
// So the machine's own name is asked for first, and the network-derived one is
// the fallback rather than the source. Every candidate is put through the
// announcement's own rule, because a name this function returns is a name that
// will be transmitted: a 30-character ComputerName with an emoji in it is
// ordinary, and one that PrintableLabel refuses must fall through to the next
// source rather than becoming a node with no announced name.
func MachineName() string {
	for _, candidate := range []string{machineNameLookup(), hostname()} {
		if name := shortenUntilAnnounceable(strings.TrimSpace(candidate)); name != "" {
			return name
		}
	}
	return "agenthub-node"
}

// shortenUntilAnnounceable returns the longest prefix of a name that an
// announcement will carry, or "" if no prefix will do.
//
// Cutting once to the bound is not enough. Normalisation can lengthen what it
// is given — NFKC expands ㍿ to four characters and ½ to three — so a 64-byte
// cut can come back over the bound and be refused, and the name is then
// discarded whole. Measured: a ComputerName of thirty ㍿ made MachineName()
// fall through to the hostname, which on this machine is the DNS name this
// change exists to stop announcing. So the cut shrinks until something fits.
func shortenUntilAnnounceable(candidate string) string {
	for cut := len(candidate); cut > 0; {
		if name := label.Printable(candidate[:cut]); name != "" {
			return name
		}
		if cut > label.MaxLength {
			// Straight to the bound on the first pass rather than one byte at
			// a time: a 4KB name would otherwise be normalised thousands of
			// times to reach the same place.
			cut = label.MaxLength
			continue
		}
		// One byte at a time from here, with no special case for a cut inside
		// a rune. Printable refuses the invalid fragment, the next step backs
		// off another byte, and within three it is on a boundary again. A
		// rune-aware backoff was here and bought nothing: removing it changed
		// no result, only a handful of iterations.
		cut--
	}
	return ""
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}
