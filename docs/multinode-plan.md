# Plan: multi-node increment

## Why this plan exists

The MVP shipped a stable local registry and a stable protocol *shape*, then a
desktop app. The desktop app immediately surfaced a requirement the headless
increments had no way to reveal: an owner does not want one global public flag,
they want to choose **who** sees a session.

This plan front-loads the decisions that harden into schema. Visual design is
deliberately deferred; structural design is not.

Design reference: four artboards covering the local view, the LAN node view,
pairing, and the audience picker.

## The single decision that drives everything

`visibility` is currently a two-value enum (`internal/model/model.go`), stored
per session and exported by `HeartbeatBuilder`. In a multi-node world that is
the wrong shape: "public" has to answer *public to whom*.

```text
now:    session.visibility  = private | public
target: session_audience(session_id, node_id)  + session export flags
```

Everything else in this plan is downstream of that change. It must land before
a broker exists, because a deployed broker freezes the export view.

## Schema gaps

Checked against `docs/broker-protocol.schema.json` and `docs/mcp-tools.json`.

### 1. `sessionSummary` has no node dimension

A remote list aggregates sessions from many nodes, so each row needs its origin.
Today `id` is `<provider>:<session-id>` and the node is only implied by the
envelope that carried it.

- Add `nodeId` to `$defs/sessionSummary`, or define the summary `id` as the
  fully-qualified `<node-id>/<provider>:<session-id>`.
- Prefer the fully-qualified id: it makes `agent_send` addressable with no
  second field, and it matches the address form already documented in
  `docs/architecture.md`.

### 2. Addressing is unqualified end to end

`agent_send.agentId` is `{"type": "string"}` and the local API's `to` field
(`internal/api/server.go`) is a bare local session ID. Cross-host delivery has
no way to name a destination.

- Accept both forms at the boundary: bare `<provider>:<id>` means "this node",
  `<node-id>/<provider>:<id>` means a peer.
- Do this before the broker ships. It touches the MCP contract, the local API,
  the CLI, the desktop app, and the messages table at once.

### 3. No trust state anywhere in the protocol

`type` is limited to `node.hello`, `node.heartbeat`, `agent.message`,
`agent.ack`. There is no pairing exchange and no field to carry a credential —
a sender asserts `nodeId` and nothing verifies it.

- Add `pair.request`, `pair.approve`, `pair.reject`, `pair.revoke`.
- Add a signature/credential field to the envelope. `docs/architecture.md`
  already requires the broker to authenticate nodes; the schema gives it
  nothing to authenticate with.
- Node identity is currently a random ID (`internal/identity`). Fingerprint
  verification needs a keypair, so identity has to grow one.

### 4. `node.hello` payload is undefined

`$defs` covers `heartbeat` and `sessionSummary` only. The pairing screen shows
a node before it is trusted, so the pre-trust advertisement needs a schema:
display name, platform, public key, fingerprint.

### 5. No per-session export flags

The artboards show two controls the model cannot express:

- omit `cwd` for this session (the deferred "directory redaction" item)
- allow or refuse inbound messages for this session

Both are per-session, per-owner decisions and belong beside audience.

### 6. What is already sufficient

- Liveness needs no new field. `sequence` plus `expiresAt` already distinguish
  online, stale, and offline.
- The full-snapshot heartbeat already handles revocation: a session that drops
  out of the array is no longer published, provided consumers treat each
  heartbeat as a replacement rather than a merge. Document that explicitly.
- `sessionSummary` keeps `additionalProperties: false` and
  `visibility: {"const": "public"}`. Both should survive the change — they are
  what makes the export view auditable.

## Increment order

Each step ends with something verifiable, and no step depends on a broker
existing until step 4.

1. **Audience model, local only.** Replace the visibility boolean with an
   audience table plus export flags. No network. The desktop app gets the
   audience picker; `ah publish` keeps working as "audience = all paired".
   Verifiable: rediscovery still preserves choices; heartbeat preview reflects
   audience; existing tests pass unchanged in meaning.
2. **Qualified addressing.** Accept fully-qualified addresses at every boundary
   while remaining single-node. Verifiable: `ah send node_x/claude:y` is
   rejected with a clear "unknown node" error rather than a parse failure.
3. **Node keypair and fingerprint.** Extend `internal/identity` with a keypair
   and derive a stable fingerprint. Verifiable: fingerprint is stable across
   restarts and differs per node.
4. **Pairing exchange.** New envelope types, signature verification, a trust
   store. Still no session data crosses the wire. Verifiable: two nodes on one
   machine can pair and refuse a mismatched fingerprint.
5. **Presence.** Authenticated heartbeat exchange between paired nodes, export
   view enforced per peer. Verifiable: a session published to node A only is
   absent from node B's view.
6. **Cross-host messaging.** Route `agent.message` to a paired node's inbox.
   Verifiable: queued on the destination node, still not injected into any
   provider.

## Boundaries for this increment

- Never widen the bind address before step 4 lands.
  `nodeconfig.ValidateLoopback` stays as the guard.
- Never let a rescan alter audience, exactly as it must not alter visibility
  today.
- Never publish a session to a node the owner did not choose; "all paired
  nodes" is an explicit choice and must stay distinguishable from a per-node
  grant, because the two differ for nodes paired later.
- Transcript and prompt bodies remain out of scope.

## Deliberately deferred

- Visual design of the desktop app. The structure is settled here; colors,
  density, and micro-interaction wait until steps 1-3 are real.
- Policy groups and aliases. The audience table is the primitive they would be
  built from; it is enough on its own for the first release.
- Provider message injection, session launch and wake-up, full MCP transport.
