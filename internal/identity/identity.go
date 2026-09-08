package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

// MaxDisplayName is what this node may call itself.
//
// The announcement's bound, not the trust store's larger one for peer names.
// This name is transmitted: a longer one is stored, shown in this node's own UI
// as the string the network sees, and then dropped from the announcement with
// nowhere to report it — so the owner is told their machine announces a name
// that is not on the wire at all.
const MaxDisplayName = model.MaxLabelLength

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
func LoadOrCreate(ctx context.Context, store *registry.Registry, chosen string) (model.NodeIdentity, error) {
	chosen = strings.TrimSpace(chosen)
	if chosen != "" {
		if err := checkAnnounceable(chosen); err != nil {
			return model.NodeIdentity{}, err
		}
	}

	identity, err := store.GetNodeIdentity(ctx)
	if err == nil {
		name, pick := chosen, true
		switch {
		case chosen != "":
		case identity.NameIsChosen:
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

// checkAnnounceable refuses a chosen name the announcement would not carry,
// while the person who typed it is still there to be told.
//
// The alternative is to accept it, store it, print it at startup as the
// announced name, and have the mDNS TXT record silently omit it. The owner then
// looks for their machine on another screen and it has no name — a failure with
// no error anywhere and nothing pointing at the name they chose.
func checkAnnounceable(name string) error {
	clean := model.PrintableLabel(name)
	if clean == "" {
		return fmt.Errorf(
			"display name %q cannot be announced: it is %d bytes, and the most an announcement "+
				"carries is %d, made of characters that render",
			name, len(name), MaxDisplayName)
	}
	if clean != name {
		return fmt.Errorf(
			"display name %q would be announced as %q; pass that instead, so what you see here "+
				"is what other machines see", name, clean)
	}
	return nil
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
		if name := model.PrintableLabel(strings.TrimSpace(candidate)); name != "" {
			return name
		}
		// Too long to announce is the common case, and truncating is better
		// than discarding: "Sheldon 的 MacBook Pro …" identifies the machine,
		// and "agenthub-node" does not.
		if name := model.PrintableLabel(truncate(strings.TrimSpace(candidate))); name != "" {
			return name
		}
	}
	return "agenthub-node"
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

// truncate cuts a name to the announcement's bound on a rune boundary, so a
// multi-byte name is not cut into invalid UTF-8.
//
// The bound is checked again by the caller, because normalisation can lengthen
// a string: this cut is what makes a long name a candidate, not what proves it
// acceptable.
func truncate(name string) string {
	if len(name) <= MaxDisplayName {
		return name
	}
	cut := MaxDisplayName
	// Back off the trailing bytes of a rune the cut landed inside. A UTF-8
	// continuation byte is 10xxxxxx; the byte that starts a rune is not.
	for cut > 0 && name[cut]&0xC0 == 0x80 {
		cut--
	}
	return name[:cut]
}
