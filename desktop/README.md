# AgentHub Desktop

AgentHub Desktop is the owner-facing privacy console for a local
`agenthub-node`. It lists every owner-local Claude and Codex session, supports
search and status/provider/audience filters, applies one audience and export
policy to multiple selected sessions, manages manually trusted nodes, triggers
discovery, and shows the current signed heartbeat preview.

The app is an HTTP client only. It does not read provider files or SQLite and
does not write a second copy of session state. It accepts loopback node URLs
only because the local API has no authentication yet.

## Prerequisites

1. Start `agenthub-node` on its default `127.0.0.1:7462` address, or set a
   loopback `AGENTHUB_URL` before launching the desktop app.
2. Install Wails v2 for development builds.

## Develop and build

```sh
go test ./...
wails dev
wails build
```

The desktop app is a separate Go module so Wails and CGo do not affect the
cross-platform node or CLI builds.

## Current boundaries

- Audience choices are implemented and persist across discovery. They prepare
  the per-peer export view but do not send anything while transport is absent.
- Pairing is a manual, loopback-only trust operation: the owner copies the peer
  identity and compares the full fingerprint out of band. The `pair.*` wire
  messages have schemas but no producer or consumer yet.
- The heartbeat dialog shows the actual signed, schema-validated envelope. It
  is the owner's union preview; peer-specific envelopes are built only by the
  future transport.
- The Network view lists trusted nodes. Remote presence, remote sessions,
  offline detection, message delivery, and provider wake-up are not implemented.
- Provider metadata is rendered through DOM text APIs and covered by a hostile
  metadata regression test; see closed
  [issue #19](https://github.com/SheldonChangL/agenthub/issues/19).
