# Spec: AgentHub MVP

## Objective

Build an executable, local-first foundation that inventories Claude Code and Codex sessions and can later route messages across providers and LAN hosts.

Success means a user can start one node, discover local sessions without transcript ingestion, inspect normalized status, explicitly publish or unpublish individual sessions, and exchange queued messages through the local API. Protocol schemas must be stable enough for a future broker and MCP server.

## Assumptions

1. The first supported AgentHub hosts are Windows, macOS, and Ubuntu. Provider support may differ: Claude Code documents Windows 10+ through WSL or Git for Windows; native provider runtime acceptance remains separate from AgentHub compatibility.
2. Existing provider sessions are unmanaged. Their activity is inferred conservatively from metadata recency and provider process presence.
3. Managed sessions will report lifecycle state directly to the registry; launching and supervising providers is outside this increment.
4. All discovered sessions default to private. Re-discovery must never reset a user's visibility choice.
5. Transcript and prompt bodies are out of scope and must not be persisted.
6. The MVP node binds to loopback. LAN transport schemas are included, but a network broker and authentication handshake are not implemented yet.

## Tech stack

- Go 1.25+, cross-compiled for `windows/amd64`, `windows/arm64`, `darwin/amd64`, `darwin/arm64`, `linux/amd64`, and `linux/arm64`
- SQLite through a CGo-free `database/sql` driver
- Go standard library HTTP and JSON
- No web UI

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
internal/adapter/    provider discovery adapters
internal/api/        local HTTP API
internal/identity/   persistent node identity
internal/model/      normalized contracts
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

- Always: default sessions to private, preserve visibility on upsert, validate external JSON, use parameterized SQL, bind locally by default, and run tests/build.
- Ask first: add remote authentication, bind to non-loopback, inject prompts into provider sessions, or change the visibility model.
- Never: store prompt/transcript bodies, copy provider credentials, auto-publish, or treat process presence alone as proof that a specific session is active.

## Success criteria

1. `agenthub-node` and `ah` build and run.
2. Discovery registers synthetic and local Claude/Codex sessions without reading message bodies.
3. A Codex App Server client boundary can initialize and parse live `thread/list` status without being enabled by default.
4. Every newly discovered session is private; rediscovery preserves an explicit public setting.
5. Public heartbeat output contains public sessions only.
6. Managed and unmanaged status behavior is covered by deterministic tests.
7. `ah list`, `status`, `publish`, `unpublish`, `send`, and `inbox` work against the node.
8. Broker heartbeat and MCP tool contracts are documented as JSON Schema-compatible JSON.
9. Windows, macOS, and Linux builds compile; macOS runs locally, while Windows and Ubuntu runtime acceptance is documented for real-host verification.

## Deferred work

- Authenticated LAN pairing and broker persistence
- Remote presence subscriptions and retries
- Provider-specific live APIs and message injection
- Session launch/supervision and wake-up
- Full MCP server transport implementation
- Policy groups, aliases, and directory redaction controls
- Windows and Ubuntu real-host acceptance runs (cross-compilation is complete)
