# ADR-005: a node listens on the addresses the owner ticks, never on all of them

Status: accepted 2026-09-23. Implemented in three stacked PRs: node (A), peers (B), desktop (C).

## Context

The peer listener takes exactly one address (`peer-listen`), and a trusted node records exactly
one address for the other side. A machine on Ethernet and Wi-Fi at once is therefore reachable on
one of them, and when that path goes away (the cable is unplugged, the laptop moves) its peers
stop reaching it although another path exists.

The obvious fix, binding `0.0.0.0`, is refused on purpose (`internal/nodeconfig/address.go`): it
binds every interface the machine has or later gains, including public ones. That rule stays.

Requirement from the owner: both interfaces usable; the owner decides in the window which
addresses are open and can close any of them; nothing is opened to the LAN until the owner picks.

## Decision

### 1. One setting, two spellings
- `nodeconfig.Settings` gains `PeerListens []string` (json `peerListens`). `PeerListen` stays and
  is always `PeerListens[0]`. `Partial` gains `PeerListens *[]string`. `SettingNames` stays five
  names; `peerListen` is the source key for both fields, and the flag is still `peer-listen`.
- `nodeconfig.NormalizePeerListen(Partial) (Partial, error)` runs on given and remembered values
  before `Resolve`; `Apply` and `Overlay` always move the two fields together:
  - scalar only: the list becomes `[scalar]`. **Writing the scalar replaces the whole list.** This
    is the safety core: an older window or CLI that chooses "this machine only" really closes every
    LAN address.
  - list only: the scalar becomes `list[0]` (an empty list means `DefaultPeerListen`).
  - both: `list[0]` must equal the scalar, or the request is refused.
- Every entry passes `ValidatePeerListen` (every spelling of the unspecified address, zones, names
  and non-private addresses not covered by treat-as-private are refused). At most 4 entries,
  unique after unmapping, one shared port; with `allowLan` off only loopback; loopback and LAN
  entries are not mixed. Loosening these later is compatible, tightening them is not.
- Storage (`internal/registry/settings.go`): a new key `peerListens` holds a JSON array, written in
  the same transaction as the scalar `peerListen`. On read, if `list[0]` differs from the scalar,
  an older node wrote the scalar during a downgrade: the list is dropped and `[scalar]` is used.
- Flags: `agenthub-node -peer-listen` is repeatable and replaces the whole list when given, like
  `-treat-as-private`. `ah service install --peer-listen` is repeatable. `ah settings set` sends
  `peerListen` for one value (works on older nodes) and `peerListens` for several; an older node's
  refusal is reported as "this node is too old for more than one address".
- Withdrawal (`WithdrawPeerListen`, the API's LAN withdrawal, `applyStartupSettings`): with
  `allowLan` off and no address given, non-loopback entries are dropped; an empty result is
  `[DefaultPeerListen]`; the withdrawal stands when the list equals `[DefaultPeerListen]`.

### 2. Binding
- A `ListenerSet` binds each entry and classifies failures with `ClassifyListenFailure`. If one
  binds there is no loopback fallback; only when all fail does the existing fallback order run.
- One `http.Server` serves every listener; the 100-connection cap is shared across them, so more
  addresses do not mean more connections.
- Every 30 s the set retries entries that are configured but unbound (address gone, port held,
  unusable): launchd starts the node at login before Wi-Fi has an address. It never adds an address
  that is not configured and does not watch routing sockets. A bound address that disappears is
  left alone.
- The pairing endpoint's addresses, the pairing state's `peerAddresses` and the announcer's
  preferred address (first bound IPv4) are read live from the set.

### 3. API
- Node settings: `settings` and `saved` both carry `peerListens`; a new `peerListeners` lists
  `{address, state: bound|failed|pending, reason?, detail?, message?}`.
- `peerListenProblem` appears only when every entry failed and the node fell back to loopback, so
  an older window's "only this machine" sentence stays true. The running `peerListen` is the first
  bound entry or the fallback. `restartRequired` compares the configured list with the saved one.
- The owner API refuses unknown fields, so a window detects support by `peerListens` in the reply
  and otherwise stays single-address.

### 4. Peers know several addresses (PR B)
- `trusted_nodes.alternate_addresses` (JSON, default `[]`) beside `address`, which stays the
  preferred: the last address that worked, so a downgraded node reads the last good one.
- `SetNodeAddress(x)` makes `x` preferred and moves the old preferred into the alternates; `""`
  clears both. `SetNodeAddresses` replaces the set (first is preferred), each through the delivery
  policy, at most 4. A non-preferred address that works is promoted.
- `pair.request` and `pair.approve` carry an optional `addresses` beside `address`: only bound,
  policy-passing, non-loopback addresses from the listener set, never an interface scan. The
  receiver filters each one and keeps at most 4. Older nodes ignore the field.
- Dialing tries the preferred address, then the alternates in order, 3 s per connection attempt
  inside the existing 10 s budget. It moves on after a connection error, a timeout, a pin mismatch
  or a challenge answered by the wrong node; it does not move on after an HTTP error once the
  challenge passed. Sequential, not parallel.
- `PUT /v1/nodes/{id}/addresses`; `ah nodes address <id> <a> [<b>...]`; `ah nodes` shows alternates.
- mDNS still broadcasts from the preferred address only: `dialGroup` requires the source address,
  and several addresses under one id are flagged contested. Multi-interface broadcast and
  heartbeat-carried addresses are not part of this decision.

### 5. The window (PR C)
- Settings → Node settings shows a checkbox list: every private IPv4 this machine has, other
  addresses under their own heading, saved entries this machine no longer has still ticked and
  marked so. Nothing is ticked that is not saved; ticking an address does not tick Allow LAN and
  ticking Allow LAN ticks no address.
- Each row says Open / Opens after restart / Closes after restart / Not open with the reason.
- The four node-settings rules in `desktop/docs/ui-contract.md` §7.8 apply to the list, compared
  as sets.
- Pairing step 1 lists every open address with a copy button, and offers "Open on all" when there
  are two or more private addresses and nothing is reachable.
- Against an older node the window stays single-address.

## Consequences
- Five readers of one setting (old and new nodes, old and new windows, the CLI) plus a pinned
  service unit. The two rules above (a scalar write replaces the list; the list is believed only
  when its first entry matches the scalar) are what keep "the window says closed" true.
- A dead alternate costs up to 3 s per attempt in the sequential publish loop.
- A machine reachable only on the second network does not see the broadcast and is paired by
  typing the address.
