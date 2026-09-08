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
- Nothing writes into a provider's session files or process, by design
- Pairing still needs the peer's public key by hand, though a node can now announce itself for a while and see who else is announcing (Step 9, issues #61 and #62)
- No release or installer: installing means building from source, though CI now
  uploads a build of every binary for six platforms (Step 10, issues #64 and #67)

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
| Writing into a provider's files or process | Never, by design | [ADR-002](docs/decisions/002-mcp-surface-trust-boundary.md), [architecture](docs/architecture.md) |

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
go build -o bin/agenthub-mcp ./cmd/agenthub-mcp
bin/ah --version   # which commit these binaries came from
```

Every binary reports the revision it was built from — `ah --version`, the node's
first log line, and the version `agenthub-mcp` sends at initialize. A build from
a modified tree says so, because a revision that does not describe the source
points at code nobody ran.

CI builds all three for six platforms and attaches them to each run, with a
`SHA256SUMS` and a `BUILD` file naming the run and the revision. The artifact
zip records the executable bit, and `gh run download`, `unzip` and Archive
Utility all keep it. `actions/download-artifact` does not — it extracts without
applying modes — so a workflow that consumes these has to `chmod +x` them. On a
pull request the revision inside the binary is the ephemeral merge commit
GitHub built, not a commit on the branch; `BUILD` records both.

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
# is skipped. With --discover the address is learned from the peer's own
# announcements and this is unnecessary — see "Two machines" below. Without it,
# there is no `ah` subcommand, so it is a raw call.
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

## Two machines

This is the walkthrough that was actually run between a MacBook and an Ubuntu
22.04 box joined by a direct Ethernet cable, recorded in
[verification.md](docs/verification.md). Substitute your own addresses.

Most of it is done in the desktop app. The `ah` commands below each step are
the same thing from a terminal — useful over SSH, and useful when something has
gone wrong and you want to see the raw answer.

**1. A node on each machine.** The defaults keep everything on loopback, so a
second machine needs to be told otherwise:

```sh
# on each machine, with its own address in -peer-listen
bin/agenthub-node --db ./data/agenthub.db \
  --peer-listen 192.168.1.10:7463 --allow-lan --discover
```

`--peer-listen` is where peers connect back to, and it is the address that gets
announced, so it has to be an address other machines can reach — not loopback.
`--allow-lan` is what permits that. `--discover` turns on finding peers, and
without it the pairing commands below refuse and say so.

### What your machine calls itself

While pairing mode is open, the node announces a display name to everyone on
the segment, and it is printed at startup so you can see what that is:

```
node display name "sheldon.chang mac" (read from this machine)
```

By default it is read from the machine — `ComputerName` on macOS, the hostname
elsewhere — and it keeps following the machine on every start. That default
matters on macOS: with no `HostName` set, `gethostname()` answers from DHCP and
DNS, so a node using it can announce a name belonging to whoever held the
address before you.

To pick your own, and pin it against any later change:

```bash
bin/agenthub-node --db ./data/agenthub.db --display-name "the machine on my desk"
```

It sticks, so the flag is not needed on later starts, and the node id does not
change — existing pairings survive a rename. A name is refused if an
announcement could not carry it: at most 64 bytes, made of characters that
render. A peer you have already paired with keeps the name it recorded at
pairing time; re-pair to update it there.

If the two machines are on a direct cable in a range that is not private —
`122.122.0.0/16`, say — add `--treat-as-private 122.122.0.0/16` **on both**.
Without it each node refuses to list the other, because it will not deliver to
an address outside the ranges it trusts.

**2. Find each other.** Open the desktop app on both, go to the 區網 tab, and
press 開啟配對模式 on one. It appears on the other's candidate list within a
few seconds. From a terminal that is `bin/ah pairing on` and `bin/ah
candidates`.

Nothing in that list is verified. Every field was chosen by whoever sent the
packet, and the fingerprint shown is the one announced — a hint for finding the
right machine, never proof of which it is. The panel says so, and flags a row
whose name or fingerprint collides with another's.

**3. Compare the fingerprints, then pair.** Click the candidate. The dialog
fills in what was announced and deliberately leaves the public key and
fingerprint fields empty: the announcement carries no key, and that fingerprint
field is your statement that you compared one on the other machine's screen.

So compare them. Both apps show the node's own fingerprint; they must match
group for group, since comparing the first few is what an attacker defeats. Then
get the peer's public key from its own machine — `bin/ah node` there, or the
desktop's node line — and complete the dialog on each side.

From a terminal the same thing is:

```sh
bin/ah node                                            # on each machine, to read and compare
bin/ah pair <their-node-id> <their-name> <their-platform> <their-public-key> <their-fingerprint>
```

The public key still has to be carried across by hand. Automating that exchange
while keeping the fingerprint comparison is issue #62.

With `--discover` running, each node learns the other's address from the
announcements; no `PUT /v1/nodes/{id}/address` is needed.

**4. Publish a session.** Pairing on its own shares nothing. In the app's 本機
tab, tick the sessions and press 設定公開對象…, then choose who and tick 允許對方
排入訊息 and 允許這個 session 主動送出訊息. Doing many at once is why the app
exists.

```sh
bin/ah list                                            # your own sessions
bin/ah audience <session-id> all-paired --messages --outbound
```

The two boxes are the same two flags: `--messages` lets other nodes send to it,
`--outbound` lets it send out. Without `--outbound` the node refuses its own
outgoing messages, on the sending side, and says so.

**5. Send, and read.** Sending is the one step with no button: it is what an
agent does, through the MCP tools in the next section. From a terminal:

```sh
bin/ah peers                                           # what they published, and the address to send to
bin/ah send --from <your-session-id> <node-id>/<their-session-id> -- "hi"
bin/ah outbound <message-id>                           # queued, delivered, or refused
```

A remote session is addressed `<node-id>/<session-id>`; `ah peers` prints that
string in its SEND TO column, because `ah list` shows only your own sessions and
nothing else showed the qualified one.

Reading has a button: 收件匣 on any session row in the app, or

```sh
bin/ah inbox <session-id>
```

**6. Give an agent the tools.** That is the next section. What no version of
this does is hand a message to an agent — it waits in the inbox until something
asks for it, whether that is you pressing 收件匣 or an agent calling
`agent_inbox`. Making it arrive is Step 8 (issue #60).

## Give an agent the four tools

`agenthub-mcp` is an MCP server over stdio. It is bound to one session at
startup, because stdio carries no caller identity: whoever runs it decides which
session it speaks for, and it will not act for any other.

```sh
# -as is required. Add -url <node url> if the node is not on 127.0.0.1:7462.
go run ./cmd/agenthub-mcp -as claude:<id>
```

In `.mcp.json`, for a Claude Code session:

```json
{
  "mcpServers": {
    "agenthub": {
      "command": "/absolute/path/to/bin/agenthub-mcp",
      "args": ["-as", "claude:<id>"]
    }
  }
}
```

The four tools are `agent_list`, `agent_status`, `agent_inbox` and `agent_send`;
their contract is [mcp-tools.json](docs/mcp-tools.json). Reading is enough on its
own, but sending needs the owner to open the gate for that session
(`ah audience <id> ... --outbound`), and the node refuses an unattributed message
to another machine regardless.

## Desktop app

The desktop app is the owner's management surface for the privacy model. It
lists local sessions, filters by provider/status/audience/working directory,
applies an audience policy to a selection, manages manually paired nodes, opens
and closes pairing mode with the candidate list beside it, and reads what other
nodes have queued for a session. Sending is not there: that is what an agent
does through the MCP tools.

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

Queued AgentHub messages are stored in the local SQLite database. They are not written into a Claude or Codex session's files or process — that is a decision, not a stage — and a successful `ah send` means queued. For a remote destination `ah outbound <message-id>` reports what became of it later, and nothing hands the message to an agent.

See [architecture](docs/architecture.md), [MVP specification](docs/spec.md), [multi-node plan](docs/multinode-plan.md), [broker protocol](docs/broker-protocol.schema.json), and [MCP tool contract](docs/mcp-tools.json).

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
| `GET` | `/v1/peers` | Presence: paired nodes, online state, and the sessions each has authorised for this node. `ah peers` renders it, including the address to send to |
| `POST` | `/v1/messages` | Queue a message for a local session, or — with `from` naming a local session whose owner opened outbound — for a session on a paired node |
| `GET` | `/v1/inbox/{id}` | Read a local inbox, in pages: `limit` (1–200) and `after` (the `next` value a full page carries) |
| `DELETE` | `/v1/inbox/{id}` | Empty one session's inbox |
| `DELETE` | `/v1/inbox/{id}/{messageId}` | Drop one message |
| `GET` | `/v1/outbound/{id}` | What became of one queued message |
| `GET` | `/v1/pairing` | Whether this node is advertising, and what the announce loop last managed to send |
| `POST` | `/v1/pairing` | Open the window, optionally `{"seconds":N}` (30s–15m, default 5m) |
| `DELETE` | `/v1/pairing` | Stop advertising now |
| `GET` | `/v1/pairing/candidates` | Machines advertising right now. Every field is the sender's own claim |

The four pairing endpoints exist only under `-discover`; without it they answer
`409 DISCOVERY_DISABLED` rather than an empty list, because "nobody is
advertising" and "this node is not looking" are different answers and only one
of them means the owner should keep waiting.

Advertising also needs somewhere for a peer to connect back to, and that is the
peer listener's own bound address — so it needs `-allow-lan` *and* a
`-peer-listen` on this machine's network address. What gets announced is that
one address, sent from that address and out of the interface holding it, because
a receiver lists an offer only when the address it carries is the address the
datagram came from.

Opening the window is refused with `409 NO_ANNOUNCEABLE_ADDRESS` when there is
no such address, and the message says which case applies: a loopback listener
that no other machine can reach, an IPv6 listener that is perfectly reachable
but cannot be discovered while announcements go out on the IPv4 group, a
`-peer-listen` naming a host rather than one address, or an address whose
interface cannot carry a multicast packet — a point-to-point or VPN interface,
where the address is fine and the announcement has nowhere to go. That last one
is checked when the window is asked for rather than at startup, because an
interface can lose the ability after boot. Announcing anyway would advertise an
address nothing is listening on: the peer would see a candidate that looks
right, with a matching fingerprint, and get a refused connection.

A node with `-discover` joins the group on every interface that can carry it,
re-checked every ten seconds so an adapter plugged in after startup is picked up
without a restart — which is how a peer whose own listener is on a direct cable
gets heard rather than silently missed. `GET /v1/pairing` carries `announcing`
because an open window and a machine that is actually sending packets are
separate facts: it reports how many addresses this node can announce, when it
last tried and last succeeded, and why nothing is going out.

Nothing in the candidate list is verified and appearing in it grants nothing.
The fingerprint shown is the one announced, which is a hint for finding the
right row and never evidence; what settles identity is comparing the
fingerprint of the key that arrives in the handshake, on both machines.

The peer listener serves a separate mux on `:7463` over TLS: `POST /v1/challenge`, `POST /v1/heartbeat`, and `POST /v1/messages`. It is never the owner's API.

See [verification notes](docs/verification.md) for the tested platform matrix and remaining runtime checks.
