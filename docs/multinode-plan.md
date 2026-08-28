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
- Every session-addressed API accepts a bare local or qualified address. Remote
  routing and a destination-node column in the message store remain in #7.
- Audience, `export_cwd`, and `accept_messages` are persisted with safe defaults
  and survive rediscovery.
- Each node has an Ed25519 keypair, fingerprint, signed-envelope implementation,
  trust store, manual fingerprint pairing, and transactional revocation.
- Schemas exist for `node.hello` and all four `pair.*` messages. They have no
  transport producer or consumer yet.

## Remaining protocol gaps

1. There is no transport or presence consumer. Nothing verifies a received
   heartbeat against a trusted key, rejects replay/expiry, or replaces stored
   presence state.
2. Manual trust is not the automated `pair.request` / `pair.approve` exchange.
   The wire types are reserved and tested, but not sent.
3. `agent.message` and `agent.ack` payloads and delivery semantics remain
   undefined until #16.
4. The desktop Network view lists trust records only. It cannot show remote
   sessions, pending peers, or real online/offline state without #14.
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

Each step ends with something verifiable. The broker is a logical server role;
no client-to-server LAN traffic is allowed until the identity and pairing gates
are complete.

0. **Export contract alignment** (#18) — done. Separate the owner-local model from the
   allowlisted remote summary and validate generated payloads against the draft
   schema. Verifiable: a published synthetic session exports only the documented
   fields and an unpublished session exports nothing.
1. **Audience model, local only** (#2, #3, #4, #5, #6) — done. Replace the visibility
   boolean with an audience table plus export flags. No network. The desktop
   app gets the audience picker; `ah publish` keeps working as "audience = all
   paired". Verifiable: rediscovery still preserves choices; heartbeat preview
   reflects audience; existing tests pass unchanged in meaning.
2. **Qualified addressing** (#7, #9; #8 done in step 0) — parser, API, CLI, and
   MCP documentation done. Remote message persistence/routing remains under #7.
   A qualified address currently returns `UNKNOWN_NODE` rather than being
   misclassified as malformed.
3. **Node keypair and fingerprint** (#10) — done. Extend `internal/identity` with a
   keypair and derive a stable fingerprint. Verifiable: fingerprint is stable
   across restarts and differs per node.
4. **Pairing exchange** (#11, #12, #13) — pairing done by hand; the exchange
   waits on transport. The trust store, the fingerprint check and revocation are
   implemented and the owner pairs by entering the peer's details. The
   `pair.request` / `approve` / `reject` / `revoke` envelope types are defined
   and schema-tested but have no producer or consumer: nothing sends them until
   there is something to send them over. New envelope types, signature
   verification, a trust store. Still no session data crosses the wire.
   Verifiable today: a fingerprint mismatch is refused and revocation removes
   all grants. Two nodes cannot complete an automated exchange until transport
   exists.
5. **Presence** (#14, #15, #17). Authenticated heartbeat exchange between
   paired nodes, export view enforced per peer. Verifiable: a session published
   to node A only is absent from node B's view.
6. **Cross-host messaging** (#16). Route `agent.message` to a paired node's
   inbox. Verifiable: queued on the destination node, still not injected into
   any provider.

## Boundaries for this increment

- Never widen the bind address before step 4 lands.
  `nodeconfig.ValidateLoopback` stays as the guard.
- Never let a rescan alter audience, exactly as it must not alter visibility
  today.
- Never migrate an old local `public` preview into remote sharing; require new
  consent after the audience migration.
- Never publish a session to a node the owner did not choose; "all paired
  nodes" is an explicit choice and must stay distinguishable from a per-node
  grant, because the two differ for nodes paired later.
- Transcript and prompt bodies remain out of scope.
- The broker may see metadata already authorized for routing, but never private
  registry rows. Message bodies may transit the router in step 6 but are not
  persisted there. End-to-end encryption is not claimed by this increment.

## Deliberately deferred

- Desktop visual polish beyond the implemented local, audience, pairing, and
  trust-record views.
- Policy groups and aliases. The audience table is the primitive they would be
  built from; it is enough on its own for the first release.
- Provider message injection, session launch and wake-up, full MCP transport.
