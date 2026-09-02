# Architecture
## Topology

Every installation runs the same components. There is no central server: nodes
deliver to each other directly over TLS. "Broker" survives as the name of the
envelope format in `broker-protocol.schema.json` and as a logical role each node
performs for itself; no such host is deployed, and none is planned.

### Inside one node

```mermaid
flowchart TB
    subgraph Agent["Agent - has its own Read/Bash tools"]
        Claude[Claude Code]
        Codex[Codex]
    end

    subgraph Client["Owner surfaces"]
        Desktop[Desktop app]
        CLI[ah CLI]
    end

    MCP["agenthub-mcp - bound to one session<br/>agent_list · agent_status<br/>agent_inbox · agent_send<br/>no file or shell tools<br/>Step 7 - not built"]

    API["Loopback HTTP API<br/>127.0.0.1:7462"]

    subgraph Node["agenthub-node"]
        Registry[("SQLite<br/>sessions · audience · trust<br/>inbox · outbox")]
        Key[["node.key - Ed25519<br/>DPAPI-protected on Windows"]]
        Scan["Filesystem discovery<br/>~/.claude · ~/.codex - read only"]
    end

    Peer["Peer listener<br/>:7463 TLS 1.3<br/>the only surface that leaves the host"]

    Claude -- stdio --> MCP
    Codex -- stdio --> MCP
    MCP --> API
    Desktop --> API
    CLI --> API
    API --> Node
    Scan --> Registry
    Node --> Peer
```

The agent starts `agenthub-mcp` as its own child process, which is why that
process cannot know which session called it unless it is told at startup — hence
the `--as` binding in #50. It reaches the registry through the same loopback API
the desktop app and CLI use, never through SQLite directly, so `agenthub-node`
remains the only writer and the MCP surface cannot bypass audience filtering.

### Between two nodes

```mermaid
flowchart LR
    subgraph A["Node A"]
        NodeA[agenthub-node]
        OutA{{"allowOutbound<br/>Step 7 - issue 53<br/>default off"}}
    end

    subgraph B["Node B"]
        InB{{"acceptMessages<br/>implemented<br/>per session"}}
        InboxB[("inbox")]
        WakeB{{"autoWake<br/>Step 8 - issue 59<br/>default off"}}
        AgentB[Agent on B]
    end

    NodeA -- "heartbeat every 15s<br/>only audience-authorised sessions" --> InB
    NodeA --> OutA
    OutA -- "agent.message over TLS<br/>certificate pinned to the key<br/>recorded at pairing" --> InB
    InB --> InboxB
    InboxB -- "Step 7: a person asks the agent to look" --> AgentB
    InboxB --> WakeB
    WakeB -- "Step 8: arrives on its own" --> AgentB
```

Three gates sit on that path and are independent of each other, because willing
to receive is not willing to send, and neither is willing to act unattended:

| Gate | Where | State |
|---|---|---|
| `acceptMessages` | on the recipient | implemented |
| `allowOutbound` | on the sender | Step 7, #53, default off |
| `autoWake` | on the recipient | Step 8, #59, default off |

TLS is pinned to the public key recorded when the two nodes paired, verified
through `VerifyConnection` so a resumed TLS 1.3 handshake is checked too. A
middlebox that substitutes its own certificate cannot complete the connection.

### How a message reaches an agent

Delivery to the inbox works today and has been exercised between two machines.
What does not exist yet is anything an agent can call, and anything that hands a
message to an agent without a person asking.

| Leg | Mechanism | State |
|---|---|---|
| Agent sends | `agent_send` over MCP | Step 7, #53 |
| Node to node | signed envelope over pinned TLS | implemented |
| Into the inbox | `acceptMessages`, deduplicated, bounded at 500 | implemented |
| Agent reads on request | `agent_inbox` over MCP | Step 7, #52 |
| Arrives unprompted, Claude Code | MCP channel, `claude/channel` capability | Step 8, #57 |
| Arrives unprompted, Codex | `thread/resume` then `turn/start` on the Codex App Server | Step 8, #58 |

