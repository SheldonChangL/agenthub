# Spec: AgentHub MVP

## Objective

Build an executable, privacy-first local foundation that inventories Claude Code and Codex sessions and routes authorized metadata and messages across providers and LAN hosts between paired nodes.

Success means a user can start one node, discover local sessions while decoding
and persisting metadata fields only, inspect normalized status, explicitly add
or remove individual sessions from a local export preview, and exchange queued
messages through the local API, and exchange them with a paired node over an
authenticated transport. The MCP contract is documented as a draft with its
unresolved implementation gaps made explicit.

## Assumptions

1. The first supported AgentHub hosts are Windows, macOS, and Ubuntu. Provider support may differ: Claude Code documents Windows 10+ through WSL or Git for Windows; native provider runtime acceptance remains separate from AgentHub compatibility.
2. Existing provider sessions are unmanaged. Their activity is inferred conservatively from metadata recency and provider process presence.
3. Managed sessions will report lifecycle state directly to the registry; launching and supervising providers is outside this increment.
4. All discovered sessions default to audience `none`. Re-discovery must never reset audience or export flags. An audience by itself sends nothing off this host: delivery also needs a paired node with a recorded address, and without `-allow-lan` the delivery policy is loopback-only, so two nodes on one machine still exchange real per-peer heartbeats while nothing reaches the network.
5. Transcript and prompt bodies are out of scope and must not be persisted.
6. The owner's API binds to loopback. A separate peer listener carries signed envelopes between paired nodes over pinned TLS, on loopback by default and on a private address when `-allow-lan` is set; there is no central host. The automated `pair.*` exchange is not implemented, so pairing is still manual.

## Tech stack

- Go 1.27.0+ (a patched toolchain; enforced by the `go` directive), cross-compiled for `windows/amd64`, `windows/arm64`, `darwin/amd64`, `darwin/arm64`, `linux/amd64`, and `linux/arm64`
- SQLite through a CGo-free `database/sql` driver
- Go standard library HTTP and JSON
- Desktop management app (`desktop/`) built with Wails v2, kept in a separate Go module so the node and CLI stay CGo-free and cross-compilable

## Commands

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/agenthub-node ./cmd/ah
GOOS=windows GOARCH=amd64 go build ./cmd/agenthub-node ./cmd/ah
GOOS=linux GOARCH=amd64 go build ./cmd/agenthub-node ./cmd/ah
go run ./cmd/agenthub-node --db ./data/agenthub.db
go run ./cmd/ah list
```

## Project structure

```text
cmd/agenthub-node/   node daemon entrypoint
cmd/ah/              user CLI entrypoint
desktop/             Wails desktop management app (separate Go module)
internal/adapter/    provider discovery adapters
internal/api/        local HTTP API
internal/identity/   persistent node identity
internal/model/      normalized contracts
internal/protocol/   signed broker envelopes, addressing, and export projection
internal/registry/   SQLite persistence
internal/status/     lifecycle inference
docs/                architecture and protocol contracts
```

## Code style

Use small packages, explicit data structures, context-aware I/O, and dependency injection only at process/filesystem boundaries.

```go
session, err := registry.GetSession(ctx, id)
if err != nil {
    return fmt.Errorf("get session %q: %w", id, err)
}
```

Errors add operation context. Public JSON uses lower camel case. Time values use RFC 3339 in JSON and UTC Unix milliseconds in SQLite.

## Testing strategy

- Unit tests for status inference and metadata parsers.
- SQLite integration tests use a temporary real database.
- HTTP tests use `httptest` and the real registry.
- CLI/node smoke tests use built binaries and a temporary database.
- Fixtures contain synthetic metadata only; no real transcripts are copied into the repository.
- HTTP list endpoints are bounded and paginated; the CLI follows pagination automatically.

## Boundaries

- Always: default sessions to audience `none`, preserve audience/export flags on upsert, validate external JSON, use parameterized SQL, bind locally by default, and run tests/build.
- Implemented: the peer listener authenticates and consumes signed envelopes, enforces expiry and replay protection, and is separate from the owner-local API, which stays on loopback.
- Ask first: inject prompts into provider sessions, weaken an export default, or expand the remote metadata allowlist.
- Never: store prompt/transcript bodies, copy provider credentials, auto-publish, or treat process presence alone as proof that a specific session is active.

## Success criteria

1. `agenthub-node` and `ah` build and run.
2. Discovery registers synthetic and local Claude/Codex sessions while decoding and persisting metadata fields only; message-body fields are ignored.
3. A Codex App Server client boundary can initialize and parse live `thread/list` status without being enabled by default.
4. Every newly discovered session has audience `none`; rediscovery preserves audience and export flags.
5. The heartbeat contains sessions published to at least one audience, projected into remote `SessionSummary`, signed by the node key, and validated against the published broker schema.
6. Managed and unmanaged status behavior is covered by deterministic tests.
7. `ah list`, `status`, `publish`, `unpublish`, `send`, and `inbox` work against the node.
8. Draft broker and MCP contracts are documented as JSON Schema-compatible JSON; the broker envelope is validated against runtime output, and remaining MCP gaps are tracked rather than presented as complete.
9. Windows, macOS, and Linux builds compile; macOS runs locally, while Windows and Ubuntu runtime acceptance is documented for real-host verification.

## Deferred work

The multi-node items below are planned in [multinode-plan.md](multinode-plan.md)
and tracked from issue #1.

- Automated pairing exchange (issue #63); LAN transport and presence are implemented
- Remote presence subscriptions and retries
- Provider-specific live APIs and message injection
- Session launch/supervision and wake-up
- Wake-up: nothing hands a message to an agent (issue #60)
- Policy groups and aliases
- Windows real-host acceptance (issue #21); a two-host macOS/Ubuntu run is recorded in [verification.md](verification.md)
