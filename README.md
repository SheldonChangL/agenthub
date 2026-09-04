# AgentHub

AgentHub is a privacy-first, local control plane for coding-agent sessions. The MVP discovers Claude Code and Codex CLI/App sessions, normalizes their status, and exposes an owner-local view through an `agenthub-node` daemon, the `ah` CLI, and a desktop app.

AgentHub targets Windows, macOS, and Ubuntu. The shared core is pure Go; provider process discovery is platform-specific. Cross-compilation is part of verification, while each provider must still be installed and supported on the target host.

Privacy is the default: discovered sessions start with audience `none`, and the peer listener stays on loopback unless the owner passes `-allow-lan` and names a private address. With that set, paired nodes exchange signed heartbeats and messages over TLS pinned to the key recorded at pairing, carrying only what each session's audience authorizes for that peer.

## MVP status

- Local SQLite session registry
- Claude Code and Codex filesystem discovery
- Codex App Server JSON-RPC initialize/thread-list client boundary
- Managed and unmanaged session model
- Conservative `active`, `idle`, `inactive`, and `unknown` status inference
- Persistent Ed25519 node identity, signed envelopes, and a schema-validated heartbeat preview
- Heartbeats bound to their recipient and a heartbeat sequence that survives restarts
- Local HTTP API and `ah` CLI
- Message inbox, bounded and deduplicated, reachable from paired nodes
- Per-session audience, working-directory export, and inbound-message policy
- Manual fingerprint pairing, trust storage, revocation, and desktop management
- Broker envelope schema and MCP tool schemas, both in use
- Architecture and issue plan for authenticated multi-node operation
- No wake-up: an agent reads its inbox when asked, and nothing hands it a message (Step 8, issue #60)
- No packaging: there is nothing to download, so installing means building from source (Step 10, issue #67)
- No provider message injection, by design
- Pairing is manual, and nothing announces itself for discovery (Step 9, issue #63)
- No release, installer, or version number (Step 10, issue #67)

The remote export contract, per-node audience model, signing identity, manual
trust workflow, and the authenticated peer transport between nodes are all
implemented and have been exercised between two machines
([verification.md](docs/verification.md)), and `agenthub-mcp` gives an agent four
tools over those pipes — also exercised between two machines, each running its
own Claude Code. What is missing is wake-up, so a message waits until someone
asks their agent to look, and everything needed for someone else to install
this. Those are Steps 8 to 10, tracked from
[issue #1](https://github.com/SheldonChangL/agenthub/issues/1).

## Roadmap and release gates

| Track | State | Source of truth |
|---|---|---|
| Local MVP | Implemented and tested | [spec](docs/spec.md), [verification](docs/verification.md) |
| Remote export contract | Implemented and schema-validated | [architecture](docs/architecture.md), [broker protocol](docs/broker-protocol.schema.json) |
| Per-node privacy and network exchange | Implemented and exercised between two hosts | [issue #1](https://github.com/SheldonChangL/agenthub/issues/1), [verification](docs/verification.md) |
| MCP server: four tools an agent calls | Implemented and exercised between two hosts | [issue #56](https://github.com/SheldonChangL/agenthub/issues/56), [verification](docs/verification.md) |
| Automated pairing, wake-up, distribution | Planned | issues [#60](https://github.com/SheldonChangL/agenthub/issues/60), [#63](https://github.com/SheldonChangL/agenthub/issues/63), [#67](https://github.com/SheldonChangL/agenthub/issues/67) |
| Desktop metadata rendering hardening | Implemented and regression-tested | [issue #19](https://github.com/SheldonChangL/agenthub/issues/19) |
| Provider injection and wake-up | Deferred by design, and by step | [spec](docs/spec.md), [issue #60](https://github.com/SheldonChangL/agenthub/issues/60) |

## Build and test

Both modules require **Go 1.27.0 or newer**, declared in `go.mod` and
`desktop/go.mod`. The floor is a security requirement, not a language-feature
one: these binaries link the toolchain's standard library, so an unpatched
toolchain ships its `crypto/tls`, `crypto/x509` and `net/http` vulnerabilities
into the built artifact regardless of how the tests do. The `go` directive makes
the go command refuse an older toolchain rather than build quietly against it.

```sh
go version   # must report go1.27.0 or newer
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
go run ./cmd/ah audience <session-id>
go run ./cmd/ah audience <session-id> all-paired --cwd
go run ./cmd/ah audience <session-id> selected node_laptop00000000 node_build000000000 --cwd --messages
go run ./cmd/ah nodes
go run ./cmd/ah pair <node-id> <display-name> <platform> <public-key> <fingerprint>

# Pairing alone does not make delivery happen: a peer with no recorded address
# is skipped. There is no `ah` subcommand for this yet, so it is a raw call.
curl -X PUT http://127.0.0.1:7462/v1/nodes/<node-id>/address \
  -H 'Content-Type: application/json' -d '{"address":"192.168.1.20:7463"}'

go run ./cmd/ah revoke <node-id>
go run ./cmd/ah send <session-id> "please review the schema"
go run ./cmd/ah inbox <session-id>
```

`selected` accepts only node IDs that already appear in `ah nodes`; pairing an
unknown node is a separate, explicit owner action.

The node listens on `127.0.0.1:7462` by default. Set `AGENTHUB_URL` for the CLI or pass `--url`.

The owner's API remains loopback-only and stays there; peer traffic uses a
separate TLS listener on `127.0.0.1:7463` by default. Trust records are created
by hand, and the receiving side authenticates every envelope against them:
signature and recipient binding on all of them, plus expiry and a strictly
advancing sequence on heartbeats, and message-id deduplication on messages.
Session list responses are paginated; `ah list` follows every page automatically.

## Desktop app

The desktop app is the owner's management surface for the privacy model. It
lists local sessions, filters by provider/status/audience/working directory,
applies an audience policy to a selection, and manages manually paired nodes.

It lives in `desktop/` as a separate Go module so that Wails' CGo requirement never reaches `agenthub-node` or `ah`, which stay CGo-free and cross-compilable.

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@latest
cd desktop
wails dev     # live-reload development
wails build   # produces build/bin/agenthub-desktop.app
```

The app requires a running node and talks to it over the same local HTTP API as the CLI. It refuses non-loopback node URLs, because the owner's API has no authentication and stays on loopback for that reason.

## Privacy model

`ah list` is an owner-local view and can show private sessions. Publishing
decides which paired nodes receive a session in the heartbeat built for them:

```text
discovered session -> audience: none -> absent from every export view
                                       |
                                       +-- all_paired -> every paired node, including future ones
                                       +-- selected   -> only the nodes the owner named
```

Publishing answers *to whom*, not merely *whether*. `all_paired` and `selected`
differ the moment a new node is paired, so they are separate choices rather than
a list that happens to hold everything.

Two per-session flags default closed: the working directory travels only when
the owner opts in, and a session accepts queued messages only when the owner
opts in.

Pairing a node establishes identity only. It publishes nothing: the audience is
a separate, per-session decision. Revoking a node withdraws trust and every
grant it held, in one step, so re-pairing later does not restore access.

The preview includes only sessions published to at least one audience and is
projected into an allowlisted `SessionSummary`: the qualified AgentHub address,
provider, status, management
mode, `statusSource`, last-seen time, and working directory. Provider source,
provider session ID as a separate field, internal update time, metadata paths,
transcript bodies, and prompt contents are excluded, and the published schema
rejects them. The working directory is omitted unless that session's
`exportCwd` flag is enabled.

The model is documented in
[ADR-001](docs/decisions/001-session-audience-and-export-boundary.md) and is now
implemented. A database written by an earlier build upgrades with every session
at audience `none`, including rows previously marked public: that flag controlled
a local preview at a time when no remote peer existed, so it was never consent to
share with one.

Queued AgentHub messages are stored in the local SQLite database. They are not injected into Claude or Codex in this MVP, and a successful `ah send` means queued. For a remote destination `ah outbound <message-id>` reports what became of it later, and nothing hands the message to an agent.

See [architecture](docs/architecture.md), [MVP specification](docs/spec.md), [multi-node plan](docs/multinode-plan.md), [broker protocol](docs/broker-protocol.schema.json), and [MCP tool draft](docs/mcp-tools.json).

The Codex App Server client boundary is implemented and schema-tested, but is not enabled in the node's default scan path yet. See [Codex App Server notes](docs/codex-app-server.md).

## Local API

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/discover` | Rescan provider metadata; reports per-provider counts and `skipped` |
| `GET` | `/v1/sessions?page=1&pageSize=50` | List owner-local sessions |
| `GET` | `/v1/sessions/{id}` | Read one session |
| `PUT` | `/v1/sessions/{id}/visibility` | Compatibility: `public` means the explicit all-paired choice |
| `GET` | `/v1/sessions/{id}/audience` | Read one session's export policy |
| `PUT` | `/v1/sessions/{id}/audience` | Replace one session's export policy |
| `POST` | `/v1/sessions/audience` | Apply one policy to many sessions |
| `GET` | `/v1/heartbeat` | Owner preview of a signed heartbeat; union of sessions published to any audience, addressed to this node so no peer can accept it |
| `GET` | `/v1/nodes` | List paired nodes |
| `POST` | `/v1/nodes` | Manually trust a node whose full fingerprint the owner compared |
| `DELETE` | `/v1/nodes/{id}` | Revoke trust and every grant that node held |
| `PUT` | `/v1/nodes/{id}/address` | Record where a paired node is reachable. Delivery skips a peer without one, and there is no `ah` subcommand for it yet |
| `GET` | `/v1/node` | This node's own identity and fingerprint |
| `GET` | `/v1/peers` | Presence: paired nodes, online state, and the sessions each has authorised for this node |
| `POST` | `/v1/messages` | Queue a message for a local session, or — with `from` naming a local session whose owner opened outbound — for a session on a paired node |
| `GET` | `/v1/inbox/{id}` | Read a local inbox, in pages: `limit` (1–200) and `after` (the `next` value a full page carries) |
| `DELETE` | `/v1/inbox/{id}` | Empty one session's inbox |
| `DELETE` | `/v1/inbox/{id}/{messageId}` | Drop one message |
| `GET` | `/v1/outbound/{id}` | What became of one queued message |

The peer listener serves a separate mux on `:7463` over TLS: `POST /v1/challenge`, `POST /v1/heartbeat`, and `POST /v1/messages`. It is never the owner's API.

See [verification notes](docs/verification.md) for the tested platform matrix and remaining runtime checks.