The two wake-up paths are asymmetric, and the difference is worth knowing before
relying on either. The Claude Code path runs through `agenthub-mcp`, which the
agent itself launched, so nothing can be pushed unless that session is already
running. The Codex path has `agenthub-node` connect outwards to the Codex App
Server, so it does not depend on an agent having started anything first.

For Claude Code, a channel is the only mechanism that works: MCP
`sampling/createMessage` and `notifications/resources/updated` are not
implemented by Claude Code, no hook fires on a timer, and hooks cannot raise a
turn on their own.

Neither path injects text into a provider's files or process. `#16`'s boundary
holds: the Codex path calls Codex's own API, and the Claude Code path uses a
documented MCP capability.

## Current implementation boundary

| Boundary | Current state |
|---|---|
| Provider session -> node | Filesystem discovery is enabled; Codex App Server parsing exists but is not wired into the daemon |
| Owner -> node | `ah`, desktop app, and loopback HTTP API are implemented |
| Node -> node | Implemented and exercised between two hosts: pinned TLS, recipient-bound signed envelopes, a persisted heartbeat sequence, presence with expiry, and message routing with acks. Bound to loopback unless `-allow-lan` names a private address |
| MCP client -> node | Tool contracts are drafted in `mcp-tools.json`; no server exists. Step 7, #56 |
| Node -> agent | Messages are queued in the inbox and nothing hands them to an agent. Step 8, #60 |
| Pairing | Manual: five arguments including a base64 public key. Discovery fills addresses for already-paired nodes only. Step 9, #63 |
| Distribution | CI cross-compiles for six platforms and discards the output. No release, no installer, no version number. Step 10, #67 |

## Platform boundary

Windows, macOS, and Linux share the same registry, protocol, API, CLI, filesystem metadata parsers, and privacy rules. Process enumeration is isolated behind a small interface with Windows and Unix implementations. Provider roots default to `%USERPROFILE%\\.claude` / `%USERPROFILE%\\.codex` on Windows and `$HOME/.claude` / `$HOME/.codex` elsewhere, and can be overridden for tests or nonstandard installs.

Cross-compilation proves source portability, not provider runtime behavior. Release acceptance therefore includes one real-host discovery smoke test per operating system.

## Session identity

The stable local key is `<provider>:<provider-session-id>`. A separate random node ID identifies the host installation. A future broker address is therefore `<node-id>/<provider>:<provider-session-id>`.

Provider metadata is untrusted input. Adapters validate identifiers, timestamps, and paths and ignore message content. Provider session IDs come from a metadata field rather than a filename, so `model.ValidateProviderSessionID` is the single rule every ingest path applies: an ID containing the address separator would move the boundary between node and session in a qualified address.

Validation failure is per record. Discovery skips an unusable session and reports the count rather than abandoning the scan, because anything able to write a file under a provider directory could otherwise disable discovery entirely. The boundary that fails closed is the export projection, not ingest.

## Lifecycle model

| Management | Evidence | Status quality |
|---|---|---|
| managed | Direct heartbeat and reported lifecycle | Authoritative while heartbeat is fresh |
| unmanaged | Metadata modification time plus provider process presence | Heuristic |

Normalized states:

- `active`: direct managed report, or very recent metadata correlated with a running provider process.
- `idle`: managed idle report, or recent metadata with a running provider process.
- `inactive`: stale metadata or expired managed heartbeat.
- `unknown`: required evidence is unavailable or invalid.

Every status response carries `statusSource` so consumers can distinguish reports from inference.

## Privacy boundary

Audience and export flags are stored in AgentHub, not provider files. Discovery
uses an upsert that never updates those owner-controlled fields, so provider
rescans cannot undo the owner's choice.

There are two views:

- Owner-local view: all local sessions, including private sessions.
- Owner export preview: the union of sessions published to at least one audience, projected into `SessionSummary`. Nothing consumes this preview over the network today.
- Per-peer export view: `HeartbeatBuilder.BuildFor(peer)` filters that same projection to sessions authorized for the named peer. It is implemented and tested but has no transport consumer.

