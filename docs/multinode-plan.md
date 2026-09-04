# Plan: multi-node increment

## Why this plan exists

The MVP implemented a stable local registry and a draft protocol shape, then a
desktop app. The desktop app immediately surfaced a requirement the headless
increments had no way to reveal: an owner does not want one global public flag,
they want to choose **who** sees a session.

This plan front-loads the decisions that harden into schema. Visual design is
deliberately deferred; structural design is not.

Design reference: four artboards covering the local view, the LAN node view,
pairing, and the audience picker.

Tracked as [#1](https://github.com/SheldonChangL/agenthub/issues/1); each step
below links its issues.

## The decision that drove the foundation

The old `private | public` flag could not answer *public to whom*. That decision
has now landed as the audience model below:

```text
sessions.audience_mode = none | all_paired | selected
session_audience(session_id, node_id)
export_cwd = false
accept_messages = false
```

Existing `public` rows migrate to `none`, not `all_paired`. The old action only
enabled a local preview when no authenticated remote recipient existed, so it
cannot be treated as consent to share after an upgrade. See
[ADR-001](decisions/001-session-audience-and-export-boundary.md).

## Foundation already landed

Checked against `docs/broker-protocol.schema.json`, `docs/mcp-tools.json`, and
the runtime tests:

- `SessionSummary` is separate from the owner-local `Session`, uses a qualified
  `<node-id>/<provider>:<session-id>` address, and is allowlisted by schema.
- `GET /v1/heartbeat` returns the same signed envelope shape validated by the
  broker schema. The owner preview is a union; `BuildFor(peer)` requires the
  recipient to be a currently trusted node and then applies the actual per-peer
  audience.
- Every heartbeat names its recipient in a signed `recipientNodeId`, so a
  snapshot built for one peer cannot be replayed to another, and the owner
  preview is addressed to the local node. The outbound sequence is persisted in
  SQLite and stays monotonic across restarts. `receiveHeartbeat` verifies both:
  the recipient binding, and a strictly advancing sequence.
- Every session-addressed API accepts a bare local or qualified address, and a
  qualified address is routed to that node. The message store carries the
  destination node.
- Audience, `export_cwd`, and `accept_messages` are persisted with safe defaults
  and survive rediscovery.
- Each node has an Ed25519 keypair, fingerprint, signed-envelope implementation,
  trust store, manual fingerprint pairing, and transactional revocation.
- Schemas exist for `node.hello` and all four `pair.*` messages. They have no
  transport producer or consumer yet.

## Remaining protocol gaps

1. Done. `Envelope.VerifyDirected` is called by `receiveHeartbeat` and
   `receiveMessage`; presence is stored with expiry and replaced as a full
   snapshot. Remaining here: the `pair.*` envelopes still have no producer or
   consumer, so pairing is manual (issue #63).
2. Manual trust is not the automated `pair.request` / `pair.approve` exchange.
   The wire types are reserved and tested, but not sent.
3. Done in #16. `agent.message` and `agent.ack` are defined in
   `protocol/message.go` with delivery, ack, and duplicate semantics.
4. Done. The desktop Network view shows remote sessions and online/offline
   state from presence. Pending peers arrive with automated pairing (issue #63).
5. `sessionSummary` retains a derived `visibility: public` compatibility field.
   Audience authorization is enforced before projection; consumers must not
   interpret this constant as a global audience.

## Step 0: done

The export contract landed first, before any audience work, because a deployed
broker would have frozen the wrong shape. `protocol.SessionSummary` is now a
separate type from `model.Session`, session addresses in the export view are
qualified, and both the builder output and the HTTP response are validated
against `broker-protocol.schema.json` with negative cases. This closed #18 and
the schema half of #8 in one change rather than two breaking ones.

## Increment order

Each step ends with something verifiable. "Broker" is the name of the envelope
format, not a host: nodes deliver directly to each other, and each performs the
role for itself. No LAN traffic was allowed until the identity gates were
complete; they are, and `-allow-lan` now opens the peer listener on a private
address.

0. **Export contract alignment** (#18) — done. Separate the owner-local model from the
   allowlisted remote summary and validate generated payloads against the draft
   schema. Verifiable: a published synthetic session exports only the documented
   fields and an unpublished session exports nothing.
1. **Audience model, local only** (#2, #3, #4, #5, #6) — done. Replace the visibility
   boolean with an audience table plus export flags. No network. The desktop
   app gets the audience picker; `ah publish` keeps working as "audience = all
   paired". Verifiable: rediscovery still preserves choices; heartbeat preview
   reflects audience; existing tests pass unchanged in meaning.
2. **Qualified addressing** (#7, #9; #8 done in step 0) — done. Parser, API,
   CLI, MCP documentation, remote persistence and routing. A qualified address
   for a paired node is queued and answered 202; for an unpaired one it returns
   `UNKNOWN_NODE` rather than being misclassified as malformed.
3. **Node keypair and fingerprint** (#10) — done. Extend `internal/identity` with a
   keypair and derive a stable fingerprint. Verifiable: fingerprint is stable
   across restarts and differs per node.
4. **Pairing exchange** (#11, #12, #13) — pairing is done by hand. The trust
   store, the fingerprint check and revocation are implemented, and the owner
   pairs by entering the peer's details. The `pair.request` / `approve` /
   `reject` / `revoke` envelope types are defined and schema-tested but still
   have no producer or consumer, so two nodes cannot complete an automated
   exchange. That is the remaining gap, tracked in #62 under Step 9 (#63); the transport it would
   ride on landed in step 5. Verifiable today: a fingerprint mismatch is refused
   and revocation removes all grants.
5. **Presence** (#14, #15, #17) — done. Authenticated heartbeat exchange between
   paired nodes, export view enforced per peer. Verified between two hosts on
   2026-09-02: with nothing published each node saw zero of the other's
   sessions, and publishing exactly one made exactly that one appear
   (verification.md).
6. **Cross-host messaging** (#16) — done. Route `agent.message` to a paired
   node's inbox. Verified between two hosts on 2026-09-02: delivered and queued
   on the destination node, and the destination provider's session file was
   confirmed unmodified — still not injected into any provider.

Step 7 (#56, the MCP server) is done. Steps 8 to 10 continue in #60 (wake-up),
#63 (pairing), and #67 (distribution).

## Boundaries for this increment

- The bind address was not widened before the identity gates in steps 3 and 5 landed. It now requires `-allow-lan` plus a private `-peer-listen` address.
  `nodeconfig.ValidatePeerListen` guards the peer listener and `ValidateLoopback` the owner API.
- Never let a rescan alter audience, exactly as it must not alter visibility
  today.
- Never migrate an old local `public` preview into remote sharing; require new
  consent after the audience migration.
- Never publish a session to a node the owner did not choose; "all paired
  nodes" is an explicit choice and must stay distinguishable from a per-node
  grant, because the two differ for nodes paired later.
- Transcript and prompt bodies remain out of scope.
- A paired node receives only the metadata its audience authorizes, never
  private registry rows. There is no router in between: message bodies go
  directly to the recipient and are persisted in the recipient's own inbox,
  which is who they are for.

## Deliberately deferred

- Desktop visual polish beyond the implemented local, audience, pairing, and
  trust-record views.
- Policy groups and aliases. The audience table is the primitive they would be
  built from; it is enough on its own for the first release.
- Session launch and supervision, and wake-up (#60), which goes through each
  provider's own API rather than writing into its files or process — that
  boundary does not move. `agenthub-mcp` serves the MCP surface over stdio;
  Streamable HTTP would put it on a socket, a separate decision not taken.
