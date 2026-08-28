# AgentHub

AgentHub is a privacy-first, local control plane for coding-agent sessions. The MVP discovers Claude Code and Codex CLI/App sessions, normalizes their status, and exposes an owner-local view through an `agenthub-node` daemon, the `ah` CLI, and a desktop app.

AgentHub targets Windows, macOS, and Ubuntu. The shared core is pure Go; provider process discovery is platform-specific. Cross-compilation is part of verification, while each provider must still be installed and supported on the target host.

Privacy is the default: discovered sessions are local and private. The current build has no LAN transport, so no session data leaves the host. Publishing a session only adds it to a local export preview; a future multi-node release will require a new, explicit audience choice before sending it to an authenticated peer.

## MVP status

- Local SQLite session registry
- Claude Code and Codex filesystem discovery
- Codex App Server JSON-RPC initialize/thread-list client boundary
- Managed and unmanaged session model
- Conservative `active`, `idle`, `inactive`, and `unknown` status inference
- Persistent local node identity and a public-only heartbeat preview
- Local HTTP API and `ah` CLI
- Local message inbox for the future broker path
- Desktop app for browsing and batch-publishing sessions
- Draft broker protocol and MCP tool schemas
- Architecture and issue plan for authenticated multi-node operation
- No complete LAN broker, remote wake-up, or provider message injection yet
- No runnable MCP server transport yet; the four tools are contract drafts

The remote export contract is now separate from the owner-local model. The next
increment turns the single public flag into a per-node audience model and adds
authenticated LAN pairing. It is planned in
[multinode-plan.md](docs/multinode-plan.md) and tracked from
[issue #1](https://github.com/SheldonChangL/agenthub/issues/1).

## Roadmap and release gates

| Track | State | Source of truth |
|---|---|---|
| Local MVP | Implemented and tested | [spec](docs/spec.md), [verification](docs/verification.md) |
| Remote export contract | Implemented and schema-validated | [architecture](docs/architecture.md), [broker protocol](docs/broker-protocol.schema.json) |
| Per-node privacy, pairing, presence, messaging | Planned in ordered increments | [issue #1](https://github.com/SheldonChangL/agenthub/issues/1), [multi-node plan](docs/multinode-plan.md) |
| Desktop metadata rendering hardening | Required before desktop distribution | [issue #19](https://github.com/SheldonChangL/agenthub/issues/19) |
| MCP runtime and provider injection/wake-up | Deferred; contracts or model only | [MCP draft](docs/mcp-tools.json), [spec](docs/spec.md) |

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

`ah list` is an owner-local view and can show private sessions. In the current
single-node build, publishing controls a local export preview only:

```text
discovered session -> PRIVATE -> absent from heartbeat/broker/MCP remote view
                                |
                                +-- ah publish -> PUBLIC preview
```

The preview is public-only and is projected into an allowlisted
`SessionSummary`: the qualified AgentHub address, provider, status, management
mode, `statusSource`, last-seen time, and working directory. Provider source,
provider session ID as a separate field, internal update time, metadata paths,
transcript bodies, and prompt contents are excluded, and the published schema
rejects them. A per-session opt-in for the working directory is still to come.

The accepted target model is documented in
[ADR-001](docs/decisions/001-session-audience-and-export-boundary.md): every
session starts with audience `none`; the owner may later choose all paired nodes
or selected nodes. Existing `public` preview choices are reset to `none` when
the first network-capable migration lands, because they were never consent to
share with a real remote peer.

Queued AgentHub messages are stored in the local SQLite database. They are not injected into Claude or Codex in this MVP, and a successful `ah send` means queued—not delivered or read.

See [architecture](docs/architecture.md), [MVP specification](docs/spec.md), [multi-node plan](docs/multinode-plan.md), [broker protocol](docs/broker-protocol.schema.json), and [MCP tool draft](docs/mcp-tools.json).

The Codex App Server client boundary is implemented and schema-tested, but is not enabled in the node's default scan path yet. See [Codex App Server notes](docs/codex-app-server.md).

## Local API

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/discover` | Rescan provider metadata; reports per-provider counts and `skipped` |
| `GET` | `/v1/sessions?page=1&pageSize=50` | List owner-local sessions |
| `GET` | `/v1/sessions/{id}` | Read one session |
| `PUT` | `/v1/sessions/{id}/visibility` | Set `private` or `public` |
| `GET` | `/v1/heartbeat` | Preview the broker envelope this node would send; public sessions only |
| `POST` | `/v1/messages` | Queue a local message |
| `GET` | `/v1/inbox/{id}` | Read a local inbox |

See [verification notes](docs/verification.md) for the tested platform matrix and remaining runtime checks.