`BuildFor` refuses a recipient that is not currently in `trusted_nodes`. The
audience filter alone cannot enforce `all_paired`: `Audience.PublishesTo`
returns true for an `all_paired` session and any non-empty string, so accepting
an arbitrary identifier would make `all_paired` mean "anyone who supplies a node
id". Checking trust in the builder also makes revocation immediate — a revoked
node stops receiving heartbeats without the owner revisiting each session's
audience. `Build` stays a union because it is the owner's own preview.

Every heartbeat also names the node it was built for, in a signed
`recipientNodeId`. Without it a snapshot is only bound to its sender, so a
snapshot built for one peer is a valid snapshot for every peer that trusts that
sender — the per-peer filtering above would be undone by anyone who could hand
the envelope on. `BuildFor(peer)` addresses the envelope to that already-trusted
peer. The owner preview is addressed to this node: it stays the union the owner
needs to see, and it is not usable as any peer's heartbeat, because the
recipient it names is not that peer. There is no way to produce an undirected
`node.heartbeat`; the constructor for undirected envelopes refuses the type.

The MVP local inbox accepts local destinations and parses both local and
qualified addresses, but a qualified remote address returns `UNKNOWN_NODE`
until routing exists. Remote `agent_send` must also require both an authorized
view and the destination session's `acceptMessages` flag.

The heartbeat builder projects each published session into `protocol.SessionSummary`,
a type separate from the owner-local `model.Session`. The projection copies field
by field, so a new registry field cannot become remotely visible by being added;
the schema's `additionalProperties: false` fails the build if one does. Session
addresses in the export view are qualified as `<node-id>/<provider>:<id>`.

`GET /v1/heartbeat` returns the complete broker envelope rather than a
differently-shaped preview, and tests validate both the builder output and the
HTTP response against `broker-protocol.schema.json`. This settles the shape and
per-peer filtering, not transport: no receiver authenticates, expires, or
deduplicates these envelopes yet, so the node must not be connected to a LAN.
The accepted audience and
migration behavior is recorded in
[ADR-001](decisions/001-session-audience-and-export-boundary.md).

## Node and broker boundary

The node identity has two parts. The random identifier persisted in SQLite is a
label and proves nothing: anyone can claim one. The Ed25519 keypair beside the
database is what a peer can check, and every envelope carries a signature over
itself with the signature field cleared.

The private key lives in `node.key` next to the database rather than inside it.
A database is copied, backed up and inspected far more casually than a file
named private key, and a copy that carried the key would clone this node's
identity. A truncated or corrupt key is reported rather than silently
regenerated, because a new identity would invalidate every pairing.

How the file protects the seed is platform-specific, because a single mechanism
would be a false claim on one of the two:

- **Unix**: the raw 32-byte Ed25519 seed at mode 0600. The kernel enforces that
  on every open, and the mode is re-checked on every load — a key restored from
  a backup as 0644 is refused rather than used silently.
- **Windows**: the seed encrypted with DPAPI for the current user, wrapped in a
  versioned envelope (`AHNK` magic, scheme byte, payload length). Go documents
  that `Chmod` on Windows only drives the read-only attribute and does not
  produce an owner-only ACL, so a `node.key` at "0600" there would be readable
  by anything that can reach the path. Because the blob is bound to the Windows
  user account, copying `node.key` to another machine or another user's profile
  yields a file that cannot be decrypted. No mode claim is made on Windows;
  DPAPI is the access control. The header exists so a file written by a future
  scheme is reported as such rather than mistaken for a damaged key.

The loader fails closed in both cases. It refuses anything under `node.key` that
is not a plain regular file — a symlink or a Windows reparse point there means
something else chose where the identity is read from — refuses a file too large
to be a key, and refuses a blob it cannot decrypt or that decrypts to the wrong
length. It never replaces a key it could not read. It also compares the `Lstat`
observation with the opened handle using `os.SameFile`: two checks that each say
"a regular file" do not prove they saw the same file, and the bytes that get used
must come from the file that was inspected. That window is small and reaching it
needs write access to a directory that is mode 0700 and owned by the same user
the key protects, so the check is defence in depth rather than the main barrier.

