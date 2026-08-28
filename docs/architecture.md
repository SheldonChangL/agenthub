# Architecture

## Target topology

```mermaid
flowchart LR
    Owner[Owner] --> CLI[ah CLI]
    Owner --> Desktop[Desktop app]

    subgraph ClientA[Client node A]
        ClaudeA[Claude sessions] --> ClaudeAdapter[Claude adapter]
        CodexA[Codex sessions] --> CodexAdapter[Codex adapter]
        ClaudeAdapter --> RegistryA[(Owner-local SQLite registry)]
        CodexAdapter --> RegistryA
        CLI --> LocalAPI[Loopback HTTP API]
        Desktop --> LocalAPI
        LocalMCP[Local MCP server - planned] --> LocalAPI
        LocalAPI --> NodeA[agenthub-node]
        NodeA <--> RegistryA
        RegistryA --> PolicyA[Audience and export policy]
        PolicyA --> ExportA[Allowlisted export view]
        InboxA[(Local inbox)] <--> NodeA
    end

    subgraph Server[AgentHub broker server - planned]
        Auth[Node authentication and pairing]
        Presence[Authorized presence directory]
        Router[Message router]
        Audit[Delivery audit without transcript storage]
        Auth --> Presence
        Presence --> Router
        Router --> Audit
    end

    subgraph ClientB[Client node B]
        NodeB[agenthub-node]
        RegistryB[(Owner-local SQLite registry)]
        PolicyB[Audience and export policy]
        InboxB[(Local inbox)]
        AgentsB[Claude and Codex sessions]
        NodeB <--> RegistryB
        RegistryB --> PolicyB
        NodeB <--> InboxB
        AgentsB --> RegistryB
        InboxB -. managed delivery or unmanaged queue .-> AgentsB
    end

    ExportA -. authenticated heartbeat .-> Presence
    NodeA -. routed message .-> Router
    Router -. authorized delivery .-> NodeB
    PolicyB -. authenticated heartbeat .-> Presence
```

The Client A solid path exists in the local MVP. Client B represents another
installation of the same local components; all cross-client/server and
provider-delivery edges are planned. The broker is a logical server role;
deployment and transport are intentionally deferred until the identity,
pairing, and export contracts are implemented.

## Current implementation boundary

| Boundary | Current state |
|---|---|
| Provider session -> client node | Filesystem discovery is enabled; Codex App Server parsing exists but is not wired into the daemon |
| Owner -> client node | `ah`, desktop app, and loopback HTTP API are implemented |
| Client node -> broker server | Not implemented; non-loopback bind is rejected. Node identity, signed envelopes, the trust store and the pairing schema exist; no transport carries them |
| MCP client -> client node | Tool contracts are drafted; no MCP transport is running |
| Node -> AI agent message delivery | Local messages are queued only; no provider injection or wake-up |

`agenthub-node` owns the local registry and is the only component intended to write it. Adapters translate provider-specific metadata into one `Session` model. The CLI and the desktop app talk to the node rather than reading provider files or SQLite directly.

The desktop app is the owner's audience management surface: it lists every
local session, filters them by provider, status, audience, and working
directory, and applies an audience policy to a selection. It holds no state of
its own and lives in a separate Go module so that Wails' CGo requirement never
reaches the node or the CLI.

Codex has two discovery foundations: rollout metadata scanning, which is the enabled MVP path, and a JSON-RPC App Server client for `initialize` plus `thread/list`. The live client requests `useStateDbOnly: true`, decodes only identity/path/status fields, and maps `active`, `idle`, `notLoaded`, and `systemError` into AgentHub status. It is intentionally not wired into the daemon until transport lifecycle and reconnect behavior are specified.

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

`Verify` takes the key the receiver already trusts for that node ID. A key
travels inside `pair.request`, but holding a key is not identity: that key is
trusted only after a person compares fingerprints on both machines. The full
fingerprint must match — the API compares the whole value, because a check of
the first group or two is cheap to forge.

A signature covers a length-prefixed encoding of the envelope's fields, not a
re-serialization of the Go value. The payload travels as the exact bytes the
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
- monotonically increasing sequence number
- sent/expiry timestamps
- capabilities
- session summaries authorized for the receiving peer only

Consumers replace the complete previous snapshot for that node; they never
merge session arrays. **A session absent from a heartbeat has had its
publication revoked**, and merging would resurrect it: revocation is expressed
by omission, so a consumer that merges never sees one. A snapshot is also scoped
to its recipient — a session published to selected nodes appears only in the
heartbeats built for those nodes — so one peer's snapshot says nothing about
another's and the two must never be combined. The broker must authenticate nodes, reject replayed or expired
heartbeats, and route only the export view produced by the owner node. These are
target requirements, not capabilities of the current build.

The centralized broker is trusted with metadata the owner has authorized for
routing. It must not receive private registry rows or provider transcripts and
must not persist message bodies. End-to-end encryption between peer nodes is a
possible later hardening step, not a current guarantee.

## Network safety

The MVP HTTP API binds to loopback. A future LAN mode must require authenticated pairing, validate request origins where applicable, use TLS or a mutually authenticated overlay, and never reuse the local API as an unauthenticated LAN endpoint.

The current API rejects non-loopback browser origins, emits no-store/nosniff headers, validates bounded JSON bodies, parameterizes all SQL, and paginates session listings. This is defense in depth for the local API, not a substitute for LAN authentication.

Loopback is a same-host trust boundary, not per-user authentication. Another process or OS account that can connect to the user's loopback port may access the local API. A production LAN release should add capability-token or OS-credential authentication before broadening the bind address.
