# AgentHub

AgentHub is a local-first session registry and message bridge for coding agents. The MVP discovers Claude Code and Codex CLI/App sessions, normalizes their status, and exposes them through an `agenthub-node` daemon and the `ah` CLI.

AgentHub targets Windows, macOS, and Ubuntu. The shared core is pure Go; provider process discovery is platform-specific. Cross-compilation is part of verification, while each provider must still be installed and supported on the target host.

Privacy is the default: discovered sessions are local and private. A session is never included in LAN heartbeat data or exposed to a remote peer until its owner explicitly publishes it.

## MVP status

- Local SQLite session registry
- Claude Code and Codex filesystem discovery
- Codex App Server JSON-RPC initialize/thread-list client boundary
- Managed and unmanaged session model
- Conservative `active`, `idle`, `inactive`, and `unknown` status inference
- Persistent node identity and LAN-ready heartbeat payload
- Local HTTP API and `ah` CLI
- Local message inbox for the future broker path
- Desktop app for browsing and batch-publishing sessions
- Broker protocol and MCP tool schemas
- No complete LAN broker, remote wake-up, or provider message injection yet

## Build and test

```sh
go test ./...
mkdir -p bin
go build -o bin/agenthub-node ./cmd/agenthub-node
go build -o bin/ah ./cmd/ah
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
go run ./cmd/ah send <session-id> "please review the schema"
go run ./cmd/ah inbox <session-id>
```

The node listens on `127.0.0.1:7462` by default. Set `AGENTHUB_URL` for the CLI or pass `--url`.

The local API is deliberately loopback-only until authenticated LAN pairing exists. Session list responses are paginated; `ah list` follows every page automatically.

## Desktop app

The desktop app is the owner's management surface for the privacy model: it lists every local session, filters by provider, status, visibility, and working directory, and publishes or unpublishes a whole selection at once.

It lives in `desktop/` as a separate Go module so that Wails' CGo requirement never reaches `agenthub-node` or `ah`, which stay CGo-free and cross-compilable.

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@latest
cd desktop
wails dev     # live-reload development
wails build   # produces build/bin/agenthub-desktop.app
```

The app requires a running node and talks to it over the same local HTTP API as the CLI. It refuses non-loopback node URLs, because the local API has no authentication yet.

## Privacy model

`ah list` is an owner-local view and can show private sessions. Network-facing output is filtered separately:

```text
discovered session -> PRIVATE -> absent from heartbeat/broker/MCP remote view
                                |
                                +-- ah publish -> PUBLIC
```

Publishing exposes only normalized metadata: AgentHub ID, provider, status, host identity, optional working directory, and last-seen time. Transcript or prompt contents are never stored by AgentHub.

Queued AgentHub messages are stored in the local SQLite database. They are not injected into Claude or Codex in this MVP, and a successful `ah send` means queued—not delivered or read.

See [architecture](docs/architecture.md), [MVP specification](docs/spec.md), [multi-node plan](docs/multinode-plan.md), [broker protocol](docs/broker-protocol.schema.json), and [MCP tool draft](docs/mcp-tools.json).

The Codex App Server client boundary is implemented and schema-tested, but is not enabled in the node's default scan path yet. See [Codex App Server notes](docs/codex-app-server.md).

## Local API

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/discover` | Rescan provider metadata |
| `GET` | `/v1/sessions?page=1&pageSize=50` | List owner-local sessions |
| `GET` | `/v1/sessions/{id}` | Read one session |
| `PUT` | `/v1/sessions/{id}/visibility` | Set `private` or `public` |
| `GET` | `/v1/heartbeat` | Preview broker heartbeat; public sessions only |
| `POST` | `/v1/messages` | Queue a local message |
| `GET` | `/v1/inbox/{id}` | Read a local inbox |

See [verification notes](docs/verification.md) for the tested platform matrix and remaining runtime checks.
