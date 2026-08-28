# AgentHub

AgentHub is a local-first session registry and message bridge for coding agents. The MVP discovers Claude Code and Codex CLI/App sessions, normalizes their status, and exposes them through an `agenthub-node` daemon and the `ah` CLI.

AgentHub targets Windows, macOS, and Ubuntu. The shared core is pure Go; provider process discovery is platform-specific. Cross-compilation is part of verification, while each provider must still be installed and supported on the target host.

Privacy is the default: discovered sessions are local and private. A session is never included in LAN heartbeat data or exposed to a remote peer until its owner explicitly publishes it.

## MVP status

- Local SQLite session registry
- Claude Code and Codex filesystem discovery
- Managed and unmanaged session model
- Conservative `active`, `idle`, `inactive`, and `unknown` status inference
- Persistent node identity and LAN-ready heartbeat payload
- Local HTTP API and `ah` CLI
- Local message inbox for the future broker path
- Broker protocol and MCP tool schemas
- No complete LAN broker, remote wake-up, or provider message injection yet

## Build and test

```sh
go test ./...
go build ./cmd/agenthub-node ./cmd/ah
```

## Run locally

```sh
mkdir -p data
go run ./cmd/agenthub-node --db ./data/agenthub.db
```

In another terminal:

```sh
go run ./cmd/ah discover
go run ./cmd/ah list
go run ./cmd/ah status <session-id>
go run ./cmd/ah publish <session-id>
go run ./cmd/ah unpublish <session-id>
```

The node listens on `127.0.0.1:7462` by default. Set `AGENTHUB_URL` for the CLI or pass `--url`.

## Privacy model

`ah list` is an owner-local view and can show private sessions. Network-facing output is filtered separately:

```text
discovered session -> PRIVATE -> absent from heartbeat/broker/MCP remote view
                                |
                                +-- ah publish -> PUBLIC
```

Publishing exposes only normalized metadata: AgentHub ID, provider, status, host identity, optional working directory, and last-seen time. Transcript or prompt contents are never stored by AgentHub.

See [architecture](docs/architecture.md), [MVP specification](docs/spec.md), [broker protocol](docs/broker-protocol.schema.json), and [MCP tool draft](docs/mcp-tools.json).
