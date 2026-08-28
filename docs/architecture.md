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
| Client node -> broker server | Not implemented; non-loopback bind is rejected |
| MCP client -> client node | Tool contracts are drafted; no MCP transport is running |
| Node -> AI agent message delivery | Local messages are queued only; no provider injection or wake-up |

`agenthub-node` owns the local registry and is the only component intended to write it. Adapters translate provider-specific metadata into one `Session` model. The CLI and the desktop app talk to the node rather than reading provider files or SQLite directly.

The desktop app is the owner's visibility management surface: it lists every local session, filters them by provider, status, visibility, and working directory, and applies one publish or unpublish choice to a whole selection. It holds no state of its own and lives in a separate Go module so that Wails' CGo requirement never reaches the node or the CLI.

Codex has two discovery foundations: rollout metadata scanning, which is the enabled MVP path, and a JSON-RPC App Server client for `initialize` plus `thread/list`. The live client requests `useStateDbOnly: true`, decodes only identity/path/status fields, and maps `active`, `idle`, `notLoaded`, and `systemError` into AgentHub status. It is intentionally not wired into the daemon until transport lifecycle and reconnect behavior are specified.

## Platform boundary

Windows, macOS, and Linux share the same registry, protocol, API, CLI, filesystem metadata parsers, and privacy rules. Process enumeration is isolated behind a small interface with Windows and Unix implementations. Provider roots default to `%USERPROFILE%\\.claude` / `%USERPROFILE%\\.codex` on Windows and `$HOME/.claude` / `$HOME/.codex` elsewhere, and can be overridden for tests or nonstandard installs.

Cross-compilation proves source portability, not provider runtime behavior. Release acceptance therefore includes one real-host discovery smoke test per operating system.

## Session identity

The stable local key is `<provider>:<provider-session-id>`. A separate random node ID identifies the host installation. A future broker address is therefore `<node-id>/<provider>:<provider-session-id>`.

Provider metadata is untrusted input. Adapters validate identifiers, timestamps, and paths and ignore message content.

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

Visibility is stored in AgentHub, not provider files. Discovery uses an upsert that never updates `visibility`, so provider rescans cannot undo the owner's choice.

There are two views:

- Owner-local view: all local sessions, including private sessions.
- Current export preview: sessions marked public. Nothing consumes this preview over the network today.
- Target per-peer export view: sessions authorized for that peer, projected into an allowlisted `SessionSummary`.

`agent_send` is allowed only when the destination is addressable in the caller's authorized view. The MVP local inbox accepts local destinations; remote delivery is deferred.

The current heartbeat builder returns `model.Session` values and therefore does
not yet match the smaller broker schema. It must not be connected to a LAN
transport. [Issue #18](https://github.com/SheldonChangL/agenthub/issues/18)
tracks the separate remote DTO and schema validation. The accepted audience and
migration behavior is recorded in
[ADR-001](decisions/001-session-audience-and-export-boundary.md).

## Node and broker boundary

The current node identity is a random identifier generated once and persisted
in SQLite. It is not an authentication credential. A keypair and verifiable
fingerprint are required before any LAN mode.

A target broker heartbeat is a replaceable presence snapshot with:

- protocol version
- node ID and display name
- monotonically increasing sequence number
- sent/expiry timestamps
- capabilities
- session summaries authorized for the receiving peer only

Consumers replace the complete previous snapshot for that node; they never
merge session arrays. If a session disappears, its publication has been
revoked. The broker must authenticate nodes, reject replayed or expired
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