Creation writes the fully formed bytes to a temporary file, flushes them, and
only then links that content to `node.key`. The hard link is the
create-if-absent primitive: it is atomic and it fails if the name already exists.
So a concurrent start loses the race and reads the winner's key rather than
overwriting it, and any failure leaves `node.key` either absent or holding a
complete key — never the empty or truncated file that every later start would
refuse.

There is deliberately no second route. Creating the final name directly, even
with `O_EXCL`, publishes the name before the content and reopens the window this
ordering exists to close. When linking is unavailable the install fails with
`ErrKeyStorageUnsupported` and names the fix — move the data directory off a
FAT/exFAT volume or network share — because an operator can move a directory but
cannot recover an identity that was never written whole.

The displayed fingerprint is the public key's SHA-256 rendered as six groups of
four hex digits. The grouping is for a human comparing two screens during
pairing; an unbroken run of hex invites people to check the first characters and
stop.

Verification takes the key the receiver already trusts for that node ID. A key
travels inside `pair.request`, but holding a key is not identity: that key is
trusted only after a person compares fingerprints on both machines. The full
fingerprint must match — the API compares the whole value, because a check of
the first group or two is cheap to forge.

There are two verification calls, and the split is deliberate:

- `VerifySender(key, senderNodeID)` answers only "is this authentically from
  that node". It refuses a directed envelope rather than reporting it verified,
  so no future receiver can reach an accept decision on a heartbeat without also
  checking who the envelope was addressed to.
- `VerifyDirected(key, senderNodeID, localNodeID)` answers that question and
  "was this built for me". A signature says who wrote an envelope, never who may
  act on it.

Both check the signature first, and the order is part of the contract. The two
failures mean different things: `ErrUnsigned` is a stranger, `ErrNotAddressed`
is a known peer's envelope meant for somebody else — something a receiver may
reasonably count or log as traffic from a node it trusts. If a forgery could
produce the second, that reading would promote an unauthenticated sender to a
known one, so authenticity is decided before anything is said about the address.
Rewriting the recipient does not reach the address comparison at all: the
recipient is a signed field, so a redirected envelope fails as unsigned. The
local node ID is validated against the shared node-ID rule before it is accepted
as a destination, so a receiver that does not yet know its own identity cannot
match an envelope that carries the same unusable value.

A signature covers a length-prefixed encoding of the envelope's fields, not a
re-serialization of the Go value. The fields, in order, are `protocolVersion`,
`messageId`, `type`, `sentAt`, `nodeId`, `recipientNodeId` and the payload's raw
bytes; an undirected envelope contributes an empty recipient rather than
omitting the field, so a receiver reproducing the bytes never has to guess
whether a field is absent or empty. The payload travels as the exact bytes the
sender produced and is signed as those bytes. This is what makes a signature
reproducible by a receiver that only ever saw JSON: a Go struct serializes in
field order while the map it decodes into serializes in key order, and a uint64
returns as a float64, so signing a decoded value would fail every verification
between two processes.

Trust says a node ID belongs to a key. It grants no session access: an audience
is a separate decision, made per session, so pairing a machine publishes
nothing. Revoking removes the trust row and every grant that node held in one
transaction, because a grant left behind would take effect again if the node
were paired a second time. A node already trusted with a different key is
refused rather than updated; silently accepting a new key is how a machine gets
impersonated.

A target broker heartbeat is a replaceable presence snapshot with:

- protocol version
- a signature over the envelope
- node ID and display name
- the recipient node ID it was built for
- monotonically increasing sequence number
- sent/expiry timestamps
- capabilities
- session summaries authorized for the receiving peer only

The sequence is owned by the registry and stored in SQLite, not by the builder.
One counter covers everything this node sends, reserved by a single atomic
`UPDATE`, so concurrent builds and a recreated builder cannot be handed the same
number. It survives a restart, which an in-memory counter does not: a counter
that returns to one after a restart forces a receiver to choose between
rejecting the restarted sender forever and not checking sequences at all. Gaps
are allowed — a build that fails after reserving a number does not give it back
— because a gap costs a receiver nothing while a repeat makes a fresh snapshot
indistinguishable from a replayed one. SQLite's INTEGER is signed, so the
counter stops at `MaxInt64` and refuses to allocate rather than wrapping,
returning zero, or reusing the last value; a build that cannot reserve a number
produces no envelope. Reaching that limit and finding the counter damaged or
missing are reported apart — `ErrSequenceExhausted` against
`ErrSequenceUnavailable` — because they describe different damage, not because
one is milder. Both fail closed.

