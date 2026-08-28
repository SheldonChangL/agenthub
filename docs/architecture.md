# Architecture

## Components

```text
Claude metadata ----> Claude adapter ---+
                                         |
Codex metadata -----> Codex adapter ----+--> session registry (SQLite)
                                         |            |
managed reporters ----------------------+            +--> local HTTP API
                                                              |
                                              ah CLI ----------+
                                              future MCP ------+
                                              future broker ---+
```

`agenthub-node` owns the local registry and is the only component intended to write it. Adapters translate provider-specific metadata into one `Session` model. The CLI talks to the node rather than reading provider files or SQLite directly.

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
- Export view: public sessions only. Heartbeats, broker messages, and remote MCP calls must use this view.

`agent_send` is allowed only when the destination is addressable in the caller's authorized view. The MVP local inbox accepts local destinations; remote delivery is deferred.

## Node and broker boundary

The node identity is generated once and persisted in SQLite. A heartbeat is a replaceable presence snapshot with:

- protocol version
- node ID and display name
- monotonically increasing sequence number
- sent/expiry timestamps
- capabilities
- public session summaries only

The broker must authenticate nodes, reject replayed/expired heartbeats, and enforce per-session visibility. These checks are schema requirements but are not yet a deployed broker.

## Network safety

The MVP HTTP API binds to loopback. A future LAN mode must require authenticated pairing, validate request origins where applicable, use TLS or a mutually authenticated overlay, and never reuse the local API as an unauthenticated LAN endpoint.
