# ADR-001: Per-node audience and an explicit export boundary

## Status

Accepted

## Date

2026-08-28

## Context

The local MVP stores `visibility = private | public`. That is sufficient for a
single-host export preview, but it cannot answer which peer may see a session.
The current heartbeat builder also serializes `model.Session` directly, which
mixes owner-local fields with the future network contract.

Privacy is a product requirement: discovering a session must never publish it,
pairing a node must never publish it, and a choice made before remote sharing
existed must not silently become consent to share with future peers.

## Decision

The network-capable model will use:

```text
sessions.audience_mode = none | all_paired | selected
session_audience(session_id, node_id)   # grants used by selected mode
sessions.export_cwd = false             # safe default
sessions.accept_messages = false        # safe default
```

- `none` exports the session to no peer.
- `all_paired` exports it to every currently and subsequently paired node.
- `selected` exports it only to rows present in `session_audience`.
- Discovery and re-discovery never update any owner policy field.
- Pairing changes trust state only; it never changes a session audience.

The owner-local `Session` model and remote `SessionSummary` are separate types.
Remote summaries use an allowlist:

- qualified AgentHub address
- provider
- normalized status and management mode
- `statusSource`
- last-seen time
- working directory only when `export_cwd` is true

Provider source, provider session ID as a separate field, internal update time,
metadata path, transcript content, and prompt content are never part of a
remote summary.

When migrating from the local MVP, every existing row—including rows marked
`public`—starts with audience `none`. The old flag controlled a local preview
and there was no authenticated remote recipient, so treating it as consent for
future peers would violate the privacy boundary. The owner must explicitly
choose a network audience after upgrading.

## Alternatives considered

### Keep a global public/private flag

Rejected because it cannot express peer-specific sharing and would expose a
session automatically to nodes paired later.

### Convert existing public rows to all paired

Rejected because an old local-preview action was not informed consent to share
with future remote machines.

### Store policy only in the broker

Rejected because the owner node must enforce policy before data leaves the
host. The broker may route an already authorized export but is not the source
of truth for owner consent.

## Consequences

- The audience schema and export DTO must land before LAN transport.
- `ah publish` can remain as a compatibility command, but after pairing exists
  it means the explicit `all_paired` choice.
- The UI must preview exactly what each selected peer will receive.
- A centralized broker is trusted with routed, authorized metadata unless a
  later end-to-end encryption design changes that boundary.
  **Amended 2026-09-02:** no centralized broker was built. Nodes deliver
  directly to each other over TLS pinned to the key recorded at pairing, so
  there is no third party in the path and this consequence no longer applies.
  The rest of this ADR stands.
- Revocation is represented by omission from the next full heartbeat snapshot;
  consumers replace a node's prior snapshot rather than merge it.