Opening the database is not a repair. The upgrade that creates the counter also
records a marker row in `schema_markers`, written in the same transaction, and
that marker — not the presence of the table — is what says the migration ever
ran. Table existence cannot carry that meaning: dropping the table would then
make a node that has been publishing for months look like a database from before
the counter existed, and the upgrade path for those starts at zero. So `Open`
reads both and acts on the pair:

| Marker | Counter table | Result |
|---|---|---|
| absent | absent | genuine pre-counter database: marker, table and row created in one transaction; first allocation is 1 |
| present and valid | present and valid | preserved and opened, exactly as found |
| present and valid | absent | `ErrSequenceUnavailable`; nothing is recreated |
| absent | present | `ErrSequenceUnavailable`; the two are written together, so one alone is lost evidence |
| present but malformed, missing its row, or from another version | any | `ErrSequenceUnavailable` |

The counter row is checked the same way: missing, duplicated, or holding a value
this store could not have written all fail with `ErrSequenceUnavailable` and
write nothing.

Re-creating either is not a recovery. It restarts at zero under the same node
identity and republishes sequences a receiver may already hold, signed by this
node's own key — the replay the persisted counter exists to prevent — and the
previous high-water mark is exactly what has been lost. The safe answers are a
backup whose high-water mark cannot roll back, or rotating to a new node
identity with explicit re-pairing.

### What this does not protect against

The marker detects *inconsistent* loss: one piece of the evidence gone while
another contradicts it. It cannot detect the loss of all of it at once. A
database rolled back to a backup taken before any heartbeat, or deleted and
recreated together with `node.key`, presents no contradiction and is
indistinguishable from a first start — as it would be to any scheme that keeps
its high-water mark in the same file it is protecting. Local storage
authenticity is not something this build can infer. Ruling out a full rollback
needs a monotonic store outside the database, or a node identity that rotates
whenever the database does; AgentHub implements neither and claims neither.

Consumers replace the complete previous snapshot for that node; they never
merge session arrays. **A session absent from a heartbeat has had its
publication revoked**, and merging would resurrect it: revocation is expressed
by omission, so a consumer that merges never sees one. A snapshot is also scoped
to its recipient — a session published to selected nodes appears only in the
heartbeats built for those nodes — so one peer's snapshot says nothing about
another's and the two must never be combined. The broker must authenticate nodes, reject replayed or expired
heartbeats, and route only the export view produced by the owner node. These are
target requirements, not capabilities of the current build.

### Proving a peer is who its address claims

Before any heartbeat is delivered, the publisher challenges the address:
`POST /v1/challenge` carries a fresh 32-byte nonce and the challenger's node id,
and the responder returns a signature over those bytes. The publisher verifies
it against the public key already recorded in the trust store — never a key
carried in the answer.

The challenge signs bytes a stranger chose, which is a signing oracle by
construction. What stops it from being a useful one is domain separation: an
envelope signature is over bytes beginning `agenthub.broker/v1alpha1/envelope\n`
and a challenge answer over bytes beginning `agenthub.broker/v1alpha1/challenge\n`,
both with length-prefixed fields. No attacker-chosen value appears before those
prefixes, so an answer can never be presented as an envelope's signature.

**What the challenge does not do on its own.** It proves the peer's key-holder
is reachable and answered; it does not prove the entity at the address *is* that
peer. An active relay forwards the challenge to the genuine peer and returns the
genuine answer — the responder id, challenger id and nonce all travel unchanged,
so binding them does not help — and then receives the heartbeat.

### The connection is the identity

Peer traffic runs over TLS 1.3 whose certificate carries the node's own identity
key, and the client accepts exactly that key: the one recorded when pairing.
There is no CA and no chain, because a certificate here is a container for a
public key and the only question asked of it is the one pairing already
answered.

