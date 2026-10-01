# Security policy

## Supported versions

Only the [latest release](https://github.com/SheldonChangL/agenthub/releases/latest)
gets security fixes. A fix ships as a new release; older releases are not
patched. If you build from source, build from `main` or the latest tag.

## Reporting a vulnerability

Please report it privately through GitHub:
**[open a private security advisory](https://github.com/SheldonChangL/agenthub/security/advisories/new)**.

Do not open a public issue, pull request or discussion for a vulnerability
until a fix has been released.

A useful report says:

- the version (`ah --version`) and the platform,
- what an attacker needs to start with (on the same network, paired, a local
  account, ...),
- the steps to reproduce it, and what they get at the end.

Before you attach logs or a database, remove session contents, node ids, host
names and addresses that are not yours to share.

## What to expect

AgentHub is a one-person project, maintained in spare time. Reports are handled
on a best-effort basis and there is no guaranteed response time. What you can
expect:

- an acknowledgement once the report has been read,
- an honest answer on whether it is in scope and whether it will be fixed,
- credit in the advisory and the release notes, if you want it.

## In scope

- **The peer listener and pairing** — the TLS listener on `:7463`
  (`/v1/challenge`, `/v1/heartbeat`, `/v1/messages`, and the pairing endpoints
  while a pairing window is open), the pairing exchange and fingerprint
  comparison, discovery announcements, and anything that lets an unpaired
  machine be trusted, see a session, or deliver a message.
- **TLS pinning** — anything that lets a peer be accepted with a key other
  than the one recorded at pairing, or that survives `ah revoke`.
- **The audience model** — a session, its working directory or its messages
  reaching a node the owner did not choose, or a message waking a session
  whose wake switches are closed.
- **The MCP tools** (`agent_list`, `agent_status`, `agent_inbox`, `agent_send`)
  — content from another machine escaping the untrusted framing described in
  [ADR-002](docs/decisions/002-mcp-surface-trust-boundary.md), or a send
  getting past `allowOutbound`.
- **`install.sh`** — anything that installs a file other than the release
  asset it checked against that release's `SHA256SUMS`, or that writes outside
  the locations [docs/install-script.md](docs/install-script.md) lists.
- **The desktop app's service install** — the app running an `ah`,
  `agenthub-node` or `agenthub-mcp` other than the one it found where the
  [developer guide](docs/developer.md#desktop-app) says it looks, or
  registering a service that does something other than
  what the form showed.

## Out of scope

These are documented limits of the design, not vulnerabilities. The reasoning
is in [How it stays private](docs/developer.md#how-it-stays-private) and in
[docs/decisions/](docs/decisions/).

- **Attacks that start with access to the machine.** The owner's API on
  `127.0.0.1:7462` has no authentication: it is reachable by any process and
  any account on that machine, and loopback is its whole protection. A local
  process can also post with a `from` that names a local session. Anything
  that can reach it can already restart the node.
- **A paired peer acting within what it was granted.** A paired machine sees
  what its owner published to it and can message sessions that accept
  messages; with wake open, its messages start turns. The remedy is revocation
  (`ah revoke <node-id>`) — see
  [ADR-003](docs/decisions/003-waking-with-nobody-present.md).
- **An agent that follows a message.** Messages from another machine are
  marked as untrusted and attributed to their sender; nothing makes a model
  refuse. What a woken agent may do is its own tool configuration in Claude
  Code or Codex, which AgentHub does not shrink.
- **Unsigned binaries.** The macOS app is ad-hoc signed and not notarized, and
  the Windows installer is not signed. A matching `SHA256SUMS` proves the file
  is what the release workflow published, not that the release page itself is
  honest; [the developer guide](docs/developer.md#what-a-checksum-proves-and-what-it-does-not)
  says what backs it.
- **Being run on a network you do not trust.** The peer listener stays on
  loopback until you pass `--allow-lan` and name a private address; opening it
  wider is the owner's decision.