This is what closes the relay. A forwarder does not hold the peer's private key,
so it cannot terminate the connection at all — there is no plaintext for it to
read and forward. Identity stops being a side exchange that can be relayed and
becomes a property of the channel carrying the data.

The pin is installed as `VerifyConnection`, not `VerifyPeerCertificate`. The two
look interchangeable and are not: a resumed TLS 1.3 session performs no full
handshake, and Go calls `VerifyPeerCertificate` only for full handshakes.
Measured against a real server, three connections over a session cache produce
one call to `VerifyPeerCertificate` and three to `VerifyConnection` — so a pin
in the former would apply to the first connection and not the rest, which is not
a pin. The publisher additionally uses no session cache and a fresh client per
peer, so a connection established for one peer can never be resumed for another.

The challenge is kept alongside it. TLS answers "is this the paired key"; the
challenge answers "does this node agree it is the peer we meant", which catches
a discovery record aimed at the wrong node and a peer whose key has rotated.

### Messages between nodes

A heartbeat carries metadata this node observed. A message carries what a person
wrote, which is a different kind of data and is treated as one: it is queued for
the owner to read, and **nothing injects it into a provider**.

`ah send <node-id>/<provider>:<id>` records the message in a local queue and
answers `202 Accepted`. That status is the contract: the message is queued here
and nothing else has happened. The destination machine may be asleep. Answering
`201 Created` would make success mean something this node cannot know, so
`ah outbound <message-id>` is where the outcome is found afterwards.

Delivery rides the same schedule and the same pinned, challenged connection as
the heartbeat. Content must not travel on a weaker path than metadata does.

The receiving side applies the same order of checks as `POST /v1/heartbeat`, and
adds two rules of its own:

- **Every refusal reads the same.** A sender that could tell "no such session"
  from "that session declines messages" could map the recipient's sessions by
  addressing guesses at them, so both answer with one sentence.
- **A redelivery is not a duplicate.** A sender whose ack was lost sends again;
  the recipient recognises the message id it already holds and answers
  `duplicate` rather than putting a second copy in the inbox. A lost ack must
  not cost the reader two of the same message.

The sender label on a stored message names the node the envelope was *signed
by*, not the node the payload claimed. Only the session part of the sender's own
label is used, because that part is the only thing it is entitled to assert.

There is no relaying node in this design, so the requirement that a relay must
not persist message content is met by there being nothing in the middle: a
message goes from the machine that queued it to the machine that stores it.

### The inbox is bounded

One session holds at most 500 messages. Bodies are capped at 32KB, so a full
inbox is about 16MB — far more than anyone reads, and far less than a
compromised paired peer needs to fill a disk. The bound is per session rather
than global because sessions come from providers on this machine: a peer cannot
invent sessions to multiply its allowance.

**A full inbox defers, it does not refuse.** The two obvious designs both
destroy a message. Refusing makes the sender settle it permanently, so the
message is lost at the sending end; dropping the oldest loses one silently at
the receiving end. Neither is acceptable for something a person wrote, so a full
inbox answers `503` with `Retry-After` and the message stays in the sender's
queue until somebody reads and clears.

That only works if clearing is possible, so `DELETE /v1/inbox/{id}` empties one
and `DELETE /v1/inbox/{id}/{messageId}` drops a single message. Deletion is
explicit rather than inferred from reading: nothing here tracks what has been
read, and guessing would throw away things the owner had not finished with. How
full an inbox is travels with its contents, so a session that is filling up is
visible before senders start backing up.

Settled outbound rows are pruned after a retention period rather than on
settlement, because `ah outbound <id>` is the only place an owner finds out what
happened to a message and deleting the answer as it becomes true would make the
command useless. Pending rows are never pruned.

### When private is not visible from the address

`-allow-lan` accepts loopback and the ranges that are private by definition —
RFC 1918, RFC 4193, link-local. It refuses everything else, including addresses
that are public by assignment but private in practice.

That refusal is right by default and wrong for some real networks. Two machines
on a direct cable can be using a block IANA assigned to somebody else: the
addresses look routable, nothing reaches them, and no amount of inspection can
tell that apart from the genuine article. So the owner says:

    -treat-as-private 122.122.0.0/16

The declaration is specific on purpose. A flag that disabled the check would be
easier to use and would also be what somebody reaches for at 2am to make an
error go away, taking every other address with it. Naming a block states a
belief about your own network, the belief is logged at startup where it can be
questioned, and it widens the rule by exactly as much as it names — a default
route is refused outright, because "everything is private" is the absence of a
belief rather than one.

The line is drawn by what a block contains, not by how many bits it has. A
declaration is refused if it covers the unspecified address (binding that means
every interface, including any public one the host later gains), multicast, or
the broadcast address. That rules out `0.0.0.0/1` and `224.0.0.0/4` on principle
while leaving any genuine unicast block, however large, to the owner's judgement.

The listener refuses the unspecified address independently, whatever is
declared, and asks the parsed address rather than comparing strings — `0.0.0.0`
is only one of its spellings, and `0::0`, `::0` and `::ffff:0.0.0.0` bind
everything too.

Both sides read the same declaration. The listener asks before binding and the
publisher asks before sending, from one definition, so a node can never be
configured to serve on an address it would then refuse to deliver to.

### Two listeners, not one

The owner's API and the peer surface are separate listeners with separate muxes.
The owner's API changes who may see a session, revokes peers, and sends
messages; the peer surface answers challenges and accepts heartbeats. Keeping
them apart is what makes opening a port to peers a bounded decision — on one mux,
exposing heartbeats would also expose `PUT /v1/sessions/{id}/audience`, and the
only thing between a peer and the owner's controls would be that nobody had sent
the request.

Both listeners are still bound to loopback. Widening the peer listener is the
remaining step, and it is a deliberate change to one guarded line.

### The receiving side

`POST /v1/heartbeat` accepts one peer's snapshot. It is the first endpoint that
exists to be called by another machine, so the order of its checks is part of
the contract rather than an implementation detail:

1. Decode the envelope. Nothing about it is believed yet.
2. Look the sender up in the trust store. An unpaired node is refused here,
   before anything is read from its payload.
3. Verify the signature *and* the recipient together, using the key the owner
   already recorded for that node ID — never a key carried in the envelope.
4. Only then read the payload.

Every refusal before step 4 answers `403` with an identical body. A caller must
not be able to tell "I do not know you" from "your signature is wrong" from
"that was addressed to somebody else": the difference is an oracle for which
nodes this owner has paired with, and answering it would leak the trust store to
anyone who can reach the port.

Storage enforces the replacement contract structurally. One peer holds exactly
one row containing the whole payload as the bytes that were verified, so there
is nowhere to merge into — a consumer that wanted to combine two snapshots would
have to defeat the schema. The sequence must *strictly* advance: equal is
refused along with lower, because a repeat of the last number is precisely what
a captured delivery replayed later looks like. A heartbeat whose own expiry has
already passed is refused rather than stored and hidden.

An expired snapshot makes its peer read as offline, not absent. The peer stays
listed with the moment it was last heard from, because "went quiet at 10:04" is
what an owner needs and deleting the row would make a peer that stopped sending
indistinguishable from one that was never paired. Revoking a node discards its
snapshot along with its grants: trust is what made that view admissible, so
withdrawing trust withdraws the view.

The centralized broker is trusted with metadata the owner has authorized for
routing. It must not receive private registry rows or provider transcripts and
must not persist message bodies. End-to-end encryption between peer nodes is a
possible later hardening step, not a current guarantee.

## Network safety

The MVP HTTP API binds to loopback. A future LAN mode must require authenticated pairing, validate request origins where applicable, use TLS or a mutually authenticated overlay, and never reuse the local API as an unauthenticated LAN endpoint.

The current API rejects non-loopback browser origins, emits no-store/nosniff headers, validates bounded JSON bodies, parameterizes all SQL, and paginates session listings. This is defense in depth for the local API, not a substitute for LAN authentication.

Loopback is a same-host trust boundary, not per-user authentication. Another process or OS account that can connect to the user's loopback port may access the local API. A production LAN release should add capability-token or OS-credential authentication before broadening the bind address.
